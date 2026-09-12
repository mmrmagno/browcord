package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mmrmagno/browcord/internal/wire"
)

type recordedCall struct {
	Method string
	Params map[string]any
}

type fakeTransport struct {
	calls   []recordedCall
	replies map[string]string
}

func (f *fakeTransport) Call(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, recordedCall{Method: method, Params: params})
	if reply, ok := f.replies[method]; ok {
		return json.RawMessage(reply), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeTransport) last() recordedCall {
	return f.calls[len(f.calls)-1]
}

func testClient() (*Client, *fakeTransport) {
	ft := &fakeTransport{replies: map[string]string{}}
	return NewClient(ft, testGuard(), 1280, 720), ft
}

func TestPointerDenormalizesToPixels(t *testing.T) {
	c, ft := testClient()

	if err := c.Pointer(context.Background(), 0.5, 0.25); err != nil {
		t.Fatalf("Pointer: %v", err)
	}

	call := ft.last()
	if call.Method != "Input.dispatchMouseEvent" {
		t.Fatalf("method = %s", call.Method)
	}
	if call.Params["x"] != float64(640) || call.Params["y"] != float64(180) {
		t.Errorf("coords = %v,%v, want 640,180 on a 1280x720 viewport", call.Params["x"], call.Params["y"])
	}
}

func TestPointerClampsOutOfRange(t *testing.T) {
	c, ft := testClient()

	if err := c.Pointer(context.Background(), 5, -3); err != nil {
		t.Fatalf("Pointer: %v", err)
	}

	call := ft.last()
	if call.Params["x"] != float64(1280) || call.Params["y"] != float64(0) {
		t.Errorf("coords = %v,%v, want clamping to 1280,0", call.Params["x"], call.Params["y"])
	}
}

func TestResizeChangesMapping(t *testing.T) {
	c, ft := testClient()
	c.Resize(1920, 1080)

	if err := c.Pointer(context.Background(), 0.5, 0.5); err != nil {
		t.Fatalf("Pointer: %v", err)
	}

	call := ft.last()
	if call.Params["x"] != float64(960) || call.Params["y"] != float64(540) {
		t.Errorf("coords = %v,%v, want 960,540 after resize", call.Params["x"], call.Params["y"])
	}
}

func TestClickMovesBeforePressing(t *testing.T) {
	c, ft := testClient()

	if err := c.Click(context.Background(), 0.25, 0.5, 2, true); err != nil {
		t.Fatalf("Click: %v", err)
	}

	if len(ft.calls) != 2 {
		t.Fatalf("got %d calls, want a move then a press", len(ft.calls))
	}
	if ft.calls[0].Params["type"] != "mouseMoved" {
		t.Errorf("first call type = %v, want mouseMoved", ft.calls[0].Params["type"])
	}

	press := ft.calls[1]
	if press.Params["type"] != "mousePressed" {
		t.Errorf("second call type = %v, want mousePressed", press.Params["type"])
	}
	if press.Params["button"] != "right" {
		t.Errorf("button = %v, want right", press.Params["button"])
	}
	if press.Params["x"] != float64(320) {
		t.Errorf("x = %v, want 320", press.Params["x"])
	}
}

func TestKeyCarriesVirtualKeyCode(t *testing.T) {
	c, ft := testClient()

	if err := c.Key(context.Background(), "ArrowLeft", "ArrowLeft", true, 0); err != nil {
		t.Fatalf("Key: %v", err)
	}

	call := ft.last()
	if call.Params["type"] != "rawKeyDown" {
		t.Errorf("type = %v, want rawKeyDown", call.Params["type"])
	}
	if call.Params["windowsVirtualKeyCode"] != 37 {
		t.Errorf("windowsVirtualKeyCode = %v, want 37; without it sites ignore arrow keys",
			call.Params["windowsVirtualKeyCode"])
	}
}

func TestPrintableKeyActuallyTypes(t *testing.T) {
	c, ft := testClient()

	if err := c.Key(context.Background(), "a", "KeyA", true, 0); err != nil {
		t.Fatalf("Key: %v", err)
	}

	call := ft.last()
	if call.Params["type"] != "keyDown" {
		t.Errorf("type = %v, want keyDown; rawKeyDown moves focus but types nothing", call.Params["type"])
	}
	if call.Params["text"] != "a" {
		t.Errorf("text = %v, want \"a\"; without it no character reaches the page", call.Params["text"])
	}
}

func TestShiftedCharacterKeepsItsCase(t *testing.T) {
	c, ft := testClient()

	if err := c.Key(context.Background(), "A", "KeyA", true, modifierShift); err != nil {
		t.Fatalf("Key: %v", err)
	}
	if got := ft.last().Params["text"]; got != "A" {
		t.Errorf("text = %v, want A", got)
	}
}

func TestControlComboDoesNotType(t *testing.T) {
	c, ft := testClient()

	if err := c.Key(context.Background(), "a", "KeyA", true, modifierCtrl); err != nil {
		t.Fatalf("Key: %v", err)
	}

	call := ft.last()
	if call.Params["type"] != "rawKeyDown" {
		t.Errorf("type = %v, want rawKeyDown for ctrl combos", call.Params["type"])
	}
	if _, typed := call.Params["text"]; typed {
		t.Error("ctrl+a must not insert the letter a into the page")
	}
}

func TestNamedKeysStayRaw(t *testing.T) {
	c, ft := testClient()

	if err := c.Key(context.Background(), "Enter", "Enter", true, 0); err != nil {
		t.Fatalf("Key: %v", err)
	}
	if got := ft.last().Params["type"]; got != "rawKeyDown" {
		t.Errorf("Enter dispatched as %v, want rawKeyDown", got)
	}
}

func TestInsertTextSendsUnicodeVerbatim(t *testing.T) {
	c, ft := testClient()

	if err := c.InsertText(context.Background(), "héllo 🎬"); err != nil {
		t.Fatalf("InsertText: %v", err)
	}

	call := ft.last()
	if call.Method != "Input.insertText" || call.Params["text"] != "héllo 🎬" {
		t.Errorf("call = %+v", call)
	}
}

func TestNavigateIsGuarded(t *testing.T) {
	c, ft := testClient()

	err := c.Navigate(context.Background(), "http://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("Navigate to metadata = %v, want %v", err, ErrBlockedHost)
	}
	if len(ft.calls) != 0 {
		t.Error("a blocked navigation must not reach Chromium at all")
	}

	if err := c.Navigate(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if ft.last().Method != "Page.navigate" {
		t.Errorf("method = %s", ft.last().Method)
	}
}

func TestNavigateNormalizesBareHost(t *testing.T) {
	c, ft := testClient()

	if err := c.Navigate(context.Background(), "example.com"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if got := ft.last().Params["url"]; got != "https://example.com" {
		t.Errorf("url = %v, want https://example.com", got)
	}
}

func TestHistoryBack(t *testing.T) {
	ft := &fakeTransport{replies: map[string]string{
		"Page.getNavigationHistory": `{"currentIndex":2,"entries":[{"id":10},{"id":11},{"id":12}]}`,
	}}
	c := NewClient(ft, testGuard(), 1280, 720)

	if err := c.History(context.Background(), -1); err != nil {
		t.Fatalf("History: %v", err)
	}

	call := ft.last()
	if call.Method != "Page.navigateToHistoryEntry" || call.Params["entryId"] != 11 {
		t.Errorf("call = %+v, want navigateToHistoryEntry with entryId 11", call)
	}
}

func TestHistoryAtBoundaryDoesNothing(t *testing.T) {
	ft := &fakeTransport{replies: map[string]string{
		"Page.getNavigationHistory": `{"currentIndex":0,"entries":[{"id":10}]}`,
	}}
	c := NewClient(ft, testGuard(), 1280, 720)

	if err := c.History(context.Background(), -1); err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(ft.calls) != 1 {
		t.Errorf("going back at the first entry should not navigate, calls = %d", len(ft.calls))
	}
}

func TestApplyRoutesEveryClientMessage(t *testing.T) {
	cases := []struct {
		msg    wire.Ctl
		method string
	}{
		{wire.Ctl{Type: wire.CtlPointer, X: 0.5, Y: 0.5}, "Input.dispatchMouseEvent"},
		{wire.Ctl{Type: wire.CtlScroll, X: 0.5, Y: 0.5, DY: -120}, "Input.dispatchMouseEvent"},
		{wire.Ctl{Type: wire.CtlKey, Key: "a", Code: "KeyA", Down: true}, "Input.dispatchKeyEvent"},
		{wire.Ctl{Type: wire.CtlText, Text: "hi"}, "Input.insertText"},
		{wire.Ctl{Type: wire.CtlTouch, X: 0.5, Y: 0.5, Phase: "start"}, "Input.dispatchTouchEvent"},
		{wire.Ctl{Type: wire.CtlNavigate, URL: "https://example.com"}, "Page.navigate"},
		{wire.Ctl{Type: wire.CtlReload}, "Page.reload"},
	}

	for _, tc := range cases {
		c, ft := testClient()
		if err := c.Apply(context.Background(), tc.msg); err != nil {
			t.Errorf("Apply(%s): %v", tc.msg.Type, err)
			continue
		}
		if ft.last().Method != tc.method {
			t.Errorf("Apply(%s) called %s, want %s", tc.msg.Type, ft.last().Method, tc.method)
		}
	}
}

func TestPlaybackKeysAreNeverGated(t *testing.T) {
	for _, key := range []string{" ", "ArrowLeft", "Escape", "f", "M"} {
		if !IsPlaybackKey(key) {
			t.Errorf("IsPlaybackKey(%q) = false; pausing must always work for everyone", key)
		}
	}
	for _, key := range []string{"a", "Z", "1"} {
		if IsPlaybackKey(key) {
			t.Errorf("IsPlaybackKey(%q) = true, want false", key)
		}
	}
}
