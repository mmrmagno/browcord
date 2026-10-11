package k8sguard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmrmagno/browcord/internal/guard"
	"github.com/mmrmagno/browcord/internal/roomspec"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var (
	podNamePattern   = regexp.MustCompile(`^` + regexp.QuoteMeta(roomspec.NamePrefix) + `[0-9a-f]{16}$`)
	namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	errNotFound      = errors.New("k8sguard: not found")
	pollInterval     = 500 * time.Millisecond
)

type Config struct {
	Token     string
	Cap       int
	Ceiling   time.Duration
	Namespace string
	Template  Template
}

type API struct {
	base      string
	tokenFile string
	client    *http.Client
}

func NewAPI(base, tokenFile string, client *http.Client) *API {
	return &API{base: strings.TrimSuffix(base, "/"), tokenFile: tokenFile, client: client}
}

func InCluster() (*API, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("k8sguard: not running in a cluster, KUBERNETES_SERVICE_HOST is unset")
	}
	ca, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("k8sguard: read cluster ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("k8sguard: cluster ca has no certificates")
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	return NewAPI("https://"+net.JoinHostPort(host, port), serviceAccountDir+"/token", client), nil
}

func (a *API) do(ctx context.Context, method, path string, query url.Values, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}

	target := a.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.tokenFile != "" {
		token, err := os.ReadFile(a.tokenFile)
		if err != nil {
			return 0, fmt.Errorf("k8sguard: read service account token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, errNotFound
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("kubernetes %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func New(cfg Config, api *API) (*guard.Server, error) {
	if !namespacePattern.MatchString(cfg.Namespace) {
		return nil, fmt.Errorf("k8sguard: namespace %q is not valid", cfg.Namespace)
	}
	if err := cfg.Template.Validate(); err != nil {
		return nil, err
	}
	if api == nil {
		return nil, errors.New("k8sguard: an api client is required")
	}
	return guard.New(guard.Config{Token: cfg.Token, Cap: cfg.Cap, Ceiling: cfg.Ceiling}, &backend{api: api, ns: cfg.Namespace, template: cfg.Template})
}

type backend struct {
	api      *API
	ns       string
	template Template
}

func (b *backend) podsPath() string {
	return "/api/v1/namespaces/" + b.ns + "/pods"
}

func (b *backend) List(ctx context.Context) ([]guard.Room, error) {
	var list PodList
	query := url.Values{"labelSelector": {roomspec.LabelManaged + "=1"}}
	if _, err := b.api.do(ctx, http.MethodGet, b.podsPath(), query, nil, &list); err != nil {
		return nil, err
	}

	rooms := make([]guard.Room, 0, len(list.Items))
	for _, p := range list.Items {
		id := p.Metadata.Annotations[roomspec.LabelRoom]
		if p.Metadata.Labels[roomspec.LabelManaged] != "1" || roomspec.ValidID(id) != nil ||
			!podNamePattern.MatchString(p.Metadata.Name) || roomspec.ContainerName(id) != p.Metadata.Name {
			continue
		}
		var created int64
		if ts, err := time.Parse(time.RFC3339, p.Metadata.CreationTimestamp); err == nil {
			created = ts.Unix()
		}
		rooms = append(rooms, guard.Room{Ref: p.Metadata.Name, ID: id, State: podState(p), Created: created})
	}
	return rooms, nil
}

func podState(p Pod) string {
	if p.Metadata.DeletionTimestamp != "" {
		return "exited"
	}
	switch p.Status.Phase {
	case "Pending":
		return "created"
	case "Running":
		for _, c := range p.Status.ContainerStatuses {
			if c.Name == containerName && c.State.Running == nil {
				return "restarting"
			}
		}
		return "running"
	default:
		return "exited"
	}
}

func (b *backend) Create(ctx context.Context, id, token string) error {
	pod, err := b.template.Pod(id, token)
	if err != nil {
		return err
	}
	pod.Metadata.Namespace = b.ns
	_, err = b.api.do(ctx, http.MethodPost, b.podsPath(), nil, pod, nil)
	return err
}

func (b *backend) Revive(ctx context.Context, room guard.Room, token string) error {
	if room.State != "exited" {
		return nil
	}
	if err := b.delete(ctx, room.Ref, 1); err != nil {
		return err
	}
	if err := b.waitGone(ctx, room.Ref); err != nil {
		return err
	}
	return b.Create(ctx, room.ID, token)
}

func (b *backend) Remove(ctx context.Context, room guard.Room) error {
	return b.delete(ctx, room.Ref, gracePeriodSec)
}

func (b *backend) delete(ctx context.Context, name string, grace int) error {
	if !podNamePattern.MatchString(name) {
		return errors.New("k8sguard: refusing to delete a malformed pod name")
	}
	query := url.Values{"gracePeriodSeconds": {strconv.Itoa(grace)}}
	_, err := b.api.do(ctx, http.MethodDelete, b.podsPath()+"/"+name, query, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (b *backend) waitGone(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		_, err := b.api.do(ctx, http.MethodGet, b.podsPath()+"/"+name, nil, nil, nil)
		if errors.Is(err, errNotFound) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("k8sguard: pod %s still terminating: %w", name, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
