package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmrmagno/browcord/internal/capture"
)

type paintHarness struct {
	agent   *Agent
	samples chan probeSample
	failed  chan error
	checks  atomic.Int32
}

func newPaintHarness(t *testing.T, window time.Duration, verdict error) (*paintHarness, context.CancelFunc) {
	t.Helper()

	h := &paintHarness{
		agent:   &Agent{},
		samples: make(chan probeSample, 16),
		failed:  make(chan error, 1),
	}
	h.agent.fail = func(err error) {
		select {
		case h.failed <- err:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go h.agent.watchPaint(ctx, h.samples, window, func(context.Context, *capture.Stall, <-chan probeSample) error {
		h.checks.Add(1)
		return verdict
	})
	return h, cancel
}

func (h *paintHarness) feed(ctx context.Context, hash func(int) uint64) {
	go func() {
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case h.samples <- probeSample{hash: hash(i)}:
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func TestPaintStallThatFailsItsCheckIsFatal(t *testing.T) {
	h, cancel := newPaintHarness(t, time.Second, errors.New("repainted but display frozen"))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	defer cancel()
	h.feed(ctx, func(int) uint64 { return 42 })

	select {
	case err := <-h.failed:
		if h.checks.Load() < confirmFailLimit {
			t.Fatalf("fatal after %d checks, want at least %d: %v", h.checks.Load(), confirmFailLimit, err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a frozen picture that failed its paint check never stopped the agent")
	}
}

func TestStaticPageThatPassesItsCheckIsLeftAlone(t *testing.T) {
	h, cancel := newPaintHarness(t, time.Second, nil)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	defer cancel()
	h.feed(ctx, func(int) uint64 { return 42 })

	select {
	case err := <-h.failed:
		t.Fatalf("a legitimately static page stopped the agent: %v", err)
	case <-time.After(3500 * time.Millisecond):
	}
	if h.checks.Load() == 0 {
		t.Fatal("the paint check never ran on an unchanged picture")
	}
}

func TestMovingPictureIsNeverChecked(t *testing.T) {
	h, cancel := newPaintHarness(t, time.Second, errors.New("would fail"))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	defer cancel()
	h.feed(ctx, func(i int) uint64 { return uint64(i) })

	time.Sleep(3 * time.Second)
	if n := h.checks.Load(); n != 0 {
		t.Fatalf("a changing picture triggered %d paint checks", n)
	}
}

func TestBrokenProbeIsFatal(t *testing.T) {
	h, cancel := newPaintHarness(t, time.Minute, nil)
	defer cancel()
	for i := 0; i < probeFaultLimit; i++ {
		h.samples <- probeSample{err: errors.New("probe frame is 22 bytes")}
	}

	select {
	case <-h.failed:
	case <-time.After(3 * time.Second):
		t.Fatal("a probe that only ever faulted was trusted")
	}
}

func TestSilentProbeIsFatal(t *testing.T) {
	h, cancel := newPaintHarness(t, time.Second, nil)
	defer cancel()

	select {
	case <-h.failed:
	case <-time.After(4 * time.Second):
		t.Fatal("a probe that never delivered a frame was trusted")
	}
}
