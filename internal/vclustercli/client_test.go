package vclustercli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateParsesVersion(t *testing.T) {
	binary := writeFakeVCluster(t, `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "vcluster version 0.35.1"
  exit 0
fi
echo "unexpected args: $@" >&2
exit 1
`)

	client := &Client{BinaryPath: binary}
	info, err := client.Validate(context.Background())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if info.Version != "0.35.1" {
		t.Fatalf("expected version 0.35.1, got %q", info.Version)
	}
	if info.BinaryPath != binary {
		t.Fatalf("expected binary path %q, got %q", binary, info.BinaryPath)
	}
}

func TestCreateBuildsExpectedArguments(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	binary := writeFakeVCluster(t, `#!/bin/sh
printf '%s\n' "$@" > "`+argsFile+`"
exit 0
`)

	client := &Client{BinaryPath: binary}
	_, err := client.Create(context.Background(), CreateOptions{
		Name:            "demo",
		Namespace:       "ns1",
		Connect:         false,
		BackgroundProxy: false,
		Upgrade:         true,
		ChartRepo:       "https://charts.loft.sh",
		ChartVersion:    "0.35.1",
		ValuesFiles:     []string{"first.yaml", "second.yaml"},
		SetValues:       []string{"sync.toHost.pods.enabled=true"},
		ExtraArgs:       []string{"--silent"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	got := strings.Fields(string(data))
	want := []string{
		"create", "demo",
		"-n", "ns1",
		"--connect=false",
		"--background-proxy=false",
		"--upgrade",
		"--chart-repo", "https://charts.loft.sh",
		"--chart-version", "0.35.1",
		"--values", "first.yaml",
		"--values", "second.yaml",
		"--set", "sync.toHost.pods.enabled=true",
		"--silent",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected args\nwant: %v\ngot:  %v", want, got)
	}
}

func TestCreateClassifiesAlreadyExists(t *testing.T) {
	binary := writeFakeVCluster(t, `#!/bin/sh
echo "Error: virtual cluster already exists" >&2
exit 1
`)

	client := &Client{BinaryPath: binary}
	_, err := client.Create(context.Background(), CreateOptions{
		Name:      "demo",
		Namespace: "ns1",
	})
	if !IsCode(err, ErrAlreadyExists) {
		t.Fatalf("expected already exists error, got %v", err)
	}
}

func TestDeleteClassifiesNotFound(t *testing.T) {
	binary := writeFakeVCluster(t, `#!/bin/sh
echo "vcluster not found" >&2
exit 1
`)

	client := &Client{BinaryPath: binary}
	_, err := client.Delete(context.Background(), DeleteOptions{
		Name:      "demo",
		Namespace: "ns1",
	})
	if !IsCode(err, ErrNotFound) {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestDeleteClassifiesCouldNotFindVClusterAsNotFound(t *testing.T) {
	binary := writeFakeVCluster(t, `#!/bin/sh
printf '\033[0;1;37m20:47:12 \033[0m\033[0;1;31mfatal \033[0mcouldn'"'"'t find vcluster demo\n' >&2
exit 1
`)

	client := &Client{BinaryPath: binary}
	_, err := client.Delete(context.Background(), DeleteOptions{
		Name:      "demo",
		Namespace: "ns1",
	})
	if !IsCode(err, ErrNotFound) {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestCreateTimesOut(t *testing.T) {
	binary := writeFakeVCluster(t, `#!/bin/sh
sleep 5
`)

	client := &Client{BinaryPath: binary}
	_, err := client.Create(context.Background(), CreateOptions{
		Name:      "demo",
		Namespace: "ns1",
		Timeout:   50 * time.Millisecond,
	})
	if !IsCode(err, ErrTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestValidateReportsMissingBinary(t *testing.T) {
	client := &Client{BinaryPath: filepath.Join(t.TempDir(), "missing-vcluster")}
	_, err := client.Validate(context.Background())
	if !IsCode(err, ErrBinaryNotFound) {
		t.Fatalf("expected binary not found, got %v", err)
	}
}

func writeFakeVCluster(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vcluster")
	//nolint:gosec // The fake CLI must be executable by the test process.
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake vcluster: %v", err)
	}
	return path
}
