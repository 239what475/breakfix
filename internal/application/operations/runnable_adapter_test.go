package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestCompileNodeOperationsRevisionCreatesGenericRepairPlan(t *testing.T) {
	entry := operationsNodeEntry()
	entry.HasReferenceRepair = true
	spec, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, config())
	if err != nil {
		t.Fatalf("compile operations node revision: %v", err)
	}
	if spec.Identity.Kind != operationsContentKind || len(spec.Initialization) != 2 || len(spec.ValidationPlan.Phases) != 2 {
		t.Fatalf("unexpected compiled node spec: %#v", spec)
	}
	if got := spec.Initialization[1]; got.Entrypoint != "nodes/client/initialize.sh" || got.Target.ID != "client" || got.BoundaryID != "node-client-write" {
		t.Fatalf("node initialization did not retain generic target boundary: %#v", got)
	}
	initial := spec.ValidationPlan.Phases[0]
	if initial.ID != "initial-observation" || len(initial.Actions) != 0 || len(initial.Assertions) != 2 || initial.Assertions[0].Entrypoint != "nodes/host/assertions/initial-host-unready.sh" {
		t.Fatalf("initial observation was not compiled from Operations evidence: %#v", initial)
	}
	final := spec.ValidationPlan.Phases[1]
	if len(final.Actions) != 2 || final.Actions[0].Entrypoint != "nodes/host/actions/apply.sh" || len(final.Assertions) != 2 || final.Assertions[1].Entrypoint != "nodes/client/assertions/final-client-ready.sh" {
		t.Fatalf("repair projection was not compiled as generic actions/assertions: %#v", final)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("compiled spec validation: %v", err)
	}
}

func TestCompileK8sObservationOnlyOperationsRevision(t *testing.T) {
	entry := scenario.Entry{
		Type: scenario.ScenarioOperationsScenario, Runtime: scenario.RuntimeK8s, Topology: "isolated cluster",
		Versions:     []scenario.Version{{Component: "kubernetes", Version: "v1.36.0"}},
		Reproduction: scenario.Reproduction{Evidence: []scenario.ReproductionEvidence{{ID: "config-absent"}}},
	}
	spec, err := Compile(Input{ContentID: "operations-k8s", ContentRevision: "revision-02", Entry: entry, Source: source()}, config())
	if err != nil {
		t.Fatalf("compile k8s operations revision: %v", err)
	}
	if spec.RuntimeProfile.Runtime != runnable.RuntimeK8s || len(spec.Initialization) != 1 || spec.Initialization[0].Entrypoint != "k8s/initialize.sh" {
		t.Fatalf("k8s initialization = %#v", spec.Initialization)
	}
	if len(spec.ValidationPlan.Phases) != 1 || spec.ValidationPlan.Phases[0].Assertions[0].Entrypoint != "k8s/assertions/initial-config-absent.sh" || spec.ValidationPlan.Phases[0].Assertions[0].Target.Kind != "management" {
		t.Fatalf("k8s observation plan = %#v", spec.ValidationPlan)
	}
}

func TestCompileExistingOperationsFixtures(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		wantPhases int
	}{
		{name: "node-repair", fixture: "node-runtime-fixture", wantPhases: 2},
		{name: "k8s-observation", fixture: "k8s-reproduction-core", wantPhases: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := scenario.ValidateCandidateDir(fixtureDirectory(t, test.fixture))
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			spec, err := Compile(Input{ContentID: "operations-" + test.fixture, ContentRevision: "revision-01", Entry: *entry, Source: source()}, config())
			if err != nil {
				t.Fatalf("compile fixture: %v", err)
			}
			if len(spec.ValidationPlan.Phases) != test.wantPhases {
				t.Fatalf("compiled phase count = %d, want %d", len(spec.ValidationPlan.Phases), test.wantPhases)
			}
		})
	}
}

func TestCompileRejectsPlatformNodeLimitAndBadComponentIdentifier(t *testing.T) {
	entry := operationsNodeEntry()
	limited := config()
	limited.MaxNodes = 1
	if _, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, limited); err == nil || !strings.Contains(err.Error(), "exceeds platform limit") {
		t.Fatalf("expected node limit rejection, got %v", err)
	}
	entry = operationsNodeEntry()
	entry.Versions[0].Component = "bad component"
	if _, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, config()); err == nil || !strings.Contains(err.Error(), "stable lowercase") {
		t.Fatalf("expected component identifier rejection, got %v", err)
	}
}

func operationsNodeEntry() scenario.Entry {
	return scenario.Entry{
		Type: scenario.ScenarioOperationsScenario, Runtime: scenario.RuntimeNode, Topology: "two nodes",
		Nodes:        []scenario.Node{{Name: "host", Title: "Host"}, {Name: "client", Title: "Client"}},
		Versions:     []scenario.Version{{Component: "runtime", Version: "v1"}},
		Reproduction: scenario.Reproduction{Evidence: []scenario.ReproductionEvidence{{ID: "host-unready", Node: "host"}, {ID: "client-unready", Node: "client"}}},
		Checkpoints:  []scenario.Checkpoint{{ID: "host-ready", Node: "host"}, {ID: "client-ready", Node: "client"}},
	}
}

func config() Config {
	profile := RuntimeProfileConfig{
		ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("a", 64),
		Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 256, MaxConcurrentTasks: 2},
		Network:   runnable.NetworkPrivate, MaxActionTimeout: 1200,
	}
	return Config{
		MaxNodes: 4, Node: profile, K8s: profile,
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 1800, MaxLifetimeSeconds: 3600},
	}
}

func source() runnable.SourceArchive {
	return runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/operations.tar.gz", Digest: "sha256:" + strings.Repeat("b", 64)}
}

func fixtureDirectory(t *testing.T, name string) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		candidate := filepath.Join(directory, "test", "fixtures", "catalog-release", "scenarios", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("find fixture directory %q", name)
		}
		directory = parent
	}
}
