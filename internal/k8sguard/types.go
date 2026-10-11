package k8sguard

type Pod struct {
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       PodSpec    `json:"spec"`
	Status     PodStatus  `json:"status,omitempty"`
}

type PodList struct {
	Items []Pod `json:"items"`
}

type ObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	DeletionTimestamp string            `json:"deletionTimestamp,omitempty"`
}

type PodSpec struct {
	RestartPolicy                 string              `json:"restartPolicy,omitempty"`
	AutomountServiceAccountToken  *bool               `json:"automountServiceAccountToken,omitempty"`
	EnableServiceLinks            *bool               `json:"enableServiceLinks,omitempty"`
	HostUsers                     *bool               `json:"hostUsers,omitempty"`
	HostNetwork                   bool                `json:"hostNetwork,omitempty"`
	HostPID                       bool                `json:"hostPID,omitempty"`
	HostIPC                       bool                `json:"hostIPC,omitempty"`
	TerminationGracePeriodSeconds *int64              `json:"terminationGracePeriodSeconds,omitempty"`
	SecurityContext               *PodSecurityContext `json:"securityContext,omitempty"`
	Containers                    []Container         `json:"containers"`
	Volumes                       []Volume            `json:"volumes,omitempty"`
}

type PodSecurityContext struct {
	RunAsNonRoot   *bool           `json:"runAsNonRoot,omitempty"`
	RunAsUser      *int64          `json:"runAsUser,omitempty"`
	RunAsGroup     *int64          `json:"runAsGroup,omitempty"`
	FSGroup        *int64          `json:"fsGroup,omitempty"`
	SeccompProfile *SeccompProfile `json:"seccompProfile,omitempty"`
}

type SeccompProfile struct {
	Type             string `json:"type"`
	LocalhostProfile string `json:"localhostProfile,omitempty"`
}

type Container struct {
	Name            string           `json:"name"`
	Image           string           `json:"image"`
	Env             []EnvVar         `json:"env,omitempty"`
	SecurityContext *SecurityContext `json:"securityContext,omitempty"`
	Resources       Resources        `json:"resources"`
	VolumeMounts    []VolumeMount    `json:"volumeMounts,omitempty"`
	Ports           []any            `json:"ports,omitempty"`
	StartupProbe    *Probe           `json:"startupProbe,omitempty"`
	LivenessProbe   *Probe           `json:"livenessProbe,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SecurityContext struct {
	Privileged               *bool         `json:"privileged,omitempty"`
	AllowPrivilegeEscalation *bool         `json:"allowPrivilegeEscalation,omitempty"`
	ReadOnlyRootFilesystem   *bool         `json:"readOnlyRootFilesystem,omitempty"`
	Capabilities             *Capabilities `json:"capabilities,omitempty"`
}

type Capabilities struct {
	Add  []string `json:"add,omitempty"`
	Drop []string `json:"drop,omitempty"`
}

type Resources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type Volume struct {
	Name     string    `json:"name"`
	EmptyDir *EmptyDir `json:"emptyDir,omitempty"`
	HostPath any       `json:"hostPath,omitempty"`
}

type EmptyDir struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"sizeLimit,omitempty"`
}

type Probe struct {
	Exec             *ExecAction `json:"exec,omitempty"`
	PeriodSeconds    int         `json:"periodSeconds,omitempty"`
	TimeoutSeconds   int         `json:"timeoutSeconds,omitempty"`
	FailureThreshold int         `json:"failureThreshold,omitempty"`
}

type ExecAction struct {
	Command []string `json:"command"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	ContainerStatuses []ContainerStatus `json:"containerStatuses,omitempty"`
}

type ContainerStatus struct {
	Name         string         `json:"name"`
	RestartCount int            `json:"restartCount"`
	State        ContainerState `json:"state"`
}

type ContainerState struct {
	Waiting    *struct{} `json:"waiting,omitempty"`
	Running    *struct{} `json:"running,omitempty"`
	Terminated *struct{} `json:"terminated,omitempty"`
}
