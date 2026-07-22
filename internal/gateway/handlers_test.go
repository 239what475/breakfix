package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/gin-gonic/gin"
)

func TestEnvironmentAutoDestroyAfterSubmitDefaultsTrue(t *testing.T) {
	if !environmentAutoDestroyAfterSubmit(nil) {
		t.Fatal("expected nil environment to use the default cleanup policy")
	}
	if !environmentAutoDestroyAfterSubmit(&activeEnvironment{}) {
		t.Fatal("expected unset cleanup policy to default to cleanup")
	}

	no := false
	if environmentAutoDestroyAfterSubmit(&activeEnvironment{AutoDestroyAfterSubmit: &no}) {
		t.Fatal("expected explicit autoDestroyAfterSubmit=false to retain the environment")
	}

	yes := true
	if !environmentAutoDestroyAfterSubmit(&activeEnvironment{AutoDestroyAfterSubmit: &yes}) {
		t.Fatal("expected explicit autoDestroyAfterSubmit=true to clean up")
	}

}

func TestListChallengesIncludesRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	challengesDir := filepath.Join(root, "challenges")
	challengeDir := filepath.Join(challengesDir, "demo")
	if err := os.MkdirAll(challengeDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeGatewayTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\ntitle: Demo\ntype: script\nruntime: vcluster\ndifficulty: easy\ntags:\n  - kubernetes\ndescription: demo\nimage: demo:v1\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "question.md"), "question\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "verify.sh"), "#!/bin/sh\nexit 0\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")

	cfg := config.Config{DataDir: root}
	handler := NewHandler(nil, nil, cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)

	handler.ListChallenges(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ChallengeList
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Challenges == nil || len(*resp.Challenges) != 1 {
		t.Fatalf("expected 1 challenge, got %#v", resp.Challenges)
	}
	got := (*resp.Challenges)[0]
	if got.Runtime == nil || *got.Runtime != "vcluster" {
		t.Fatalf("expected runtime vcluster, got %#v", got.Runtime)
	}
}

func writeGatewayTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
