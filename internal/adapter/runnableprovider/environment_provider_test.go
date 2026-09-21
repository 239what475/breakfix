package runnableprovider

import (
	"context"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/controller/runtimeenvironment"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestEnvironmentProviderProvisionsAndResetsNodeFromRunnableRevision(t *testing.T) {
	revision := providerRevision(t, runnable.RuntimeNode)
	node := &fakeNodeEnvironmentProvider{}
	k8s := &fakeK8sEnvironmentProvider{}
	provider := testEnvironmentProvider(t, node, k8s)
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, RunnableRevision: revision}

	observation, err := provider.Provision(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Ready || node.provisioned != 1 || node.request.ImageFingerprint != strings.Repeat("a", 64) || node.request.ProfileRevision != revision.Spec.RuntimeProfile.ProfileRevision {
		t.Fatalf("Node provision = %#v, calls = %d", node.request, node.provisioned)
	}
	if len(observation.ResourceRefs) != 5 || observation.EndpointRefs[0].Ref != "10.1.2.3" {
		t.Fatalf("Node observation = %#v", observation)
	}
	if _, err := provider.Reset(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if node.deleted != 1 || node.provisioned != 2 {
		t.Fatalf("Node reset calls delete=%d provision=%d", node.deleted, node.provisioned)
	}
}

func TestEnvironmentProviderProvisionsAndReleasesK8sFromRunnableRevision(t *testing.T) {
	revision := providerRevision(t, runnable.RuntimeK8s)
	node := &fakeNodeEnvironmentProvider{}
	k8s := &fakeK8sEnvironmentProvider{}
	provider := testEnvironmentProvider(t, node, k8s)
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeVerification, RunnableRevision: revision}

	observation, err := provider.Provision(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Ready || k8s.provisioned != 1 || k8s.request.Runtime.ImageDigest != revision.Artifact.ProviderReference || k8s.request.Purpose != environment.PurposeVerification {
		t.Fatalf("K8s provision = %#v, calls = %d", k8s.request, k8s.provisioned)
	}
	if len(observation.ResourceRefs) != 3 || len(observation.EndpointRefs) != 2 {
		t.Fatalf("K8s observation = %#v", observation)
	}
	done, err := provider.Release(context.Background(), binding)
	if err != nil || !done || k8s.deleted != 1 {
		t.Fatalf("K8s release done=%v deletes=%d err=%v", done, k8s.deleted, err)
	}
}

func TestEnvironmentProviderRejectsMismatchedInstalledProfile(t *testing.T) {
	revision := providerRevision(t, runnable.RuntimeNode)
	provider := testEnvironmentProvider(t, &fakeNodeEnvironmentProvider{}, &fakeK8sEnvironmentProvider{})
	revision.Spec.RuntimeProfile.ProfileRevision = "another-profile"
	digest, err := revision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revision.Artifact.BuiltFromSpecDigest = digest
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, RunnableRevision: revision}
	if _, err := provider.Provision(context.Background(), binding); err == nil || !strings.Contains(err.Error(), "Node runnable profile") {
		t.Fatalf("expected installed profile error, got %v", err)
	}
}

func TestEnvironmentProviderTreatsMissingNodeResourcesAsReleased(t *testing.T) {
	revision := providerRevision(t, runnable.RuntimeNode)
	node := &fakeNodeEnvironmentProvider{deleteErr: environment.ErrProviderNotFound}
	provider := testEnvironmentProvider(t, node, &fakeK8sEnvironmentProvider{})
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, RunnableRevision: revision}
	done, err := provider.Release(context.Background(), binding)
	if err != nil || !done {
		t.Fatalf("missing Node release done=%v err=%v", done, err)
	}
}

func testEnvironmentProvider(t *testing.T, node NodeEnvironmentProvider, k8s K8sEnvironmentProvider) *EnvironmentProvider {
	t.Helper()
	provider, err := NewEnvironmentProvider(node, k8s, EnvironmentProviderConfig{
		Node: NodeEnvironmentConfig{ProfileRevision: "profile-1", NetworkPolicyRevision: "network-1", Resources: incus.NodeEnvironmentResources{CPU: "1", Memory: "1Gi", Processes: 1, RootDisk: "1Gi"}},
		K8s:  environment.VK8sRuntime{ProfileRevision: "profile-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func providerRevision(t *testing.T, runtimeType runnable.Runtime) runnable.RunnableRevision {
	t.Helper()
	spec := testSpec(runtimeType)
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	providerReference := "incus://breakfix/image@" + artifactDigest
	if runtimeType == runnable.RuntimeK8s {
		providerReference = "registry.example/runnable@" + artifactDigest
	}
	return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runtimeType, ProviderReference: providerReference, ArtifactDigest: artifactDigest, BuiltFromSpecDigest: digest, BuilderVersion: "builder-1"}}
}

type fakeNodeEnvironmentProvider struct {
	request     incus.ProvisionNodeEnvironmentRequest
	provisioned int
	deleted     int
	deleteErr   error
}

func (p *fakeNodeEnvironmentProvider) NodeEnvironmentIdentity(_ string, names []string) (incus.NodeEnvironmentIdentity, error) {
	identity := incus.NodeEnvironmentIdentity{Project: "project", Network: "network", ACL: "acl", Profile: "profile"}
	for _, name := range names {
		identity.Nodes = append(identity.Nodes, incus.NodeIdentity{LogicalName: name, InstanceName: "node-" + name, Address: "10.1.2.3"})
	}
	return identity, nil
}

func (p *fakeNodeEnvironmentProvider) ProvisionNodeEnvironment(_ context.Context, request incus.ProvisionNodeEnvironmentRequest) (incus.NodeEnvironmentObservation, error) {
	p.request = request
	p.provisioned++
	return incus.NodeEnvironmentObservation{Identity: request.Identity, Ready: true}, nil
}

func (p *fakeNodeEnvironmentProvider) DeleteNodeEnvironment(_ context.Context, _ incus.ProvisionNodeEnvironmentRequest) error {
	p.deleted++
	return p.deleteErr
}

type fakeK8sEnvironmentProvider struct {
	request       environment.VK8sProvisionRequest
	deleteRequest environment.VK8sProvisionRequest
	provisioned   int
	deleted       int
}

func (p *fakeK8sEnvironmentProvider) Identity(_ string) (environment.VK8sEnvironmentIdentity, error) {
	return environment.VK8sEnvironmentIdentity{Namespace: "runtime-environment", VClusterName: "vc-runtime", KubeconfigSecretName: "kubeconfig", TerminalPodName: "terminal"}, nil
}

func (p *fakeK8sEnvironmentProvider) Provision(_ context.Context, request environment.VK8sProvisionRequest) (environment.VK8sEnvironmentObservation, error) {
	p.request = request
	p.provisioned++
	return environment.VK8sEnvironmentObservation{ControlPlaneReady: true, KubeconfigReady: true, TerminalReady: true}, nil
}

func (p *fakeK8sEnvironmentProvider) Delete(_ context.Context, request environment.VK8sProvisionRequest) (bool, error) {
	p.deleteRequest = request
	p.deleted++
	return true, nil
}
