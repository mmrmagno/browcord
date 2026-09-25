package wire

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCtlAccepts(t *testing.T) {
	cases := []string{
		`{"type":"pointer","x":0.5,"y":0.25}`,
		`{"type":"pointer","x":0,"y":1}`,
		`{"type":"click","x":0.1,"y":0.1,"button":2,"down":true}`,
		`{"type":"scroll","x":0.5,"y":0.5,"dy":-120}`,
		`{"type":"key","key":"ArrowLeft","code":"ArrowLeft","down":true}`,
		`{"type":"text","text":"hello ünicode 🎬"}`,
		`{"type":"navigate","url":"https://example.com"}`,
		`{"type":"back"}`,
		`{"type":"reload"}`,
		`{"type":"stroke","seq":1,"points":[0.1,0.2,0.3,0.4]}`,
		`{"type":"stroke","seq":0,"points":[0,1]}`,
		`{"type":"clear","scope":"mine"}`,
		`{"type":"clear","scope":"all"}`,
		`{"type":"color","color":0}`,
		`{"type":"color","color":7}`,
	}

	for _, in := range cases {
		if _, err := ParseCtl([]byte(in)); err != nil {
			t.Errorf("ParseCtl(%s) = %v, want accepted", in, err)
		}
	}
}

func TestParseCtlRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"unknown type", `{"type":"exec"}`, ErrCtlUnknown},
		{"server type from client", `{"type":"cursors"}`, ErrCtlUnknown},
		{"empty type", `{"type":""}`, ErrCtlUnknown},
		{"unknown field", `{"type":"back","evil":1}`, ErrCtlMalformed},
		{"not json", `nope`, ErrCtlMalformed},
		{"trailing content", `{"type":"back"}{"type":"back"}`, ErrCtlMalformed},
		{"pointer above range", `{"type":"pointer","x":1.5,"y":0.5}`, ErrCtlOutOfRange},
		{"pointer negative", `{"type":"pointer","x":-0.1,"y":0.5}`, ErrCtlOutOfRange},
		{"click out of range", `{"type":"click","x":2,"y":0.5}`, ErrCtlOutOfRange},
		{"bad button", `{"type":"click","x":0.5,"y":0.5,"button":9}`, ErrCtlOutOfRange},
		{"navigate without url", `{"type":"navigate"}`, ErrCtlMalformed},
		{"oversized url", `{"type":"navigate","url":"https://e.com/` + strings.Repeat("a", MaxURLBytes) + `"}`, ErrCtlOutOfRange},
		{"long key name", `{"type":"key","key":"` + strings.Repeat("k", 100) + `"}`, ErrCtlOutOfRange},
		{"stroke without points", `{"type":"stroke","seq":1,"points":[]}`, ErrCtlMalformed},
		{"stroke with odd coordinates", `{"type":"stroke","seq":1,"points":[0.1,0.2,0.3]}`, ErrCtlMalformed},
		{"stroke point above range", `{"type":"stroke","seq":1,"points":[0.1,1.5]}`, ErrCtlOutOfRange},
		{"stroke point negative", `{"type":"stroke","seq":1,"points":[0.1,-0.2]}`, ErrCtlOutOfRange},
		{"stroke too long", `{"type":"stroke","seq":1,"points":[` +
			strings.TrimSuffix(strings.Repeat("0.5,", 2*(MaxStrokePoints+1)), ",") + `]}`, ErrCtlOutOfRange},
		{"stroke seq out of range", `{"type":"stroke","seq":1048576,"points":[0.1,0.2]}`, ErrCtlOutOfRange},
		{"clear without scope", `{"type":"clear"}`, ErrCtlOutOfRange},
		{"clear with unknown scope", `{"type":"clear","scope":"everything"}`, ErrCtlOutOfRange},
		{"colour above palette", `{"type":"color","color":8}`, ErrCtlOutOfRange},
		{"colour negative", `{"type":"color","color":-1}`, ErrCtlOutOfRange},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCtl([]byte(tc.in)); !errors.Is(err, tc.want) {
				t.Errorf("ParseCtl = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseCtlRejectsOversizedFrame(t *testing.T) {
	big := `{"type":"text","text":"` + strings.Repeat("a", MaxCtlBytes) + `"}`
	if _, err := ParseCtl([]byte(big)); !errors.Is(err, ErrCtlTooLarge) {
		t.Errorf("ParseCtl = %v, want %v", err, ErrCtlTooLarge)
	}
}

func TestParseCtlRejectsOversizedText(t *testing.T) {
	in := `{"type":"text","text":"` + strings.Repeat("a", MaxTextRune+1) + `"}`
	if _, err := ParseCtl([]byte(in)); !errors.Is(err, ErrCtlOutOfRange) {
		t.Errorf("ParseCtl = %v, want %v", err, ErrCtlOutOfRange)
	}
}

func TestServerMessageEncode(t *testing.T) {
	out := string(ServerMessage{Type: CtlTyping, UserID: "u1", Name: "marcos"}.Encode())
	if !strings.Contains(out, `"type":"typing"`) || !strings.Contains(out, `"name":"marcos"`) {
		t.Errorf("Encode = %s", out)
	}
	if strings.Contains(out, `"cursors"`) {
		t.Errorf("empty fields should be omitted, got %s", out)
	}
}

func FuzzParseCtl(f *testing.F) {
	f.Add([]byte(`{"type":"pointer","x":0.5,"y":0.5}`))
	f.Add([]byte(`{"type":"navigate","url":"https://x"}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := ParseCtl(data)
		if err != nil {
			return
		}
		if !clientTypes[c.Type] {
			t.Fatalf("accepted a non-client message type %q", c.Type)
		}
		if c.Type == CtlPointer && (c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1) {
			t.Fatalf("accepted pointer outside the unit square: %f,%f", c.X, c.Y)
		}
	})
}
