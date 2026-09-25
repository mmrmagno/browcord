package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/mmrmagno/browcord/internal/capture"
	"github.com/mmrmagno/browcord/internal/cdp"
	"github.com/mmrmagno/browcord/internal/wire"
)

type Config struct {
	GatewayURL  string
	RoomID      string
	Token       string
	CDPEndpoint string
	StartURL    string
	Capture     capture.Config
}

func (c *Config) applyDefaults() {
	if c.CDPEndpoint == "" {
		c.CDPEndpoint = "http://127.0.0.1:9222"
	}
	if c.StartURL == "" {
		c.StartURL = "https://duckduckgo.com"
	}
	if c.Capture.Source == "" {
		c.Capture.Source = capture.SourceX11
	}
}

type Agent struct {
	cfg    Config
	client *cdp.Client
	trans  *cdp.WSTransport

	mu     sync.Mutex
	conn   *websocket.Conn
	cancel context.CancelFunc
}

func Run(ctx context.Context, cfg Config) error {
	cfg.applyDefaults()

	target, err := cdp.DiscoverWithRetry(ctx, cfg.CDPEndpoint, 50, 200*time.Millisecond)
	if err != nil {
		return fmt.Errorf("agent: locate chromium: %w", err)
	}

	trans, err := cdp.Dial(ctx, target.WS)
	if err != nil {
		return fmt.Errorf("agent: connect chromium: %w", err)
	}
	defer trans.Close()

	guard := cdp.NewGuard(nil)
	client := cdp.NewClient(trans, guard, cfg.Capture.Width, cfg.Capture.Height)
	a := &Agent{cfg: cfg, client: client, trans: trans}

	if _, err := trans.Call(ctx, "Page.enable", nil); err != nil {
		return fmt.Errorf("agent: enable page domain: %w", err)
	}

	if _, err := trans.Call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": sameTabShim,
	}); err != nil {
		log.Printf("agent: could not install the popup shim: %v", err)
	}
	if err := client.Navigate(ctx, cfg.StartURL); err != nil {
		log.Printf("agent: start url refused: %v", err)
	}

	pipeline, err := capture.New(cfg.Capture, a.publish)
	if err != nil {
		return fmt.Errorf("agent: capture: %w", err)
	}
	defer pipeline.Stop()

	if err := pipeline.Start(); err != nil {
		return fmt.Errorf("agent: start capture: %w", err)
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	fatal := make(chan string, 1)
	go pipeline.WatchBus(runCtx, func(kind gst.MessageType, detail string) {
		if kind == gst.MessageWarning {
			log.Printf("agent: capture warning: %s", detail)
			return
		}
		log.Printf("agent: capture stopped: %s", detail)
		select {
		case fatal <- detail:
			cancelRun()
		default:
		}
	})

	go a.watchNavigation(runCtx)
	go a.keepOneTab(runCtx, target.ID)

	backoff := time.Second
	for {
		err := a.session(runCtx)
		if runCtx.Err() != nil {
			return captureFailure(fatal)
		}
		log.Printf("agent: gateway session ended: %v", err)

		select {
		case <-runCtx.Done():
			return captureFailure(fatal)
		case <-time.After(backoff):
		}

		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func captureFailure(fatal chan string) error {
	select {
	case detail := <-fatal:
		return fmt.Errorf("agent: capture pipeline failed: %s", detail)
	default:
		return nil
	}
}

func (a *Agent) session(ctx context.Context) error {
	conn, err := a.dialGateway(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	a.setConn(conn)
	defer a.setConn(nil)

	log.Printf("agent: room %s connected to %s", a.cfg.RoomID, a.cfg.GatewayURL)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()

	return a.readCommands(sessionCtx, conn)
}

func (a *Agent) setConn(conn *websocket.Conn) {
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
}

func (a *Agent) currentConn() *websocket.Conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

func (a *Agent) dropConn(conn *websocket.Conn, cause error) {
	a.mu.Lock()
	if a.conn != conn {
		a.mu.Unlock()
		return
	}
	a.conn = nil
	cancel := a.cancel
	a.cancel = nil
	a.mu.Unlock()

	log.Printf("agent: dropping gateway connection: %v", cause)
	conn.CloseNow()
	if cancel != nil {
		cancel()
	}
}

func (a *Agent) dialGateway(ctx context.Context) (*websocket.Conn, error) {
	header := http.Header{}
	if a.cfg.Token != "" {
		header.Set("Authorization", "Bearer "+a.cfg.Token)
	}

	url := fmt.Sprintf("%s/agent?room=%s", a.cfg.GatewayURL, a.cfg.RoomID)

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}

	conn.SetReadLimit(wire.MaxCtlBytes)
	return conn, nil
}

func (a *Agent) publish(c wire.Chunk) {
	conn := a.currentConn()
	if conn == nil {
		return
	}

	encoded, err := c.Append(nil)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		a.dropConn(conn, fmt.Errorf("publish: %w", err))
	}
}

func (a *Agent) send(ctx context.Context, msg wire.ServerMessage) {
	conn := a.currentConn()
	if conn == nil {
		return
	}
	_ = conn.Write(ctx, websocket.MessageText, msg.Encode())
}

func (a *Agent) watchNavigation(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-a.trans.Events():
			switch ev.Method {
			case "Page.frameNavigated":
				var params struct {
					Frame struct {
						URL      string `json:"url"`
						ParentID string `json:"parentId"`
					} `json:"frame"`
				}
				if err := json.Unmarshal(ev.Params, &params); err != nil {
					continue
				}
				if params.Frame.ParentID != "" {
					continue
				}
				a.send(ctx, wire.ServerMessage{Type: wire.CtlNav, Nav: map[string]any{
					"url":     params.Frame.URL,
					"loading": true,
				}})
			case "Page.loadEventFired":
				a.send(ctx, wire.ServerMessage{Type: wire.CtlNav, Nav: map[string]any{
					"loading": false,
				}})
			}
		}
	}
}

const sameTabShim = `
(() => {
  const go = (url) => { if (url) location.assign(String(url)); return null; };
  window.open = (url) => go(url);
  document.addEventListener("click", (e) => {
    const a = e.target && e.target.closest && e.target.closest("a[target]");
    if (a && a.target && a.target !== "_self") a.target = "_self";
  }, true);
  document.addEventListener("submit", (e) => {
    const f = e.target;
    if (f && f.target && f.target !== "_self") f.target = "_self";
  }, true);
})();
`

func (a *Agent) keepOneTab(ctx context.Context, keep string) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		targets, err := cdp.Targets(ctx, a.cfg.CDPEndpoint)
		if err != nil {
			continue
		}

		pages := cdp.Pages(targets)
		if len(pages) < 2 {
			continue
		}

		for _, page := range pages {
			if page.ID == keep {
				continue
			}
			log.Printf("agent: closing extra tab %s", trim(page.URL, 90))
			if err := cdp.CloseTarget(ctx, a.cfg.CDPEndpoint, page.ID); err != nil {
				log.Printf("agent: could not close tab: %v", err)
			}
		}
	}
}

func trim(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func explain(msg wire.Ctl, err error) string {
	switch {
	case errors.Is(err, cdp.ErrUnresolved):
		return "Could not find that site. Check the address."
	case errors.Is(err, cdp.ErrBlockedHost):
		return "That address is not allowed from this browser."
	case errors.Is(err, cdp.ErrBlockedScheme):
		return "Only http and https links can be opened here."
	case errors.Is(err, cdp.ErrInvalidURL):
		return "That does not look like a web address."
	default:
		return "The browser could not handle that " + msg.Type + "."
	}
}

func (a *Agent) readCommands(ctx context.Context, conn *websocket.Conn) error {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("agent: gateway connection closed: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}

		msg, err := wire.ParseCtl(data)
		if err != nil {
			log.Printf("agent: rejected command: %v", err)
			continue
		}

		if err := a.client.Apply(ctx, msg); err != nil {
			log.Printf("agent: %s failed: %v", msg.Type, err)
			a.send(ctx, wire.ServerMessage{Type: wire.CtlError, Message: explain(msg, err)})
		}
	}
}
