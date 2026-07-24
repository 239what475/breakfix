package controller

import (
	"os"
	"path/filepath"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
)

func TestValidateVerifyTaskSpec(t *testing.T) {
	valid := func() *breakfixv1.VerifyTask {
		return &breakfixv1.VerifyTask{Spec: breakfixv1.VerifyTaskSpec{
			Source:      breakfixv1.VerifyTaskSource{Kind: "agent", Ref: "gen-abc123"},
			ChallengeID: "chal-abc123def456",
			Submission:  breakfixv1.VerifyTaskSubmission{ID: "sub-abc123"},
		}}
	}

	cases := []struct {
		name    string
		mutate  func(*breakfixv1.VerifyTask)
		wantErr bool
	}{
		{name: "agent artifact", mutate: func(*breakfixv1.VerifyTask) {}},
		{name: "missing challenge id", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.ChallengeID = "" }, wantErr: true},
		{name: "missing submission id", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Submission.ID = "" }, wantErr: true},
		{name: "unknown source", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Source.Kind = "legacy" }, wantErr: true},
		{name: "user source is no longer supported", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Source.Kind = "user" }, wantErr: true},
		{name: "agent without generation", mutate: func(task *breakfixv1.VerifyTask) { task.Spec.Source.Ref = "" }, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := valid()
			tc.mutate(task)
			err := validateVerifyTaskSpec(task)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVerifyTaskSpec() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestCopyDirForPublishPreservesNestedAssets(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for name, content := range map[string]string{
		"challenge.yaml":          "title: demo\n",
		"checks/checkpoints.sh":   "#!/bin/sh\n",
		"hints/checkpoint-one.md": "hint\n",
	} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := challenge.CopyRegularFiles(src, dst); err != nil {
		t.Fatalf("CopyRegularFiles: %v", err)
	}
	for _, name := range []string{"challenge.yaml", "checks/checkpoints.sh", "hints/checkpoint-one.md"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Fatalf("published copy missing %s: %v", name, err)
		}
	}
}
