package capture

import (
	"testing"
	"time"
)

func TestStallGrowsOnlyWhileThePictureIsIdentical(t *testing.T) {
	var s Stall
	t0 := time.Unix(1000, 0)

	if got := s.Unchanged(t0); got != 0 {
		t.Fatalf("an unobserved stall reported %v", got)
	}

	for i := 0; i <= 30; i++ {
		s.Observe(7, t0.Add(time.Duration(i)*time.Second))
	}
	if got := s.Unchanged(t0.Add(30 * time.Second)); got != 30*time.Second {
		t.Fatalf("thirty identical frames gave %v, want 30s", got)
	}

	s.Observe(8, t0.Add(31*time.Second))
	if got := s.Unchanged(t0.Add(31 * time.Second)); got != 0 {
		t.Fatalf("a changed frame left %v of stall", got)
	}
}

func TestStallRestartOpensANewWindow(t *testing.T) {
	var s Stall
	t0 := time.Unix(1000, 0)
	s.Observe(1, t0)
	s.Restart(t0.Add(40 * time.Second))
	if got := s.Unchanged(t0.Add(45 * time.Second)); got != 5*time.Second {
		t.Fatalf("after a restart the window is %v, want 5s", got)
	}
}

func TestStallSilence(t *testing.T) {
	var s Stall
	start := time.Unix(1000, 0)
	if got := s.Silent(start.Add(10*time.Second), start); got != 10*time.Second {
		t.Fatalf("a probe that never delivered is silent for %v, want 10s", got)
	}
	s.Observe(1, start.Add(12*time.Second))
	if got := s.Silent(start.Add(15*time.Second), start); got != 3*time.Second {
		t.Fatalf("silence after the last frame is %v, want 3s", got)
	}
}

func TestHashProbeRefusesAnythingThatIsNotAProbeFrame(t *testing.T) {
	if _, err := HashProbe([]byte("xwd: unable to open display")); err == nil {
		t.Fatal("a 27 byte error string hashed as if it were a frame")
	}
	if _, err := HashProbe(make([]byte, ProbeBytes+1)); err == nil {
		t.Fatal("an oversized buffer hashed as if it were a frame")
	}
	a, err := HashProbe(make([]byte, ProbeBytes))
	if err != nil {
		t.Fatalf("a correctly sized frame was refused: %v", err)
	}
	frame := make([]byte, ProbeBytes)
	frame[ProbeBytes/2] = 1
	b, _ := HashProbe(frame)
	if a == b {
		t.Fatal("one changed pixel produced the same hash")
	}
}
