package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/mmrmagno/browcord/internal/wire"
)

type Transport interface {
	Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
}

type Client struct {
	transport Transport
	guard     *Guard

	mu     sync.RWMutex
	width  int
	height int
}

func NewClient(t Transport, guard *Guard, width, height int) *Client {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	return &Client{transport: t, guard: guard, width: width, height: height}
}

func (c *Client) Resize(width, height int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if width > 0 {
		c.width = width
	}
	if height > 0 {
		c.height = height
	}
}

func (c *Client) denormalize(x, y float64) (float64, float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return math.Round(clamp01(x) * float64(c.width)), math.Round(clamp01(y) * float64(c.height))
}

func clamp01(v float64) float64 {
	if v != v {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

func (c *Client) Navigate(ctx context.Context, rawURL string) error {
	target := Normalize(rawURL)
	if err := c.guard.Check(ctx, target); err != nil {
		return err
	}
	_, err := c.transport.Call(ctx, "Page.navigate", map[string]any{"url": target})
	return err
}

func (c *Client) Reload(ctx context.Context) error {
	_, err := c.transport.Call(ctx, "Page.reload", map[string]any{"ignoreCache": false})
	return err
}

func (c *Client) History(ctx context.Context, delta int) error {
	raw, err := c.transport.Call(ctx, "Page.getNavigationHistory", map[string]any{})
	if err != nil {
		return err
	}

	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		return fmt.Errorf("cdp: navigation history: %w", err)
	}

	target := history.CurrentIndex + delta
	if target < 0 || target >= len(history.Entries) {
		return nil
	}

	_, err = c.transport.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{
		"entryId": history.Entries[target].ID,
	})
	return err
}

var buttonNames = map[int]string{0: "left", 1: "middle", 2: "right"}

func (c *Client) Pointer(ctx context.Context, x, y float64) error {
	px, py := c.denormalize(x, y)
	_, err := c.transport.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": px, "y": py, "button": "none",
	})
	return err
}

func (c *Client) Click(ctx context.Context, x, y float64, button int, down bool) error {
	px, py := c.denormalize(x, y)

	name, ok := buttonNames[button]
	if !ok {
		name = "left"
	}

	eventType := "mouseReleased"
	if down {
		eventType = "mousePressed"
	}

	if _, err := c.transport.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": px, "y": py, "button": "none",
	}); err != nil {
		return err
	}

	_, err := c.transport.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": eventType, "x": px, "y": py, "button": name, "clickCount": 1,
	})
	return err
}

func (c *Client) Scroll(ctx context.Context, x, y, dx, dy float64) error {
	px, py := c.denormalize(x, y)
	_, err := c.transport.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": px, "y": py, "deltaX": dx, "deltaY": dy, "button": "none",
	})
	return err
}

const (
	modifierAlt   = 1
	modifierCtrl  = 2
	modifierMeta  = 4
	modifierShift = 8
)

func typesCharacter(key string, modifiers int) bool {
	if modifiers&(modifierCtrl|modifierMeta|modifierAlt) != 0 {
		return false
	}
	runes := []rune(key)
	return len(runes) == 1 && unicode.IsPrint(runes[0])
}

func (c *Client) Key(ctx context.Context, key, code string, down bool, modifiers int) error {
	eventType := "keyUp"
	if down {
		eventType = "rawKeyDown"
	}

	params := map[string]any{
		"type":      eventType,
		"key":       key,
		"code":      code,
		"modifiers": modifiers,
	}

	if down && typesCharacter(key, modifiers) {
		params["type"] = "keyDown"
		params["text"] = key
		params["unmodifiedText"] = strings.ToLower(key)
	}
	if vk, ok := virtualKeyCodes[key]; ok {
		params["windowsVirtualKeyCode"] = vk
		params["nativeVirtualKeyCode"] = vk
	}

	_, err := c.transport.Call(ctx, "Input.dispatchKeyEvent", params)
	return err
}

func (c *Client) InsertText(ctx context.Context, text string) error {
	_, err := c.transport.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

func (c *Client) Touch(ctx context.Context, x, y float64, phase string) error {
	px, py := c.denormalize(x, y)

	eventType := map[string]string{
		"start": "touchStart",
		"move":  "touchMove",
		"end":   "touchEnd",
	}[phase]
	if eventType == "" {
		return fmt.Errorf("cdp: unknown touch phase %q", phase)
	}

	points := []map[string]any{}
	if eventType != "touchEnd" {
		points = append(points, map[string]any{"x": px, "y": py})
	}

	_, err := c.transport.Call(ctx, "Input.dispatchTouchEvent", map[string]any{
		"type":        eventType,
		"touchPoints": points,
	})
	return err
}

func (c *Client) Apply(ctx context.Context, msg wire.Ctl) error {
	switch msg.Type {
	case wire.CtlPointer:
		return c.Pointer(ctx, msg.X, msg.Y)
	case wire.CtlClick:
		return c.Click(ctx, msg.X, msg.Y, msg.Button, msg.Down)
	case wire.CtlScroll:
		return c.Scroll(ctx, msg.X, msg.Y, msg.DX, msg.DY)
	case wire.CtlKey:
		return c.Key(ctx, msg.Key, msg.Code, msg.Down, msg.Modifiers)
	case wire.CtlText:
		return c.InsertText(ctx, msg.Text)
	case wire.CtlTouch:
		return c.Touch(ctx, msg.X, msg.Y, msg.Phase)
	case wire.CtlNavigate:
		return c.Navigate(ctx, msg.URL)
	case wire.CtlBack:
		return c.History(ctx, -1)
	case wire.CtlForward:
		return c.History(ctx, 1)
	case wire.CtlReload:
		return c.Reload(ctx)
	default:
		return fmt.Errorf("cdp: no handler for %q", msg.Type)
	}
}

var virtualKeyCodes = map[string]int{
	"Backspace": 8, "Tab": 9, "Enter": 13, "Shift": 16, "Control": 17, "Alt": 18,
	"Escape": 27, " ": 32, "PageUp": 33, "PageDown": 34, "End": 35, "Home": 36,
	"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
	"Delete": 46, "Meta": 91,
	"F1": 112, "F2": 113, "F3": 114, "F4": 115, "F5": 116, "F6": 117,
	"F7": 118, "F8": 119, "F9": 120, "F10": 121, "F11": 122, "F12": 123,
}

var playbackKeys = map[string]bool{
	" ": true, "ArrowLeft": true, "ArrowRight": true, "ArrowUp": true, "ArrowDown": true,
	"Escape": true, "f": true, "F": true, "m": true, "M": true, "k": true, "K": true,
	"PageUp": true, "PageDown": true,
}

func IsPlaybackKey(key string) bool {
	return playbackKeys[key]
}
