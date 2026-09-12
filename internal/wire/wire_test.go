package wire

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []Chunk{
		{Type: VideoKey, PTS: 0, Payload: []byte{0, 0, 0, 1, 0x65}},
		{Type: VideoDelta, PTS: 33333, Payload: []byte{0, 0, 0, 1, 0x41}},
		{Type: Audio, PTS: 1234567890, Payload: bytes.Repeat([]byte{0xAB}, 320)},
		{Type: VideoConfig, PTS: 0, Payload: []byte{0, 0, 0, 1, 0x67, 0x42}},
		{Type: AudioConfig, PTS: 0, Payload: []byte("OpusHead")},
		{Type: VideoDelta, PTS: ^uint64(0), Payload: []byte{}},
	}

	for _, want := range cases {
		encoded, err := want.Append(nil)
		if err != nil {
			t.Fatalf("Append(%v): %v", want.Type, err)
		}
		if len(encoded) != HeaderSize+len(want.Payload) {
			t.Errorf("Type %v: encoded %d bytes, want %d", want.Type, len(encoded), HeaderSize+len(want.Payload))
		}

		got, err := Unmarshal(encoded)
		if err != nil {
			t.Fatalf("Unmarshal(%v): %v", want.Type, err)
		}
		if got.Type != want.Type || got.PTS != want.PTS || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("round trip: got %v/%d/%x, want %v/%d/%x", got.Type, got.PTS, got.Payload, want.Type, want.PTS, want.Payload)
		}
	}
}

func TestAppendReusesBuffer(t *testing.T) {
	buf := make([]byte, 0, 1024)
	c := Chunk{Type: VideoDelta, PTS: 42, Payload: []byte{1, 2, 3}}

	out, err := c.Append(buf)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if cap(out) != cap(buf) {
		t.Errorf("Append reallocated: cap %d, want %d", cap(out), cap(buf))
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	valid, err := Chunk{Type: VideoKey, PTS: 7, Payload: []byte{9, 9, 9}}.Append(nil)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	oversized := make([]byte, HeaderSize)
	oversized[0] = byte(VideoDelta)
	be32(oversized[9:], uint32(MaxPayload+1))

	cases := []struct {
		name  string
		input []byte
		want  error
	}{
		{"empty", nil, ErrShortFrame},
		{"header truncated", valid[:HeaderSize-1], ErrShortFrame},
		{"payload truncated", valid[:len(valid)-1], ErrLengthMismatch},
		{"trailing bytes", append(append([]byte{}, valid...), 0xFF), ErrLengthMismatch},
		{"declared length overflows max", oversized, ErrPayloadTooLarge},
		{"unknown type", func() []byte {
			b := append([]byte{}, valid...)
			b[0] = 0
			return b
		}(), ErrUnknownType},
		{"type above range", func() []byte {
			b := append([]byte{}, valid...)
			b[0] = 99
			return b
		}(), ErrUnknownType},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Unmarshal(tc.input); !errors.Is(err, tc.want) {
				t.Errorf("Unmarshal: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAppendRejectsOversizedPayload(t *testing.T) {
	c := Chunk{Type: VideoKey, Payload: make([]byte, MaxPayload+1)}
	if _, err := c.Append(nil); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("Append: got %v, want %v", err, ErrPayloadTooLarge)
	}
}

func TestUnmarshalAliasesNothing(t *testing.T) {
	encoded, err := Chunk{Type: Audio, PTS: 1, Payload: []byte{7, 7}}.Append(nil)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	encoded[HeaderSize] = 0xFF
	if got.Payload[0] != 7 {
		t.Error("Unmarshal aliased the source buffer; a reused read buffer would corrupt decoded chunks")
	}
}

func TestStreamRoundTrip(t *testing.T) {
	want := []Chunk{
		{Type: VideoConfig, PTS: 0, Payload: []byte{0x67, 0x42}},
		{Type: VideoKey, PTS: 0, Payload: bytes.Repeat([]byte{1}, 5000)},
		{Type: VideoDelta, PTS: 33333, Payload: []byte{2}},
	}

	var buf bytes.Buffer
	for _, c := range want {
		if err := WriteChunk(&buf, c); err != nil {
			t.Fatalf("WriteChunk: %v", err)
		}
	}

	r := NewReader(&buf)
	for i, expect := range want {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next(%d): %v", i, err)
		}
		if got.Type != expect.Type || got.PTS != expect.PTS || !bytes.Equal(got.Payload, expect.Payload) {
			t.Errorf("chunk %d: got %v/%d/%d bytes, want %v/%d/%d bytes", i, got.Type, got.PTS, len(got.Payload), expect.Type, expect.PTS, len(expect.Payload))
		}
	}
	if _, err := r.Next(); !errors.Is(err, ErrEndOfStream) {
		t.Errorf("after last chunk: got %v, want %v", err, ErrEndOfStream)
	}
}

func FuzzUnmarshal(f *testing.F) {
	valid, _ := Chunk{Type: VideoKey, PTS: 1, Payload: []byte{1, 2, 3}}.Append(nil)
	f.Add(valid)
	f.Add([]byte{})
	f.Add(make([]byte, HeaderSize))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Unmarshal(data)
		if err != nil {
			return
		}
		if _, err := c.Append(nil); err != nil {
			t.Fatalf("chunk accepted by Unmarshal failed to re-encode: %v", err)
		}
	})
}
