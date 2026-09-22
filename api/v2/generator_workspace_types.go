package v2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GeneratorWorkspaceCleanupFinalizer guards the cluster-external OpenSandbox
// Sandbox: whoever removes it has deleted the Sandbox, so the API server may
// drop the CR and let the garbage collector cascade to the owned PVC.
const GeneratorWorkspaceCleanupFinalizer = "breakfix.dev/workspace-cleanup"

type WorkspacePhase string

const (
	WorkspacePhasePending  WorkspacePhase = "Pending"
	WorkspacePhaseActive   WorkspacePhase = "Active"
	WorkspacePhaseDeleting WorkspacePhase = "Deleting"
)

// GeneratorWorkspaceSpec is intentionally empty. The Server is the only
// writer of the whole object and its desired state is the CR existing at all:
// the name is the durable workspace ID, while every ownership fact lives in
// the status.
type GeneratorWorkspaceSpec struct{}

// GeneratorWorkspaceStatus carries the ownership facts that must survive a
// PostgreSQL reset. The name is the workspace ID; these fields let the Server
// rebuild a projection row and drop cluster resources without the database.
type GeneratorWorkspaceStatus struct {
	// +kubebuilder:validation:MinLength=1
	WorkflowID string `json:"workflowID"`
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	PVCName string `json:"pvcName"`
	// +optional
	SandboxID string `json:"sandboxID,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Active;Deleting
	Phase WorkspacePhase `json:"phase"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Workflow",type=string,JSONPath=`.status.workflowID`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type GeneratorWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GeneratorWorkspaceSpec   `json:"spec"`
	Status GeneratorWorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GeneratorWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GeneratorWorkspace `json:"items"`
}
