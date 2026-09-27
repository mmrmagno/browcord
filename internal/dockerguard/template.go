package dockerguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

type Template struct {
	Image      string
	Network    string
	GatewayURL string
	Seccomp    string
	Env        map[string]string
}

type CreateBody struct {
	Image            string            `json:"Image"`
	Env              []string          `json:"Env"`
	Labels           map[string]string `json:"Labels"`
	Healthcheck      Healthcheck       `json:"Healthcheck"`
	HostConfig       HostConfig        `json:"HostConfig"`
	NetworkingConfig NetworkingConfig  `json:"NetworkingConfig"`
}

type Healthcheck struct {
	Test        []string      `json:"Test"`
	Interval    time.Duration `json:"Interval"`
	Timeout     time.Duration `json:"Timeout"`
	Retries     int           `json:"Retries"`
	StartPeriod time.Duration `json:"StartPeriod"`
}

type HostConfig struct {
	Privileged     bool              `json:"Privileged"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	CapAdd         []string          `json:"CapAdd"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Binds          []string          `json:"Binds"`
	Mounts         []any             `json:"Mounts"`
	Devices        []any             `json:"Devices"`
	PidsLimit      int64             `json:"PidsLimit"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCpus       int64             `json:"NanoCpus"`
	ShmSize        int64             `json:"ShmSize"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	NetworkMode    string            `json:"NetworkMode"`
	PidMode        string            `json:"PidMode"`
	IpcMode        string            `json:"IpcMode"`
	UsernsMode     string            `json:"UsernsMode"`
	PublishAll     bool              `json:"PublishAllPorts"`
	RestartPolicy  RestartPolicy     `json:"RestartPolicy"`
	LogConfig      LogConfig         `json:"LogConfig"`
}

type RestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type NetworkingConfig struct {
	EndpointsConfig map[string]struct{} `json:"EndpointsConfig"`
}

const (
	gib = 1 << 30
	mib = 1 << 20
)

var reservedEnv = map[string]bool{
	"BROWCORD_ROOM_ID":     true,
	"BROWCORD_AGENT_TOKEN": true,
	"BROWCORD_GATEWAY_URL": true,
}

func CompactSeccomp(profile []byte) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, profile); err != nil {
		return "", fmt.Errorf("dockerguard: seccomp profile is not json: %w", err)
	}
	var probe struct {
		DefaultAction string `json:"defaultAction"`
	}
	if err := json.Unmarshal(buf.Bytes(), &probe); err != nil || probe.DefaultAction == "" {
		return "", errors.New("dockerguard: seccomp profile has no defaultAction")
	}
	if probe.DefaultAction == "SCMP_ACT_ALLOW" {
		return "", errors.New("dockerguard: seccomp profile allows by default")
	}
	return buf.String(), nil
}

func (t Template) Validate() error {
	switch {
	case t.Image == "":
		return errors.New("dockerguard: room image is required")
	case t.Network == "" || t.Network == "host" || t.Network == "bridge" || t.Network == "none":
		return fmt.Errorf("dockerguard: room network %q is not a dedicated network", t.Network)
	case t.GatewayURL == "":
		return errors.New("dockerguard: gateway url is required")
	case t.Seccomp == "":
		return errors.New("dockerguard: a seccomp profile is required")
	}
	for k := range t.Env {
		if reservedEnv[k] {
			return fmt.Errorf("dockerguard: %s is set per room and cannot be templated", k)
		}
	}
	return nil
}

func (t Template) Body(roomID, token string) (CreateBody, error) {
	if err := roomspec.ValidID(roomID); err != nil {
		return CreateBody{}, err
	}
	if err := roomspec.ValidToken(token); err != nil {
		return CreateBody{}, err
	}

	keys := make([]string, 0, len(t.Env))
	for k := range t.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		env = append(env, k+"="+t.Env[k])
	}
	env = append(env,
		"BROWCORD_GATEWAY_URL="+t.GatewayURL,
		"BROWCORD_ROOM_ID="+roomID,
		"BROWCORD_AGENT_TOKEN="+token,
	)

	return CreateBody{
		Image: t.Image,
		Env:   env,
		Labels: map[string]string{
			roomspec.LabelManaged: "1",
			roomspec.LabelRoom:    roomID,
		},
		Healthcheck: Healthcheck{
			Test: []string{
				"CMD-SHELL",
				"curl -fsS http://127.0.0.1:9222/json/version >/dev/null && xdpyinfo -display :0 >/dev/null 2>&1",
			},
			Interval:    30 * time.Second,
			Timeout:     5 * time.Second,
			Retries:     3,
			StartPeriod: 45 * time.Second,
		},
		HostConfig: HostConfig{
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{},
			SecurityOpt:    []string{"no-new-privileges:true", "seccomp=" + t.Seccomp},
			Binds:          []string{},
			Mounts:         []any{},
			Devices:        []any{},
			PidsLimit:      768,
			Memory:         4 * gib,
			MemorySwap:     4 * gib,
			NanoCpus:       2_500_000_000,
			ShmSize:        1 * gib,
			Tmpfs: map[string]string{
				"/tmp":           "size=512m,mode=1777",
				"/profile":       "size=768m,mode=0700,uid=10001,gid=10001",
				"/home/browcord": "size=64m,mode=0700,uid=10001,gid=10001",
			},
			NetworkMode:   t.Network,
			IpcMode:       "private",
			RestartPolicy: RestartPolicy{Name: "on-failure", MaximumRetryCount: 5},
			LogConfig: LogConfig{
				Type:   "json-file",
				Config: map[string]string{"max-size": "10m", "max-file": "3"},
			},
		},
		NetworkingConfig: NetworkingConfig{
			EndpointsConfig: map[string]struct{}{t.Network: {}},
		},
	}, nil
}
