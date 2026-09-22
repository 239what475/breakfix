package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeachingAssetFixturesValidate(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		directory string
		validate  func(string) (*Entry, error)
	}{
		{name: "catalog release fixture", directory: filepath.Join(projectRoot, "test", "fixtures", "catalog-release", "scenarios", "node-runtime-fixture"), validate: ValidatePortableDir},
		{name: "node runtime init", directory: filepath.Join(projectRoot, "test", "fixtures", "candidates", "node-runtime-init"), validate: ValidateCandidateDir},
		{name: "node checkpoint coverage", directory: filepath.Join(projectRoot, "test", "fixtures", "candidates", "node-checkpoint-dependency"), validate: ValidateCandidateDir},
		{name: "node reverse proxy", directory: filepath.Join(projectRoot, "test", "fixtures", "candidates", "node-reverse-proxy"), validate: ValidateCandidateDir},
		{name: "k8s web service", directory: filepath.Join(projectRoot, "test", "fixtures", "candidates", "k8s-web-service"), validate: ValidateCandidateDir},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := os.Stat(test.directory); err != nil {
				t.Fatal(err)
			}
			if _, err := test.validate(test.directory); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateCandidateDirAllowsCheckpointWithoutHint(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "", "<!-- checkpoint: complete -->\n")

	if _, err := ValidateCandidateDir(root); err != nil {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRequiresReferencedCheckpointHint(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
	if err := os.Remove(filepath.Join(root, "hints", "complete.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), `checkpoint "complete" hint is not a file`) {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRequiresOneSolutionMarkerPerCheckpoint(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: complete -->\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRejectsMissingSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "# Solution\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRejectsUnknownSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: unknown -->\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), "unknown checkpoint") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirValidatesBundledMarkdownResources(t *testing.T) {
	tests := []struct {
		name    string
		problem string
		want    string
	}{
		{name: "missing", problem: "[diagram](assets/missing.txt)\n", want: "does not resolve"},
		{name: "escape", problem: "[outside](../../outside.txt)\n", want: "must remain inside"},
		{name: "absolute", problem: "[host](/etc/passwd)\n", want: "must be a non-empty relative path"},
		{name: "non HTTPS", problem: "[host](http://example.test)\n", want: "HTTPS URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
			writeFile(t, filepath.Join(root, "problem.md"), test.problem)
			_, err := ValidateCandidateDir(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCandidateDir error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateCandidateDirAcceptsRelativeAndHTTPSMarkdownResources(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n[asset](assets/guide.txt)\n")
	writeFile(t, filepath.Join(root, "assets", "guide.txt"), "guide\n")
	writeFile(t, filepath.Join(root, "problem.md"), "[guide](assets/guide.txt)\n[docs](https://example.test/docs)\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "[guide](../assets/guide.txt)\n")

	if _, err := ValidateCandidateDir(root); err != nil {
		t.Fatalf("ValidateCandidateDir = %v", err)
	}
}

func TestValidatePortableDirNormalizesOperationsScenarioTags(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
	manifest := "type: operations-scenario\ntags: [K8S, linux, k8s]\n"
	data, err := os.ReadFile(filepath.Join(root, "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scenario.yaml"), append([]byte(manifest), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := ValidatePortableDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entry.Tags, ","); got != "k8s,linux" || entry.Type != ScenarioOperationsScenario {
		t.Fatalf("scenario metadata = %#v", entry)
	}
}

func TestValidatePortableDirRejectsLegacyDocumentationExampleType(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
	data, err := os.ReadFile(filepath.Join(root, "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := append([]byte("type: documentation-example\n"), data...)
	if err := os.WriteFile(filepath.Join(root, "scenario.yaml"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePortableDir(root); err == nil || !strings.Contains(err.Error(), "必须为 operations-scenario") {
		t.Fatalf("ValidatePortableDir error = %v", err)
	}
}

func TestValidateCandidateDirRejectsInvalidTags(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
	data, err := os.ReadFile(filepath.Join(root, "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := append([]byte("tags: [invalid_tag]\n"), data...)
	if err := os.WriteFile(filepath.Join(root, "scenario.yaml"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRequiresOperationsReproductionCore(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   string
	}{
		{name: "versions", remove: "versions:\n  - component: fixture\n    version: v1\n", want: "operations scenario versions are required"},
		{name: "topology", remove: "topology: One teaching host.\n", want: "operations scenario topology is required"},
		{name: "initialization", remove: "initialization: initialize.sh prepares the missing marker.\n", want: "operations scenario initialization is required"},
		{name: "objective", remove: "  objective: The fixture starts incomplete.\n", want: "operations scenario reproduction objective is required"},
		{name: "evidence", remove: "  evidence:\n    - id: fixture-incomplete\n      description: The fixture is initially incomplete.\n      node: host\n", want: "operations scenario reproduction evidence is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
			path := filepath.Join(root, "scenario.yaml")
			manifest, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			updated := strings.Replace(string(manifest), test.remove, "", 1)
			if updated == string(manifest) {
				t.Fatalf("fixture manifest did not contain %q", test.remove)
			}
			if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCandidateDir error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateCandidateDirRequiresInitialAssertionForOperationsScenario(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
	if err := os.Remove(filepath.Join(root, "nodes", "host", "assertions", "initial-fixture-incomplete.sh")); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), "missing nodes/host/assertions/initial-fixture-incomplete.sh") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRequiresK8sInitialAssertionForOperationsScenario(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "scenario.yaml"), "type: operations-scenario\ntitle: K8s fixture\nruntime: k8s\ndescription: fixture\nversions:\n  - component: kubernetes\n    version: v1.31.0\ntopology: One Kubernetes cluster.\ninitialization: initialize.sh removes the workload.\nreproduction:\n  objective: The workload is absent.\n  evidence:\n    - id: workload-absent\n      description: The workload is absent.\ncheckpoints:\n  - id: workload-ready\n    title: Workload ready\n    description: The workload is ready.\n    hint: hints/workload-ready.md\n")
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "<!-- checkpoint: workload-ready -->\n")
	writeFile(t, filepath.Join(root, "hints", "workload-ready.md"), "hint\n")
	writeFile(t, filepath.Join(root, "k8s", "initialize.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "k8s", "actions", "apply.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "k8s", "assertions", "final-workload-ready.sh"), "#!/bin/sh\n")

	if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), "missing k8s/assertions/initial-workload-absent.sh") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirAllowsOperationsScenarioWithoutLearningAids(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, string)
	}{
		{name: "node", write: writeNodeOperationsCore},
		{name: "k8s", write: writeK8sOperationsCore},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.write(t, root)
			entry, err := ValidateCandidateDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if entry.HasReferenceRepair || len(entry.Checkpoints) != 0 {
				t.Fatalf("learning aids were inferred for core-only scenario: %#v", entry)
			}
		})
	}
}

func TestValidateCandidateDirRequiresCompleteReferenceRepairSet(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   string
	}{
		{name: "solution", remove: "solution.md", want: "solution.md and reference answer scripts must be provided together"},
		{name: "apply", remove: "nodes/host/actions/apply.sh", want: "solution.md and reference answer scripts must be provided together"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTeachingScenario(t, root, "", "<!-- checkpoint: complete -->\n")
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(test.remove))); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCandidateDir error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateCandidateDirRequiresFinalAssertionForDeclaredCheckpointWithoutReferenceRepair(t *testing.T) {
	root := t.TempDir()
	writeTeachingScenario(t, root, "", "<!-- checkpoint: complete -->\n")
	if err := os.Remove(filepath.Join(root, "solution.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "nodes", "host", "actions", "apply.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "nodes", "host", "assertions", "final-complete.sh")); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateCandidateDir(root); err == nil || !strings.Contains(err.Error(), "missing nodes/host/assertions/final-complete.sh") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func writeTeachingScenario(t *testing.T, root, hint, solution string) {
	t.Helper()
	manifest := "title: Teaching fixture\nruntime: node\ndescription: fixture\nversions:\n  - component: fixture\n    version: v1\ntopology: One teaching host.\ninitialization: initialize.sh prepares the missing marker.\nreproduction:\n  objective: The fixture starts incomplete.\n  evidence:\n    - id: fixture-incomplete\n      description: The fixture is initially incomplete.\n      node: host\nnodes:\n  - name: host\n    title: Teaching host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete it\n    node: host\n"
	if hint != "" {
		manifest += "    hint: " + hint + "\n"
	}
	writeFile(t, filepath.Join(root, "scenario.yaml"), manifest)
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), solution)
	if hint != "" {
		writeFile(t, filepath.Join(root, hint), "hint\n")
	}
	writeFile(t, filepath.Join(root, "nodes", "host", "initialize.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "assertions", "initial-fixture-incomplete.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"fixture-incomplete\",\"satisfied\":true,\"summary\":\"incomplete\"}]}'\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "assertions", "final-complete.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "actions", "apply.sh"), "#!/bin/sh\n")
}

func writeNodeOperationsCore(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "scenario.yaml"), "type: operations-scenario\ntitle: Core node fixture\nruntime: node\ndescription: fixture\nversions:\n  - component: fixture\n    version: v1\ntopology: One host.\ninitialization: initialize.sh creates the target state.\nreproduction:\n  objective: The fixture starts incomplete.\n  evidence:\n    - id: fixture-incomplete\n      description: The fixture is incomplete.\n      node: host\nnodes:\n  - name: host\n    title: Host\ncheckpoints: []\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "initialize.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "assertions", "initial-fixture-incomplete.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"fixture-incomplete\",\"satisfied\":true,\"summary\":\"incomplete\"}]}'\n")
}

func writeK8sOperationsCore(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "scenario.yaml"), "type: operations-scenario\ntitle: Core Kubernetes fixture\nruntime: k8s\ndescription: fixture\nversions:\n  - component: kubernetes\n    version: v1.31.0\ntopology: One Kubernetes cluster.\ninitialization: initialize.sh creates the target state.\nreproduction:\n  objective: The workload is absent.\n  evidence:\n    - id: workload-absent\n      description: The workload is absent.\ncheckpoints: []\n")
	writeFile(t, filepath.Join(root, "k8s", "initialize.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "k8s", "assertions", "initial-workload-absent.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"workload-absent\",\"satisfied\":true,\"summary\":\"absent\"}]}'\n")
}
