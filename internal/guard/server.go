package guard

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

var ErrFull = errors.New("guard: room cap reached")

type Room struct {
	Ref     string
	ID      string
	State   string
	Created int64
}

type Backend interface {
	List(ctx context.Context) ([]Room, error)
	Create(ctx context.Context, id, token string) error
	Revive(ctx context.Context, room Room, token string) error
	Remove(ctx context.Context, room Room) error
}

type Config struct {
	Token   string
	Cap     int
	Ceiling time.Duration
}

type Server struct {
	cfg     Config
	backend Backend
	mu      sync.Mutex
}

type RoomInfo struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Created int64  `json:"created"`
}

func New(cfg Config, backend Backend) (*Server, error) {
	if len(cfg.Token) < 32 {
		return nil, errors.New("guard: token must be at least 32 characters")
	}
	if cfg.Cap < 1 {
		return nil, errors.New("guard: room cap must be at least 1")
	}
	if cfg.Ceiling <= 0 {
		return nil, errors.New("guard: a room lifetime ceiling is required")
	}
	if backend == nil {
		return nil, errors.New("guard: a backend is required")
	}
	return &Server{cfg: cfg, backend: backend}, nil
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
		log.Printf("guard: list: %v", err)
		http.Error(w, "backend unavailable", http.StatusBadGateway)
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
		log.Printf("guard: ensure %s: %v", id, err)
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
		log.Printf("guard: remove %s: %v", id, err)
		http.Error(w, "could not remove room", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Rooms(ctx context.Context) ([]RoomInfo, error) {
	list, err := s.backend.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoomInfo, 0, len(list))
	for _, r := range list {
		out = append(out, RoomInfo{ID: r.ID, State: r.State, Created: r.Created})
	}
	return out, nil
}

func (s *Server) Ensure(ctx context.Context, id, token string) (RoomInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.backend.List(ctx)
	if err != nil {
		return RoomInfo{}, err
	}

	for _, r := range list {
		if r.ID != id {
			continue
		}
		if r.State != "running" && r.State != "restarting" {
			if err := s.backend.Revive(ctx, r, token); err != nil {
				return RoomInfo{}, err
			}
		}
		return RoomInfo{ID: id, State: "running", Created: r.Created}, nil
	}

	if len(list) >= s.cfg.Cap {
		return RoomInfo{}, ErrFull
	}

	if err := s.backend.Create(ctx, id, token); err != nil {
		return RoomInfo{}, err
	}

	log.Printf("guard: started room %s as %s", id, roomspec.ContainerName(id))
	return RoomInfo{ID: id, State: "running", Created: time.Now().Unix()}, nil
}

func (s *Server) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.backend.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range list {
		if r.ID != id {
			continue
		}
		if err := s.backend.Remove(ctx, r); err != nil {
			return err
		}
		log.Printf("guard: removed room %s", id)
	}
	return nil
}

func (s *Server) Reap(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.backend.List(ctx)
	if err != nil {
		log.Printf("guard: reap: %v", err)
		return
	}
	for _, r := range list {
		age := now.Sub(time.Unix(r.Created, 0))
		if age <= s.cfg.Ceiling {
			continue
		}
		log.Printf("guard: room %s is %s old, past the %s ceiling, removing", r.ID, age.Truncate(time.Second), s.cfg.Ceiling)
		if err := s.backend.Remove(ctx, r); err != nil {
			log.Printf("guard: reap %s: %v", r.ID, err)
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
