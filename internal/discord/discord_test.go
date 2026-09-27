package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsMemberAsksDiscordWithTheUsersOwnToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/users/@me/guilds/111/member":
			_, _ = w.Write([]byte(`{"roles":[]}`))
		case "/users/@me/guilds/222/member":
			http.Error(w, `{"message":"Unknown Guild"}`, http.StatusNotFound)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := New("id", "secret")
	c.API = srv.URL
	ctx := context.Background()

	if ok, err := c.IsMember(ctx, "user-token", "111"); !ok || err != nil {
		t.Fatalf("member of 111 = %v, %v", ok, err)
	}
	if ok, err := c.IsMember(ctx, "user-token", "222"); ok || err != nil {
		t.Fatalf("non member of 222 = %v, %v", ok, err)
	}
	if ok, err := c.IsMember(ctx, "stolen-token", "111"); ok || err == nil {
		t.Fatalf("a rejected token was treated as membership: %v, %v", ok, err)
	}
	for _, bad := range []string{"", "../../users/@me", "111/../222", "abc"} {
		if ok, err := c.IsMember(ctx, "user-token", bad); ok || err == nil {
			t.Errorf("guild id %q was sent to discord", bad)
		}
	}
}
