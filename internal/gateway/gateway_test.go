package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mmrmagno/browcord/internal/authz"
	"github.com/mmrmagno/browcord/internal/wire"
)

const testAgentToken = "agent-token-for-tests"

func newTestGateway(t *testing.T) (*Gateway, *httptest.Server) {
	t.Helper()

	g, err := New(Config{
		StaticDir:   t.TempDir(),
		ClientID:    "test-client",
		AgentToken:  testAgentToken,
		Secret:      authz.GenerateSecret(),
		DevIdentity: true,
		RoomIdle:    time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, srv
}

func mintSession(t *testing.T, srv *httptest.Server, instanceID string) (token, userID string) {
	t.Helper()

	body := strings.NewReader(`{"instanceId":"` + instanceID + `"}`)
	resp, err := http.Post(srv.URL+"/api/token", "application/json", body)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token request returned %d", resp.StatusCode)
	}

	var payload struct {
		Token  string `json:"token"`
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return payload.Token, payload.UserID
}

func wsDial(t *testing.T, url string, header http.Header) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func wsURLOf(srv *httptest.Server, path, query string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path + "?" + query
}

func readUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, want string) []byte {
	t.Helper()

	for i := 0; i < 10; i++ {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read while waiting for %q: %v", want, err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			continue
		}
		if probe.Type == want {
			return data
		}
	}
	t.Fatalf("never received a %q message", want)
	return nil
}

func agentHeader() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+testAgentToken)
	return h
}

func TestAgentMediaReachesViewer(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-1")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-1"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/media", "room=room-1&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	send := func(c wire.Chunk) {
		encoded, err := c.Append(nil)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := agent.Write(ctx, websocket.MessageBinary, encoded); err != nil {
			t.Fatalf("agent write: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	send(wire.Chunk{Type: wire.VideoConfig, PTS: 0, Payload: []byte{0x67, 0x42}})
	send(wire.Chunk{Type: wire.VideoKey, PTS: 1000, Payload: []byte{0x65}})
	send(wire.Chunk{Type: wire.VideoDelta, PTS: 2000, Payload: []byte{0x41}})

	want := []wire.Type{wire.VideoConfig, wire.VideoKey, wire.VideoDelta}
	for i, expect := range want {
		typ, data, err := viewer.Read(ctx)
		if err != nil {
			t.Fatalf("viewer read %d: %v", i, err)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("message %d is not binary", i)
		}
		chunk, err := wire.Unmarshal(data)
		if err != nil {
			t.Fatalf("viewer got a malformed chunk: %v", err)
		}
		if chunk.Type != expect {
			t.Errorf("chunk %d is %v, want %v", i, chunk.Type, expect)
		}
	}
}

func TestLateViewerGetsConfigAndKeyframe(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-late")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-late"), agentHeader())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, c := range []wire.Chunk{
		{Type: wire.VideoConfig, PTS: 0, Payload: []byte{0x67}},
		{Type: wire.VideoKey, PTS: 1000, Payload: []byte{0x65}},
		{Type: wire.VideoDelta, PTS: 2000, Payload: []byte{0x41}},
	} {
		encoded, _ := c.Append(nil)
		if err := agent.Write(ctx, websocket.MessageBinary, encoded); err != nil {
			t.Fatalf("agent write: %v", err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	viewer := wsDial(t, wsURLOf(srv, "/ws/media", "room=room-late&token="+token), nil)

	for i, expect := range []wire.Type{wire.VideoConfig, wire.VideoKey} {
		_, data, err := viewer.Read(ctx)
		if err != nil {
			t.Fatalf("late viewer read %d: %v", i, err)
		}
		chunk, _ := wire.Unmarshal(data)
		if chunk.Type != expect {
			t.Errorf("late viewer chunk %d is %v, want %v; joining mid-stream must not need the next keyframe",
				i, chunk.Type, expect)
		}
	}
}

func TestViewerInputReachesAgent(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-input")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-input"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-input&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := viewer.Read(ctx); err != nil {
		t.Fatalf("expected a hello message: %v", err)
	}

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"navigate","url":"https://example.com"}`)); err != nil {
		t.Fatalf("viewer write: %v", err)
	}

	typ, data, err := agent.Read(ctx)
	if err != nil {
		t.Fatalf("agent read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("agent got a binary message, want text")
	}

	var msg wire.Ctl
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("agent got unparseable command: %v", err)
	}
	if msg.Type != wire.CtlNavigate || msg.URL != "https://example.com" {
		t.Errorf("agent received %+v", msg)
	}
}

func TestPointerBecomesCursorBroadcastAndNotAgentTraffic(t *testing.T) {
	_, srv := newTestGateway(t)
	token, userID := mintSession(t, srv, "room-cursor")

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-cursor&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"pointer","x":0.25,"y":0.75}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	data := readUntil(t, ctx, viewer, "cursors")

	var msg struct {
		Type    string `json:"type"`
		Cursors []struct {
			UserID string  `json:"userId"`
			X      float64 `json:"x"`
		} `json:"cursors"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != "cursors" || len(msg.Cursors) != 1 {
		t.Fatalf("got %s with %d cursors", msg.Type, len(msg.Cursors))
	}
	if msg.Cursors[0].UserID != userID || msg.Cursors[0].X != 0.25 {
		t.Errorf("cursor = %+v", msg.Cursors[0])
	}
}

func TestMalformedControlMessageIsRejectedNotFatal(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-bad")

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-bad&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"exec","cmd":"rm -rf /"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	data := readUntil(t, ctx, viewer, "error")
	if !strings.Contains(string(data), "rejected") {
		t.Errorf("expected a rejection, got %s", data)
	}
}

func TestUnauthorizedAccess(t *testing.T) {
	_, srv := newTestGateway(t)
	tokenA, _ := mintSession(t, srv, "room-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name   string
		url    string
		header http.Header
	}{
		{"no token", wsURLOf(srv, "/ws/media", "room=room-a"), nil},
		{"garbage token", wsURLOf(srv, "/ws/media", "room=room-a&token=nonsense"), nil},
		{"token for another room", wsURLOf(srv, "/ws/media", "room=room-b&token="+tokenA), nil},
		{"agent without token", wsURLOf(srv, "/agent", "room=room-a"), nil},
		{"agent with wrong token", wsURLOf(srv, "/agent", "room=room-a"), func() http.Header {
			h := http.Header{}
			h.Set("Authorization", "Bearer wrong")
			return h
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, _, err := websocket.Dial(ctx, tc.url, &websocket.DialOptions{HTTPHeader: tc.header})
			if err == nil {
				conn.CloseNow()
				t.Fatal("connection was accepted; it must be refused")
			}
		})
	}
}

func TestAgentChunkTooLargeClosesAgent(t *testing.T) {
	_, srv := newTestGateway(t)
	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-big"), agentHeader())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := agent.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, _, err := agent.Read(ctx); err == nil {
		t.Error("gateway kept an agent connection alive after a malformed chunk")
	}
}

func TestGatewayRequiresAllowListWithoutDevIdentity(t *testing.T) {
	_, err := New(Config{
		ClientID:   "c",
		AgentToken: "t",
		Secret:     authz.GenerateSecret(),
	})
	if err == nil {
		t.Fatal("gateway started with no allow-list and no dev identity: it would accept every guild")
	}
}

func TestMediaSocketSurvivesItsOwnPings(t *testing.T) {
	oldInterval, oldTimeout := ctlPingInterval, ctlPingTimeout
	t.Cleanup(func() {
		ctlPingInterval, ctlPingTimeout = oldInterval, oldTimeout
	})
	ctlPingInterval = 50 * time.Millisecond
	ctlPingTimeout = 100 * time.Millisecond

	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-ping")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-ping"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/media", "room=room-ping&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	received := make(chan wire.Chunk, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := viewer.Read(ctx)
			if err != nil {
				readErr <- err
				return
			}
			chunk, err := wire.Unmarshal(data)
			if err != nil {
				readErr <- err
				return
			}
			received <- chunk
		}
	}()

	time.Sleep(6 * ctlPingInterval)

	select {
	case err := <-readErr:
		t.Fatalf("the media socket died across %d ping cycles: %v", 6, err)
	default:
	}

	encoded, err := wire.Chunk{Type: wire.VideoKey, PTS: 1000, Payload: []byte{0x65}}.Append(nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := agent.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		t.Fatalf("agent write: %v", err)
	}

	select {
	case chunk := <-received:
		if chunk.Type != wire.VideoKey {
			t.Errorf("got chunk %v, want %v", chunk.Type, wire.VideoKey)
		}
	case err := <-readErr:
		t.Fatalf("media socket closed instead of delivering a frame: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no frame arrived after the ping cycles")
	}
}

func getJSON(t *testing.T, srv *httptest.Server, path string, header http.Header) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	for k, v := range header {
		req.Header[k] = v
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()

	body := map[string]any{}
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode, body
}

func TestHealthDoesNotLeakRoomDetail(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-secret")
	wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-secret&token="+token), nil)

	code, body := getJSON(t, srv, "/api/health", nil)

	if code != http.StatusOK {
		t.Fatalf("health returned %d, want 200; the container healthcheck depends on it", code)
	}
	if _, present := body["stats"]; present {
		t.Error("health exposes per room stats to unauthenticated callers")
	}
	if _, present := body["agents"]; !present {
		t.Error("health must keep reporting agents; scripts/deploy.sh greps for it")
	}
}

func TestStatsNeedsTheAgentToken(t *testing.T) {
	_, srv := newTestGateway(t)

	if code, _ := getJSON(t, srv, "/api/stats", nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated stats returned %d, want 401", code)
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer wrong-token")
	if code, _ := getJSON(t, srv, "/api/stats", header); code != http.StatusUnauthorized {
		t.Errorf("stats accepted a wrong bearer token, returned %d", code)
	}

	code, body := getJSON(t, srv, "/api/stats", agentHeader())
	if code != http.StatusOK {
		t.Fatalf("authenticated stats returned %d, want 200", code)
	}
	if _, present := body["stats"]; !present {
		t.Error("authenticated stats omitted the per room detail")
	}
}

func assertAgentSilent(t *testing.T, agent *websocket.Conn) {
	t.Helper()

	quiet, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	typ, data, err := agent.Read(quiet)
	if err == nil {
		t.Fatalf("the message reached the agent as %v %q; Chromium would answer with an error toast on every frame", typ, data)
	}
}

func TestStrokeIsBroadcastAndNeverReachesTheAgent(t *testing.T) {
	_, srv := newTestGateway(t)
	token, userID := mintSession(t, srv, "room-ink")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-ink"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-ink&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := viewer.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"stroke","seq":1,"points":[0.25,0.25,0.5,0.5]}`)); err != nil {
		t.Fatalf("viewer write: %v", err)
	}

	raw := readUntil(t, ctx, viewer, wire.CtlDraw)

	var msg struct {
		Strokes []struct {
			UserID string    `json:"userId"`
			Points []float64 `json:"points"`
		} `json:"strokes"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode draw: %v", err)
	}
	if len(msg.Strokes) != 1 {
		t.Fatalf("draw carried %d strokes, want 1", len(msg.Strokes))
	}
	if msg.Strokes[0].UserID != userID {
		t.Errorf("stroke attributed to %q, want %q", msg.Strokes[0].UserID, userID)
	}
	if len(msg.Strokes[0].Points) != 4 {
		t.Errorf("stroke carried %d coordinates, want 4", len(msg.Strokes[0].Points))
	}

	assertAgentSilent(t, agent)
}

func TestClearIsBroadcastAsACanvasAndNeverReachesTheAgent(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-clear")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-clear"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-clear&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := viewer.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"clear","scope":"all"}`)); err != nil {
		t.Fatalf("viewer write: %v", err)
	}

	readUntil(t, ctx, viewer, wire.CtlCanvas)
	assertAgentSilent(t, agent)
}

func TestColourIsBroadcastAndNeverReachesTheAgent(t *testing.T) {
	_, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-colour")

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-colour"), agentHeader())
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-colour&token="+token), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"color","color":5}`)); err != nil {
		t.Fatalf("viewer write: %v", err)
	}

	seen := []int{}
	for i := 0; i < 10; i++ {
		raw := readUntil(t, ctx, viewer, wire.CtlPresence)

		var msg struct {
			Presence []struct {
				Color int `json:"color"`
			} `json:"presence"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode presence: %v", err)
		}
		if len(msg.Presence) > 0 {
			seen = append(seen, msg.Presence[0].Color)
			if msg.Presence[0].Color == 5 {
				assertAgentSilent(t, agent)
				return
			}
		}
	}

	t.Fatalf("presence colours seen were %v, want the chosen colour 5 to reach other viewers", seen)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func agentAttached(g *Gateway, roomID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.agents[roomID] != nil
}

func sendChunk(t *testing.T, ctx context.Context, agent *websocket.Conn, c wire.Chunk) {
	t.Helper()

	encoded, err := c.Append(nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := agent.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		t.Fatalf("agent write: %v", err)
	}
}

func readStatus(t *testing.T, ctx context.Context, viewer *websocket.Conn) string {
	t.Helper()

	var msg struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(readUntil(t, ctx, viewer, wire.CtlStatus), &msg); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return msg.State
}

func TestWatchdogKicksThenRestartsAStaleAgent(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-stale")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-stale&token="+token), nil)
	readUntil(t, ctx, viewer, wire.CtlHello)

	first := wsDial(t, wsURLOf(srv, "/agent", "room=room-stale"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-stale") })
	sendChunk(t, ctx, first, wire.Chunk{Type: wire.VideoConfig, Payload: []byte("vp8")})
	waitFor(t, "the video config", func() bool {
		rm, _ := g.rooms.Get("room-stale")
		return rm.HasVideo()
	})

	start := time.Now()
	g.heal(start.Add(stallAfter / 2))
	if !agentAttached(g, "room-stale") {
		t.Fatal("the agent was kicked before the stall window elapsed")
	}

	g.heal(start.Add(stallAfter + time.Second))
	if agentAttached(g, "room-stale") {
		t.Fatal("a stale agent was not dropped")
	}
	if got := readStatus(t, ctx, viewer); got != wire.StatusRecovering {
		t.Errorf("status after kick = %q, want %q", got, wire.StatusRecovering)
	}

	second := wsDial(t, wsURLOf(srv, "/agent", "room=room-stale"), agentHeader())
	waitFor(t, "the agent to reattach", func() bool { return agentAttached(g, "room-stale") })

	g.heal(time.Now().Add(stallAfter + time.Second))

	_, data, err := second.Read(ctx)
	if err != nil {
		t.Fatalf("the reconnected agent never heard from the gateway: %v", err)
	}
	cmd, err := wire.ParseAgentCmd(data)
	if err != nil || cmd.Type != wire.AgentRestart {
		t.Fatalf("second escalation sent %q, want a restart command", data)
	}
}

func TestWatchdogGivesANewRoomItsGrace(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-new")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-new&token="+token), nil)
	readUntil(t, ctx, viewer, wire.CtlHello)
	wsDial(t, wsURLOf(srv, "/agent", "room=room-new"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-new") })

	rm, _ := g.rooms.Get("room-new")
	if rm.Stats().SecondsIdle != -1 {
		t.Fatal("precondition: a brand new room should report -1 seconds since its last chunk")
	}

	g.heal(time.Now().Add(stallAfter / 2))
	if !agentAttached(g, "room-new") {
		t.Fatal("a room that has never produced a chunk was treated as infinitely stale")
	}
}

func TestWatchdogIgnoresRoomsWithoutViewers(t *testing.T) {
	g, srv := newTestGateway(t)

	wsDial(t, wsURLOf(srv, "/agent", "room=room-empty"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-empty") })

	g.heal(time.Now().Add(time.Hour))
	if !agentAttached(g, "room-empty") {
		t.Fatal("the watchdog acted on a room nobody is watching")
	}
}

func TestWatchdogReportsOfflineAndLive(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-off")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-off&token="+token), nil)
	readUntil(t, ctx, viewer, wire.CtlHello)

	g.heal(time.Now())
	if got := readStatus(t, ctx, viewer); got != wire.StatusOffline {
		t.Fatalf("status with no agent = %q, want %q", got, wire.StatusOffline)
	}

	late := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-off&token="+token), nil)
	if got := readStatus(t, ctx, late); got != wire.StatusOffline {
		t.Errorf("a late joiner saw %q, want %q", got, wire.StatusOffline)
	}

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-off"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-off") })
	sendChunk(t, ctx, agent, wire.Chunk{Type: wire.VideoConfig, Payload: []byte("vp8")})
	waitFor(t, "the video config", func() bool {
		rm, _ := g.rooms.Get("room-off")
		return rm.HasVideo()
	})

	g.heal(time.Now())
	if got := readStatus(t, ctx, viewer); got != wire.StatusLive {
		t.Errorf("status after the agent returned = %q, want %q", got, wire.StatusLive)
	}
}

func TestViewerCannotSendRestart(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-evil")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agent := wsDial(t, wsURLOf(srv, "/agent", "room=room-evil"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-evil") })
	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-evil&token="+token), nil)

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"restart","reason":"x"}`)); err != nil {
		t.Fatalf("viewer write: %v", err)
	}
	readUntil(t, ctx, viewer, wire.CtlError)
	assertAgentSilent(t, agent)
}

func TestAgentSocketIsPinged(t *testing.T) {
	oldInterval, oldTimeout := agentPingInterval, agentPingTimeout
	t.Cleanup(func() {
		agentPingInterval, agentPingTimeout = oldInterval, oldTimeout
	})
	agentPingInterval = 20 * time.Millisecond
	agentPingTimeout = 50 * time.Millisecond

	g, srv := newTestGateway(t)
	wsDial(t, wsURLOf(srv, "/agent", "room=room-deaf"), agentHeader())
	waitFor(t, "the agent to attach", func() bool { return agentAttached(g, "room-deaf") })

	waitFor(t, "a deaf agent to be dropped", func() bool { return !agentAttached(g, "room-deaf") })
}

func TestExpiredInkIsClearedForEveryone(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-fade")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer := wsDial(t, wsURLOf(srv, "/ws/ctl", "room=room-fade&token="+token), nil)
	hello := readUntil(t, ctx, viewer, wire.CtlHello)
	var greeting struct {
		InkTTL int64 `json:"inkTtl"`
	}
	if err := json.Unmarshal(hello, &greeting); err != nil || greeting.InkTTL != g.cfg.InkTTL.Milliseconds() {
		t.Fatalf("hello carried inkTtl %d, want %d", greeting.InkTTL, g.cfg.InkTTL.Milliseconds())
	}

	if err := viewer.Write(ctx, websocket.MessageText, []byte(`{"type":"stroke","seq":1,"points":[0.1,0.1,0.2,0.2]}`)); err != nil {
		t.Fatalf("stroke: %v", err)
	}
	readUntil(t, ctx, viewer, wire.CtlDraw)

	g.expireInk(time.Now().Add(g.cfg.InkTTL + time.Second))

	var canvas struct {
		Strokes []json.RawMessage `json:"strokes"`
	}
	if err := json.Unmarshal(readUntil(t, ctx, viewer, wire.CtlCanvas), &canvas); err != nil {
		t.Fatalf("decode canvas: %v", err)
	}
	if len(canvas.Strokes) != 0 {
		t.Fatalf("expired ink is still on the canvas: %d strokes", len(canvas.Strokes))
	}
}

func TestGuildAllowListRequiresRealMembership(t *testing.T) {
	discordAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"tok-` + r.FormValue("code") + `","token_type":"Bearer"}`))
		case r.URL.Path == "/oauth2/@me":
			user := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-"), "0000000000000000")
			_, _ = w.Write([]byte(`{"user":{"id":"` + user + `","username":"` + user + `"}}`))
		case r.URL.Path == "/users/@me/guilds/1000/member" && r.Header.Get("Authorization") == "Bearer tok-member0000000000000000":
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/member"):
			http.Error(w, `{"message":"Unknown Guild"}`, http.StatusNotFound)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	defer discordAPI.Close()

	g, err := New(Config{
		StaticDir:   t.TempDir(),
		ClientID:    "test-client",
		AgentToken:  testAgentToken,
		Secret:      authz.GenerateSecret(),
		AllowGuilds: []string{"1000"},
		AllowUsers:  []string{"friend"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.discord.API = discordAPI.URL
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	mint := func(code, guild string) int {
		body := `{"code":"` + code + `0000000000000000","instanceId":"i-1","guildId":"` + guild + `"}`
		resp, err := http.Post(srv.URL+"/api/token", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := mint("stranger", "1000"); code != http.StatusForbidden {
		t.Errorf("a stranger claiming the allowed guild got %d, want 403", code)
	}
	if code := mint("member", "1000"); code != http.StatusOK {
		t.Errorf("a real member of the allowed guild got %d, want 200", code)
	}
	if code := mint("member", "2000"); code != http.StatusForbidden {
		t.Errorf("a member launching from an unlisted guild got %d, want 403", code)
	}
	if code := mint("friend", "2000"); code != http.StatusOK {
		t.Errorf("an allow listed user got %d, want 200", code)
	}
}

func TestTokenRejectsMalformedInputBeforeCallingDiscord(t *testing.T) {
	calls := 0
	discordAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer discordAPI.Close()

	g, err := New(Config{
		StaticDir:   t.TempDir(),
		ClientID:    "c",
		AgentToken:  testAgentToken,
		Secret:      authz.GenerateSecret(),
		AllowGuilds: []string{"1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	g.discord.API = discordAPI.URL
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	for _, body := range []string{
		`{"code":"short","instanceId":"i-1","guildId":"1000"}`,
		`{"code":"` + strings.Repeat("a", 30) + `","instanceId":"i-1","guildId":"` + strings.Repeat("x", 4000) + `"}`,
		`{"code":"` + strings.Repeat("a", 30) + `","instanceId":"` + strings.Repeat("i", 500) + `","guildId":"1000"}`,
		`{"code":"a b c d e f g h i j k l","instanceId":"i-1","guildId":"1000"}`,
	} {
		resp, err := http.Post(srv.URL+"/api/token", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%.60s got %d, want 400", body, resp.StatusCode)
		}
	}
	if calls != 0 {
		t.Fatalf("malformed requests reached discord %d times", calls)
	}
}

func TestTokenEndpointIsRateLimited(t *testing.T) {
	_, srv := newTestGateway(t)
	limited := false
	for i := 0; i < 60; i++ {
		resp, err := http.Post(srv.URL+"/api/token", "application/json", strings.NewReader(`{"instanceId":"i-1"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("sixty sign ins in a burst were all accepted, a flood would reach discord unthrottled")
	}
}

func TestOneUserCannotOpenUnlimitedSockets(t *testing.T) {
	g, srv := newTestGateway(t)
	token, _ := mintSession(t, srv, "room-flood")

	for i := 0; i < maxSocketsPerUser; i++ {
		wsDial(t, wsURLOf(srv, "/ws/media", "room=room-flood&token="+token), nil)
	}
	waitFor(t, "the sockets to join", func() bool {
		rm, ok := g.rooms.Get("room-flood")
		return ok && rm.ViewerCount() == maxSocketsPerUser
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	extra := wsDial(t, wsURLOf(srv, "/ws/media", "room=room-flood&token="+token), nil)
	_, _, err := extra.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("socket %d was not refused: %v", maxSocketsPerUser+1, err)
	}

	rm, _ := g.rooms.Get("room-flood")
	if n := rm.ViewerCount(); n != maxSocketsPerUser {
		t.Fatalf("%d sockets held, want %d", n, maxSocketsPerUser)
	}
}
