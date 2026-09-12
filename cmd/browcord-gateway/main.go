package main

import (
	"context"
	"encoding/hex"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mmrmagno/browcord/internal/authz"
	"github.com/mmrmagno/browcord/internal/gateway"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	secret, err := loadSecret()
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	cfg := gateway.Config{
		Addr:         envOr("BROWCORD_ADDR", ":8080"),
		StaticDir:    envOr("BROWCORD_STATIC", "web/dist"),
		ClientID:     os.Getenv("DISCORD_CLIENT_ID"),
		ClientSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
		AgentToken:   os.Getenv("BROWCORD_AGENT_TOKEN"),
		Secret:       secret,
		AllowGuilds:  splitList(os.Getenv("BROWCORD_ALLOW_GUILDS")),
		AllowUsers:   splitList(os.Getenv("BROWCORD_ALLOW_USERS")),
		RoomIdle:     envDuration("BROWCORD_ROOM_IDLE", 60*time.Second),
		DevIdentity:  os.Getenv("BROWCORD_DEV_IDENTITY") == "1",
		FixedRoom:    os.Getenv("BROWCORD_FIXED_ROOM"),
	}

	if cfg.DevIdentity {
		log.Print("WARNING: BROWCORD_DEV_IDENTITY=1 issues sessions without Discord authentication")
	}

	g, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	if err := g.Run(ctx); err != nil {
		log.Fatalf("gateway: %v", err)
	}
}

func loadSecret() ([]byte, error) {
	raw := os.Getenv("BROWCORD_SECRET")
	if raw == "" {
		log.Print("BROWCORD_SECRET unset: generated an ephemeral one, so sessions will not survive a restart")
		return authz.GenerateSecret(), nil
	}
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) >= 32 {
		return decoded, nil
	}
	return []byte(raw), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return d
}

func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
