package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type EnvironmentPurpose string

const (
	EnvironmentPurposeLearning     EnvironmentPurpose = "learning"
	EnvironmentPurposeVerification EnvironmentPurpose = "verification"
)

type EnvironmentSourceKind string

const (
	EnvironmentSourcePublished EnvironmentSourceKind = "published"
	EnvironmentSourceCandidate EnvironmentSourceKind = "candidate"
)

type EnvironmentFailureClass string

const (
	EnvironmentFailureArtifact       EnvironmentFailureClass = "artifact"
	EnvironmentFailureInfrastructure EnvironmentFailureClass = "infrastructure"
)

type EnvironmentSourceSpec struct {
	// +kubebuilder:validation:Enum=published;candidate
	Kind     EnvironmentSourceKind `json:"kind"`
	Ref      string                `json:"ref"`
	Revision string                `json:"revision"`
}

type EnvironmentCheckpointSpec struct {
	ID   string `json:"id"`
	Node string `json:"node,omitempty"`
}

type EnvironmentLifecycleSpec struct {
	ActivityAt              *metav1.Time `json:"activityAt,omitempty"`
	DeadlineAt              *metav1.Time `json:"deadlineAt,omitempty"`
	IdleTTLSeconds          *int64       `json:"idleTtlSeconds,omitempty"`
	DrainGracePeriodSeconds *int64       `json:"drainGracePeriodSeconds,omitempty"`
}

// EnvironmentSpec is shared by both runtime kinds. Source, purpose and
// checkpoints are immutable; only ActivityAt may be renewed by Server.
type EnvironmentSpec struct {
	// +kubebuilder:validation:Enum=learning;verification
	Purpose     EnvironmentPurpose          `json:"purpose"`
	Source      EnvironmentSourceSpec       `json:"source"`
	UserRef     string                      `json:"userRef,omitempty"`
	Checkpoints []EnvironmentCheckpointSpec `json:"checkpoints"`
	Lifecycle   EnvironmentLifecycleSpec    `json:"lifecycle"`
}

type EnvironmentFailureStatus struct {
	// +kubebuilder:validation:Enum=artifact;infrastructure
	Class     EnvironmentFailureClass `json:"class"`
	Component string                  `json:"component"`
	Reason    string                  `json:"reason"`
	Message   string                  `json:"message,omitempty"`
	At        metav1.Time             `json:"at"`
}

type EnvironmentStatus struct {
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Draining;Completed;Destroyed;Failed
	Phase              EnvironmentPhase          `json:"phase,omitempty"`
	ObservedGeneration int64                     `json:"observedGeneration,omitempty"`
	StartedAt          *metav1.Time              `json:"startedAt,omitempty"`
	ReadyAt            *metav1.Time              `json:"readyAt,omitempty"`
	LastActivityAt     *metav1.Time              `json:"lastActivityAt,omitempty"`
	CompletedAt        *metav1.Time              `json:"completedAt,omitempty"`
	ExpiresAt          *metav1.Time              `json:"expiresAt,omitempty"`
	DestroyedAt        *metav1.Time              `json:"destroyedAt,omitempty"`
	Conditions         []metav1.Condition        `json:"conditions,omitempty"`
	Failure            *EnvironmentFailureStatus `json:"failure,omitempty"`
	Checkpoints        *CheckpointStatus         `json:"checkpoints,omitempty"`
}

type NodeRuntimeNodeSpec struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

type NodeResourceSnapshot struct {
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	Processes int64  `json:"processes"`
	RootDisk  string `json:"rootDisk"`
}

type NodeRuntimeSnapshot struct {
	ImageFingerprint      string                `json:"imageFingerprint"`
	ProfileRevision       string                `json:"profileRevision"`
	NetworkPolicyRevision string                `json:"networkPolicyRevision"`
	Nodes                 []NodeRuntimeNodeSpec `json:"nodes"`
	Resources             NodeResourceSnapshot  `json:"resources"`
}

type NodeEnvironmentSpec struct {
	Environment EnvironmentSpec     `json:"environment"`
	Runtime     NodeRuntimeSnapshot `json:"runtime"`
}

type NodeInstanceStatus struct {
	Name         string `json:"name"`
	InstanceName string `json:"instanceName"`
	Address      string `json:"address,omitempty"`
	Initialized  bool   `json:"initialized,omitempty"`
}

type NodeRuntimeStatus struct {
	Project          string               `json:"project,omitempty"`
	Network          string               `json:"network,omitempty"`
	Profile          string               `json:"profile,omitempty"`
	ImageFingerprint string               `json:"imageFingerprint,omitempty"`
	Nodes            []NodeInstanceStatus `json:"nodes,omitempty"`
}

type NodeEnvironmentStatus struct {
	Environment EnvironmentStatus `json:"environment,omitempty"`
	Runtime     NodeRuntimeStatus `json:"runtime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Purpose",type=string,JSONPath=`.spec.environment.purpose`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.environment.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NodeEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeEnvironmentSpec   `json:"spec"`
	Status NodeEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeEnvironment `json:"items"`
}

type VK8sResourceSnapshot struct {
	ControlPlaneCPU       string `json:"controlPlaneCpu"`
	ControlPlaneMemory    string `json:"controlPlaneMemory"`
	QuotaCPU              string `json:"quotaCpu"`
	QuotaMemory           string `json:"quotaMemory"`
	QuotaEphemeralStorage string `json:"quotaEphemeralStorage"`
	TerminalCPU           string `json:"terminalCpu"`
	TerminalMemory        string `json:"terminalMemory"`
}

type VK8sRuntimeSnapshot struct {
	ImageDigest             string               `json:"imageDigest"`
	ProfileRevision         string               `json:"profileRevision"`
	Version                 string               `json:"version"`
	ManagementTerminalImage string               `json:"managementTerminalImage"`
	Resources               VK8sResourceSnapshot `json:"resources"`
}

type VK8sEnvironmentSpec struct {
	Environment EnvironmentSpec     `json:"environment"`
	Runtime     VK8sRuntimeSnapshot `json:"runtime"`
}

type VK8sRuntimeStatus struct {
	Namespace            string `json:"namespace,omitempty"`
	VClusterName         string `json:"vclusterName,omitempty"`
	KubeconfigSecretName string `json:"kubeconfigSecretName,omitempty"`
	TerminalPodName      string `json:"terminalPodName,omitempty"`
	Initialized          bool   `json:"initialized,omitempty"`
}

type VK8sEnvironmentStatus struct {
	Environment EnvironmentStatus `json:"environment,omitempty"`
	Runtime     VK8sRuntimeStatus `json:"runtime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Purpose",type=string,JSONPath=`.spec.environment.purpose`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.environment.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VK8sEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VK8sEnvironmentSpec   `json:"spec"`
	Status VK8sEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VK8sEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VK8sEnvironment `json:"items"`
}

func (in *NodeEnvironment) CommonEnvironmentSpec() *EnvironmentSpec {
	return &in.Spec.Environment
}

func (in *NodeEnvironment) CommonEnvironmentStatus() *EnvironmentStatus {
	return &in.Status.Environment
}

func (in *VK8sEnvironment) CommonEnvironmentSpec() *EnvironmentSpec {
	return &in.Spec.Environment
}

func (in *VK8sEnvironment) CommonEnvironmentStatus() *EnvironmentStatus {
	return &in.Status.Environment
}
