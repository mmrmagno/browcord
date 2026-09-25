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
