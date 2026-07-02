package challenge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTarGz(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	writeTarFile(t, tw, "challenge.yaml", []byte("id: archive-task\ntitle: Archive\n"))
	writeTarFile(t, tw, "Dockerfile", []byte("FROM alpine:3.20\n"))
	writeTarFile(t, tw, "verify.sh", []byte("#!/bin/sh\nexit 0\n"))
	writeTarFile(t, tw, "answer.sh", []byte("#!/bin/sh\nexit 0\n"))

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := ExtractTarGz(root, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "challenge.yaml")); err != nil {
		t.Fatalf("expected challenge.yaml, got %v", err)
	}
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	writeTarFile(t, tw, "../escape.txt", []byte("nope"))

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := ExtractTarGz(t.TempDir(), bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected traversal error")
	}
}

func writeTarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}
