package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mmrmagno/browcord/internal/authz"
	"github.com/mmrmagno/browcord/internal/roomspec"
	"github.com/mmrmagno/browcord/internal/supervisor"
	"github.com/mmrmagno/browcord/internal/wire"
)

type fakeSupervisor struct {
	mu      sync.Mutex
	cap     int
	rooms   map[string]supervisor.Room
	tokens  map[string]string
	removed []string
}

func newFakeSupervisor(cap int) *fakeSupervisor {
	return &fakeSupervisor{cap: cap, rooms: map[string]supervisor.Room{}, tokens: map[string]string{}}
}

func (f *fakeSupervisor) Ensure(_ context.Context, id, token string) (supervisor.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rooms[id]; ok {
		return r, nil
	}
	if len(f.rooms) >= f.cap {
		return supervisor.Room{}, supervisor.ErrFull
	}
	r := supervisor.Room{ID: id, State: "running", Created: time.Now().Unix()}
	f.rooms[id] = r
	f.tokens[id] = token
	return r, nil
}

func (f *fakeSupervisor) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rooms, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeSupervisor) List(context.Context) ([]supervisor.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []supervisor.Room{}
	for _, r := range f.rooms {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeSupervisor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rooms)
}

func (f *fakeSupervisor) wasRemoved(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.removed {
		if r == id {
			return true
		}
	}
	return false
}

func newMultiGateway(t *testing.T, cap int) (*Gateway, *httptest.Server, *fakeSupervisor) {
	t.Helper()

	sup := newFakeSupervisor(cap)
	g, err := New(Config{
		StaticDir:   t.TempDir(),
		ClientID:    "test-client",
		AgentToken:  testAgentToken,
		Secret:      authz.GenerateSecret(),
		DevIdentity: true,
		RoomIdle:    time.Hour,
		Supervisor:  sup,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, srv, sup
}

func roomAgentHeader(g *Gateway, roomID string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+roomspec.AgentToken(g.cfg.Secret, roomID))
	return h
}

func dialStatus(t *testing.T, url string, header http.Header) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		conn.CloseNow()
		return http.StatusSwitchingProtocols
	}
	if resp == nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return resp.StatusCode
}

func tokenStatus(t *testing.T, srv *httptest.Server, instanceID string) int {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/token", "application/json", strings.NewReader(`{"instanceId":"`+instanceID+`"}`))
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestEachInstanceGetsItsOwnRoom(t *testing.T) {
	g, srv, sup := newMultiGateway(t, 3)

	mintSession(t, srv, "i-a")
	mintSession(t, srv, "i-b")
	mintSession(t, srv, "i-a")

	if n := sup.count(); n != 2 {
		t.Fatalf("%d rooms started for two instances", n)
	}
	if sup.tokens["i-a"] != roomspec.AgentToken(g.cfg.Secret, "i-a") {
		t.Fatal("room i-a was not handed its own derived agent token")
	}
	if sup.tokens["i-a"] == testAgentToken || sup.tokens["i-a"] == sup.tokens["i-b"] {
		t.Fatal("rooms share an agent token, or were given the global one")
	}
}

func TestTokenIsRefusedWhenEveryRoomIsBusy(t *testing.T) {
	_, srv, sup := newMultiGateway(t, 1)
	mintSession(t, srv, "i-a")

	if code := tokenStatus(t, srv, "i-b"); code != http.StatusServiceUnavailable {
		t.Fatalf("token past the cap = %d, want 503", code)
	}
	if n := sup.count(); n != 1 {
		t.Fatalf("%d rooms running past a cap of 1", n)
	}
}

func TestHostileInstanceIDNeverReachesTheSupervisor(t *testing.T) {
	_, srv, sup := newMultiGateway(t, 5)
	for _, id := range []string{"../x", "a b", "a%2fb", strings.Repeat("a", 101)} {
		if code := tokenStatus(t, srv, id); code != http.StatusBadRequest {
			t.Errorf("instanceId %q = %d, want 400", id, code)
		}
	}
	if n := sup.count(); n != 0 {
		t.Fatalf("hostile instance ids started %d rooms", n)
	}
}

func TestRoomAgentsAreConfinedToTheirOwnRoom(t *testing.T) {
	g, srv, _ := newMultiGateway(t, 3)
	mintSession(t, srv, "i-a")
	mintSession(t, srv, "i-b")

	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-b"), roomAgentHeader(g, "i-a")); code != http.StatusUnauthorized {
		t.Errorf("room a's token opened room b: %d", code)
	}
	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-a"), agentHeader()); code != http.StatusUnauthorized {
		t.Errorf("the global agent token opened a supervised room: %d", code)
	}
	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-a"), roomAgentHeader(g, "i-a")); code != http.StatusSwitchingProtocols {
		t.Errorf("room a's own token was refused: %d", code)
	}
}

func TestAgentCannotResurrectAnUnknownRoom(t *testing.T) {
	g, srv, _ := newMultiGateway(t, 3)
	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-ghost"), roomAgentHeader(g, "i-ghost")); code != http.StatusNotFound {
		t.Fatalf("an agent for a room nobody started was accepted: %d", code)
	}
	if _, ok := g.rooms.Get("i-ghost"); ok {
		t.Fatal("the rejected agent created a room anyway")
	}
}

func TestIdleRoomIsDestroyedAndStaysDead(t *testing.T) {
	g, srv, sup := newMultiGateway(t, 3)
	mintSession(t, srv, "i-a")

	g.destroyRoom("i-a")
	waitFor(t, "the container to be removed", func() bool { return sup.wasRemoved("i-a") })

	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-a"), roomAgentHeader(g, "i-a")); code != http.StatusNotFound {
		t.Fatalf("a reaped room's agent reconnected: %d", code)
	}
}

func TestRoomPastItsCeilingIsEnded(t *testing.T) {
	g, srv, sup := newMultiGateway(t, 3)
	token, _ := mintSession(t, srv, "i-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=i-a&token="+token), nil)
	readUntil(t, ctx, viewer, wire.CtlHello)

	g.endExpiredRooms(time.Now().Add(g.cfg.RoomCeiling + time.Minute))

	if got := readStatus(t, ctx, viewer); got != wire.StatusEnding {
		t.Fatalf("status = %q, want %q", got, wire.StatusEnding)
	}
	for {
		if _, _, err := viewer.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("the evicted viewer's socket stayed open")
			}
			break
		}
	}
	waitFor(t, "the container to be removed", func() bool { return sup.wasRemoved("i-a") })
	if g.isManaged("i-a") {
		t.Fatal("an ended room is still managed")
	}
}

func TestGatewayAdoptsRoomsThatSurvivedItsRestart(t *testing.T) {
	g, srv, sup := newMultiGateway(t, 3)
	sup.rooms["i-old"] = supervisor.Room{ID: "i-old", State: "running", Created: time.Now().Add(-time.Hour).Unix()}

	g.syncManaged(context.Background(), time.Now())

	if !g.isManaged("i-old") {
		t.Fatal("a running room was not adopted")
	}
	if code := dialStatus(t, wsURLOf(srv, "/agent", "room=i-old"), roomAgentHeader(g, "i-old")); code != http.StatusSwitchingProtocols {
		t.Fatalf("an adopted room's agent was refused: %d", code)
	}
	if _, ok := g.rooms.Get("i-old"); !ok {
		t.Fatal("an adopted room has no registry entry, so it would never be reaped when idle")
	}
}

func TestFixedRoomAndSupervisorAreExclusive(t *testing.T) {
	_, err := New(Config{
		ClientID:    "c",
		AgentToken:  "t",
		Secret:      authz.GenerateSecret(),
		DevIdentity: true,
		FixedRoom:   "main",
		Supervisor:  newFakeSupervisor(1),
	})
	if err == nil {
		t.Fatal("a fixed room was accepted alongside a supervisor, every instance would share one container")
	}
}

func TestExitedRoomIsStartedAgainWhileWatched(t *testing.T) {
	g, srv, sup := newMultiGateway(t, 3)
	token, _ := mintSession(t, srv, "i-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=i-a&token="+token), nil)
	readUntil(t, ctx, viewer, wire.CtlHello)

	sup.mu.Lock()
	r := sup.rooms["i-a"]
	r.State = "exited"
	sup.rooms["i-a"] = r
	sup.mu.Unlock()

	g.syncManaged(context.Background(), time.Now())
	if g.isManaged("i-a") {
		t.Fatal("an exited container still counts as a managed room")
	}

	sup.mu.Lock()
	r.State = "running"
	sup.rooms["i-a"] = r
	sup.mu.Unlock()

	waitFor(t, "the room to be ensured again", func() bool { return g.isManaged("i-a") })
}
