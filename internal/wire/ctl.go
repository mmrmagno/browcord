package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	MaxCtlBytes = 8 << 10
	MaxTextRune = 4096
	MaxURLBytes = 2048
)

var (
	ErrCtlTooLarge   = errors.New("wire: control message too large")
	ErrCtlMalformed  = errors.New("wire: control message malformed")
	ErrCtlUnknown    = errors.New("wire: unknown control message type")
	ErrCtlOutOfRange = errors.New("wire: control message field out of range")
)

const (
	CtlPointer  = "pointer"
	CtlClick    = "click"
	CtlScroll   = "scroll"
	CtlKey      = "key"
	CtlText     = "text"
	CtlTouch    = "touch"
	CtlNavigate = "navigate"
	CtlBack     = "back"
	CtlForward  = "forward"
	CtlReload   = "reload"

	CtlCursors  = "cursors"
	CtlNav      = "nav"
	CtlPresence = "presence"
	CtlTyping   = "typing"
	CtlError    = "error"
	CtlHello    = "hello"
)

type Ctl struct {
	Type string `json:"type"`

	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`

	DX float64 `json:"dx,omitempty"`
	DY float64 `json:"dy,omitempty"`

	Button    int    `json:"button,omitempty"`
	Down      bool   `json:"down,omitempty"`
	Key       string `json:"key,omitempty"`
	Code      string `json:"code,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`

	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`

	Phase string `json:"phase,omitempty"`
}

var clientTypes = map[string]bool{
	CtlPointer:  true,
	CtlClick:    true,
	CtlScroll:   true,
	CtlKey:      true,
	CtlText:     true,
	CtlTouch:    true,
	CtlNavigate: true,
	CtlBack:     true,
	CtlForward:  true,
	CtlReload:   true,
}

func ParseCtl(data []byte) (Ctl, error) {
	if len(data) > MaxCtlBytes {
		return Ctl{}, fmt.Errorf("%w: %d bytes", ErrCtlTooLarge, len(data))
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var c Ctl
	if err := dec.Decode(&c); err != nil {
		return Ctl{}, fmt.Errorf("%w: %v", ErrCtlMalformed, err)
	}
	if dec.More() {
		return Ctl{}, fmt.Errorf("%w: trailing content", ErrCtlMalformed)
	}

	if !clientTypes[c.Type] {
		return Ctl{}, fmt.Errorf("%w: %q", ErrCtlUnknown, c.Type)
	}

	if err := c.validate(); err != nil {
		return Ctl{}, err
	}

	return c, nil
}

func (c Ctl) validate() error {
	switch c.Type {
	case CtlPointer, CtlClick, CtlScroll, CtlTouch:
		if !inUnit(c.X) || !inUnit(c.Y) {
			return fmt.Errorf("%w: pointer position %.3f,%.3f must be normalised 0..1", ErrCtlOutOfRange, c.X, c.Y)
		}
	}

	switch c.Type {
	case CtlClick:
		if c.Button < 0 || c.Button > 2 {
			return fmt.Errorf("%w: button %d", ErrCtlOutOfRange, c.Button)
		}
	case CtlScroll:
		if !finite(c.DX) || !finite(c.DY) {
			return fmt.Errorf("%w: scroll delta", ErrCtlOutOfRange)
		}
	case CtlText:
		if len([]rune(c.Text)) > MaxTextRune {
			return fmt.Errorf("%w: text of %d runes", ErrCtlOutOfRange, len([]rune(c.Text)))
		}
	case CtlNavigate:
		if c.URL == "" {
			return fmt.Errorf("%w: navigate without a url", ErrCtlMalformed)
		}
		if len(c.URL) > MaxURLBytes {
			return fmt.Errorf("%w: url of %d bytes", ErrCtlOutOfRange, len(c.URL))
		}
	case CtlKey:
		if len(c.Key) > 64 || len(c.Code) > 64 {
			return fmt.Errorf("%w: key name too long", ErrCtlOutOfRange)
		}
	}

	return nil
}

func inUnit(v float64) bool {
	return finite(v) && v >= 0 && v <= 1
}

func finite(v float64) bool {
	return v == v && v < 1e9 && v > -1e9
}

type ServerMessage struct {
	Type     string   `json:"type"`
	Cursors  any      `json:"cursors,omitempty"`
	Nav      any      `json:"nav,omitempty"`
	Presence any      `json:"presence,omitempty"`
	UserID   string   `json:"userId,omitempty"`
	Name     string   `json:"name,omitempty"`
	Message  string   `json:"message,omitempty"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
	Codecs   []string `json:"codecs,omitempty"`
}

func (m ServerMessage) Encode() []byte {
	out, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"type":"error","message":"encode failed"}`)
	}
	return out
}
