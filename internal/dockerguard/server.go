package dockerguard

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

var ErrFull = errors.New("dockerguard: room cap reached")

type Config struct {
	Token    string
	Cap      int
	Ceiling  time.Duration
	Template Template
}

type Server struct {
	cfg    Config
	engine *Engine
	mu     sync.Mutex
}

type RoomInfo struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Created int64  `json:"created"`
}

func New(cfg Config, engine *Engine) (*Server, error) {
	if len(cfg.Token) < 32 {
		return nil, errors.New("dockerguard: token must be at least 32 characters")
	}
	if cfg.Cap < 1 {
		return nil, errors.New("dockerguard: room cap must be at least 1")
	}
	if cfg.Ceiling <= 0 {
		return nil, errors.New("dockerguard: a room lifetime ceiling is required")
	}
	if err := cfg.Template.Validate(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, engine: engine}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms", s.list)
	mux.HandleFunc("PUT /rooms/{id}", s.ensure)
	mux.HandleFunc("DELETE /rooms/{id}", s.remove)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return s.authorise(mux)
}

func (s *Server) authorise(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.cfg.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.Rooms(r.Context())
	if err != nil {
		log.Printf("dockerguard: list: %v", err)
		http.Error(w, "docker unavailable", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, rooms)
}

func (s *Server) ensure(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if roomspec.ValidID(id) != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body struct {
		Token string `json:"token"`
	}
	if err := dec.Decode(&body); err != nil || dec.Decode(&struct{}{}) != io.EOF || roomspec.ValidToken(body.Token) != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	info, err := s.Ensure(r.Context(), id, body.Token)
	switch {
	case errors.Is(err, ErrFull):
		http.Error(w, "room cap reached", http.StatusConflict)
	case err != nil:
		log.Printf("dockerguard: ensure %s: %v", id, err)
		http.Error(w, "could not start room", http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusOK, info)
	}
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if roomspec.ValidID(id) != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}
	if err := s.Remove(r.Context(), id); err != nil {
		log.Printf("dockerguard: remove %s: %v", id, err)
		http.Error(w, "could not remove room", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Rooms(ctx context.Context) ([]RoomInfo, error) {
	list, err := s.engine.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoomInfo, 0, len(list))
	for _, c := range list {
		out = append(out, RoomInfo{ID: c.Room(), State: c.State, Created: c.Created})
	}
	return out, nil
}

func (s *Server) Ensure(ctx context.Context, id, token string) (RoomInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.engine.List(ctx)
	if err != nil {
		return RoomInfo{}, err
	}

	for _, c := range list {
		if c.Room() != id {
			continue
		}
		if c.State != "running" && c.State != "restarting" {
			if err := s.engine.Start(ctx, c.ID); err != nil {
				return RoomInfo{}, err
			}
		}
		return RoomInfo{ID: id, State: "running", Created: c.Created}, nil
	}

	if len(list) >= s.cfg.Cap {
		return RoomInfo{}, ErrFull
	}

	body, err := s.cfg.Template.Body(id, token)
	if err != nil {
		return RoomInfo{}, err
	}

	containerID, err := s.engine.Create(ctx, roomspec.ContainerName(id), body)
	if err != nil {
		return RoomInfo{}, err
	}
	if err := s.engine.Start(ctx, containerID); err != nil {
		_ = s.engine.Remove(context.WithoutCancel(ctx), containerID)
		return RoomInfo{}, err
	}

	log.Printf("dockerguard: started room %s as %s", id, roomspec.ContainerName(id))
	return RoomInfo{ID: id, State: "running", Created: time.Now().Unix()}, nil
}

func (s *Server) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.engine.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.Room() != id {
			continue
		}
		if err := s.engine.Remove(ctx, c.ID); err != nil {
			return err
		}
		log.Printf("dockerguard: removed room %s", id)
	}
	return nil
}

func (s *Server) Reap(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.engine.List(ctx)
	if err != nil {
		log.Printf("dockerguard: reap: %v", err)
		return
	}
	for _, c := range list {
		age := now.Sub(time.Unix(c.Created, 0))
		if age <= s.cfg.Ceiling {
			continue
		}
		log.Printf("dockerguard: room %s is %s old, past the %s ceiling, removing", c.Room(), age.Truncate(time.Second), s.cfg.Ceiling)
		if err := s.engine.Remove(ctx, c.ID); err != nil {
			log.Printf("dockerguard: reap %s: %v", c.Room(), err)
		}
	}
}

func (s *Server) RunReaper(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.Reap(ctx, now)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
