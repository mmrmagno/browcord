package authz

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrMalformed = errors.New("authz: token malformed")
	ErrSignature = errors.New("authz: signature mismatch")
	ErrExpired   = errors.New("authz: token expired")
	ErrWrongRoom = errors.New("authz: token is not valid for this room")
)

type Session struct {
	UserID  string `json:"u"`
	Name    string `json:"n"`
	RoomID  string `json:"r"`
	Owner   bool   `json:"o,omitempty"`
	Expires int64  `json:"e"`
}

type Signer struct {
	secret []byte
}

func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("authz: secret must be at least 32 bytes, got %d", len(secret))
	}
	return &Signer{secret: append([]byte(nil), secret...)}, nil
}

func GenerateSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func (s *Signer) Issue(sess Session, ttl time.Duration) (string, error) {
	sess.Expires = time.Now().Add(ttl).Unix()

	payload, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + s.sign(encoded), nil
}

func (s *Signer) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Signer) Verify(token string) (Session, error) {
	payload, signature, ok := strings.Cut(token, ".")
	if !ok || payload == "" || signature == "" {
		return Session{}, ErrMalformed
	}

	if !hmac.Equal([]byte(signature), []byte(s.sign(payload))) {
		return Session{}, ErrSignature
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Session{}, ErrMalformed
	}

	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, ErrMalformed
	}

	if time.Now().Unix() > sess.Expires {
		return Session{}, ErrExpired
	}

	return sess, nil
}

func (s *Signer) VerifyForRoom(token, roomID string) (Session, error) {
	sess, err := s.Verify(token)
	if err != nil {
		return Session{}, err
	}
	if sess.RoomID != roomID {
		return Session{}, fmt.Errorf("%w: token for %q, asked for %q", ErrWrongRoom, sess.RoomID, roomID)
	}
	return sess, nil
}

type GuildAllowList struct {
	mu      sync.RWMutex
	guilds  map[string]bool
	users   map[string]bool
	enabled bool
}

func NewGuildAllowList(guilds, users []string) *GuildAllowList {
	a := &GuildAllowList{
		guilds:  make(map[string]bool),
		users:   make(map[string]bool),
		enabled: len(guilds) > 0 || len(users) > 0,
	}
	for _, g := range guilds {
		if g = strings.TrimSpace(g); g != "" {
			a.guilds[g] = true
		}
	}
	for _, u := range users {
		if u = strings.TrimSpace(u); u != "" {
			a.users[u] = true
		}
	}
	return a
}

func (a *GuildAllowList) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

func (a *GuildAllowList) Permit(guildID, userID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !a.enabled {
		return false
	}
	if guildID != "" && a.guilds[guildID] {
		return true
	}
	return userID != "" && a.users[userID]
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(perSecond, burst float64) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    perSecond,
		burst:   burst,
	}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}
