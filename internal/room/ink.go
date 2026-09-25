package room

import "math"

const (
	maxInkPointsPerRoom   = 4000
	maxInkStrokesPerRoom  = 256
	maxInkStrokesPerUser  = 64
	maxInkPointsPerStroke = 512
)

type Stroke struct {
	ID     uint64    `json:"id"`
	UserID string    `json:"userId"`
	Color  int       `json:"color"`
	Points []float64 `json:"points"`
}

type pen struct {
	seq    int
	stroke *Stroke
}

func quantise(pts []float64) []float64 {
	out := make([]float64, len(pts))
	for i, v := range pts {
		out[i] = math.Round(v*1000) / 1000
	}
	return out
}

func (r *Room) AppendInk(userID string, seq int, pts []float64) (Stroke, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(pts) == 0 || r.inkPoints >= maxInkPointsPerRoom {
		return Stroke{}, false
	}

	held, open := r.pens[userID]
	if !open || held.seq != seq {
		if len(r.strokes) >= maxInkStrokesPerRoom || r.strokesBy(userID) >= maxInkStrokesPerUser {
			return Stroke{}, false
		}
		r.nextInkID++
		fresh := &Stroke{ID: r.nextInkID, UserID: userID, Color: r.colors[userID]}
		r.strokes = append(r.strokes, fresh)
		held = pen{seq: seq, stroke: fresh}
		r.pens[userID] = held
	}

	allowed := min(
		len(pts)/2,
		maxInkPointsPerRoom-r.inkPoints,
		maxInkPointsPerStroke-len(held.stroke.Points)/2,
	)
	if allowed <= 0 {
		return Stroke{}, false
	}

	stored := quantise(pts[:allowed*2])
	held.stroke.Points = append(held.stroke.Points, stored...)
	r.inkPoints += allowed

	return Stroke{ID: held.stroke.ID, UserID: userID, Color: held.stroke.Color, Points: stored}, true
}

func (r *Room) strokesBy(userID string) int {
	n := 0
	for _, s := range r.strokes {
		if s.UserID == userID {
			n++
		}
	}
	return n
}

func (r *Room) ClearInk(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.strokes[:0]
	points := 0
	for _, s := range r.strokes {
		if s.UserID == userID {
			continue
		}
		kept = append(kept, s)
		points += len(s.Points) / 2
	}

	r.strokes = kept
	r.inkPoints = points
	delete(r.pens, userID)
}

func (r *Room) ClearAllInk() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.strokes = nil
	r.inkPoints = 0
	r.pens = make(map[string]pen)
}

func (r *Room) InkSnapshot() []Stroke {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.inkSnapshotLocked()
}

func (r *Room) inkSnapshotLocked() []Stroke {
	out := make([]Stroke, 0, len(r.strokes))
	for _, s := range r.strokes {
		points := make([]float64, len(s.Points))
		copy(points, s.Points)
		out = append(out, Stroke{ID: s.ID, UserID: s.UserID, Color: s.Color, Points: points})
	}
	return out
}

func (r *Room) SetColor(userID string, idx int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.colors[userID] = idx
	if c, ok := r.cursors[userID]; ok {
		c.Color = idx
		r.cursors[userID] = c
	}
}

func (r *Room) Color(userID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.colors[userID]
}

func (r *Room) leastUsedColorLocked(palette int) int {
	if palette <= 0 {
		return 0
	}

	used := make([]int, palette)
	for _, idx := range r.colors {
		if idx >= 0 && idx < palette {
			used[idx]++
		}
	}

	pick := 0
	for i, n := range used {
		if n < used[pick] {
			pick = i
		}
	}
	return pick
}
