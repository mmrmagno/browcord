package dockerguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

const guardToken = "guard-token-for-tests-0123456789abcdef"

type fakeDocker struct {
	mu         sync.Mutex
	containers []Container
	creates    []CreateBody
	names      []string
	removed    []string
	requests   []string
	next       int
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
		_ = json.NewEncoder(w).Encode(f.containers)

	case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
		var body CreateBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.next++
		id := fmt.Sprintf("%064x", f.next)
		f.creates = append(f.creates, body)
		f.names = append(f.names, r.URL.Query().Get("name"))
		f.containers = append(f.containers, Container{ID: id, Labels: body.Labels, State: "created", Created: time.Now().Unix()})
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/start")
		for i := range f.containers {
			if f.containers[i].ID == id {
				f.containers[i].State = "running"
			}
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/containers/")
		f.removed = append(f.removed, id)
		kept := f.containers[:0]
		for _, c := range f.containers {
			if c.ID != id {
				kept = append(kept, c)
			}
		}
		f.containers = kept
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

func testSeccomp(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/seccomp/chromium.json")
	if err != nil {
		t.Fatalf("read seccomp profile: %v", err)
	}
	profile, err := CompactSeccomp(raw)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	return profile
}

func newGuard(t *testing.T, cap int) (*Server, *fakeDocker, *httptest.Server) {
	t.Helper()

	docker := &fakeDocker{}
	engineSrv := httptest.NewServer(docker)
	t.Cleanup(engineSrv.Close)

	s, err := New(Config{
		Token:   guardToken,
		Cap:     cap,
		Ceiling: 6 * time.Hour,
		Template: Template{
			Image:      "ghcr.io/mmrmagno/browcord-room:latest",
			Network:    "browcord-rooms",
			GatewayURL: "ws://browcord-gateway:8080",
			Seccomp:    testSeccomp(t),
			Env:        map[string]string{"ROOM_WIDTH": "1280"},
		},
	}, NewEngine(engineSrv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)
	return s, docker, api
}

func call(t *testing.T, api *httptest.Server, method, path, body, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, api.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func agentToken(room string) string {
	return roomspec.AgentToken([]byte("secret"), room)
}

func ensureBody(room string) string {
	return `{"token":"` + agentToken(room) + `"}`
}

func TestCreatedRoomCarriesTheFullHardeningSet(t *testing.T) {
	_, docker, api := newGuard(t, 2)

	code, body := call(t, api, http.MethodPut, "/rooms/i-123", ensureBody("i-123"), guardToken)
	if code != http.StatusOK {
		t.Fatalf("ensure = %d %s", code, body)
	}
	if len(docker.creates) != 1 {
		t.Fatalf("%d creates, want 1", len(docker.creates))
	}

	c := docker.creates[0]
	h := c.HostConfig
	switch {
	case h.Privileged:
		t.Error("room is privileged")
	case !h.ReadonlyRootfs:
		t.Error("room root filesystem is writable")
	case len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL":
		t.Errorf("CapDrop = %v", h.CapDrop)
	case len(h.CapAdd) != 0:
		t.Errorf("CapAdd = %v", h.CapAdd)
	case len(h.Binds) != 0 || len(h.Mounts) != 0 || len(h.Devices) != 0:
		t.Error("room has host mounts or devices")
	case h.NetworkMode != "browcord-rooms":
		t.Errorf("NetworkMode = %q", h.NetworkMode)
	case h.PidMode != "" || h.UsernsMode != "" || h.IpcMode != "private":
		t.Errorf("namespace modes pid=%q userns=%q ipc=%q", h.PidMode, h.UsernsMode, h.IpcMode)
	case h.PublishAll:
		t.Error("room publishes ports")
	case h.Memory != 4*gib || h.MemorySwap != h.Memory || h.PidsLimit != 768 || h.NanoCpus != 2_500_000_000:
		t.Errorf("limits mem=%d swap=%d pids=%d cpu=%d", h.Memory, h.MemorySwap, h.PidsLimit, h.NanoCpus)
	case h.RestartPolicy.Name != "on-failure":
		t.Errorf("restart policy %q, the entrypoint relies on exiting to be restarted", h.RestartPolicy.Name)
	}

	var seccomp, nnp bool
	for _, opt := range h.SecurityOpt {
		if opt == "no-new-privileges:true" {
			nnp = true
		}
		if strings.HasPrefix(opt, "seccomp={") {
			seccomp = true
		}
		if strings.Contains(opt, "unconfined") {
			t.Errorf("security opt %q", opt)
		}
	}
	if !seccomp || !nnp {
		t.Errorf("SecurityOpt missing seccomp=%v nnp=%v", seccomp, nnp)
	}

	for _, path := range []string{"/tmp", "/profile", "/home/browcord"} {
		if _, ok := h.Tmpfs[path]; !ok {
			t.Errorf("tmpfs %s missing, the read only room cannot start without it", path)
		}
	}

	env := strings.Join(c.Env, "\n")
	for _, want := range []string{"BROWCORD_ROOM_ID=i-123", "BROWCORD_AGENT_TOKEN=" + agentToken("i-123"), "ROOM_WIDTH=1280"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	if c.Labels[roomspec.LabelManaged] != "1" || c.Labels[roomspec.LabelRoom] != "i-123" {
		t.Errorf("labels = %v", c.Labels)
	}
	if strings.Contains(fmt.Sprint(c.Labels), "com.docker.compose") {
		t.Error("room carries a compose label and would be removed as an orphan")
	}
	if docker.names[0] != roomspec.ContainerName("i-123") {
		t.Errorf("container name %q", docker.names[0])
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	_, docker, api := newGuard(t, 2)
	for i := 0; i < 3; i++ {
		if code, body := call(t, api, http.MethodPut, "/rooms/i-1", ensureBody("i-1"), guardToken); code != http.StatusOK {
			t.Fatalf("ensure %d = %d %s", i, code, body)
		}
	}
	if len(docker.creates) != 1 {
		t.Fatalf("%d creates for one room", len(docker.creates))
	}
}

func TestCapIsEnforcedByTheGuard(t *testing.T) {
	_, docker, api := newGuard(t, 2)
	call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken)
	call(t, api, http.MethodPut, "/rooms/b", ensureBody("b"), guardToken)

	code, _ := call(t, api, http.MethodPut, "/rooms/c", ensureBody("c"), guardToken)
	if code != http.StatusConflict {
		t.Fatalf("third room = %d, want 409", code)
	}
	if len(docker.creates) != 2 {
		t.Fatalf("%d containers created past a cap of 2", len(docker.creates))
	}
}

func TestConcurrentEnsuresCannotBreachTheCap(t *testing.T) {
	_, docker, api := newGuard(t, 2)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			room := fmt.Sprintf("r%d", i)
			call(t, api, http.MethodPut, "/rooms/"+room, ensureBody(room), guardToken)
		}(i)
	}
	wg.Wait()
	if len(docker.creates) > 2 {
		t.Fatalf("%d containers created concurrently past a cap of 2", len(docker.creates))
	}
}

func TestGuardRefusesWithoutItsToken(t *testing.T) {
	_, docker, api := newGuard(t, 2)
	for _, token := range []string{"", "wrong", guardToken + "x"} {
		if code, _ := call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), token); code != http.StatusUnauthorized {
			t.Errorf("token %q got %d, want 401", token, code)
		}
	}
	if code, _ := call(t, api, http.MethodGet, "/rooms", "", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list = %d", code)
	}
	if len(docker.requests) != 0 {
		t.Fatalf("docker was called %d times without authentication", len(docker.requests))
	}
}

func TestGuardRefusesHostileInputBeforeTouchingDocker(t *testing.T) {
	_, docker, api := newGuard(t, 2)
	cases := []struct{ path, body string }{
		{"/rooms/..%2f..%2fetc", ensureBody("a")},
		{"/rooms/a%20b", ensureBody("a")},
		{"/rooms/a", `{"token":"short"}`},
		{"/rooms/a", `{"token":"` + agentToken("a") + `","image":"alpine","privileged":true}`},
		{"/rooms/a", `{"token":"` + agentToken("a") + `","HostConfig":{"Binds":["/:/host"]}}`},
		{"/rooms/a", `not json`},
		{"/rooms/a", `{"token":"` + agentToken("a") + `"}` + strings.Repeat(" ", 2000) + `x`},
	}
	for _, c := range cases {
		code, _ := call(t, api, http.MethodPut, c.path, c.body, guardToken)
		if code == http.StatusOK {
			t.Errorf("PUT %s %.60s was accepted", c.path, c.body)
		}
	}
	for _, path := range []string{"/containers/create", "/containers/json", "/exec/x/start", "/images/create"} {
		if code, _ := call(t, api, http.MethodPost, path, "{}", guardToken); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("raw docker path %s answered %d", path, code)
		}
	}
	if len(docker.creates) != 0 {
		t.Fatalf("hostile input created %d containers", len(docker.creates))
	}
}

func TestRemoveOnlyTouchesItsOwnRoom(t *testing.T) {
	_, docker, api := newGuard(t, 3)
	call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken)
	call(t, api, http.MethodPut, "/rooms/b", ensureBody("b"), guardToken)

	docker.mu.Lock()
	docker.containers = append(docker.containers, Container{
		ID:     fmt.Sprintf("%064x", 999),
		Labels: map[string]string{roomspec.LabelRoom: "a"},
		State:  "running",
	})
	docker.mu.Unlock()

	if code, _ := call(t, api, http.MethodDelete, "/rooms/a", "", guardToken); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if len(docker.removed) != 1 {
		t.Fatalf("removed %v, want only room a's managed container", docker.removed)
	}
	if docker.removed[0] == fmt.Sprintf("%064x", 999) {
		t.Fatal("an unmanaged container that merely claimed the room label was removed")
	}
}

func TestReapRemovesRoomsPastTheCeiling(t *testing.T) {
	s, docker, _ := newGuard(t, 3)
	now := time.Now()
	docker.containers = []Container{
		{ID: fmt.Sprintf("%064x", 1), Labels: map[string]string{roomspec.LabelManaged: "1", roomspec.LabelRoom: "old"}, State: "running", Created: now.Add(-7 * time.Hour).Unix()},
		{ID: fmt.Sprintf("%064x", 2), Labels: map[string]string{roomspec.LabelManaged: "1", roomspec.LabelRoom: "new"}, State: "running", Created: now.Add(-time.Hour).Unix()},
	}
	s.Reap(context.Background(), now)
	if len(docker.removed) != 1 || docker.removed[0] != fmt.Sprintf("%064x", 1) {
		t.Fatalf("reap removed %v, want only the 7 hour old room", docker.removed)
	}
}

func TestSeccompProfileMustDenyByDefault(t *testing.T) {
	if _, err := CompactSeccomp([]byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`)); err == nil {
		t.Error("an allow by default profile was accepted")
	}
	if _, err := CompactSeccomp([]byte(`{}`)); err == nil {
		t.Error("a profile without a default action was accepted")
	}
	if _, err := CompactSeccomp([]byte(`not json`)); err == nil {
		t.Error("junk was accepted as a profile")
	}
}

func TestTemplateRefusesHostNetworkAndReservedEnv(t *testing.T) {
	base := Template{Image: "i", Network: "browcord-rooms", GatewayURL: "ws://g", Seccomp: "{}"}
	for _, network := range []string{"host", "bridge", "none", ""} {
		bad := base
		bad.Network = network
		if bad.Validate() == nil {
			t.Errorf("network %q accepted", network)
		}
	}
	bad := base
	bad.Env = map[string]string{"BROWCORD_AGENT_TOKEN": "x"}
	if bad.Validate() == nil {
		t.Error("a templated agent token was accepted")
	}
}
