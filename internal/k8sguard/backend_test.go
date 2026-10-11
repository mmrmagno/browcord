package k8sguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmrmagno/browcord/internal/guard"
	"github.com/mmrmagno/browcord/internal/roomspec"
)

const (
	guardToken = "guard-token-for-tests-0123456789abcdef"
	saToken    = "service-account-token"
	ns         = "browcord-rooms"
	podsPath   = "/api/v1/namespaces/" + ns + "/pods"
)

type fakeAPI struct {
	mu       sync.Mutex
	pods     map[string]Pod
	creates  []Pod
	deletes  []string
	graces   []string
	requests []string
	badAuth  int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+saToken {
		f.badAuth++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, podsPath), "/")

	switch {
	case !strings.HasPrefix(r.URL.Path, podsPath):
		http.Error(w, "wrong namespace", http.StatusForbidden)

	case r.Method == http.MethodGet && name == "":
		if r.URL.Query().Get("labelSelector") != roomspec.LabelManaged+"=1" {
			http.Error(w, "unexpected selector", http.StatusBadRequest)
			return
		}
		list := PodList{Items: []Pod{}}
		for _, p := range f.pods {
			list.Items = append(list.Items, p)
		}
		_ = json.NewEncoder(w).Encode(list)

	case r.Method == http.MethodGet:
		p, ok := f.pods[name]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(p)

	case r.Method == http.MethodPost && name == "":
		var p Pod
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, exists := f.pods[p.Metadata.Name]; exists {
			http.Error(w, "already exists", http.StatusConflict)
			return
		}
		f.creates = append(f.creates, p)
		p.Metadata.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		p.Status.Phase = "Pending"
		f.pods[p.Metadata.Name] = p
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)

	case r.Method == http.MethodDelete:
		f.deletes = append(f.deletes, name)
		f.graces = append(f.graces, r.URL.Query().Get("gracePeriodSeconds"))
		if _, ok := f.pods[name]; !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		delete(f.pods, name)
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

func (f *fakeAPI) put(p Pod) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[p.Metadata.Name] = p
}

func managedPod(room, phase string, created time.Time) Pod {
	return Pod{
		Metadata: ObjectMeta{
			Name:              roomspec.ContainerName(room),
			Labels:            map[string]string{roomspec.LabelManaged: "1", LabelRoomHash: roomHash(room)},
			Annotations:       map[string]string{roomspec.LabelRoom: room},
			CreationTimestamp: created.UTC().Format(time.RFC3339),
		},
		Status: PodStatus{Phase: phase},
	}
}

func testTemplate() Template {
	return Template{
		Image:      "ghcr.io/mmrmagno/browcord-room:latest",
		GatewayURL: "ws://browcord-gateway.browcord.svc:8080",
		Seccomp:    "profiles/browcord-chromium.json",
		Env:        map[string]string{"ROOM_WIDTH": "1280"},
	}
}

func newGuard(t *testing.T, cap int) (*guard.Server, *fakeAPI, *httptest.Server) {
	t.Helper()

	fake := &fakeAPI{pods: map[string]Pod{}}
	apiSrv := httptest.NewServer(fake)
	t.Cleanup(apiSrv.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(saToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		Token:     guardToken,
		Cap:       cap,
		Ceiling:   6 * time.Hour,
		Namespace: ns,
		Template:  testTemplate(),
	}, NewAPI(apiSrv.URL, tokenFile, apiSrv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)
	return s, fake, api
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

func TestCreatedPodCarriesTheFullHardeningSet(t *testing.T) {
	_, fake, api := newGuard(t, 2)

	code, body := call(t, api, http.MethodPut, "/rooms/i-123", ensureBody("i-123"), guardToken)
	if code != http.StatusOK {
		t.Fatalf("ensure = %d %s", code, body)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("%d creates, want 1", len(fake.creates))
	}

	p := fake.creates[0]
	s := p.Spec
	switch {
	case p.Metadata.Name != roomspec.ContainerName("i-123"):
		t.Errorf("pod name %q", p.Metadata.Name)
	case p.Metadata.Namespace != ns:
		t.Errorf("namespace %q", p.Metadata.Namespace)
	case p.Metadata.Labels[roomspec.LabelManaged] != "1" || p.Metadata.Annotations[roomspec.LabelRoom] != "i-123":
		t.Errorf("labels %v annotations %v", p.Metadata.Labels, p.Metadata.Annotations)
	case s.HostNetwork || s.HostPID || s.HostIPC:
		t.Error("pod shares a host namespace")
	case s.HostUsers == nil || *s.HostUsers:
		t.Error("pod runs in the host user namespace")
	case s.AutomountServiceAccountToken == nil || *s.AutomountServiceAccountToken:
		t.Error("pod mounts a service account token")
	case s.EnableServiceLinks == nil || *s.EnableServiceLinks:
		t.Error("pod gets service link env vars")
	case s.RestartPolicy != "Always":
		t.Errorf("restart policy %q, the entrypoint relies on exiting to be restarted", s.RestartPolicy)
	case s.SecurityContext == nil || s.SecurityContext.RunAsNonRoot == nil || !*s.SecurityContext.RunAsNonRoot || *s.SecurityContext.RunAsUser != roomUID:
		t.Error("pod may run as root")
	case s.SecurityContext.SeccompProfile == nil || s.SecurityContext.SeccompProfile.Type != "Localhost" ||
		s.SecurityContext.SeccompProfile.LocalhostProfile != "profiles/browcord-chromium.json":
		t.Errorf("seccomp %+v", s.SecurityContext.SeccompProfile)
	case len(s.Containers) != 1:
		t.Fatalf("%d containers", len(s.Containers))
	}

	c := s.Containers[0]
	sc := c.SecurityContext
	switch {
	case sc == nil:
		t.Fatal("container has no security context")
	case sc.Privileged == nil || *sc.Privileged:
		t.Error("container is privileged")
	case sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation:
		t.Error("container may escalate privileges")
	case sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem:
		t.Error("container root filesystem is writable")
	case sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0:
		t.Errorf("capabilities %+v", sc.Capabilities)
	case len(c.Ports) != 0:
		t.Error("container exposes ports")
	case c.Resources.Limits["memory"] != "4Gi" || c.Resources.Limits["cpu"] != "4":
		t.Errorf("limits %v", c.Resources.Limits)
	case c.Resources.Requests["memory"] != c.Resources.Limits["memory"] || c.Resources.Requests["cpu"] != c.Resources.Limits["cpu"]:
		t.Errorf("requests %v differ from limits, the room would not get Guaranteed QoS", c.Resources.Requests)
	case c.LivenessProbe == nil || c.StartupProbe == nil:
		t.Error("container has no health probes, a stalled browser would never restart")
	}

	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m.Name
	}
	volumes := map[string]Volume{}
	for _, v := range s.Volumes {
		volumes[v.Name] = v
	}
	for _, path := range []string{"/dev/shm", "/tmp", "/profile", "/home/browcord"} {
		v, ok := volumes[mounts[path]]
		if !ok {
			t.Errorf("%s has no volume, the read only room cannot start without it", path)
			continue
		}
		if v.EmptyDir == nil || v.EmptyDir.Medium != "Memory" || v.EmptyDir.SizeLimit == "" || v.HostPath != nil {
			t.Errorf("%s volume %+v is not a bounded memory emptyDir", path, v)
		}
	}
	if len(s.Volumes) != 4 {
		t.Errorf("%d volumes, want exactly the 4 scratch dirs", len(s.Volumes))
	}

	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["BROWCORD_ROOM_ID"] != "i-123" || env["BROWCORD_AGENT_TOKEN"] != agentToken("i-123") || env["ROOM_WIDTH"] != "1280" {
		t.Errorf("env %v", env)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	for i := 0; i < 3; i++ {
		if code, body := call(t, api, http.MethodPut, "/rooms/i-1", ensureBody("i-1"), guardToken); code != http.StatusOK {
			t.Fatalf("ensure %d = %d %s", i, code, body)
		}
	}
	if len(fake.creates) != 1 {
		t.Fatalf("%d creates for one room", len(fake.creates))
	}
}

func TestCapIsEnforcedByTheGuard(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken)
	call(t, api, http.MethodPut, "/rooms/b", ensureBody("b"), guardToken)

	if code, _ := call(t, api, http.MethodPut, "/rooms/c", ensureBody("c"), guardToken); code != http.StatusConflict {
		t.Fatalf("third room = %d, want 409", code)
	}
	if len(fake.creates) != 2 {
		t.Fatalf("%d pods created past a cap of 2", len(fake.creates))
	}
}

func TestConcurrentEnsuresCannotBreachTheCap(t *testing.T) {
	_, fake, api := newGuard(t, 2)
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
	if len(fake.creates) > 2 {
		t.Fatalf("%d pods created concurrently past a cap of 2", len(fake.creates))
	}
}

func TestGuardRefusesWithoutItsToken(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	for _, token := range []string{"", "wrong", guardToken + "x"} {
		if code, _ := call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), token); code != http.StatusUnauthorized {
			t.Errorf("token %q got %d, want 401", token, code)
		}
	}
	if len(fake.requests) != 0 {
		t.Fatalf("the api was called %d times without authentication", len(fake.requests))
	}
}

func TestGuardRefusesHostileInputBeforeTouchingTheAPI(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	cases := []struct{ path, body string }{
		{"/rooms/..%2f..%2fetc", ensureBody("a")},
		{"/rooms/a%20b", ensureBody("a")},
		{"/rooms/a", `{"token":"short"}`},
		{"/rooms/a", `{"token":"` + agentToken("a") + `","spec":{"hostNetwork":true}}`},
		{"/rooms/a", `not json`},
	}
	for _, c := range cases {
		if code, _ := call(t, api, http.MethodPut, c.path, c.body, guardToken); code == http.StatusOK {
			t.Errorf("PUT %s %.60s was accepted", c.path, c.body)
		}
	}
	for _, path := range []string{podsPath, "/api/v1/namespaces/kube-system/pods"} {
		if code, _ := call(t, api, http.MethodPost, path, "{}", guardToken); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("raw api path %s answered %d", path, code)
		}
	}
	if len(fake.creates) != 0 {
		t.Fatalf("hostile input created %d pods", len(fake.creates))
	}
}

func TestListIgnoresPodsThatOnlyClaimToBeRooms(t *testing.T) {
	s, fake, _ := newGuard(t, 3)
	now := time.Now()

	spoofed := managedPod("a", "Running", now)
	spoofed.Metadata.Name = roomspec.ContainerName("b")
	unmanaged := managedPod("c", "Running", now)
	unmanaged.Metadata.Labels = map[string]string{}
	badID := managedPod("d", "Running", now)
	badID.Metadata.Annotations[roomspec.LabelRoom] = "../etc"
	fake.put(spoofed)
	fake.put(unmanaged)
	fake.put(badID)
	fake.put(managedPod("real", "Running", now))

	rooms, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].ID != "real" {
		t.Fatalf("rooms %+v, want only the genuine room", rooms)
	}
}

func TestRemoveOnlyTouchesItsOwnRoom(t *testing.T) {
	_, fake, api := newGuard(t, 3)
	call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken)
	call(t, api, http.MethodPut, "/rooms/b", ensureBody("b"), guardToken)

	if code, _ := call(t, api, http.MethodDelete, "/rooms/a", "", guardToken); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != roomspec.ContainerName("a") {
		t.Fatalf("deleted %v, want only room a", fake.deletes)
	}
	if fake.graces[0] != "5" {
		t.Errorf("grace %q", fake.graces[0])
	}
	if code, _ := call(t, api, http.MethodDelete, "/rooms/a", "", guardToken); code != http.StatusNoContent {
		t.Fatalf("removing a gone room = %d, want 204", code)
	}
}

func TestReapRemovesRoomsPastTheCeiling(t *testing.T) {
	s, fake, _ := newGuard(t, 3)
	now := time.Now()
	fake.put(managedPod("old", "Running", now.Add(-7*time.Hour)))
	fake.put(managedPod("new", "Running", now.Add(-time.Hour)))

	s.Reap(context.Background(), now)
	if len(fake.deletes) != 1 || fake.deletes[0] != roomspec.ContainerName("old") {
		t.Fatalf("reap deleted %v, want only the 7 hour old room", fake.deletes)
	}
}

func TestPodStateMapsToTheGatewayVocabulary(t *testing.T) {
	waiting := managedPod("w", "Running", time.Now())
	waiting.Status.ContainerStatuses = []ContainerStatus{{Name: containerName, RestartCount: 2, State: ContainerState{Waiting: &struct{}{}}}}
	running := managedPod("r", "Running", time.Now())
	running.Status.ContainerStatuses = []ContainerStatus{{Name: containerName, State: ContainerState{Running: &struct{}{}}}}
	deleting := managedPod("d", "Running", time.Now())
	deleting.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339)

	cases := map[string]Pod{
		"created":    managedPod("p", "Pending", time.Now()),
		"running":    running,
		"restarting": waiting,
		"exited":     managedPod("f", "Failed", time.Now()),
	}
	for want, p := range cases {
		if got := podState(p); got != want {
			t.Errorf("%s pod mapped to %q, want %q", p.Status.Phase, got, want)
		}
	}
	if got := podState(deleting); got != "exited" {
		t.Errorf("a terminating pod mapped to %q", got)
	}
	if got := podState(managedPod("s", "Succeeded", time.Now())); got != "exited" {
		t.Errorf("a succeeded pod mapped to %q", got)
	}
}

func TestEnsureReplacesAnExitedPod(t *testing.T) {
	pollInterval = time.Millisecond
	_, fake, api := newGuard(t, 2)
	fake.put(managedPod("a", "Failed", time.Now().Add(-time.Minute)))

	if code, body := call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken); code != http.StatusOK {
		t.Fatalf("ensure = %d %s", code, body)
	}
	if len(fake.deletes) != 1 || len(fake.creates) != 1 {
		t.Fatalf("deletes %v creates %d, want the failed pod replaced", fake.deletes, len(fake.creates))
	}
	if fake.pods[roomspec.ContainerName("a")].Status.Phase != "Pending" {
		t.Fatal("the room was not recreated")
	}
}

func TestEnsureLeavesAPendingPodAlone(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	fake.put(managedPod("a", "Pending", time.Now()))
	if code, _ := call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken); code != http.StatusOK {
		t.Fatalf("ensure = %d", code)
	}
	if len(fake.deletes) != 0 || len(fake.creates) != 0 {
		t.Fatalf("a starting pod was touched: deletes %v creates %d", fake.deletes, len(fake.creates))
	}
}

func TestEveryAPIRequestCarriesTheServiceAccountToken(t *testing.T) {
	_, fake, api := newGuard(t, 2)
	call(t, api, http.MethodPut, "/rooms/a", ensureBody("a"), guardToken)
	call(t, api, http.MethodDelete, "/rooms/a", "", guardToken)
	if fake.badAuth != 0 || len(fake.requests) == 0 {
		t.Fatalf("%d of %d requests lacked the token", fake.badAuth, len(fake.requests))
	}
}

func TestConfigRefusesUnsafeSettings(t *testing.T) {
	api := NewAPI("http://x", "", http.DefaultClient)
	base := Config{Token: guardToken, Cap: 1, Ceiling: time.Hour, Namespace: ns, Template: testTemplate()}

	for _, n := range []string{"", "Bad", "a/b", "../kube-system"} {
		c := base
		c.Namespace = n
		if _, err := New(c, api); err == nil {
			t.Errorf("namespace %q accepted", n)
		}
	}
	for _, profile := range []string{"", "/etc/passwd", "../../x.json"} {
		c := base
		c.Template.Seccomp = profile
		if _, err := New(c, api); err == nil {
			t.Errorf("seccomp profile %q accepted", profile)
		}
	}
	c := base
	c.Template.Env = map[string]string{"BROWCORD_AGENT_TOKEN": "x"}
	if _, err := New(c, api); err == nil {
		t.Error("a templated agent token was accepted")
	}
}
