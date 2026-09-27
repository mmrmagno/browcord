package capture

import "time"

type Stall struct {
	hash     uint64
	since    time.Time
	lastSeen time.Time
	seen     bool
}

func (s *Stall) Observe(hash uint64, now time.Time) {
	if !s.seen || hash != s.hash {
		s.hash = hash
		s.since = now
		s.seen = true
	}
	s.lastSeen = now
}

func (s *Stall) Unchanged(now time.Time) time.Duration {
	if !s.seen {
		return 0
	}
	return now.Sub(s.since)
}

func (s *Stall) Silent(now time.Time, started time.Time) time.Duration {
	if !s.seen {
		return now.Sub(started)
	}
	return now.Sub(s.lastSeen)
}

func (s *Stall) Restart(now time.Time) {
	s.since = now
}
