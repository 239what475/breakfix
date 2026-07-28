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
		{name: "container runtime init", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "container-runtime-init"), validate: ValidateSubmissionDir},
		{name: "container dependency", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "container-checkpoint-dependency"), validate: ValidateSubmissionDir},
		{name: "vcluster web service", directory: filepath.Join(projectRoot, "test", "fixtures", "challenges", "vcluster-web-service"), validate: ValidateSubmissionDir},
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

func TestValidateSubmissionDirRequiresCheckpointHints(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "", "<!-- checkpoint: complete -->\n")

	_, err := ValidateSubmissionDir(root)
	if err == nil || !strings.Contains(err.Error(), `checkpoint "complete" hint is required`) {
		t.Fatalf("ValidateSubmissionDir error = %v", err)
	}
}

func TestValidateSubmissionDirRequiresOneSolutionMarkerPerCheckpoint(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: complete -->\n")

	_, err := ValidateSubmissionDir(root)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ValidateSubmissionDir error = %v", err)
	}
}

func TestValidateSubmissionDirRejectsMissingSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "# Solution\n")

	_, err := ValidateSubmissionDir(root)
	if err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("ValidateSubmissionDir error = %v", err)
	}
}

func TestValidateSubmissionDirRejectsUnknownSolutionMarker(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n<!-- checkpoint: unknown -->\n")

	_, err := ValidateSubmissionDir(root)
	if err == nil || !strings.Contains(err.Error(), "unknown checkpoint") {
		t.Fatalf("ValidateSubmissionDir error = %v", err)
	}
}

func TestValidateSubmissionDirValidatesBundledMarkdownResources(t *testing.T) {
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
			_, err := ValidateSubmissionDir(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSubmissionDir error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSubmissionDirAcceptsRelativeAndHTTPSMarkdownResources(t *testing.T) {
	root := t.TempDir()
	writeTeachingChallenge(t, root, "hints/complete.md", "<!-- checkpoint: complete -->\n[asset](assets/guide.txt)\n")
	writeFile(t, filepath.Join(root, "assets", "guide.txt"), "guide\n")
	writeFile(t, filepath.Join(root, "problem.md"), "[guide](assets/guide.txt)\n[docs](https://example.test/docs)\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "[guide](../assets/guide.txt)\n")

	if _, err := ValidateSubmissionDir(root); err != nil {
		t.Fatalf("ValidateSubmissionDir = %v", err)
	}
}

func writeTeachingChallenge(t *testing.T, root, hint, solution string) {
	t.Helper()
	manifest := "title: Teaching fixture\ntype: script\nruntime: container\ndifficulty: easy\ndescription: fixture\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete it\n"
	if hint != "" {
		manifest += "    hint: " + hint + "\n"
	}
	writeFile(t, filepath.Join(root, "challenge.yaml"), manifest)
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM scratch\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), solution)
	if hint != "" {
		writeFile(t, filepath.Join(root, hint), "hint\n")
	}
	writeFile(t, filepath.Join(root, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\n")
}
