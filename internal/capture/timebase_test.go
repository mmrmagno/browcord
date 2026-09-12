package capture

import (
	"sync"
	"testing"
)

func TestTimebaseSharesOneOriginAcrossStreams(t *testing.T) {
	var clock timebase

	video := clock.relative(5_000_000)
	audio := clock.relative(5_020_000)

	if video != 0 {
		t.Fatalf("first buffer should define the origin, got %d", video)
	}
	if audio != 20_000 {
		t.Fatalf("audio captured 20ms after video should read 20000, got %d", audio)
	}
}

func TestTimebasePreservesSkewWhenAudioLeads(t *testing.T) {
	var clock timebase

	clock.relative(1_000_000)

	if got := clock.relative(1_500_000); got != 500_000 {
		t.Fatalf("want 500000, got %d", got)
	}
	if got := clock.relative(1_250_000); got != 250_000 {
		t.Fatalf("out-of-order buffer should stay on the shared origin, got %d", got)
	}
}

func TestTimebaseClampsBufferOlderThanOrigin(t *testing.T) {
	var clock timebase

	clock.relative(2_000_000)

	if got := clock.relative(1_900_000); got != 0 {
		t.Fatalf("earlier buffer must clamp to 0 rather than wrap, got %d", got)
	}
}

func TestTimebaseIsRaceFree(t *testing.T) {
	var clock timebase
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				clock.relative(uint64(1_000_000 + n*1000 + j))
			}
		}(i)
	}
	wg.Wait()
}
