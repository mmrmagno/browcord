package authz

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(GenerateSecret())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestIssueAndVerify(t *testing.T) {
	s := testSigner(t)

	token, err := s.Issue(Session{UserID: "u1", Name: "marcos", RoomID: "room-a", Owner: true}, time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != "u1" || got.Name != "marcos" || got.RoomID != "room-a" || !got.Owner {
		t.Errorf("session = %+v", got)
	}
}

func TestSecretMustBeLongEnough(t *testing.T) {
	if _, err := NewSigner([]byte("short")); err == nil {
		t.Error("NewSigner accepted a 5 byte secret")
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	s := testSigner(t)
	token, _ := s.Issue(Session{UserID: "u1", RoomID: "room-a"}, time.Minute)

	payload, sig, _ := strings.Cut(token, ".")
	forgedPayload := payload[:len(payload)-2] + "AA"

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", ErrMalformed},
		{"no separator", "abcdef", ErrMalformed},
		{"empty signature", payload + ".", ErrMalformed},
		{"altered payload", forgedPayload + "." + sig, ErrSignature},
		{"altered signature", payload + "." + strings.Repeat("A", len(sig)), ErrSignature},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Verify(tc.token); !errors.Is(err, tc.want) {
				t.Errorf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTokenFromAnotherSignerIsRejected(t *testing.T) {
	a, b := testSigner(t), testSigner(t)

	token, _ := a.Issue(Session{UserID: "u1", RoomID: "room-a"}, time.Minute)
	if _, err := b.Verify(token); !errors.Is(err, ErrSignature) {
		t.Errorf("Verify with a different secret = %v, want %v", err, ErrSignature)
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	s := testSigner(t)
	token, _ := s.Issue(Session{UserID: "u1", RoomID: "room-a"}, -time.Second)

	if _, err := s.Verify(token); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify = %v, want %v", err, ErrExpired)
	}
}

func TestTokenIsScopedToOneRoom(t *testing.T) {
	s := testSigner(t)
	token, _ := s.Issue(Session{UserID: "u1", RoomID: "room-a"}, time.Minute)

	if _, err := s.VerifyForRoom(token, "room-a"); err != nil {
		t.Fatalf("VerifyForRoom on the right room: %v", err)
	}
	if _, err := s.VerifyForRoom(token, "room-b"); !errors.Is(err, ErrWrongRoom) {
		t.Errorf("a token for room-a must not open room-b, got %v", err)
	}
}

func TestAllowListDefaultDenies(t *testing.T) {
	empty := NewGuildAllowList(nil, nil)
	if empty.Enabled() {
		t.Error("an empty allow-list must not be considered configured")
	}
	if empty.Permit("any-guild", "any-user") {
		t.Error("an unconfigured allow-list must deny everything, not permit everything")
	}
}

func TestAllowListPermitsConfiguredOnly(t *testing.T) {
	a := NewGuildAllowList([]string{"guild-1"}, []string{"user-9"})

	if !a.Permit("guild-1", "someone") {
		t.Error("allowed guild rejected")
	}
	if !a.Permit("other-guild", "user-9") {
		t.Error("allowed user rejected")
	}
	if a.Permit("other-guild", "someone-else") {
		t.Error("unknown guild and user permitted")
	}
	if a.Permit("", "") {
		t.Error("empty identifiers permitted")
	}
}

func TestLimiterAllowsBurstThenThrottles(t *testing.T) {
	l := NewLimiter(10, 3)

	for i := 0; i < 3; i++ {
		if !l.Allow("user-1") {
			t.Fatalf("request %d within burst was denied", i)
		}
	}
	if l.Allow("user-1") {
		t.Error("burst exhausted but request still allowed")
	}

	if !l.Allow("user-2") {
		t.Error("a different user must have their own bucket")
	}
}

func TestLimiterRefills(t *testing.T) {
	l := NewLimiter(100, 1)

	if !l.Allow("user-1") {
		t.Fatal("first request denied")
	}
	if l.Allow("user-1") {
		t.Fatal("second immediate request should be denied")
	}

	time.Sleep(30 * time.Millisecond)

	if !l.Allow("user-1") {
		t.Error("bucket did not refill")
	}
}
