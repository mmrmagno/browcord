package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type Event struct {
	Method string
	Params json.RawMessage
}

type WSTransport struct {
	conn *websocket.Conn

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan rpcResponse

	events chan Event
	closed chan struct{}
	once   sync.Once
}

type rpcResponse struct {
	Result json.RawMessage
	Err    error
}

type rpcFrame struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type Target struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
	WS    string `json:"webSocketDebuggerUrl"`
}

func Targets(ctx context.Context, endpoint string) ([]Target, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/list", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cdp: list targets: %w", err)
	}
	defer resp.Body.Close()

	var targets []Target
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("cdp: decode targets: %w", err)
	}
	return targets, nil
}

func Pages(targets []Target) []Target {
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if t.Type == "page" && t.WS != "" {
			out = append(out, t)
		}
	}
	return out
}

func CloseTarget(ctx context.Context, endpoint, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/close/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func Discover(ctx context.Context, endpoint string) (Target, error) {
	targets, err := Targets(ctx, endpoint)
	if err != nil {
		return Target{}, err
	}

	pages := Pages(targets)
	if len(pages) == 0 {
		return Target{}, errors.New("cdp: no page target found")
	}
	return pages[0], nil
}

func DiscoverWithRetry(ctx context.Context, endpoint string, attempts int, delay time.Duration) (Target, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		target, err := Discover(ctx, endpoint)
		if err == nil {
			return target, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return Target{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return Target{}, lastErr
}

func Dial(ctx context.Context, wsURL string) (*WSTransport, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("cdp: dial: %w", err)
	}
	conn.SetReadLimit(32 << 20)

	t := &WSTransport{
		conn:    conn,
		pending: make(map[int64]chan rpcResponse),
		events:  make(chan Event, 64),
		closed:  make(chan struct{}),
	}

	go t.readLoop()
	return t, nil
}

func (t *WSTransport) readLoop() {
	defer t.Close()

	for {
		_, data, err := t.conn.Read(context.Background())
		if err != nil {
			t.failPending(err)
			return
		}

		var frame rpcFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}

		if frame.ID != 0 {
			t.mu.Lock()
			ch, ok := t.pending[frame.ID]
			delete(t.pending, frame.ID)
			t.mu.Unlock()

			if ok {
				var rerr error
				if frame.Error != nil {
					rerr = fmt.Errorf("cdp: %s (%d)", frame.Error.Message, frame.Error.Code)
				}
				ch <- rpcResponse{Result: frame.Result, Err: rerr}
			}
			continue
		}

		if frame.Method != "" {
			select {
			case t.events <- Event{Method: frame.Method, Params: frame.Params}:
			default:
			}
		}
	}
}

func (t *WSTransport) failPending(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, ch := range t.pending {
		ch <- rpcResponse{Err: err}
		delete(t.pending, id)
	}
}

func (t *WSTransport) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	select {
	case <-t.closed:
		return nil, errors.New("cdp: transport closed")
	default:
	}

	if params == nil {
		params = map[string]any{}
	}

	encodedParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	id := t.nextID.Add(1)
	payload, err := json.Marshal(rpcFrame{ID: id, Method: method, Params: encodedParams})
	if err != nil {
		return nil, err
	}

	ch := make(chan rpcResponse, 1)
	t.mu.Lock()
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		return resp.Result, resp.Err
	}
}

func (t *WSTransport) Events() <-chan Event {
	return t.events
}

func (t *WSTransport) Close() {
	t.once.Do(func() {
		close(t.closed)
		t.conn.CloseNow()
	})
}
