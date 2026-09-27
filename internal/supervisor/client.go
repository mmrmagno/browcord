package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

var ErrFull = errors.New("supervisor: every room slot is in use")

type Room struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Created int64  `json:"created"`
}

type Client struct {
	base   string
	token  string
	client *http.Client
}

func New(base, token string) (*Client, error) {
	if base == "" {
		return nil, errors.New("supervisor: guard url is required")
	}
	if len(token) < 32 {
		return nil, errors.New("supervisor: guard token must be at least 32 characters")
	}
	return &Client{base: base, token: token, client: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("supervisor: %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) Ensure(ctx context.Context, roomID, agentToken string) (Room, error) {
	if err := roomspec.ValidID(roomID); err != nil {
		return Room{}, err
	}
	var room Room
	status, err := c.do(ctx, http.MethodPut, "/rooms/"+url.PathEscape(roomID), map[string]string{"token": agentToken}, &room)
	if status == http.StatusConflict {
		return Room{}, ErrFull
	}
	return room, err
}

func (c *Client) Remove(ctx context.Context, roomID string) error {
	if err := roomspec.ValidID(roomID); err != nil {
		return err
	}
	_, err := c.do(ctx, http.MethodDelete, "/rooms/"+url.PathEscape(roomID), nil, nil)
	return err
}

func (c *Client) List(ctx context.Context) ([]Room, error) {
	var rooms []Room
	if _, err := c.do(ctx, http.MethodGet, "/rooms", nil, &rooms); err != nil {
		return nil, err
	}
	valid := rooms[:0]
	for _, r := range rooms {
		if roomspec.ValidID(r.ID) == nil {
			valid = append(valid, r)
		}
	}
	return valid, nil
}
