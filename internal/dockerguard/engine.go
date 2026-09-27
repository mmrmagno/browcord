package dockerguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
	Created int64             `json:"Created"`
}

func (c Container) Room() string {
	return c.Labels[roomspec.LabelRoom]
}

type Engine struct {
	base   string
	client *http.Client
}

func NewEngine(base string) *Engine {
	return &Engine{base: base, client: &http.Client{Timeout: 30 * time.Second}}
}

func (e *Engine) do(ctx context.Context, method, path string, query url.Values, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}

	target := e.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (e *Engine) List(ctx context.Context) ([]Container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {roomspec.LabelManaged + "=1"}})
	query := url.Values{"all": {"1"}, "filters": {string(filters)}}

	var list []Container
	if _, err := e.do(ctx, http.MethodGet, "/containers/json", query, nil, &list); err != nil {
		return nil, err
	}

	managed := list[:0]
	for _, c := range list {
		if c.Labels[roomspec.LabelManaged] == "1" && roomspec.ValidID(c.Room()) == nil && containerIDPattern.MatchString(c.ID) {
			managed = append(managed, c)
		}
	}
	return managed, nil
}

func (e *Engine) Create(ctx context.Context, name string, spec CreateBody) (string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	if _, err := e.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, spec, &created); err != nil {
		return "", err
	}
	if !containerIDPattern.MatchString(created.ID) {
		return "", fmt.Errorf("docker returned container id %q", created.ID)
	}
	return created.ID, nil
}

func (e *Engine) Start(ctx context.Context, id string) error {
	if !containerIDPattern.MatchString(id) {
		return errors.New("dockerguard: refusing to start a malformed container id")
	}
	status, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
	if status == http.StatusNotModified {
		return nil
	}
	return err
}

func (e *Engine) Remove(ctx context.Context, id string) error {
	if !containerIDPattern.MatchString(id) {
		return errors.New("dockerguard: refusing to remove a malformed container id")
	}
	status, err := e.do(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
