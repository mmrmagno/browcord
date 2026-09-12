package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/mmrmagno/browcord/internal/agent"
	"github.com/mmrmagno/browcord/internal/capture"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := agent.Config{
		GatewayURL:  envOr("BROWCORD_GATEWAY_URL", "ws://browcord-gateway:8080"),
		RoomID:      os.Getenv("BROWCORD_ROOM_ID"),
		Token:       os.Getenv("BROWCORD_AGENT_TOKEN"),
		CDPEndpoint: envOr("BROWCORD_CDP", "http://127.0.0.1:9222"),
		StartURL:    envOr("ROOM_START_URL", "https://duckduckgo.com"),
		Capture: capture.Config{
			Source:           capture.Source(envOr("ROOM_SOURCE", "x11")),
			Display:          envOr("DISPLAY", ":0"),
			Width:            envInt("ROOM_WIDTH", 1280),
			Height:           envInt("ROOM_HEIGHT", 720),
			FPS:              envInt("ROOM_FPS", 30),
			VideoBitrateKbps: envInt("ROOM_BITRATE_KBPS", 4000),
			Encoder:          capture.Encoder(envOr("ROOM_ENCODER", "auto")),
			Audio:            os.Getenv("ROOM_AUDIO") != "0",
			AudioDevice:      envOr("ROOM_AUDIO_DEVICE", "browcord.monitor"),
		},
	}

	if cfg.RoomID == "" {
		log.Fatal("BROWCORD_ROOM_ID is required")
	}
	if cfg.Token == "" {
		log.Fatal("BROWCORD_AGENT_TOKEN is required")
	}

	if err := agent.Run(ctx, cfg); err != nil {
		log.Fatalf("agent: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
