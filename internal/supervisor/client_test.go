package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "guard-token-for-tests-0123456789abcdef"

func TestClientSpeaksTheGuardProtocol(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rooms/full":
			http.Error(w, "room cap reached", http.StatusConflict)
		case r.Method == http.MethodPut:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || body["token"] == "" {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(Room{ID: strings.TrimPrefix(r.URL.Path, "/rooms/"), State: "running"})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]Room{{ID: "a"}, {ID: "../evil"}})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if room, err := c.Ensure(ctx, "i-1", "t"); err != nil || room.ID != "i-1" {
		t.Fatalf("Ensure = %+v, %v", room, err)
	}
	if _, err := c.Ensure(ctx, "full", "t"); !errors.Is(err, ErrFull) {
		t.Fatalf("a full guard gave %v, want ErrFull", err)
	}
	if _, err := c.Ensure(ctx, "../x", "t"); err == nil {
		t.Fatal("an invalid room id reached the guard")
	}
	if err := c.Remove(ctx, "i-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	rooms, err := c.List(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].ID != "a" {
		t.Fatalf("List = %+v, %v; a malformed id from the guard must be dropped", rooms, err)
	}
	for _, s := range seen {
		if strings.Contains(s, "..") {
			t.Fatalf("request %q carried a traversal", s)
		}
	}
}

func TestClientRefusesAWeakToken(t *testing.T) {
	if _, err := New("http://guard", "short"); err == nil {
		t.Fatal("a short guard token was accepted")
	}
}
