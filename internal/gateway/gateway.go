package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mmrmagno/browcord/internal/authz"
	"github.com/mmrmagno/browcord/internal/cdp"
	"github.com/mmrmagno/browcord/internal/discord"
	"github.com/mmrmagno/browcord/internal/room"
	"github.com/mmrmagno/browcord/internal/wire"
)

var (
	ctlPingInterval = 25 * time.Second
	ctlPingTimeout  = 10 * time.Second
)

type Config struct {
	Addr         string
	StaticDir    string
	ClientID     string
	ClientSecret string
	AgentToken   string
	Secret       []byte
	AllowGuilds  []string
	AllowUsers   []string
	SessionTTL   time.Duration
	RoomIdle     time.Duration
	DevIdentity  bool
	FixedRoom    string
}

func (c *Config) applyDefaults() {
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	if c.StaticDir == "" {
		c.StaticDir = "web/dist"
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 8 * time.Hour
	}
	if c.RoomIdle == 0 {
		c.RoomIdle = 60 * time.Second
	}
}

type Gateway struct {
	cfg     Config
	rooms   *room.Registry
	signer  *authz.Signer
	allow   *authz.GuildAllowList
	input   *authz.Limiter
	discord *discord.Client

	mu     sync.RWMutex
	agents map[string]*websocket.Conn
}

func New(cfg Config) (*Gateway, error) {
	cfg.applyDefaults()

	signer, err := authz.NewSigner(cfg.Secret)
	if err != nil {
		return nil, err
	}

	if cfg.AgentToken == "" {
		return nil, errors.New("gateway: agent token is required")
	}

	allow := authz.NewGuildAllowList(cfg.AllowGuilds, cfg.AllowUsers)
	if !allow.Enabled() && !cfg.DevIdentity {
		return nil, errors.New("gateway: configure at least one allowed guild or user, or nobody can open a room")
	}

	return &Gateway{
		cfg:     cfg,
		rooms:   room.NewRegistry(),
		signer:  signer,
		allow:   allow,
		input:   authz.NewLimiter(120, 240),
		discord: discord.New(cfg.ClientID, cfg.ClientSecret),
		agents:  make(map[string]*websocket.Conn),
	}, nil
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", g.health)
	mux.HandleFunc("/api/config", g.config)
	mux.HandleFunc("/api/token", g.token)
	mux.HandleFunc("/api/clientlog", g.clientLog)
	mux.HandleFunc("/ws/ctl", g.viewerCtl)
	mux.HandleFunc("/ws/media", g.viewerMedia)
	mux.HandleFunc("/agent", g.agent)
	mux.Handle("/", noStore(http.FileServer(http.Dir(g.cfg.StaticDir))))

	return stripProxyPrefix(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

func stripProxyPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/.proxy")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              g.cfg.Addr,
		Handler:           g.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go g.reapIdleRooms(ctx)

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("gateway: listening on %s", g.cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (g *Gateway) reapIdleRooms(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, r := range g.rooms.IdleRooms(g.cfg.RoomIdle) {
				log.Printf("gateway: reaping idle room %s", r.ID)
				g.closeAgent(r.ID)
				g.rooms.Remove(r.ID)
			}
		}
	}
}

func (g *Gateway) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"clientId": g.cfg.ClientID,
	})
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	agents := len(g.agents)
	g.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"rooms":  g.rooms.Count(),
		"agents": agents,
		"stats":  g.rooms.AllStats(),
	})
}

type tokenRequest struct {
	Code       string `json:"code"`
	InstanceID string `json:"instanceId"`
	GuildID    string `json:"guildId"`
}

func (g *Gateway) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}

	var req tokenRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.InstanceID == "" {
		http.Error(w, "instanceId required", http.StatusBadRequest)
		return
	}

	roomID := req.InstanceID
	if g.cfg.FixedRoom != "" {
		roomID = g.cfg.FixedRoom
	}

	var (
		userID string
		name   string
	)

	if g.cfg.DevIdentity && req.Code == "" {
		userID = "dev-" + randomID()
		name = "dev-" + userID[4:10]
	} else {
		if req.Code == "" {
			log.Printf("gateway: token request carried no oauth code (instance=%q guild=%q)", req.InstanceID, req.GuildID)
			http.Error(w, "no authorization code supplied", http.StatusBadRequest)
			return
		}

		accessToken, err := g.discord.Exchange(r.Context(), req.Code)
		if err != nil {
			log.Printf("gateway: token exchange failed (code len=%d, instance=%q, guild=%q): %v",
				len(req.Code), req.InstanceID, req.GuildID, err)
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		identity, err := g.discord.Identify(r.Context(), accessToken)
		if err != nil {
			log.Printf("gateway: identify failed: %v", err)
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		if !g.allow.Permit(req.GuildID, identity.User.ID) {
			log.Printf("gateway: refused guild=%q user=%q", req.GuildID, identity.User.ID)
			http.Error(w, "not permitted", http.StatusForbidden)
			return
		}

		userID = identity.User.ID
		name = identity.User.DisplayName()
	}

	session, err := g.signer.Issue(authz.Session{
		UserID: userID,
		Name:   name,
		RoomID: roomID,
	}, g.cfg.SessionTTL)
	if err != nil {
		http.Error(w, "could not issue session", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":  session,
		"userId": userID,
		"name":   name,
		"roomId": roomID,
	})
}

func (g *Gateway) clientLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}

	var report struct {
		Token   string `json:"token"`
		Event   string `json:"event"`
		Detail  string `json:"detail"`
		Codec   string `json:"codec"`
		Agent   string `json:"userAgent"`
		Decoded int    `json:"decoded"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	sess, err := g.signer.Verify(report.Token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	log.Printf("client[%q/%q] %q: codec=%q decoded=%d detail=%q ua=%q",
		sess.Name, sess.RoomID, report.Event, report.Codec, report.Decoded, report.Detail, report.Agent)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (g *Gateway) acceptViewer(w http.ResponseWriter, r *http.Request) (*websocket.Conn, authz.Session, bool) {
	roomID := r.URL.Query().Get("room")
	token := r.URL.Query().Get("token")

	sess, err := g.signer.VerifyForRoom(token, roomID)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, authz.Session{}, false
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  g.originPatterns(),
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("gateway: viewer accept: %v", err)
		return nil, authz.Session{}, false
	}

	return conn, sess, true
}

func (g *Gateway) originPatterns() []string {
	if g.cfg.DevIdentity {
		return []string{"*"}
	}
	return []string{fmt.Sprintf("%s.discordsays.com", g.cfg.ClientID)}
}

func (g *Gateway) viewerMedia(w http.ResponseWriter, r *http.Request) {
	conn, sess, ok := g.acceptViewer(w, r)
	if !ok {
		return
	}
	defer conn.CloseNow()

	rm := g.rooms.GetOrCreate(sess.RoomID)
	viewerID := randomID()
	v := rm.Join(viewerID, sess.UserID, sess.Name)
	defer rm.Leave(viewerID)

	ctx := conn.CloseRead(r.Context())

	ping := time.NewTicker(ctlPingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			deadline, cancel := context.WithTimeout(ctx, ctlPingTimeout)
			err := conn.Ping(deadline)
			cancel()
			if err != nil {
				return
			}
		case frame, open := <-v.Media:
			if !open {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				return
			}
		}
	}
}

func (g *Gateway) viewerCtl(w http.ResponseWriter, r *http.Request) {
	conn, sess, ok := g.acceptViewer(w, r)
	if !ok {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxCtlBytes)

	rm := g.rooms.GetOrCreate(sess.RoomID)
	viewerID := randomID()
	v := rm.Join(viewerID, sess.UserID, sess.Name)
	defer func() {
		rm.Leave(viewerID)
		g.input.Forget(sess.UserID)
		g.broadcastCursors(rm)
		g.broadcastPresence(rm)
	}()

	ctx := r.Context()

	go func() {
		ping := time.NewTicker(ctlPingInterval)
		defer ping.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				deadline, cancel := context.WithTimeout(ctx, ctlPingTimeout)
				err := conn.Ping(deadline)
				cancel()
				if err != nil {
					return
				}
			case payload, open := <-v.Ctl:
				if !open {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
					return
				}
			}
		}
	}()

	_ = conn.Write(ctx, websocket.MessageText, wire.ServerMessage{
		Type:   wire.CtlHello,
		UserID: sess.UserID,
		Name:   sess.Name,
	}.Encode())

	g.broadcastPresence(rm)

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		if !g.input.Allow(sess.UserID) {
			continue
		}

		msg, err := wire.ParseCtl(data)
		if err != nil {
			_ = conn.Write(ctx, websocket.MessageText, wire.ServerMessage{
				Type:    wire.CtlError,
				Message: "rejected: " + err.Error(),
			}.Encode())
			continue
		}

		g.handleViewerMessage(ctx, rm, sess, msg)
	}
}

func (g *Gateway) handleViewerMessage(ctx context.Context, rm *room.Room, sess authz.Session, msg wire.Ctl) {
	switch msg.Type {
	case wire.CtlPointer:
		rm.SetCursor(sess.UserID, msg.X, msg.Y)
		g.broadcastCursors(rm)
		return

	case wire.CtlKey:
		if !cdp.IsPlaybackKey(msg.Key) && isPrintable(msg.Key) {
			if !rm.Typing.Acquire(sess.UserID) {
				return
			}
			rm.BroadcastCtl(wire.ServerMessage{
				Type:   wire.CtlTyping,
				UserID: sess.UserID,
				Name:   sess.Name,
			}.Encode())
		}

	case wire.CtlText:
		if !rm.Typing.Acquire(sess.UserID) {
			return
		}
		rm.BroadcastCtl(wire.ServerMessage{
			Type:   wire.CtlTyping,
			UserID: sess.UserID,
			Name:   sess.Name,
		}.Encode())
	}

	g.toAgent(ctx, rm.ID, msg)
}

func isPrintable(key string) bool {
	if key == "" {
		return false
	}
	runes := []rune(key)
	return len(runes) == 1 && runes[0] >= 0x20
}

func (g *Gateway) broadcastPresence(rm *room.Room) {
	rm.BroadcastCtl(wire.ServerMessage{
		Type:     wire.CtlPresence,
		Presence: rm.Participants(),
	}.Encode())
}

func (g *Gateway) broadcastCursors(rm *room.Room) {
	rm.BroadcastCtl(wire.ServerMessage{
		Type:    wire.CtlCursors,
		Cursors: rm.Cursors(),
	}.Encode())
}

func (g *Gateway) toAgent(ctx context.Context, roomID string, msg wire.Ctl) {
	g.mu.RLock()
	conn := g.agents[roomID]
	g.mu.RUnlock()

	if conn == nil {
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = conn.Write(ctx, websocket.MessageText, payload)
}

func (g *Gateway) agent(w http.ResponseWriter, r *http.Request) {
	if !bearerEquals(r.Header.Get("Authorization"), g.cfg.AgentToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	roomID := r.URL.Query().Get("room")
	if roomID == "" {
		http.Error(w, "room required", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("gateway: agent accept: %v", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxPayload + wire.HeaderSize)

	g.mu.Lock()
	g.agents[roomID] = conn
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		if g.agents[roomID] == conn {
			delete(g.agents, roomID)
		}
		g.mu.Unlock()
	}()

	rm := g.rooms.GetOrCreate(roomID)
	rm.ResetStream()
	log.Printf("gateway: agent connected for room %s", roomID)

	ctx := r.Context()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("gateway: agent for room %s disconnected: %v", roomID, err)
			return
		}

		switch typ {
		case websocket.MessageBinary:
			chunk, err := wire.Unmarshal(data)
			if err != nil {
				log.Printf("gateway: agent sent a malformed chunk: %v", err)
				return
			}
			rm.Publish(chunk)

		case websocket.MessageText:
			rm.BroadcastCtl(data)
		}
	}
}

func (g *Gateway) closeAgent(roomID string) {
	g.mu.Lock()
	conn := g.agents[roomID]
	delete(g.agents, roomID)
	g.mu.Unlock()

	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "room closed")
	}
}

func bearerEquals(header, expected string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || expected == "" {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(expected) {
		return false
	}
	var diff byte
	for i := range got {
		diff |= got[i] ^ expected[i]
	}
	return diff == 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
