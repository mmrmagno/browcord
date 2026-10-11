package k8sguard

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mmrmagno/browcord/internal/roomspec"
)

const (
	LabelRoomHash  = "browcord.room-hash"
	containerName  = "room"
	roomUID        = 10001
	healthCommand  = "curl -fsS http://127.0.0.1:9222/json/version >/dev/null && xdpyinfo -display :0 >/dev/null 2>&1"
	gracePeriodSec = 5
)

var reservedEnv = map[string]bool{
	"BROWCORD_ROOM_ID":     true,
	"BROWCORD_AGENT_TOKEN": true,
	"BROWCORD_GATEWAY_URL": true,
}

type Template struct {
	Image      string
	GatewayURL string
	Seccomp    string
	Env        map[string]string
}

func (t Template) Validate() error {
	switch {
	case t.Image == "":
		return errors.New("k8sguard: room image is required")
	case t.GatewayURL == "":
		return errors.New("k8sguard: gateway url is required")
	case t.Seccomp == "":
		return errors.New("k8sguard: a localhost seccomp profile is required")
	case strings.HasPrefix(t.Seccomp, "/") || strings.Contains(t.Seccomp, ".."):
		return fmt.Errorf("k8sguard: seccomp profile %q must be relative to the kubelet seccomp root", t.Seccomp)
	}
	for k := range t.Env {
		if reservedEnv[k] {
			return fmt.Errorf("k8sguard: %s is set per room and cannot be templated", k)
		}
	}
	return nil
}

func roomHash(roomID string) string {
	return strings.TrimPrefix(roomspec.ContainerName(roomID), roomspec.NamePrefix)
}

func (t Template) Pod(roomID, token string) (Pod, error) {
	if err := roomspec.ValidID(roomID); err != nil {
		return Pod{}, err
	}
	if err := roomspec.ValidToken(token); err != nil {
		return Pod{}, err
	}

	keys := make([]string, 0, len(t.Env))
	for k := range t.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]EnvVar, 0, len(keys)+3)
	for _, k := range keys {
		env = append(env, EnvVar{Name: k, Value: t.Env[k]})
	}
	env = append(env,
		EnvVar{Name: "BROWCORD_GATEWAY_URL", Value: t.GatewayURL},
		EnvVar{Name: "BROWCORD_ROOM_ID", Value: roomID},
		EnvVar{Name: "BROWCORD_AGENT_TOKEN", Value: token},
	)

	probe := func(period, timeout, failures int) *Probe {
		return &Probe{
			Exec:             &ExecAction{Command: []string{"/bin/sh", "-c", healthCommand}},
			PeriodSeconds:    period,
			TimeoutSeconds:   timeout,
			FailureThreshold: failures,
		}
	}
	memDir := func(name, size string) Volume {
		return Volume{Name: name, EmptyDir: &EmptyDir{Medium: "Memory", SizeLimit: size}}
	}

	return Pod{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: ObjectMeta{
			Name: roomspec.ContainerName(roomID),
			Labels: map[string]string{
				roomspec.LabelManaged: "1",
				LabelRoomHash:         roomHash(roomID),
			},
			Annotations: map[string]string{roomspec.LabelRoom: roomID},
		},
		Spec: PodSpec{
			RestartPolicy:                 "Always",
			AutomountServiceAccountToken:  ptr(false),
			EnableServiceLinks:            ptr(false),
			HostUsers:                     ptr(false),
			TerminationGracePeriodSeconds: ptr(int64(gracePeriodSec)),
			SecurityContext: &PodSecurityContext{
				RunAsNonRoot:   ptr(true),
				RunAsUser:      ptr(int64(roomUID)),
				RunAsGroup:     ptr(int64(roomUID)),
				FSGroup:        ptr(int64(roomUID)),
				SeccompProfile: &SeccompProfile{Type: "Localhost", LocalhostProfile: t.Seccomp},
			},
			Containers: []Container{{
				Name:  containerName,
				Image: t.Image,
				Env:   env,
				SecurityContext: &SecurityContext{
					Privileged:               ptr(false),
					AllowPrivilegeEscalation: ptr(false),
					ReadOnlyRootFilesystem:   ptr(true),
					Capabilities:             &Capabilities{Drop: []string{"ALL"}},
				},
				Resources: Resources{
					Requests: map[string]string{"cpu": "1", "memory": "1Gi"},
					Limits:   map[string]string{"cpu": "2500m", "memory": "4Gi"},
				},
				VolumeMounts: []VolumeMount{
					{Name: "shm", MountPath: "/dev/shm"},
					{Name: "tmp", MountPath: "/tmp"},
					{Name: "profile", MountPath: "/profile"},
					{Name: "home", MountPath: "/home/browcord"},
				},
				StartupProbe:  probe(5, 5, 12),
				LivenessProbe: probe(30, 5, 3),
			}},
			Volumes: []Volume{
				memDir("shm", "1Gi"),
				memDir("tmp", "512Mi"),
				memDir("profile", "768Mi"),
				memDir("home", "64Mi"),
			},
		},
	}, nil
}

func ptr[T any](v T) *T { return &v }
