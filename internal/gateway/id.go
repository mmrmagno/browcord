package gateway

import (
	"crypto/rand"
	"encoding/hex"
)

func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}
