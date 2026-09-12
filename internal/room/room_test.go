package room

import (
	"testing"
	"time"

	"github.com/mmrmagno/browcord/internal/wire"
)

func chunk(t wire.Type, pts uint64, payload ...byte) wire.Chunk {
	if len(payload) == 0 {
		payload = []byte{0xAA}
	}
	return wire.Chunk{Type: t, PTS: pts, Payload: payload}
}

func drain(v *Viewer) []wire.Chunk {
	var out []wire.Chunk
	for {
		select {
		case frame := <-v.Media:
			c, err := wire.Unmarshal(frame)
			if err != nil {
				panic(err)
			}
			out = append(out, c)
		default:
			return out
		}
	}
}

func TestLateJoinerGetsConfigAndKeyframe(t *testing.T) {
	r := New("room-1")

	r.Publish(chunk(wire.VideoConfig, 0, 0x67, 0x42))
	r.Publish(chunk(wire.VideoKey, 1000))
	r.Publish(chunk(wire.VideoDelta, 2000))
	r.Publish(chunk(wire.VideoDelta, 3000))

	late := r.Join("v1", "user-1", "marcos")
	got := drain(late)

	if len(got) != 2 {
		t.Fatalf("late joiner received %d chunks, want 2 (config + keyframe)", len(got))
	}
	if got[0].Type != wire.VideoConfig {
		t.Errorf("first chunk is %v, want video-config: a joiner cannot configure a decoder without it", got[0].Type)
	}
	if got[1].Type != wire.VideoKey {
		t.Errorf("second chunk is %v, want video-key: deltas alone decode to garbage", got[1].Type)
	}
	if got[1].PTS != 1000 {
		t.Errorf("keyframe PTS %d, want the most recent keyframe (1000)", got[1].PTS)
	}
}

func TestLateJoinerGetsMostRecentKeyframe(t *testing.T) {
	r := New("room-1")
	r.Publish(chunk(wire.VideoConfig, 0))
	r.Publish(chunk(wire.VideoKey, 1000))
	r.Publish(chunk(wire.VideoDelta, 2000))
	r.Publish(chunk(wire.VideoKey, 3000))

	got := drain(r.Join("v1", "user-1", "marcos"))
	if len(got) != 2 || got[1].PTS != 3000 {
		t.Errorf("late joiner keyframe PTS = %v, want 3000", got)
	}
}

func TestPublishReachesJoinedViewers(t *testing.T) {
	r := New("room-1")
	v := r.Join("v1", "user-1", "marcos")
	drain(v)

	r.Publish(chunk(wire.VideoConfig, 0))
	r.Publish(chunk(wire.VideoKey, 100))
	r.Publish(chunk(wire.VideoDelta, 200))

	got := drain(v)
	if len(got) != 3 {
		t.Fatalf("viewer received %d chunks, want 3", len(got))
	}
}

func TestSlowViewerDropsDeltasUntilNextKeyframe(t *testing.T) {
	r := New("room-1")
	v := r.Join("v1", "user-1", "marcos")
	drain(v)

	r.Publish(chunk(wire.VideoConfig, 0))
	r.Publish(chunk(wire.VideoKey, 0))
	drain(v)

	for i := 0; i < mediaBuffer+10; i++ {
		r.Publish(chunk(wire.VideoDelta, uint64(i+1)))
	}

	if v.Dropped() == 0 {
		t.Fatal("expected the slow viewer to drop frames rather than block the publisher")
	}

	drain(v)

	r.Publish(chunk(wire.VideoDelta, 9000))
	if got := drain(v); len(got) != 0 {
		t.Errorf("after a drop the viewer got a delta (%v); it must wait for a keyframe or it decodes garbage", got)
	}

	r.Publish(chunk(wire.VideoKey, 9001))
	r.Publish(chunk(wire.VideoDelta, 9002))
	got := drain(v)
	if len(got) != 2 || got[0].Type != wire.VideoKey {
		t.Errorf("viewer should resume at the next keyframe, got %v", got)
	}
}

func TestPublishNeverBlocks(t *testing.T) {
	r := New("room-1")
	r.Join("v1", "user-1", "marcos")

	done := make(chan struct{})
	go func() {
		for i := 0; i < mediaBuffer*3; i++ {
			r.Publish(chunk(wire.VideoDelta, uint64(i)))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a viewer that never reads; the agent must never be stalled by one slow client")
	}
}

func TestLeaveRemovesViewer(t *testing.T) {
	r := New("room-1")
	v := r.Join("v1", "user-1", "marcos")
	if r.ViewerCount() != 1 {
		t.Fatalf("ViewerCount = %d, want 1", r.ViewerCount())
	}

	r.Leave(v.ID)
	if r.ViewerCount() != 0 {
		t.Errorf("ViewerCount = %d after Leave, want 0", r.ViewerCount())
	}

	r.Publish(chunk(wire.VideoKey, 1))
}

func TestTypingTokenGrantsAndExpires(t *testing.T) {
	tok := &TypingToken{Hold: 50 * time.Millisecond}

	if !tok.Acquire("user-1") {
		t.Fatal("first typist should get the token")
	}
	if tok.Acquire("user-2") {
		t.Error("second user must not type while the token is held; interleaved input produces garbage")
	}
	if !tok.Acquire("user-1") {
		t.Error("the holder must keep typing")
	}

	time.Sleep(80 * time.Millisecond)

	if !tok.Acquire("user-2") {
		t.Error("token should be free after the idle period")
	}
}

func TestTypingTokenHolder(t *testing.T) {
	tok := &TypingToken{Hold: time.Minute}
	if tok.Holder() != "" {
		t.Errorf("fresh token holder = %q, want empty", tok.Holder())
	}
	tok.Acquire("user-1")
	if tok.Holder() != "user-1" {
		t.Errorf("holder = %q, want user-1", tok.Holder())
	}
}

func TestRegistryReusesRoomsByID(t *testing.T) {
	reg := NewRegistry()
	a := reg.GetOrCreate("instance-1")
	b := reg.GetOrCreate("instance-1")

	if a != b {
		t.Error("the same activity instance must map to the same room")
	}
	if reg.GetOrCreate("instance-2") == a {
		t.Error("different instances must get different rooms")
	}
	if n := reg.Count(); n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

func TestRegistryRemove(t *testing.T) {
	reg := NewRegistry()
	reg.GetOrCreate("instance-1")
	reg.Remove("instance-1")

	if reg.Count() != 0 {
		t.Errorf("Count = %d after Remove, want 0", reg.Count())
	}
}

func TestCursorsTrackedPerUser(t *testing.T) {
	r := New("room-1")
	r.Join("v1", "user-1", "marcos")
	r.Join("v2", "user-2", "friend")

	r.SetCursor("user-1", 0.25, 0.5)
	r.SetCursor("user-2", 0.75, 0.1)
	r.SetCursor("user-1", 0.3, 0.6)

	cursors := r.Cursors()
	if len(cursors) != 2 {
		t.Fatalf("got %d cursors, want 2", len(cursors))
	}
	for _, c := range cursors {
		if c.UserID == "user-1" && (c.X != 0.3 || c.Y != 0.6) {
			t.Errorf("user-1 cursor = %v, want the latest position 0.3,0.6", c)
		}
	}
}

func TestLeaveDropsCursor(t *testing.T) {
	r := New("room-1")
	v := r.Join("v1", "user-1", "marcos")
	r.SetCursor("user-1", 0.5, 0.5)
	r.Leave(v.ID)

	if len(r.Cursors()) != 0 {
		t.Error("a departed viewer must not leave a ghost cursor behind")
	}
}
