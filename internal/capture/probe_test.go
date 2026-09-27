package capture

import (
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/mmrmagno/browcord/internal/wire"
)

func probeHashes(t *testing.T, pattern string, frames int) (map[uint64]bool, []error) {
	t.Helper()
	ensureInit()
	if gst.ElementFactoryFind("videotestsrc") == nil || gst.ElementFactoryFind("vp8enc") == nil {
		t.Skip("gstreamer test elements are not installed")
	}

	p, err := New(Config{
		Source:      SourceTest,
		TestPattern: pattern,
		Width:       320,
		Height:      180,
		FPS:         10,
		Encoder:     EncoderVP8,
		Probe:       true,
	}, func(wire.Chunk) {})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Stop()

	var (
		mu     sync.Mutex
		hashes = map[uint64]bool{}
		errs   []error
		count  int
		done   = make(chan struct{})
	)
	p.OnProbe(func(hash uint64, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, err)
		} else {
			hashes[hash] = true
		}
		count++
		if count == frames {
			close(done)
		}
	})

	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Duration(frames+5) * time.Second):
		t.Fatalf("the probe delivered %d of %d frames", count, frames)
	}

	mu.Lock()
	defer mu.Unlock()
	return hashes, errs
}

func TestProbeSeesAMovingPicture(t *testing.T) {
	hashes, errs := probeHashes(t, "ball", 4)
	if len(errs) > 0 {
		t.Fatalf("probe faults: %v", errs)
	}
	if len(hashes) < 3 {
		t.Fatalf("a moving ball produced only %d distinct probe hashes", len(hashes))
	}
}

func TestProbeSeesAFrozenPicture(t *testing.T) {
	hashes, errs := probeHashes(t, "solid-color", 4)
	if len(errs) > 0 {
		t.Fatalf("probe faults: %v", errs)
	}
	if len(hashes) != 1 {
		t.Fatalf("a solid colour produced %d distinct probe hashes, want 1", len(hashes))
	}
}
