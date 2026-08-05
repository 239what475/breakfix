package environment

import (
	"context"
	"errors"
)

// Provider errors classify retryable infrastructure outcomes without exposing
// an Incus, Kubernetes, or vcluster SDK error to a reconciler.
var (
	ErrProviderUnavailable = errors.New("environment provider unavailable")
	ErrProviderNotFound    = errors.New("environment provider resource not found")
	ErrProviderConflict    = errors.New("environment provider resource conflict")
)

type NodeResources struct {
	CPU       string
	Memory    string
	Processes int64
	RootDisk  string
}

type NodeIdentity struct {
	LogicalName  string
	InstanceName string
	Address      string
}

type NodeEnvironmentIdentity struct {
	Project string
	Network string
	ACL     string
	Profile string
	Nodes   []NodeIdentity
}

type NodeProvisionRequest struct {
	EnvironmentUID        string
	Revision              string
	ImageFingerprint      string
	ProfileRevision       string
	NetworkPolicyRevision string
	Identity              NodeEnvironmentIdentity
	Resources             NodeResources
}

type InitializationObservation struct {
	Complete bool
	Failed   bool
	ExitCode int
	Message  string
}

type NodeObservation struct {
	Node           NodeIdentity
	Running        bool
	Initialization InitializationObservation
}

type NodeEnvironmentObservation struct {
	Identity NodeEnvironmentIdentity
	Nodes    []NodeObservation
	Ready    bool
}

type NodeExecutionRequest struct {
	EnvironmentUID string
	Revision       string
	Identity       NodeEnvironmentIdentity
	LogicalName    string
	Command        []string
}

type ExecutionResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// NodeProvider owns the provider-specific lifecycle for a NodeEnvironment.
// Reconciliation uses this port instead of Incus SDK types.
type NodeProvider interface {
	Preflight(context.Context) error
	Identity(environmentUID string, logicalNames []string) (NodeEnvironmentIdentity, error)
	Provision(context.Context, NodeProvisionRequest) (NodeEnvironmentObservation, error)
	Observe(context.Context, NodeProvisionRequest) (NodeEnvironmentObservation, error)
	Delete(context.Context, NodeProvisionRequest) error
	Execute(context.Context, NodeExecutionRequest) (ExecutionResult, error)
}

type VK8sRuntimeResources struct {
	ControlPlaneCPU              string
	ControlPlaneMemory           string
	ControlPlaneEphemeralStorage string
	WorkloadCPU                  string
	WorkloadMemory               string
	WorkloadEphemeralStorage     string
	QuotaCPU                     string
	QuotaMemory                  string
	QuotaEphemeralStorage        string
}

type VK8sRuntime struct {
	ImageDigest             string
	ProfileRevision         string
	Version                 string
	ManagementTerminalImage string
	Resources               VK8sRuntimeResources
	Network                 VK8sNetwork
}

type VK8sEnvironmentIdentity struct {
	Namespace            string
	VClusterName         string
	KubeconfigSecretName string
	TerminalPodName      string
}

type VK8sProvisionRequest struct {
	EnvironmentUID string
	Revision       string
	Purpose        Purpose
	Identity       VK8sEnvironmentIdentity
	Runtime        VK8sRuntime
}

type VK8sEnvironmentObservation struct {
	ControlPlaneReady bool
	KubeconfigReady   bool
	TerminalReady     bool
	Initialization    InitializationObservation
}

// VK8sProvider owns the provider-specific lifecycle for a VK8sEnvironment.
// Its implementation composes Kubernetes and vcluster adapters.
type VK8sProvider interface {
	Identity(environmentUID string) (VK8sEnvironmentIdentity, error)
	Provision(context.Context, VK8sProvisionRequest) (VK8sEnvironmentObservation, error)
	Delete(context.Context, VK8sProvisionRequest) (bool, error)
	ExecuteTerminal(context.Context, VK8sProvisionRequest, []string) (ExecutionResult, error)
}
