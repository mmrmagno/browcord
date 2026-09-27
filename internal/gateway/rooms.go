package gateway

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
	"github.com/mmrmagno/browcord/internal/supervisor"
	"github.com/mmrmagno/browcord/internal/wire"
)

var (
	supervisorTimeout = 60 * time.Second
	adoptGrace        = 30 * time.Second
)

type Supervisor interface {
	Ensure(ctx context.Context, roomID, agentToken string) (supervisor.Room, error)
	Remove(ctx context.Context, roomID string) error
	List(ctx context.Context) ([]supervisor.Room, error)
}

func (g *Gateway) multiRoom() bool {
	return g.cfg.Supervisor != nil
}

func (g *Gateway) agentTokenFor(roomID string) string {
	if g.multiRoom() {
		return roomspec.AgentToken(g.cfg.Secret, roomID)
	}
	return g.cfg.AgentToken
}

func (g *Gateway) isManaged(roomID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.managed[roomID]
	return ok
}

func (g *Gateway) ensureRoom(ctx context.Context, roomID string) error {
	if !g.multiRoom() || g.isManaged(roomID) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, supervisorTimeout)
	defer cancel()

	room, err := g.cfg.Supervisor.Ensure(ctx, roomID, g.agentTokenFor(roomID))
	if errors.Is(err, supervisor.ErrFull) {
		g.metrics.roomsFull.Add(1)
	}
	if err != nil {
		return err
	}
	g.metrics.roomsStarted.Add(1)

	created := time.Now()
	if room.Created > 0 {
		created = time.Unix(room.Created, 0)
	}

	g.mu.Lock()
	g.managed[roomID] = created
	g.mu.Unlock()
	g.rooms.GetOrCreate(roomID)
	return nil
}

func (g *Gateway) reviveRoom(roomID string) {
	if !g.multiRoom() || g.isManaged(roomID) {
		return
	}

	err := g.ensureRoom(context.Background(), roomID)
	if err == nil {
		return
	}

	log.Printf("gateway: could not start room %s for its viewers: %v", roomID, err)
	if errors.Is(err, supervisor.ErrFull) {
		if rm, ok := g.rooms.Get(roomID); ok {
			rm.BroadcastCtl(wire.ServerMessage{Type: wire.CtlStatus, State: wire.StatusFull}.Encode())
		}
	}
}

func (g *Gateway) destroyRoom(roomID string) {
	g.closeAgent(roomID)
	g.rooms.Remove(roomID)

	if !g.multiRoom() {
		return
	}

	g.mu.Lock()
	delete(g.managed, roomID)
	g.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), supervisorTimeout)
		defer cancel()
		if err := g.cfg.Supervisor.Remove(ctx, roomID); err != nil {
			log.Printf("gateway: could not remove room %s: %v", roomID, err)
		}
	}()
}

func (g *Gateway) endExpiredRooms(now time.Time) {
	if !g.multiRoom() {
		return
	}

	g.mu.RLock()
	var expired []string
	for id, created := range g.managed {
		if now.Sub(created) > g.cfg.RoomCeiling {
			expired = append(expired, id)
		}
	}
	g.mu.RUnlock()

	for _, id := range expired {
		log.Printf("gateway: room %s reached its %s ceiling, ending it", id, g.cfg.RoomCeiling)
		if rm, ok := g.rooms.Get(id); ok {
			rm.BroadcastCtl(wire.ServerMessage{Type: wire.CtlStatus, State: wire.StatusEnding}.Encode())
			rm.Evict()
		}
		g.metrics.roomsCeiling.Add(1)
		g.destroyRoom(id)
	}
}

func (g *Gateway) syncManaged(ctx context.Context, now time.Time) {
	if !g.multiRoom() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, supervisorTimeout)
	defer cancel()

	rooms, err := g.cfg.Supervisor.List(ctx)
	if err != nil {
		log.Printf("gateway: could not list rooms: %v", err)
		return
	}

	present := make(map[string]bool, len(rooms))
	var adopted, dead []string

	g.mu.Lock()
	for _, r := range rooms {
		present[r.ID] = true
		if r.State != "running" && r.State != "restarting" && r.State != "created" {
			if _, ok := g.managed[r.ID]; ok {
				delete(g.managed, r.ID)
				dead = append(dead, r.ID)
			}
			continue
		}
		if _, ok := g.managed[r.ID]; ok {
			continue
		}
		created := now
		if r.Created > 0 {
			created = time.Unix(r.Created, 0)
		}
		g.managed[r.ID] = created
		adopted = append(adopted, r.ID)
	}
	var vanished []string
	for id, created := range g.managed {
		if !present[id] && now.Sub(created) > adoptGrace {
			vanished = append(vanished, id)
			delete(g.managed, id)
		}
	}
	g.mu.Unlock()

	for _, id := range adopted {
		log.Printf("gateway: adopted running room %s", id)
		g.rooms.GetOrCreate(id)
	}
	for _, id := range dead {
		log.Printf("gateway: room %s container is not running, starting it again", id)
		g.closeAgent(id)
		if rm, ok := g.rooms.Get(id); ok && rm.ViewerCount() > 0 {
			g.metrics.roomsRevived.Add(1)
			go g.reviveRoom(id)
		}
	}
	for _, id := range vanished {
		log.Printf("gateway: room %s disappeared from docker, forgetting it", id)
		g.closeAgent(id)
	}
}
