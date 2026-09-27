package gateway

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/mmrmagno/browcord/internal/room"
	"github.com/mmrmagno/browcord/internal/wire"
)

var (
	watchdogInterval  = 5 * time.Second
	stallAfter        = 15 * time.Second
	noVideoAfter      = 20 * time.Second
	kickCooldown      = 60 * time.Second
	agentPingInterval = 25 * time.Second
	agentPingTimeout  = 10 * time.Second
)

type healState struct {
	status    string
	kickedAt  time.Time
	restartAt time.Time
}

type healAction int

const (
	healNone healAction = iota
	healKick
	healRestart
)

func (g *Gateway) watchRooms(ctx context.Context) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			g.heal(now)
		}
	}
}

func (g *Gateway) heal(now time.Time) {
	for _, rm := range g.rooms.All() {
		if rm.ViewerCount() == 0 {
			g.healMu.Lock()
			delete(g.healing, rm.ID)
			g.healMu.Unlock()
			continue
		}
		g.healRoom(rm, now)
	}
}

func (g *Gateway) healRoom(rm *room.Room, now time.Time) {
	g.mu.RLock()
	conn := g.agents[rm.ID]
	since := g.agentSince[rm.ID]
	g.mu.RUnlock()

	g.healMu.Lock()
	st := g.healing[rm.ID]
	if st == nil {
		st = &healState{status: wire.StatusLive}
		g.healing[rm.ID] = st
	}

	recent := !st.kickedAt.IsZero() && now.Sub(st.kickedAt) <= kickCooldown
	status, action, reason := wire.StatusLive, healNone, ""

	if conn == nil {
		status = wire.StatusOffline
		if recent {
			status = wire.StatusRecovering
		}
	} else {
		fresh := since
		if last := rm.LastChunkAt(); last.After(fresh) {
			fresh = last
		}

		switch {
		case now.Sub(fresh) > stallAfter:
			reason = "no media for " + now.Sub(fresh).Truncate(time.Second).String()
		case !rm.HasVideo() && now.Sub(since) > noVideoAfter:
			reason = "no video config after " + now.Sub(since).Truncate(time.Second).String()
		}

		if reason == "" {
			if !recent {
				st.kickedAt, st.restartAt = time.Time{}, time.Time{}
			}
		} else {
			status = wire.StatusRecovering
			switch {
			case !recent:
				st.kickedAt = now
				action = healKick
			case st.restartAt.IsZero() || now.Sub(st.restartAt) > kickCooldown:
				st.restartAt = now
				action = healRestart
			}
		}
	}

	changed := st.status != status
	st.status = status
	g.healMu.Unlock()

	if changed {
		rm.BroadcastCtl(wire.ServerMessage{Type: wire.CtlStatus, State: status}.Encode())
	}

	switch action {
	case healKick:
		log.Printf("gateway: room %s unhealthy (%s), dropping its agent", rm.ID, reason)
		g.closeAgent(rm.ID)
	case healRestart:
		log.Printf("gateway: room %s still unhealthy (%s), asking its agent to restart", rm.ID, reason)
		g.restartAgent(conn, reason)
	}
}

func (g *Gateway) restartAgent(conn *websocket.Conn, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, wire.AgentCmd{Type: wire.AgentRestart, Reason: reason}.Encode()); err != nil {
		conn.CloseNow()
	}
}

func (g *Gateway) roomStatus(roomID string) string {
	g.healMu.Lock()
	defer g.healMu.Unlock()

	if st := g.healing[roomID]; st != nil {
		return st.status
	}
	return wire.StatusLive
}

func pingAgent(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(agentPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deadline, cancel := context.WithTimeout(ctx, agentPingTimeout)
			err := conn.Ping(deadline)
			cancel()
			if err != nil {
				conn.CloseNow()
				return
			}
		}
	}
}
