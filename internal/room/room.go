package room

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmrmagno/browcord/internal/wire"
)

const (
	mediaBuffer = 16
	ctlBuffer   = 64
)

type Viewer struct {
	ID     string
	UserID string
	Name   string
	Media  chan []byte
	Ctl    chan []byte

	dropped       atomic.Int64
	needsKeyframe atomic.Bool
	needsCanvas   atomic.Bool
}

func (v *Viewer) Dropped() int64 {
	return v.dropped.Load()
}

func (v *Viewer) TakeCanvasRepair() bool {
	return v.needsCanvas.CompareAndSwap(true, false)
}

type Cursor struct {
	UserID string  `json:"userId"`
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Color  int     `json:"color"`
}

type NavState struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Loading bool   `json:"loading"`
	CanBack bool   `json:"canBack"`
}

type Room struct {
	ID string

	mu           sync.RWMutex
	viewers      map[string]*Viewer
	videoConfig  *wire.Chunk
	lastKeyframe *wire.Chunk
	audioConfig  *wire.Chunk
	cursors      map[string]Cursor
	nav          NavState
	emptySince   time.Time
	strokes      []*Stroke
	pens         map[string]pen
	colors       map[string]int
	inkPoints    int
	nextInkID    uint64

	chunks      atomic.Int64
	bytes       atomic.Int64
	lastChunk   atomic.Int64
	videoChunks atomic.Int64
	audioChunks atomic.Int64
	lastAudio   atomic.Int64

	Typing TypingToken
}

func New(id string) *Room {
	return &Room{
		ID:         id,
		viewers:    make(map[string]*Viewer),
		cursors:    make(map[string]Cursor),
		pens:       make(map[string]pen),
		colors:     make(map[string]int),
		emptySince: time.Now(),
		Typing:     TypingToken{Hold: 1500 * time.Millisecond},
	}
}

func (r *Room) Join(viewerID, userID, name string) *Viewer {
	v := &Viewer{
		ID:     viewerID,
		UserID: userID,
		Name:   name,
		Media:  make(chan []byte, mediaBuffer),
		Ctl:    make(chan []byte, ctlBuffer),
	}

	r.mu.Lock()
	r.viewers[viewerID] = v
	config := r.videoConfig
	audio := r.audioConfig
	keyframe := r.lastKeyframe
	if _, known := r.colors[userID]; !known {
		r.colors[userID] = r.leastUsedColorLocked(wire.PaletteSize)
	}
	if len(r.strokes) > 0 {
		v.Ctl <- wire.ServerMessage{Type: wire.CtlCanvas, Strokes: r.inkSnapshotLocked()}.Encode()
	}
	r.mu.Unlock()

	if config != nil {
		sendMedia(v, *config)
	}
	if audio != nil {
		sendMedia(v, *audio)
	}
	if keyframe != nil {
		sendMedia(v, *keyframe)
	}

	return v
}

func (r *Room) Leave(viewerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	v, ok := r.viewers[viewerID]
	if !ok {
		return
	}

	delete(r.viewers, viewerID)
	delete(r.cursors, v.UserID)

	stillHere := false
	for _, other := range r.viewers {
		if other.UserID == v.UserID {
			stillHere = true
			break
		}
	}
	if !stillHere {
		delete(r.colors, v.UserID)
		delete(r.pens, v.UserID)
	}

	close(v.Media)
	close(v.Ctl)

	if len(r.viewers) == 0 {
		r.emptySince = time.Now()
	}
}

func (r *Room) ViewerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.viewers)
}

func (r *Room) EmptyFor() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.viewers) > 0 {
		return 0
	}
	return time.Since(r.emptySince)
}

func (r *Room) ResetStream() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.videoConfig = nil
	r.audioConfig = nil
	r.lastKeyframe = nil

	for _, v := range r.viewers {
		v.needsKeyframe.Store(true)
	}
}

type Stats struct {
	ID          string `json:"id"`
	Viewers     int    `json:"viewers"`
	Chunks      int64  `json:"chunks"`
	Bytes       int64  `json:"bytes"`
	SecondsIdle int64  `json:"secondsSinceChunk"`
	HasVideo    bool   `json:"hasVideo"`
	VideoChunks int64  `json:"videoChunks"`
	AudioChunks int64  `json:"audioChunks"`
	AudioIdle   int64  `json:"secondsSinceAudio"`
}

func (r *Room) Stats() Stats {
	r.mu.RLock()
	viewers := len(r.viewers)
	hasVideo := r.videoConfig != nil
	r.mu.RUnlock()

	idle := int64(-1)
	if last := r.lastChunk.Load(); last > 0 {
		idle = time.Now().Unix() - last
	}

	audioIdle := int64(-1)
	if last := r.lastAudio.Load(); last > 0 {
		audioIdle = time.Now().Unix() - last
	}

	return Stats{
		ID:          r.ID,
		Viewers:     viewers,
		Chunks:      r.chunks.Load(),
		Bytes:       r.bytes.Load(),
		SecondsIdle: idle,
		HasVideo:    hasVideo,
		VideoChunks: r.videoChunks.Load(),
		AudioChunks: r.audioChunks.Load(),
		AudioIdle:   audioIdle,
	}
}

func (r *Room) Publish(c wire.Chunk) {
	r.chunks.Add(1)
	r.bytes.Add(int64(len(c.Payload)))
	r.lastChunk.Store(time.Now().Unix())

	switch c.Type {
	case wire.VideoKey, wire.VideoDelta:
		r.videoChunks.Add(1)
	case wire.Audio:
		r.audioChunks.Add(1)
		r.lastAudio.Store(time.Now().Unix())
	}

	r.mu.Lock()
	switch c.Type {
	case wire.VideoConfig:
		stored := c
		r.videoConfig = &stored
	case wire.AudioConfig:
		stored := c
		r.audioConfig = &stored
	case wire.VideoKey:
		stored := c
		r.lastKeyframe = &stored
	}

	viewers := make([]*Viewer, 0, len(r.viewers))
	for _, v := range r.viewers {
		viewers = append(viewers, v)
	}
	r.mu.Unlock()

	for _, v := range viewers {
		if v.needsKeyframe.Load() {
			if c.Type != wire.VideoKey && c.Type != wire.VideoConfig && c.Type != wire.AudioConfig {
				continue
			}
			v.needsKeyframe.Store(false)
		}
		sendMedia(v, c)
	}
}

func sendMedia(v *Viewer, c wire.Chunk) {
	encoded, err := c.Append(nil)
	if err != nil {
		return
	}

	defer func() {
		_ = recover()
	}()

	select {
	case v.Media <- encoded:
	default:
		v.dropped.Add(1)
		if c.Type == wire.VideoDelta || c.Type == wire.VideoKey {
			v.needsKeyframe.Store(true)
		}
	}
}

func (r *Room) BroadcastCtl(payload []byte) {
	r.mu.RLock()
	viewers := make([]*Viewer, 0, len(r.viewers))
	for _, v := range r.viewers {
		viewers = append(viewers, v)
	}
	r.mu.RUnlock()

	for _, v := range viewers {
		func() {
			defer func() { _ = recover() }()
			select {
			case v.Ctl <- payload:
			default:
			}
		}()
	}
}

func (r *Room) BroadcastInk(payload []byte) {
	r.mu.RLock()
	viewers := make([]*Viewer, 0, len(r.viewers))
	for _, v := range r.viewers {
		viewers = append(viewers, v)
	}
	r.mu.RUnlock()

	for _, v := range viewers {
		func() {
			defer func() { _ = recover() }()
			select {
			case v.Ctl <- payload:
			default:
				v.needsCanvas.Store(true)
			}
		}()
	}
}

func (r *Room) SetCursor(userID string, x, y float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := userID
	for _, v := range r.viewers {
		if v.UserID == userID {
			name = v.Name
			break
		}
	}

	r.cursors[userID] = Cursor{UserID: userID, Name: name, X: x, Y: y, Color: r.colors[userID]}
}

type Participant struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Color  int    `json:"color"`
}

func (r *Room) Participants() []Participant {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool, len(r.viewers))
	out := make([]Participant, 0, len(r.viewers))
	for _, v := range r.viewers {
		if seen[v.UserID] {
			continue
		}
		seen[v.UserID] = true
		out = append(out, Participant{UserID: v.UserID, Name: v.Name, Color: r.colors[v.UserID]})
	}
	return out
}

func (r *Room) Cursors() []Cursor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Cursor, 0, len(r.cursors))
	for _, c := range r.cursors {
		out = append(out, c)
	}
	return out
}

func (r *Room) SetNav(n NavState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nav = n
}

func (r *Room) Nav() NavState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.nav
}

type TypingToken struct {
	Hold time.Duration

	mu       sync.Mutex
	holder   string
	lastType time.Time
}

func (t *TypingToken) Acquire(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	hold := t.Hold
	if hold == 0 {
		hold = 1500 * time.Millisecond
	}

	if t.holder != "" && t.holder != userID && time.Since(t.lastType) < hold {
		return false
	}

	t.holder = userID
	t.lastType = time.Now()
	return true
}

func (t *TypingToken) Holder() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	hold := t.Hold
	if hold == 0 {
		hold = 1500 * time.Millisecond
	}
	if t.holder != "" && time.Since(t.lastType) >= hold {
		return ""
	}
	return t.holder
}

type Registry struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewRegistry() *Registry {
	return &Registry{rooms: make(map[string]*Room)}
}

func (reg *Registry) GetOrCreate(id string) *Room {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if r, ok := reg.rooms[id]; ok {
		return r
	}

	r := New(id)
	reg.rooms[id] = r
	return r
}

func (reg *Registry) Get(id string) (*Room, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	r, ok := reg.rooms[id]
	return r, ok
}

func (reg *Registry) Remove(id string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	delete(reg.rooms, id)
}

func (reg *Registry) Count() int {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return len(reg.rooms)
}

func (reg *Registry) AllStats() []Stats {
	reg.mu.Lock()
	rooms := make([]*Room, 0, len(reg.rooms))
	for _, r := range reg.rooms {
		rooms = append(rooms, r)
	}
	reg.mu.Unlock()

	out := make([]Stats, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.Stats())
	}
	return out
}

func (reg *Registry) IdleRooms(threshold time.Duration) []*Room {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	var idle []*Room
	for _, r := range reg.rooms {
		if d := r.EmptyFor(); d > threshold {
			idle = append(idle, r)
		}
	}
	return idle
}
