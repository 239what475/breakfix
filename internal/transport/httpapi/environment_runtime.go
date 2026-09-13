package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	nodeEnvironmentReadyTimeout = 5 * time.Minute
	vk8sEnvironmentReadyTimeout = 10 * time.Minute
	environmentDrainGrace       = 30 * time.Second
	environmentCreateAttempts   = 3
)

type serverEnvironment interface {
	CommonEnvironmentSpec() *breakfixv1.EnvironmentSpec
}

func (h *Handler) newEnvironmentSpec(userID string, entry *scenario.Entry) (breakfixv1.EnvironmentSpec, error) {
	if entry == nil {
		return breakfixv1.EnvironmentSpec{}, fmt.Errorf("scenario is required")
	}
	if entry.Runtime != scenario.RuntimeNode && entry.Runtime != scenario.RuntimeK8s {
		return breakfixv1.EnvironmentSpec{}, fmt.Errorf("scenario %q has unsupported runtime %q", entry.ID, entry.Runtime)
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(entry.ID) == "" || !scenario.ValidRevisionID(entry.RevisionID) {
		return breakfixv1.EnvironmentSpec{}, fmt.Errorf("scenario %q has an incomplete published identity", entry.ID)
	}

	checkpoints := make([]breakfixv1.EnvironmentCheckpointSpec, 0, len(entry.Checkpoints))
	seen := make(map[string]struct{}, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" {
			return breakfixv1.EnvironmentSpec{}, fmt.Errorf("scenario %q has an empty checkpoint id", entry.ID)
		}
		if _, duplicate := seen[id]; duplicate {
			return breakfixv1.EnvironmentSpec{}, fmt.Errorf("scenario %q has duplicate checkpoint id %q", entry.ID, id)
		}
		seen[id] = struct{}{}
		checkpoints = append(checkpoints, breakfixv1.EnvironmentCheckpointSpec{ID: id, Node: strings.TrimSpace(checkpoint.Node)})
	}
	activityAt := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	idleTTLSeconds := int64(h.cooldownMin * 60)
	drainGraceSeconds := int64(environmentDrainGrace / time.Second)
	return breakfixv1.EnvironmentSpec{
		Purpose: breakfixv1.EnvironmentPurposeLearning,
		Source: breakfixv1.EnvironmentSourceSpec{
			Kind: breakfixv1.EnvironmentSourcePublished, Ref: entry.ID, Revision: entry.RevisionID,
		},
		UserRef:     userID,
		Checkpoints: checkpoints,
		Lifecycle: breakfixv1.EnvironmentLifecycleSpec{
			ActivityAt: &activityAt, IdleTTLSeconds: &idleTTLSeconds, DrainGracePeriodSeconds: &drainGraceSeconds,
		},
	}, nil
}

type environmentRuntimeAdapter struct {
	runtime         string
	readyTimeout    time.Duration
	list            func(context.Context, string) ([]activeEnvironment, error)
	get             func(context.Context, string) (*activeEnvironment, error)
	create          func(context.Context, *postgres.User, *scenario.Entry) (string, error)
	updateSpec      func(context.Context, string, func(*breakfixv1.EnvironmentSpec)) error
	requestDeletion func(context.Context, string, types.UID) error
}

func (a *environmentRuntimeAdapter) renewActivity(ctx context.Context, name string, activityAt metav1.Time) error {
	if a == nil || a.updateSpec == nil {
		return fmt.Errorf("environment runtime does not support activity updates")
	}
	return a.updateSpec(ctx, name, func(spec *breakfixv1.EnvironmentSpec) {
		activityAt = metav1.NewTime(activityAt.UTC().Truncate(time.Second))
		if spec.Lifecycle.ActivityAt == nil || activityAt.After(spec.Lifecycle.ActivityAt.Time) {
			next := activityAt
			spec.Lifecycle.ActivityAt = &next
		}
	})
}

func collectActiveEnvironments[T any](items []T, toActive func(T) *activeEnvironment) []activeEnvironment {
	result := make([]activeEnvironment, 0, len(items))
	for _, item := range items {
		if environment := toActive(item); environment != nil {
			result = append(result, *environment)
		}
	}
	return result
}

func mutateEnvironmentSpec[T serverEnvironment](ctx context.Context, name string, get func(context.Context, string) (T, error), update func(context.Context, T) error, mutate func(*breakfixv1.EnvironmentSpec)) error {
	environment, err := get(ctx, name)
	if err != nil {
		return err
	}
	mutate(environment.CommonEnvironmentSpec())
	return update(ctx, environment)
}

func (h *Handler) environmentRuntimeAdapter(runtime string) (*environmentRuntimeAdapter, error) {
	switch scenario.NormalizeRuntime(runtime) {
	case scenario.RuntimeNode:
		getNode := func(ctx context.Context, name string) (*breakfixv1.NodeEnvironment, error) {
			return h.k8s.GetNodeEnvironment(ctx, h.crdNamespace, name)
		}
		return &environmentRuntimeAdapter{
			runtime: scenario.RuntimeNode, readyTimeout: nodeEnvironmentReadyTimeout,
			list: func(ctx context.Context, selector string) ([]activeEnvironment, error) {
				environments, err := h.k8s.ListNodeEnvironments(ctx, h.crdNamespace, selector)
				if err != nil {
					return nil, err
				}
				items := make([]*breakfixv1.NodeEnvironment, 0, len(environments.Items))
				for index := range environments.Items {
					if environments.Items[index].DeletionTimestamp == nil {
						items = append(items, &environments.Items[index])
					}
				}
				return collectActiveEnvironments(items, environmentFromNode), nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				environment, err := getNode(ctx, name)
				if err != nil {
					return nil, err
				}
				return environmentFromNode(environment), nil
			},
			create: func(ctx context.Context, user *postgres.User, entry *scenario.Entry) (string, error) {
				spec, err := h.newEnvironmentSpec(user.ID, entry)
				if err != nil {
					return "", err
				}
				nodes := make([]breakfixv1.NodeRuntimeNodeSpec, len(entry.Nodes))
				for index, node := range entry.Nodes {
					nodes[index] = breakfixv1.NodeRuntimeNodeSpec{Name: node.Name, Title: node.Title}
				}
				name := learningEnvironmentName(user.ID, entry)
				environment := &breakfixv1.NodeEnvironment{
					ObjectMeta: environmentObjectMeta(name, h.crdNamespace, user.ID, entry.ID, breakfixv1.EnvironmentPurposeLearning),
					Spec: breakfixv1.NodeEnvironmentSpec{
						Environment: spec,
						Runtime: breakfixv1.NodeRuntimeSnapshot{
							ImageFingerprint: entry.Image, ProfileRevision: h.runtimeConfig.Node.ProfileRevision,
							NetworkPolicyRevision: h.runtimeConfig.Node.NetworkPolicyRevision, Nodes: nodes,
							Resources: breakfixv1.NodeResourceSnapshot{
								CPU: h.incusConfig.NodeCPU, Memory: h.incusConfig.NodeMemory,
								Processes: h.incusConfig.NodeProcesses, RootDisk: h.incusConfig.NodeRootDisk,
							},
						},
					},
				}
				if _, err := h.k8s.CreateNodeEnvironment(ctx, h.crdNamespace, environment); err != nil {
					return "", fmt.Errorf("create node environment: %w", err)
				}
				return name, nil
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.EnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getNode, func(ctx context.Context, environment *breakfixv1.NodeEnvironment) error {
					_, err := h.k8s.UpdateNodeEnvironment(ctx, h.crdNamespace, environment)
					return err
				}, mutate)
			},
			requestDeletion: func(ctx context.Context, name string, uid types.UID) error {
				if uid != "" {
					return h.k8s.DeleteNodeEnvironmentWithUID(ctx, h.crdNamespace, name, uid)
				}
				return h.k8s.DeleteNodeEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil

	case scenario.RuntimeK8s:
		getVK8s := func(ctx context.Context, name string) (*breakfixv1.VK8sEnvironment, error) {
			return h.k8s.GetVK8sEnvironment(ctx, h.crdNamespace, name)
		}
		return &environmentRuntimeAdapter{
			runtime: scenario.RuntimeK8s, readyTimeout: vk8sEnvironmentReadyTimeout,
			list: func(ctx context.Context, selector string) ([]activeEnvironment, error) {
				environments, err := h.k8s.ListVK8sEnvironments(ctx, h.crdNamespace, selector)
				if err != nil {
					return nil, err
				}
				items := make([]*breakfixv1.VK8sEnvironment, 0, len(environments.Items))
				for index := range environments.Items {
					if environments.Items[index].DeletionTimestamp == nil {
						items = append(items, &environments.Items[index])
					}
				}
				return collectActiveEnvironments(items, environmentFromVK8s), nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				environment, err := getVK8s(ctx, name)
				if err != nil {
					return nil, err
				}
				return environmentFromVK8s(environment), nil
			},
			create: func(ctx context.Context, user *postgres.User, entry *scenario.Entry) (string, error) {
				spec, err := h.newEnvironmentSpec(user.ID, entry)
				if err != nil {
					return "", err
				}
				name := learningEnvironmentName(user.ID, entry)
				resources := h.runtimeConfig.K8s.Resources
				environment := &breakfixv1.VK8sEnvironment{
					ObjectMeta: environmentObjectMeta(name, h.crdNamespace, user.ID, entry.ID, breakfixv1.EnvironmentPurposeLearning),
					Spec: breakfixv1.VK8sEnvironmentSpec{
						Environment: spec,
						Runtime: breakfixv1.VK8sRuntimeSnapshot{
							ImageDigest: entry.Image, ProfileRevision: h.runtimeConfig.K8s.ProfileRevision,
							Version: h.runtimeConfig.K8s.Version, ManagementTerminalImage: h.runtimeConfig.K8s.ManagementTerminalImage,
							Resources: breakfixv1.VK8sResourceSnapshot{
								ControlPlaneCPU: resources.ControlPlaneCPU, ControlPlaneMemory: resources.ControlPlaneMemory,
								ControlPlaneEphemeralStorage: resources.ControlPlaneEphemeralStorage,
								WorkloadCPU:                  resources.WorkloadCPU, WorkloadMemory: resources.WorkloadMemory,
								WorkloadEphemeralStorage: resources.WorkloadEphemeralStorage,
								QuotaCPU:                 resources.QuotaCPU, QuotaMemory: resources.QuotaMemory,
								QuotaEphemeralStorage: resources.QuotaEphemeralStorage,
							},
							Network: breakfixv1.VK8sNetworkSnapshot{
								PublicEgressCIDR: h.runtimeConfig.K8s.Network.PublicEgressCIDR,
								ProtectedCIDRs:   append([]string(nil), h.runtimeConfig.K8s.Network.ProtectedCIDRs...),
							},
						},
					},
				}
				if _, err := h.k8s.CreateVK8sEnvironment(ctx, h.crdNamespace, environment); err != nil {
					return "", fmt.Errorf("create VK8s environment: %w", err)
				}
				return name, nil
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.EnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getVK8s, func(ctx context.Context, environment *breakfixv1.VK8sEnvironment) error {
					_, err := h.k8s.UpdateVK8sEnvironment(ctx, h.crdNamespace, environment)
					return err
				}, mutate)
			},
			requestDeletion: func(ctx context.Context, name string, uid types.UID) error {
				if uid != "" {
					return h.k8s.DeleteVK8sEnvironmentWithUID(ctx, h.crdNamespace, name, uid)
				}
				return h.k8s.DeleteVK8sEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
}

// learningEnvironmentName gives concurrent Start requests the same CRD name.
// A stable name makes the Kubernetes API the cross-server ownership fence,
// rather than relying on a process-local lock or a best-effort List then Create.
func learningEnvironmentName(userID string, entry *scenario.Entry) string {
	if entry == nil {
		return kubernetes.DNSLabelName("learning", userID)
	}
	return kubernetes.DNSLabelName("learning", userID, entry.ID, entry.RevisionID)
}

func environmentObjectMeta(name, namespace, userID, scenarioID string, purpose breakfixv1.EnvironmentPurpose) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name, Namespace: namespace,
		Labels: map[string]string{
			"breakfix.dev/user": userID, "breakfix.dev/scenario": scenarioID, "breakfix.dev/purpose": string(purpose),
		},
	}
}

func nowActivity() metav1.Time {
	return metav1.NewTime(time.Now().UTC())
}
