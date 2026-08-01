package generator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

// OpenSandboxBackend implements Eino's filesystem and streaming-shell
// contracts over the Server-owned sandbox proxy. The worker never receives a
// sandbox identifier or lifecycle credential.
type OpenSandboxBackend struct {
	claim  generation.Claim
	client RuntimeClient
}

func NewOpenSandboxBackend(claim generation.Claim, client RuntimeClient) (*OpenSandboxBackend, error) {
	if !claim.Valid() || client == nil {
		return nil, errors.New("opensandbox backend requires an active workflow claim and runtime client")
	}
	return &OpenSandboxBackend{claim: claim, client: client}, nil
}

func (b *OpenSandboxBackend) LsInfo(ctx context.Context, request *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	root, err := backendPath(request.Path, true)
	if err != nil {
		return nil, err
	}
	output, code, err := b.execute(ctx, "find "+shellQuote(root)+" -mindepth 1 -maxdepth 1 -printf '%y\\t%s\\t%T@\\t%p\\n'")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("list workspace files exited with code %d: %s", code, output)
	}
	return parseFileInfo(output), nil
}

func (b *OpenSandboxBackend) Read(ctx context.Context, request *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	if request == nil {
		return nil, errors.New("read request is required")
	}
	file, err := backendPath(request.FilePath, false)
	if err != nil {
		return nil, err
	}
	value, err := b.client.ReadFile(ctx, b.claim, file, request.Offset, request.Limit)
	if err != nil {
		return nil, err
	}
	return &filesystem.FileContent{Content: value.Content}, nil
}

func (b *OpenSandboxBackend) GrepRaw(ctx context.Context, request *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	if request == nil || strings.TrimSpace(request.Pattern) == "" {
		return nil, errors.New("grep pattern is required")
	}
	root, err := backendPath(request.Path, true)
	if err != nil {
		return nil, err
	}
	flags := "-R -n -H"
	if request.CaseInsensitive {
		flags += " -i"
	}
	output, code, err := b.execute(ctx, "grep "+flags+" -- "+shellQuote(request.Pattern)+" "+shellQuote(root))
	if err != nil {
		return nil, err
	}
	if code == 1 {
		return []filesystem.GrepMatch{}, nil
	}
	if code != 0 {
		return nil, fmt.Errorf("grep workspace files exited with code %d: %s", code, output)
	}
	return parseGrepMatches(output), nil
}

func (b *OpenSandboxBackend) GlobInfo(ctx context.Context, request *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	if request == nil || strings.TrimSpace(request.Pattern) == "" {
		return nil, errors.New("glob pattern is required")
	}
	root, err := backendPath(request.Path, true)
	if err != nil {
		return nil, err
	}
	output, code, err := b.execute(ctx, "find "+shellQuote(root)+" -mindepth 1 -printf '%y\\t%s\\t%T@\\t%p\\n'")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("glob workspace files exited with code %d: %s", code, output)
	}
	all := parseFileInfo(output)
	result := make([]filesystem.FileInfo, 0)
	for _, item := range all {
		relative := strings.TrimPrefix(item.Path, "./")
		matched, matchErr := doublestar.PathMatch(request.Pattern, relative)
		if matchErr != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", matchErr)
		}
		if matched {
			result = append(result, item)
		}
	}
	return result, nil
}

func (b *OpenSandboxBackend) Write(ctx context.Context, request *filesystem.WriteRequest) error {
	if request == nil {
		return errors.New("write request is required")
	}
	file, err := backendPath(request.FilePath, false)
	if err != nil {
		return err
	}
	return b.client.WriteFile(ctx, b.claim, file, request.Content)
}

func (b *OpenSandboxBackend) Edit(ctx context.Context, request *filesystem.EditRequest) error {
	if request == nil || request.OldString == "" {
		return errors.New("edit request and old string are required")
	}
	file, err := backendPath(request.FilePath, false)
	if err != nil {
		return err
	}
	current, err := b.client.ReadFile(ctx, b.claim, file, 0, 0)
	if err != nil {
		return err
	}
	count := strings.Count(current.Content, request.OldString)
	if count == 0 {
		return errors.New("edit old string was not found")
	}
	if !request.ReplaceAll && count != 1 {
		return fmt.Errorf("edit old string matched %d times", count)
	}
	updated := strings.Replace(current.Content, request.OldString, request.NewString, 1)
	if request.ReplaceAll {
		updated = strings.ReplaceAll(current.Content, request.OldString, request.NewString)
	}
	return b.client.WriteFile(ctx, b.claim, file, updated)
}

func (b *OpenSandboxBackend) ExecuteStreaming(ctx context.Context, request *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	if request == nil || strings.TrimSpace(request.Command) == "" {
		return nil, errors.New("execute command is required")
	}
	reader, writer := schema.Pipe[*filesystem.ExecuteResponse](8)
	go func() {
		defer writer.Close()
		var terminal bool
		err := b.client.Execute(ctx, b.claim, request.Command, func(event ExecuteEvent) error {
			switch event.Type {
			case "stdout":
				if event.Content != "" {
					writer.Send(&filesystem.ExecuteResponse{Output: event.Content}, nil)
				}
			case "result":
				terminal = true
				writer.Send(&filesystem.ExecuteResponse{Output: event.Content, ExitCode: event.ExitCode}, nil)
			case "error":
				terminal = true
				writer.Send(nil, errors.New(event.Error))
			}
			return nil
		})
		if err != nil && !terminal {
			writer.Send(nil, err)
		}
	}()
	return reader, nil
}

func (b *OpenSandboxBackend) execute(ctx context.Context, command string) (string, int, error) {
	var output strings.Builder
	var exitCode *int
	if err := b.client.Execute(ctx, b.claim, command, func(event ExecuteEvent) error {
		switch event.Type {
		case "stdout":
			output.WriteString(event.Content)
		case "result":
			output.WriteString(event.Content)
			if event.ExitCode != nil {
				exitCode = event.ExitCode
			}
		case "error":
			return errors.New(event.Error)
		}
		return nil
	}); err != nil {
		return "", 0, err
	}
	if exitCode == nil {
		return "", 0, errors.New("workspace command ended without result")
	}
	return output.String(), *exitCode, nil
}

func backendPath(value string, directory bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" && directory {
		return ".", nil
	}
	if value == "/workspace" || value == "/workspace/" {
		if directory {
			return ".", nil
		}
		return "", errors.New("workspace root is not a file")
	}
	value = strings.TrimPrefix(value, "/workspace/")
	value = strings.TrimPrefix(value, "./")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("workspace path must be relative")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("workspace path cannot escape its root")
		}
	}
	return "./" + value, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func parseFileInfo(output string) []filesystem.FileInfo {
	result := make([]filesystem.FileInfo, 0)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		result = append(result, filesystem.FileInfo{Path: fields[3], IsDir: fields[0] == "d", Size: size, ModifiedAt: fields[2]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func parseGrepMatches(output string) []filesystem.GrepMatch {
	result := make([]filesystem.GrepMatch, 0)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		lineNumber, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		result = append(result, filesystem.GrepMatch{Path: parts[0], Line: lineNumber, Content: parts[2]})
	}
	return result
}

var _ filesystem.Backend = (*OpenSandboxBackend)(nil)
var _ filesystem.StreamingShell = (*OpenSandboxBackend)(nil)
