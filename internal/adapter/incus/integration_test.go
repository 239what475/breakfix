package incus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
)

func TestPreflightAgainstIncus(t *testing.T) {
	endpoint := os.Getenv("BREAKFIX_INCUS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREAKFIX_INCUS_TEST_ENDPOINT is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, role := range []incus.Role{
		incus.RoleController,
		incus.RoleServer,
		incus.RoleRuntime,
	} {
		t.Run(string(role), func(t *testing.T) {
			client, err := incus.Connect(ctx, integrationConfig(t, endpoint, role))
			if err != nil {
				t.Fatalf("create Incus %s client: %v", role, err)
			}
			defer client.Close()
			result, err := client.Preflight(ctx, role)
			if err != nil {
				t.Fatalf("preflight: %v", err)
			}
			if result.ServerVersion != incus.SupportedServerVersion {
				t.Fatalf("server version = %q", result.ServerVersion)
			}
		})
	}
}

func TestNodeImageAndEnvironmentAgainstIncus(t *testing.T) {
	endpoint := os.Getenv("BREAKFIX_INCUS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREAKFIX_INCUS_TEST_ENDPOINT is not set")
	}
	config := integrationConfig(t, endpoint, incus.RoleController)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	builderClient, err := incus.Connect(ctx, integrationConfig(t, endpoint, incus.RoleRuntime))
	if err != nil {
		t.Fatalf("create Incus builder client: %v", err)
	}
	defer builderClient.Close()
	publisherClient, err := incus.Connect(ctx, integrationConfig(t, endpoint, incus.RoleRuntime))
	if err != nil {
		t.Fatalf("create Incus publisher client: %v", err)
	}
	defer publisherClient.Close()
	controllerClient, err := incus.Connect(ctx, config)
	if err != nil {
		t.Fatalf("create Incus controller client: %v", err)
	}
	defer controllerClient.Close()

	runID := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	revision := "sha256:integration-" + runID
	t.Log("build stopped Node image")
	build, err := builderClient.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: runID, CandidateRevisionID: runID,
		Attempt: 1, Revision: revision,
		Files: []incus.ImageFile{
			{Path: "scenario.yaml", Content: []byte("runtime: node\n"), Mode: 0o644},
			{Path: "nodes/node/generate.sh", Content: []byte("#!/bin/bash\nset -euo pipefail\nprintf ready >/var/lib/breakfix-node-ready\n"), Mode: 0o644},
			{Path: "nodes/node/answer.sh", Content: []byte("#!/bin/sh\nset -eu\n"), Mode: 0o755},
			{Path: "nodes/node/checks.sh", Content: []byte("#!/bin/sh\nprintf '{\"results\":[]}\\n'\n"), Mode: 0o755},
		},
	})
	if err != nil {
		t.Fatalf("build node image: %v", err)
	}
	defer cleanupBuildImage(t, builderClient, build)
	t.Log("publish candidate Node image")
	published, err := publisherClient.PublishNodeImage(ctx, incus.PublishNodeImageRequest{
		CandidateRevisionID: runID,
		Revision:            revision,
		Build:               build,
	})
	if err != nil {
		t.Fatalf("publish node image: %v", err)
	}
	defer cleanupPublishedImage(t, publisherClient, runID, published)

	environments := make([]incus.ProvisionNodeEnvironmentRequest, 0, 2)
	for index := 0; index < 2; index++ {
		t.Logf("provision isolated Node environment %d", index+1)
		environmentUID := fmt.Sprintf("%s-environment-%d", runID, index)
		identity, err := incus.IdentityForNodeEnvironment(config.NamePrefix, environmentUID, []string{"node"})
		if err != nil {
			t.Fatalf("derive environment identity: %v", err)
		}
		request := incus.ProvisionNodeEnvironmentRequest{
			EnvironmentUID:        environmentUID,
			Revision:              revision,
			ImageFingerprint:      published.Fingerprint,
			ProfileRevision:       "node-profile-v1",
			NetworkPolicyRevision: "node-network-v1",
			Identity:              identity,
			Resources: incus.NodeEnvironmentResources{
				CPU: config.NodeCPU, Memory: config.NodeMemory, Processes: config.NodeProcesses, RootDisk: config.NodeRootDisk,
			},
		}
		defer cleanupEnvironment(t, controllerClient, request)
		observation, err := controllerClient.ProvisionNodeEnvironment(ctx, request)
		if err != nil {
			t.Fatalf("provision environment %d: %v", index, err)
		}
		request.Identity = observation.Identity
		environments = append(environments, request)
		waitForNodeEnvironment(t, ctx, controllerClient, request)
	}

	first := environments[0]
	second := environments[1]
	t.Log("validate initialized Node environment")
	result, err := controllerClient.ExecNode(ctx, incus.ExecNodeRequest{
		EnvironmentUID: first.EnvironmentUID,
		Revision:       first.Revision,
		Identity:       first.Identity,
		LogicalName:    "node",
		Command: []string{
			"sh", "-ec", "test \"$(cat /proc/1/comm)\" = systemd; test \"$(cat /var/lib/breakfix-node-ready)\" = ready; getent hosts node",
		},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("validate initialized node: result=%+v err=%v", result, err)
	}
	t.Log("validate cross-environment network isolation")
	result, err = controllerClient.ExecNode(ctx, incus.ExecNodeRequest{
		EnvironmentUID: first.EnvironmentUID,
		Revision:       first.Revision,
		Identity:       first.Identity,
		LogicalName:    "node",
		Command:        []string{"ping", "-c", "1", "-W", "1", second.Identity.Nodes[0].Address},
	})
	if err != nil {
		t.Fatalf("execute isolation probe: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("cross-environment address %s was reachable", second.Identity.Nodes[0].Address)
	}
}

func TestNodeBuildSlotsAreCandidateScopedAgainstIncus(t *testing.T) {
	endpoint := os.Getenv("BREAKFIX_INCUS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREAKFIX_INCUS_TEST_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := incus.Connect(ctx, integrationConfig(t, endpoint, incus.RoleRuntime))
	if err != nil {
		t.Fatalf("create Incus build client: %v", err)
	}
	defer client.Close()

	workflowID := fmt.Sprintf("build-slot-%d", time.Now().UnixNano())
	first, err := client.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: workflowID, CandidateRevisionID: workflowID + "-first",
		Attempt: 1, Revision: "sha256:first-" + workflowID,
		Files: []incus.ImageFile{
			{Path: "scenario.yaml", Content: []byte("runtime: node\n"), Mode: 0o644},
			{Path: "nodes/node/generate.sh", Content: []byte("#!/bin/sh\nprintf first >/var/lib/breakfix-build-slot\n"), Mode: 0o755},
		},
	})
	if err != nil {
		t.Fatalf("build first candidate: %v", err)
	}
	defer cleanupBuildImage(t, client, first)

	second, err := client.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: workflowID, CandidateRevisionID: workflowID + "-second",
		Attempt: 1, Revision: "sha256:second-" + workflowID,
		Files: []incus.ImageFile{
			{Path: "scenario.yaml", Content: []byte("runtime: node\n"), Mode: 0o644},
			{Path: "nodes/node/generate.sh", Content: []byte("#!/bin/sh\nprintf second >/var/lib/breakfix-build-slot\n"), Mode: 0o755},
		},
	})
	if err != nil {
		t.Fatalf("build second candidate: %v", err)
	}
	defer cleanupBuildImage(t, client, second)
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("candidate-scoped builds retained the same image fingerprint")
	}
}

func TestNodeReverseProxyAgainstIncus(t *testing.T) {
	endpoint := os.Getenv("BREAKFIX_INCUS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREAKFIX_INCUS_TEST_ENDPOINT is not set")
	}
	config := integrationConfig(t, endpoint, incus.RoleController)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	builderClient, err := incus.Connect(ctx, integrationConfig(t, endpoint, incus.RoleRuntime))
	if err != nil {
		t.Fatalf("create Incus builder client: %v", err)
	}
	defer builderClient.Close()
	publisherClient, err := incus.Connect(ctx, integrationConfig(t, endpoint, incus.RoleRuntime))
	if err != nil {
		t.Fatalf("create Incus publisher client: %v", err)
	}
	defer publisherClient.Close()
	controllerClient, err := incus.Connect(ctx, config)
	if err != nil {
		t.Fatalf("create Incus controller client: %v", err)
	}
	defer controllerClient.Close()

	files, err := incus.ImageFilesFromDirectory(nodeReverseProxyFixture(t))
	if err != nil {
		t.Fatalf("read reverse-proxy fixture: %v", err)
	}
	runID := fmt.Sprintf("reverse-proxy-%d", time.Now().UnixNano())
	revision := "sha256:" + runID
	t.Log("build stopped three-node reverse-proxy image")
	build, err := builderClient.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: runID, CandidateRevisionID: runID, Attempt: 1, Revision: revision, Files: files,
	})
	if err != nil {
		t.Fatalf("build Node image: %v", err)
	}
	defer cleanupBuildImage(t, builderClient, build)
	t.Log("publish reverse-proxy candidate Node image")
	published, err := publisherClient.PublishNodeImage(ctx, incus.PublishNodeImageRequest{
		CandidateRevisionID: runID, Revision: revision, Build: build,
	})
	if err != nil {
		t.Fatalf("publish Node image: %v", err)
	}
	defer cleanupPublishedImage(t, publisherClient, runID, published)

	identity, err := incus.IdentityForNodeEnvironment(config.NamePrefix, runID+"-environment", []string{"client", "proxy", "app"})
	if err != nil {
		t.Fatalf("derive environment identity: %v", err)
	}
	request := incus.ProvisionNodeEnvironmentRequest{
		EnvironmentUID: runID + "-environment", Revision: revision, ImageFingerprint: published.Fingerprint,
		ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "node-network-v1", Identity: identity,
		Resources: incus.NodeEnvironmentResources{
			CPU: config.NodeCPU, Memory: config.NodeMemory, Processes: config.NodeProcesses, RootDisk: config.NodeRootDisk,
		},
	}
	defer cleanupEnvironment(t, controllerClient, request)
	t.Log("provision three-node reverse-proxy environment")
	observation, err := controllerClient.ProvisionNodeEnvironment(ctx, request)
	if err != nil {
		t.Fatalf("provision reverse-proxy environment: %v", err)
	}
	request.Identity = observation.Identity
	waitForNodeEnvironment(t, ctx, controllerClient, request)

	t.Log("verify the intentional broken initial state")
	expectNodeCheck(t, ctx, controllerClient, request, "proxy", "proxy-service-ready", false)
	expectNodeCheck(t, ctx, controllerClient, request, "client", "application-reachable", false)

	t.Log("run all answer scripts concurrently as the Verifier does")
	executeNodeAnswers(t, ctx, controllerClient, request)

	t.Log("verify the repaired systemd proxy path and client reachability")
	expectNodeCheck(t, ctx, controllerClient, request, "proxy", "proxy-service-ready", true)
	expectNodeCheck(t, ctx, controllerClient, request, "client", "application-reachable", true)
	result, err := controllerClient.ExecNode(ctx, incus.ExecNodeRequest{
		EnvironmentUID: request.EnvironmentUID, Revision: request.Revision, Identity: request.Identity, LogicalName: "app",
		Command: []string{"/bin/bash", "-ec", "test \"$(cat /proc/1/comm)\" = systemd; systemctl is-active --quiet breakfix-app.service; getent hosts client proxy app"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("validate application node: result=%+v err=%v", result, err)
	}
}

func nodeReverseProxyFixture(t *testing.T) string {
	t.Helper()
	_, source, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate Incus integration test source")
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "..", "test", "fixtures", "candidates", "node-reverse-proxy")
}

type nodeCheckDocument struct {
	Checks []struct {
		ID      string `json:"id"`
		Passed  bool   `json:"passed"`
		Summary string `json:"summary"`
		Details string `json:"details"`
	} `json:"checks"`
}

func expectNodeCheck(t *testing.T, ctx context.Context, client *incus.Client, request incus.ProvisionNodeEnvironmentRequest, node, checkID string, passed bool) {
	t.Helper()
	result, err := client.ExecNode(ctx, incus.ExecNodeRequest{
		EnvironmentUID: request.EnvironmentUID, Revision: request.Revision, Identity: request.Identity, LogicalName: node,
		Command: []string{"/bin/bash", filepath.ToSlash(filepath.Join("/opt/breakfix/scenario", "nodes", node, "checks.sh"))},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execute %s checkpoint on %s: result=%+v err=%v", checkID, node, result, err)
	}
	var document nodeCheckDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &document); err != nil {
		t.Fatalf("decode %s checkpoint on %s: %v; stdout=%q", checkID, node, err, result.Stdout)
	}
	if len(document.Checks) != 1 || document.Checks[0].ID != checkID || document.Checks[0].Passed != passed {
		logNodeDiagnostics(t, ctx, client, request)
		t.Fatalf("checkpoint %s on %s = %+v, want passed=%t; stdout=%q", checkID, node, document.Checks, passed, result.Stdout)
	}
}

func logNodeDiagnostics(t *testing.T, ctx context.Context, client *incus.Client, request incus.ProvisionNodeEnvironmentRequest) {
	t.Helper()
	for _, node := range request.Identity.Nodes {
		result, err := client.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: request.EnvironmentUID, Revision: request.Revision, Identity: request.Identity, LogicalName: node.LogicalName,
			Command: []string{"/bin/bash", "-ec", "getent hosts client proxy app; curl -sv --max-time 5 http://app:8080 || true; curl -sv --max-time 5 http://proxy:8080 || true; systemctl --no-pager --full status breakfix-app.service breakfix-proxy.service || true; journalctl --no-pager --lines 80 -u breakfix-app.service -u breakfix-proxy.service || true"},
		})
		t.Logf("diagnostics on %s: exit=%d err=%v stdout=%q stderr=%q", node.LogicalName, result.ExitCode, err, result.Stdout, result.Stderr)
	}
}

func executeNodeAnswers(t *testing.T, ctx context.Context, client *incus.Client, request incus.ProvisionNodeEnvironmentRequest) {
	t.Helper()
	results := make(chan struct {
		node   string
		result incus.ExecNodeResult
		err    error
	}, len(request.Identity.Nodes))
	var group sync.WaitGroup
	for _, identity := range request.Identity.Nodes {
		node := identity.LogicalName
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := client.ExecNode(ctx, incus.ExecNodeRequest{
				EnvironmentUID: request.EnvironmentUID, Revision: request.Revision, Identity: request.Identity, LogicalName: node,
				Command: []string{"/bin/bash", filepath.ToSlash(filepath.Join("/opt/breakfix/scenario", "nodes", node, "answer.sh"))},
			})
			results <- struct {
				node   string
				result incus.ExecNodeResult
				err    error
			}{node: node, result: result, err: err}
		}()
	}
	group.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.result.ExitCode != 0 {
			t.Fatalf("execute answer on %s: result=%+v err=%v", result.node, result.result, result.err)
		}
		t.Logf("answer completed on %s: stdout=%q stderr=%q", result.node, result.result.Stdout, result.result.Stderr)
	}
}

func integrationConfig(t *testing.T, endpoint string, role incus.Role) incus.Config {
	t.Helper()
	tlsDir := requireEnv(t, "BREAKFIX_INCUS_TEST_TLS_DIR")
	roleDir := filepath.Join(tlsDir, string(role))
	return incus.Config{
		Endpoint: endpoint,
		TLS: incus.TLSConfig{
			ServerCertificateFile: filepath.Join(roleDir, "server.crt"),
			ClientCertificateFile: filepath.Join(roleDir, "client.crt"),
			ClientKeyFile:         filepath.Join(roleDir, "client.key"),
		},
		StoragePool:            "local",
		NetworkDriver:          "bridge",
		BuildProject:           "breakfix-build",
		ImageProject:           "breakfix-images",
		BaseImageAlias:         "node-systemd-base-v1",
		BaseImageFingerprint:   requireEnv(t, "BREAKFIX_INCUS_TEST_BASE_IMAGE_FINGERPRINT"),
		NamePrefix:             "bf",
		MaxNodesPerEnvironment: 4,
		NodeCPU:                "1",
		NodeMemory:             "512MiB",
		NodeProcesses:          512,
		NodeRootDisk:           "5GiB",
		NodeNetworkPool:        "10.240.0.0/16",
		NodeNetworkPrefix:      24,
		BlockedEgressCIDRs: []string{
			"10.0.0.0/8",
			"100.64.0.0/10",
			"169.254.0.0/16",
			"172.16.0.0/12",
			"192.168.0.0/16",
		},
	}
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when BREAKFIX_INCUS_TEST_ENDPOINT is set", name)
	}
	return value
}

func waitForNodeEnvironment(t *testing.T, ctx context.Context, client *incus.Client, request incus.ProvisionNodeEnvironmentRequest) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		observation, err := client.ObserveNodeEnvironment(ctx, request)
		if err != nil {
			t.Fatalf("observe node environment: %v", err)
		}
		for _, node := range observation.Nodes {
			if node.Initialization.Failed {
				t.Fatalf("node %s initialization failed: %s", node.Node.LogicalName, node.Initialization.Message)
			}
		}
		if observation.Ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func cleanupEnvironment(t *testing.T, client *incus.Client, request incus.ProvisionNodeEnvironmentRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.DeleteNodeEnvironment(ctx, request); err != nil {
		t.Errorf("clean up node environment: %v", err)
	}
}

func cleanupPublishedImage(t *testing.T, client *incus.Client, candidateID string, result incus.PublishNodeImageResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.DeleteCandidateNodeImage(ctx, candidateID, result.Fingerprint); err != nil {
		t.Errorf("clean up published node image: %v", err)
	}
}

func cleanupBuildImage(t *testing.T, client *incus.Client, result incus.BuildNodeImageResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.DeleteBuildNodeImage(ctx, result); err != nil {
		t.Errorf("clean up build node image: %v", err)
	}
}
