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

	"github.com/mmrmagno/browcord/internal/k8sguard"
	"github.com/mmrmagno/browcord/internal/roomspec"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := map[string]string{}
	for _, key := range roomspec.PassthroughEnv {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}

	cap, err := strconv.Atoi(envOr("ROOM_CAP", "2"))
	if err != nil {
		log.Fatalf("k8sguard: ROOM_CAP: %v", err)
	}
	ceiling, err := time.ParseDuration(envOr("ROOM_CEILING", "6h10m"))
	if err != nil {
		log.Fatalf("k8sguard: ROOM_CEILING: %v", err)
	}

	api, err := k8sguard.InCluster()
	if err != nil {
		log.Fatalf("k8sguard: %v", err)
	}

	namespace := envOr("ROOM_NAMESPACE", "browcord-rooms")
	guard, err := k8sguard.New(k8sguard.Config{
		Token:     os.Getenv("GUARD_TOKEN"),
		Cap:       cap,
		Ceiling:   ceiling,
		Namespace: namespace,
		Template: k8sguard.Template{
			Image:      os.Getenv("ROOM_IMAGE"),
			GatewayURL: envOr("ROOM_GATEWAY_URL", "ws://browcord-gateway.browcord.svc.cluster.local:8080"),
			Seccomp:    envOr("ROOM_SECCOMP_PROFILE", "profiles/browcord-chromium.json"),
			Env:        env,
		},
	}, api)
	if err != nil {
		log.Fatalf("k8sguard: %v", err)
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

	log.Printf("k8sguard: listening on %s, namespace %s, cap %d, ceiling %s", srv.Addr, namespace, cap, ceiling)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("k8sguard: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
