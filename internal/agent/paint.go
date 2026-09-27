package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"time"

	"github.com/mmrmagno/browcord/internal/capture"
)

const (
	probeFaultLimit  = 3
	confirmTimeout   = 5 * time.Second
	screenshotGap    = 2 * time.Second
	confirmFailLimit = 2
)

type probeSample struct {
	hash uint64
	err  error
}

type paintCheck func(context.Context, *capture.Stall, <-chan probeSample) error

func (a *Agent) watchPaint(ctx context.Context, samples <-chan probeSample, window time.Duration, confirm paintCheck) {
	var (
		stall    capture.Stall
		faults   int
		failures int
		started  = time.Now()
		first    = true
	)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case s := <-samples:
			if s.err != nil {
				faults++
				log.Printf("agent: paint probe fault %d of %d: %v", faults, probeFaultLimit, s.err)
				if faults >= probeFaultLimit {
					a.fail(fmt.Errorf("agent: paint probe is broken: %w", s.err))
					return
				}
				continue
			}
			if first {
				log.Printf("agent: paint probe live, first hash %016x", s.hash)
				first = false
			}
			stall.Observe(s.hash, time.Now())

		case now := <-ticker.C:
			if silent := stall.Silent(now, started); silent > window {
				a.fail(fmt.Errorf("agent: paint probe delivered nothing for %s", silent.Truncate(time.Second)))
				return
			}

			if stall.Unchanged(now) < window {
				continue
			}

			err := confirm(ctx, &stall, samples)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				failures = 0
				stall.Restart(time.Now())
				continue
			}

			failures++
			log.Printf("agent: picture unchanged for %s and Chromium failed a paint check (%d of %d): %v",
				stall.Unchanged(time.Now()).Truncate(time.Second), failures, confirmFailLimit, err)
			if failures >= confirmFailLimit {
				a.fail(fmt.Errorf("agent: paint stall: %w", err))
				return
			}
			stall.Restart(time.Now())
		}
	}
}

func (a *Agent) confirmPaint(ctx context.Context, stall *capture.Stall, samples <-chan probeSample) error {
	if err := a.frameRoundTrip(ctx); err != nil {
		return fmt.Errorf("animation frame: %w", err)
	}

	before, err := a.screenshotHash(ctx)
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}
	unchangedAt := stall.Unchanged(time.Now())

	gap := time.After(screenshotGap)
	for waiting := true; waiting; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s := <-samples:
			if s.err == nil {
				stall.Observe(s.hash, time.Now())
			}
		case <-gap:
			waiting = false
		}
	}

	after, err := a.screenshotHash(ctx)
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}

	if before != after && stall.Unchanged(time.Now()) > unchangedAt {
		return errors.New("Chromium repainted but the captured display did not change")
	}
	return nil
}

func (a *Agent) frameRoundTrip(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()

	contextID, err := a.isolatedWorld(callCtx)
	if err != nil {
		return err
	}

	raw, err := a.trans.Call(callCtx, "Runtime.evaluate", map[string]any{
		"expression":    "new Promise((r) => requestAnimationFrame(() => r(1)))",
		"contextId":     contextID,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return err
	}

	var result struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.ExceptionDetails != nil {
		return fmt.Errorf("evaluate threw: %s", *result.ExceptionDetails)
	}
	if v, ok := result.Result.Value.(float64); !ok || v != 1 {
		return fmt.Errorf("evaluate returned %v", result.Result.Value)
	}
	return nil
}

func (a *Agent) isolatedWorld(ctx context.Context) (int, error) {
	raw, err := a.trans.Call(ctx, "Page.getFrameTree", nil)
	if err != nil {
		return 0, err
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil || tree.FrameTree.Frame.ID == "" {
		return 0, fmt.Errorf("no main frame: %v", err)
	}

	raw, err = a.trans.Call(ctx, "Page.createIsolatedWorld", map[string]any{
		"frameId":   tree.FrameTree.Frame.ID,
		"worldName": "browcord-paint",
	})
	if err != nil {
		return 0, err
	}
	var world struct {
		ContextID int `json:"executionContextId"`
	}
	if err := json.Unmarshal(raw, &world); err != nil || world.ContextID == 0 {
		return 0, fmt.Errorf("no isolated world: %v", err)
	}
	return world.ContextID, nil
}

func (a *Agent) screenshotHash(ctx context.Context) (uint64, error) {
	callCtx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()

	raw, err := a.trans.Call(callCtx, "Page.captureScreenshot", map[string]any{
		"format": "png",
		"clip": map[string]any{
			"x":      0,
			"y":      0,
			"width":  a.cfg.Capture.Width,
			"height": a.cfg.Capture.Height,
			"scale":  0.1,
		},
	})
	if err != nil {
		return 0, err
	}

	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &shot); err != nil {
		return 0, err
	}
	if len(shot.Data) < 64 {
		return 0, fmt.Errorf("screenshot of %d bytes is not an image", len(shot.Data))
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(shot.Data))
	return h.Sum64(), nil
}
