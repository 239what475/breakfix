package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/incusprovider"
)

type environmentAssistantReader struct {
	k8s interface {
		CaptureTMUXPane(context.Context, string, string, string, string, int, int) ([]string, int, error)
		ListPodFiles(context.Context, string, string, string, int, int) ([]string, int, error)
		ReadPodFile(context.Context, string, string, string, int64, int) (string, int64, error)
	}
	node           NodeTerminalProvider
	getEnvironment func(context.Context, string, string) (*activeEnvironment, error)
	env            *activeEnvironment
	entry          *challenge.Entry
	content        *challenge.Content
}

func (r *environmentAssistantReader) TerminalScrollback(ctx context.Context, node, window string, offset, lines int) (assistant.Scrollback, error) {
	var values []string
	var total int
	var err error
	switch r.env.Runtime {
	case challenge.RuntimeNode:
		values, total, err = r.captureNodeTMUXPane(ctx, node, window, offset, lines)
	case challenge.RuntimeK8s:
		if node != "" {
			return assistant.Scrollback{}, errors.New("k8s environment has no logical node selector")
		}
		values, total, err = r.k8s.CaptureTMUXPane(ctx, r.env.Namespace, r.env.WorkspacePod, terminalSessionName(r.env.UID), window, offset, lines)
	default:
		err = fmt.Errorf("unsupported environment runtime %q", r.env.Runtime)
	}
	if err != nil {
		return assistant.Scrollback{}, err
	}
	return assistant.Scrollback{Node: node, Window: window, Offset: offset, Lines: values, TotalLines: total, HasMore: offset+len(values) < total}, nil
}

func (r *environmentAssistantReader) CheckpointStatus(ctx context.Context) (assistant.CheckpointSnapshot, error) {
	env, err := r.getEnvironment(ctx, r.env.Runtime, r.env.Name)
	if err != nil {
		return assistant.CheckpointSnapshot{}, err
	}
	if env.UID != r.env.UID {
		return assistant.CheckpointSnapshot{}, fmt.Errorf("assistant environment changed")
	}
	return assistantCheckpointSnapshot(r.entry, env), nil
}

func (r *environmentAssistantReader) ListEnvironmentFiles(ctx context.Context, node, path string, offset, limit int) (assistant.EnvironmentFiles, error) {
	var entries []string
	var total int
	var err error
	switch r.env.Runtime {
	case challenge.RuntimeNode:
		entries, total, err = r.listNodeFiles(ctx, node, path, offset, limit)
	case challenge.RuntimeK8s:
		if node != "" {
			return assistant.EnvironmentFiles{}, errors.New("k8s environment has no logical node selector")
		}
		entries, total, err = r.k8s.ListPodFiles(ctx, r.env.Namespace, r.env.WorkspacePod, path, offset, limit)
	default:
		err = fmt.Errorf("unsupported environment runtime %q", r.env.Runtime)
	}
	if err != nil {
		return assistant.EnvironmentFiles{}, err
	}
	return assistant.EnvironmentFiles{Node: node, Path: path, Offset: offset, Entries: entries, Total: total, HasMore: offset+len(entries) < total}, nil
}

func (r *environmentAssistantReader) ReadEnvironmentFile(ctx context.Context, node, path string, offset int64, maxBytes int) (assistant.EnvironmentFile, error) {
	var content string
	var size int64
	var err error
	switch r.env.Runtime {
	case challenge.RuntimeNode:
		content, size, err = r.readNodeFile(ctx, node, path, offset, maxBytes)
	case challenge.RuntimeK8s:
		if node != "" {
			return assistant.EnvironmentFile{}, errors.New("k8s environment has no logical node selector")
		}
		content, size, err = r.k8s.ReadPodFile(ctx, r.env.Namespace, r.env.WorkspacePod, path, offset, maxBytes)
	default:
		err = fmt.Errorf("unsupported environment runtime %q", r.env.Runtime)
	}
	if err != nil {
		return assistant.EnvironmentFile{}, err
	}
	nextOffset := offset + int64(len([]byte(content)))
	return assistant.EnvironmentFile{Node: node, Path: path, Offset: offset, Content: content, Size: size, NextOffset: nextOffset, HasMore: nextOffset < size}, nil
}

func (r *environmentAssistantReader) Solution(context.Context) (string, error) {
	return r.content.Solution, nil
}

func (r *environmentAssistantReader) captureNodeTMUXPane(ctx context.Context, node, window string, offset, lines int) ([]string, int, error) {
	if offset < 0 || lines < 1 {
		return nil, 0, errors.New("invalid scrollback range")
	}
	target := terminalSessionName(r.env.UID) + ":" + window
	command := `set -euo pipefail
target="$1"
offset="$2"
lines="$3"
content="$(tmux capture-pane -p -J -t "$target" -S - -E -)"
printf '%s\n' "$content" | wc -l
printf '%s\n' "$content" | tail -n "$((offset + lines))" | head -n "$lines"`
	result, err := r.execNode(ctx, node, command, target, strconv.Itoa(offset), strconv.Itoa(lines))
	if err != nil {
		return nil, 0, fmt.Errorf("capture node tmux scrollback: %w", err)
	}
	count, page, ok := strings.Cut(result, "\n")
	if !ok {
		return nil, 0, errors.New("capture node tmux scrollback returned no line count")
	}
	total, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return nil, 0, fmt.Errorf("parse node tmux scrollback count %q: %w", count, err)
	}
	page = strings.TrimSuffix(page, "\n")
	if page == "" {
		return []string{}, total, nil
	}
	return strings.Split(page, "\n"), total, nil
}

func (r *environmentAssistantReader) listNodeFiles(ctx context.Context, node, path string, offset, limit int) ([]string, int, error) {
	if offset < 0 || limit < 1 {
		return nil, 0, errors.New("invalid file listing range")
	}
	command := `set -euo pipefail
path="$1"
offset="$2"
limit="$3"
if [[ ! -d "$path" ]]; then
  echo "not a directory: $path" >&2
  exit 2
fi
content="$(find "$path" -mindepth 1 -maxdepth 1 -printf '%f\t%y\t%s\n' | LC_ALL=C sort)"
if [[ -z "$content" ]]; then
  printf '0\n'
  exit 0
fi
printf '%s\n' "$content" | wc -l
printf '%s\n' "$content" | tail -n "+$((offset + 1))" | head -n "$limit"`
	result, err := r.execNode(ctx, node, command, path, strconv.Itoa(offset), strconv.Itoa(limit))
	if err != nil {
		return nil, 0, fmt.Errorf("list node files: %w", err)
	}
	count, page, ok := strings.Cut(result, "\n")
	if !ok {
		return nil, 0, errors.New("list node files returned no entry count")
	}
	total, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return nil, 0, fmt.Errorf("parse node file count %q: %w", count, err)
	}
	page = strings.TrimSuffix(page, "\n")
	if page == "" {
		return []string{}, total, nil
	}
	return strings.Split(page, "\n"), total, nil
}

func (r *environmentAssistantReader) readNodeFile(ctx context.Context, node, path string, offset int64, maxBytes int) (string, int64, error) {
	if offset < 0 || maxBytes < 1 {
		return "", 0, errors.New("invalid file read range")
	}
	command := `set -euo pipefail
path="$1"
offset="$2"
max_bytes="$3"
if [[ ! -f "$path" ]]; then
  echo "not a regular file: $path" >&2
  exit 2
fi
wc -c < "$path"
dd if="$path" iflag=skip_bytes,count_bytes skip="$offset" count="$max_bytes" status=none | base64 -w 0`
	result, err := r.execNode(ctx, node, command, path, strconv.FormatInt(offset, 10), strconv.Itoa(maxBytes))
	if err != nil {
		return "", 0, fmt.Errorf("read node file: %w", err)
	}
	sizeValue, encoded, ok := strings.Cut(result, "\n")
	if !ok {
		return "", 0, errors.New("read node file returned no size")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeValue), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse node file size %q: %w", sizeValue, err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", 0, fmt.Errorf("decode node file: %w", err)
	}
	return string(data), size, nil
}

func (r *environmentAssistantReader) execNode(ctx context.Context, node, script string, arguments ...string) (string, error) {
	if r.node == nil {
		return "", errors.New("node environment reader is unavailable")
	}
	command := []string{"/bin/bash", "-lc", script, "--"}
	command = append(command, arguments...)
	result, err := r.node.ExecNode(ctx, incusprovider.ExecNodeRequest{
		EnvironmentUID: r.env.UID,
		Revision:       r.env.SourceRevision,
		Identity:       r.env.NodeIdentity,
		LogicalName:    node,
		Command:        command,
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("node command exited with %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}
