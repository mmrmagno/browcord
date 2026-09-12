package wire

import (
	"errors"
	"fmt"
	"io"
)

type Type uint8

const (
	VideoKey    Type = 1
	VideoDelta  Type = 2
	Audio       Type = 3
	VideoConfig Type = 4
	AudioConfig Type = 5
)

const (
	HeaderSize = 13
	MaxPayload = 4 << 20
)

var (
	ErrShortFrame      = errors.New("wire: frame shorter than header")
	ErrLengthMismatch  = errors.New("wire: declared payload length does not match frame size")
	ErrPayloadTooLarge = errors.New("wire: payload exceeds maximum")
	ErrUnknownType     = errors.New("wire: unknown chunk type")
	ErrEndOfStream     = errors.New("wire: end of stream")
)

type Chunk struct {
	Type    Type
	PTS     uint64
	Payload []byte
}

func (t Type) valid() bool {
	return t >= VideoKey && t <= AudioConfig
}

func (t Type) String() string {
	switch t {
	case VideoKey:
		return "video-key"
	case VideoDelta:
		return "video-delta"
	case Audio:
		return "audio"
	case VideoConfig:
		return "video-config"
	case AudioConfig:
		return "audio-config"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(t))
	}
}

func (c Chunk) Append(dst []byte) ([]byte, error) {
	if !c.Type.valid() {
		return nil, fmt.Errorf("%w: %d", ErrUnknownType, uint8(c.Type))
	}
	if len(c.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, len(c.Payload))
	}

	var header [HeaderSize]byte
	header[0] = byte(c.Type)
	be64(header[1:], c.PTS)
	be32(header[9:], uint32(len(c.Payload)))

	dst = append(dst, header[:]...)
	return append(dst, c.Payload...), nil
}

func Unmarshal(b []byte) (Chunk, error) {
	if len(b) < HeaderSize {
		return Chunk{}, ErrShortFrame
	}

	t := Type(b[0])
	if !t.valid() {
		return Chunk{}, fmt.Errorf("%w: %d", ErrUnknownType, b[0])
	}

	length := int(readBE32(b[9:]))
	if length > MaxPayload {
		return Chunk{}, fmt.Errorf("%w: declared %d bytes", ErrPayloadTooLarge, length)
	}
	if len(b)-HeaderSize != length {
		return Chunk{}, fmt.Errorf("%w: declared %d, have %d", ErrLengthMismatch, length, len(b)-HeaderSize)
	}

	payload := make([]byte, length)
	copy(payload, b[HeaderSize:])

	return Chunk{Type: t, PTS: readBE64(b[1:]), Payload: payload}, nil
}

func WriteChunk(w io.Writer, c Chunk) error {
	encoded, err := c.Append(nil)
	if err != nil {
		return err
	}
	_, err = w.Write(encoded)
	return err
}

type Reader struct {
	r      io.Reader
	header [HeaderSize]byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

func (r *Reader) Next() (Chunk, error) {
	if _, err := io.ReadFull(r.r, r.header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Chunk{}, ErrEndOfStream
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Chunk{}, ErrShortFrame
		}
		return Chunk{}, err
	}

	t := Type(r.header[0])
	if !t.valid() {
		return Chunk{}, fmt.Errorf("%w: %d", ErrUnknownType, r.header[0])
	}

	length := int(readBE32(r.header[9:]))
	if length > MaxPayload {
		return Chunk{}, fmt.Errorf("%w: declared %d bytes", ErrPayloadTooLarge, length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r.r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Chunk{}, ErrLengthMismatch
		}
		return Chunk{}, err
	}

	return Chunk{Type: t, PTS: readBE64(r.header[1:]), Payload: payload}, nil
}

func be32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func be64(b []byte, v uint64) {
	be32(b, uint32(v>>32))
	be32(b[4:], uint32(v))
}

func readBE32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func readBE64(b []byte) uint64 {
	return uint64(readBE32(b))<<32 | uint64(readBE32(b[4:]))
}
