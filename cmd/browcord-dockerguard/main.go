package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mmrmagno/browcord/internal/dockerguard"
)

var passthrough = []string{
	"ROOM_WIDTH",
	"ROOM_HEIGHT",
	"ROOM_FPS",
	"ROOM_BITRATE_KBPS",
	"ROOM_ENCODER",
	"ROOM_START_URL",
	"ROOM_STALL_SECONDS",
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	raw, err := os.ReadFile(envOr("ROOM_SECCOMP", "/app/seccomp/chromium.json"))
	if err != nil {
		log.Fatalf("dockerguard: read seccomp profile: %v", err)
	}
	profile, err := dockerguard.CompactSeccomp(raw)
	if err != nil {
		log.Fatalf("dockerguard: %v", err)
	}

	env := map[string]string{}
	for _, key := range passthrough {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}

	cap, err := strconv.Atoi(envOr("ROOM_CAP", "2"))
	if err != nil {
		log.Fatalf("dockerguard: ROOM_CAP: %v", err)
	}
	ceiling, err := time.ParseDuration(envOr("ROOM_CEILING", "6h10m"))
	if err != nil {
		log.Fatalf("dockerguard: ROOM_CEILING: %v", err)
	}

	guard, err := dockerguard.New(dockerguard.Config{
		Token:   os.Getenv("GUARD_TOKEN"),
		Cap:     cap,
		Ceiling: ceiling,
		Template: dockerguard.Template{
			Image:      os.Getenv("ROOM_IMAGE"),
			Network:    envOr("ROOM_NETWORK", "browcord-rooms"),
			GatewayURL: envOr("ROOM_GATEWAY_URL", "ws://browcord-gateway:8080"),
			Seccomp:    profile,
			Env:        env,
		},
	}, dockerguard.NewEngine(envOr("DOCKER_URL", "http://browcord-docker:2375")))
	if err != nil {
		log.Fatalf("dockerguard: %v", err)
	}

	go guard.RunReaper(ctx, time.Minute)

	srv := &http.Server{
		Addr:              envOr("GUARD_ADDR", ":7070"),
		Handler:           guard.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      90 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("dockerguard: listening on %s, cap %d, ceiling %s", srv.Addr, cap, ceiling)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("dockerguard: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
