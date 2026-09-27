package roomspec

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
)

const (
	LabelManaged = "browcord.managed"
	LabelRoom    = "browcord.room"
	NamePrefix   = "browcord-room-"
)

var (
	ErrInvalidID    = errors.New("roomspec: invalid room id")
	ErrInvalidToken = errors.New("roomspec: invalid agent token")

	idPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func ValidID(id string) error {
	if !idPattern.MatchString(id) {
		return ErrInvalidID
	}
	return nil
}

func ValidToken(token string) error {
	if !tokenPattern.MatchString(token) {
		return ErrInvalidToken
	}
	return nil
}

func AgentToken(secret []byte, roomID string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("browcord-agent:"))
	mac.Write([]byte(roomID))
	return hex.EncodeToString(mac.Sum(nil))
}

func ContainerName(roomID string) string {
	sum := sha256.Sum256([]byte(roomID))
	return NamePrefix + hex.EncodeToString(sum[:8])
}
