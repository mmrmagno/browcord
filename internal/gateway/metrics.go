package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mmrmagno/browcord/internal/wire"
)

type metrics struct {
	signinOK      atomic.Int64
	signinRefused atomic.Int64
	signinFull    atomic.Int64
	signinLimited atomic.Int64
	signinFailed  atomic.Int64

	roomsStarted  atomic.Int64
	roomsFull     atomic.Int64
	roomsIdle     atomic.Int64
	roomsCeiling  atomic.Int64
	roomsRevived  atomic.Int64
	agentConnects atomic.Int64

	watchdogKicks    atomic.Int64
	watchdogRestarts atomic.Int64
}

func (g *Gateway) metricsHandler(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	agents := len(g.agents)
	managed := len(g.managed)
	g.mu.RUnlock()

	viewers := 0
	for _, rm := range g.rooms.All() {
		viewers += rm.ViewerCount()
	}

	states := map[string]int{}
	g.healMu.Lock()
	for _, st := range g.healing {
		states[st.status]++
	}
	g.healMu.Unlock()

	m := &g.metrics
	var b strings.Builder

	gauge := func(name, help string, v int) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
	}
	counter := func(name, help string, series map[string]int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		keys := make([]string, 0, len(series))
		for labels := range series {
			keys = append(keys, labels)
		}
		sort.Strings(keys)
		for _, labels := range keys {
			v := series[labels]
			if labels == "" {
				fmt.Fprintf(&b, "%s %d\n", name, v)
				continue
			}
			fmt.Fprintf(&b, "%s{%s} %d\n", name, labels, v)
		}
	}

	multi := 0
	if g.multiRoom() {
		multi = 1
	}

	gauge("browcord_multi_room", "1 when rooms are started per Discord instance.", multi)
	gauge("browcord_rooms", "Rooms the gateway holds state for.", g.rooms.Count())
	gauge("browcord_rooms_managed", "Room containers the gateway is running through the guard.", managed)
	gauge("browcord_agents", "Room agents connected.", agents)
	gauge("browcord_viewers", "Viewer sockets connected, two per person.", viewers)

	fmt.Fprintf(&b, "# HELP browcord_rooms_watched Rooms with viewers, by health.\n# TYPE browcord_rooms_watched gauge\n")
	for _, state := range []string{wire.StatusLive, wire.StatusOffline, wire.StatusRecovering, wire.StatusFull} {
		fmt.Fprintf(&b, "browcord_rooms_watched{state=%q} %d\n", state, states[state])
	}

	counter("browcord_signins_total", "Sign in attempts by result.", map[string]int64{
		`result="ok"`:      m.signinOK.Load(),
		`result="refused"`: m.signinRefused.Load(),
		`result="full"`:    m.signinFull.Load(),
		`result="limited"`: m.signinLimited.Load(),
		`result="failed"`:  m.signinFailed.Load(),
	})
	counter("browcord_rooms_started_total", "Room containers started.", map[string]int64{"": m.roomsStarted.Load()})
	counter("browcord_rooms_full_total", "Room starts refused because every slot was in use.", map[string]int64{"": m.roomsFull.Load()})
	counter("browcord_rooms_removed_total", "Room containers removed, by reason.", map[string]int64{
		`reason="idle"`:    m.roomsIdle.Load(),
		`reason="ceiling"`: m.roomsCeiling.Load(),
	})
	counter("browcord_rooms_revived_total", "Room containers started again after they died.", map[string]int64{"": m.roomsRevived.Load()})
	counter("browcord_agent_connects_total", "Agent connections, a reconnect or a room restart each add one.", map[string]int64{"": m.agentConnects.Load()})
	counter("browcord_watchdog_actions_total", "Watchdog interventions on stalled rooms.", map[string]int64{
		`action="kick"`:    m.watchdogKicks.Load(),
		`action="restart"`: m.watchdogRestarts.Load(),
	})

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (g *Gateway) serveMetrics(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", g.metricsHandler)

	srv := &http.Server{
		Addr:              g.cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Printf("gateway: metrics on %s", g.cfg.MetricsAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("gateway: metrics listener: %v", err)
	}
}
