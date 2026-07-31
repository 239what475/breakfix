package challenge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeachingAssetFixturesValidate(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		directory string
		validate  func(string) (*Entry, error)
	}{
		{name: "published cleanup logs", directory: filepath.Join(projectRoot, "data", "challenges", "cleanup-logs"), validate: ValidateDir},
		{name: "node runtime init", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "node-runtime-init"), validate: ValidateCandidateDir},
		{name: "node checkpoint coverage", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "node-checkpoint-dependency"), validate: ValidateCandidateDir},
		{name: "node reverse proxy", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "node-reverse-proxy"), validate: ValidateCandidateDir},
		{name: "k8s web service", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "k8s-web-service"), validate: ValidateCandidateDir},
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

func TestValidateCandidateDirRequiresCheckpointHints(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "", "<!-- checkpoint: complete -->\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), `checkpoint "complete" hint is required`) {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRequiresOneSolutionMarkerPerCheckpoint(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: complete -->\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRejectsMissingSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "# Solution\n")

	_, err := ValidateCandidateDir(root)
	if err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func TestValidateCandidateDirRejectsUnknownSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: unknown -->\n")

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
			writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n")
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
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n[asset](assets/guide.txt)\n")
	writeFile(t, filepath.Join(root, "assets", "guide.txt"), "guide\n")
	writeFile(t, filepath.Join(root, "problem.md"), "[guide](assets/guide.txt)\n[docs](https://example.test/docs)\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "[guide](../assets/guide.txt)\n")

	if _, err := ValidateCandidateDir(root); err != nil {
		t.Fatalf("ValidateCandidateDir = %v", err)
	}
}

func writeTeachingChallenge(t *testing.T, root, hint, solution string) {
	t.Helper()
	manifest := "title: Teaching fixture\nruntime: node\ndifficulty: easy\ndescription: fixture\nnodes:\n  - name: host\n    title: Teaching host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete it\n    node: host\n"
	if hint != "" {
		manifest += "    hint: " + hint + "\n"
	}
	writeFile(t, filepath.Join(root, "challenge.yaml"), manifest)
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), solution)
	if hint != "" {
		writeFile(t, filepath.Join(root, hint), "hint\n")
	}
	writeFile(t, filepath.Join(root, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "answer.sh"), "#!/bin/sh\n")
}
