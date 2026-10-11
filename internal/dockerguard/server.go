package dockerguard

import (
	"context"
	"time"

	"github.com/mmrmagno/browcord/internal/guard"
	"github.com/mmrmagno/browcord/internal/roomspec"
)

var ErrFull = guard.ErrFull

type (
	Server   = guard.Server
	RoomInfo = guard.RoomInfo
)

type Config struct {
	Token    string
	Cap      int
	Ceiling  time.Duration
	Template Template
}

func New(cfg Config, engine *Engine) (*Server, error) {
	if err := cfg.Template.Validate(); err != nil {
		return nil, err
	}
	return guard.New(guard.Config{Token: cfg.Token, Cap: cfg.Cap, Ceiling: cfg.Ceiling}, &backend{engine: engine, template: cfg.Template})
}

type backend struct {
	engine   *Engine
	template Template
}

func (b *backend) List(ctx context.Context) ([]guard.Room, error) {
	list, err := b.engine.List(ctx)
	if err != nil {
		return nil, err
	}
	rooms := make([]guard.Room, 0, len(list))
	for _, c := range list {
		rooms = append(rooms, guard.Room{Ref: c.ID, ID: c.Room(), State: c.State, Created: c.Created})
	}
	return rooms, nil
}

func (b *backend) Create(ctx context.Context, id, token string) error {
	body, err := b.template.Body(id, token)
	if err != nil {
		return err
	}
	containerID, err := b.engine.Create(ctx, roomspec.ContainerName(id), body)
	if err != nil {
		return err
	}
	if err := b.engine.Start(ctx, containerID); err != nil {
		_ = b.engine.Remove(context.WithoutCancel(ctx), containerID)
		return err
	}
	return nil
}

func (b *backend) Revive(ctx context.Context, room guard.Room, _ string) error {
	return b.engine.Start(ctx, room.Ref)
}

func (b *backend) Remove(ctx context.Context, room guard.Room) error {
	return b.engine.Remove(ctx, room.Ref)
}
