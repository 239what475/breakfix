package opensandbox

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	sdk "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/breakfix/breakfix/internal/content/workspacearchive"
)

func TestProviderFileModeUsesOpenSandboxOctalNotation(t *testing.T) {
	for _, test := range []struct {
		mode int
		want int
	}{
		{mode: 0o600, want: 600},
		{mode: 0o644, want: 644},
		{mode: 0o755, want: 755},
		{mode: 0o4755, want: 755},
	} {
		if got := providerFileMode(test.mode); got != test.want {
			t.Errorf("providerFileMode(%#o) = %d, want %d", test.mode, got, test.want)
		}
	}
}

func TestWorkspaceModeParsesOpenSandboxOctalNotation(t *testing.T) {
	for _, test := range []struct {
		value int
		want  int
	}{
		{value: 600, want: 0o600},
		{value: 644, want: 0o644},
		{value: 755, want: 0o755},
	} {
		got, err := workspaceMode(test.value)
		if err != nil || got != test.want {
			t.Errorf("workspaceMode(%d) = %#o, %v; want %#o", test.value, got, err, test.want)
		}
	}
	if _, err := workspaceMode(888); err == nil {
		t.Fatal("workspaceMode accepted invalid mode")
	}
}

func TestArchiveAndRestoreWorkspaceUseFileAPI(t *testing.T) {
	filesystem := &testWorkspaceFilesystem{files: map[string]testWorkspaceFile{
		"/workspace/challenge.yaml":        {content: []byte("title: test\n"), mode: 644},
		"/workspace/checks/checkpoints.sh": {content: []byte("#!/bin/sh\n"), mode: 755},
	}, directories: map[string]int{"/workspace/checks": 755}}
	archive, err := archiveWorkspace(context.Background(), filesystem)
	if err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
	if filesystem.commands != 0 {
		t.Fatalf("archive unexpectedly used a shell command: %d", filesystem.commands)
	}
	entries, err := restoreEntries(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreWorkspace(context.Background(), filesystem, entries); err != nil {
		t.Fatalf("restore workspace: %v", err)
	}
	if got := string(filesystem.files["/workspace/checks/checkpoints.sh"].content); got != "#!/bin/sh\n" {
		t.Fatalf("restored script = %q", got)
	}
	if got := filesystem.files["/workspace/checks/checkpoints.sh"].mode; got != 755 {
		t.Fatalf("restored script mode = %d", got)
	}
}

func restoreEntries(archive []byte) ([]workspacearchive.Entry, error) {
	return workspacearchive.Decode(archive)
}

type testWorkspaceFilesystem struct {
	files       map[string]testWorkspaceFile
	directories map[string]int
	commands    int
}

type testWorkspaceFile struct {
	content []byte
	mode    int
}

func (f *testWorkspaceFilesystem) ListDirectory(_ context.Context, directory string) ([]sdk.FileInfo, error) {
	entries := make([]sdk.FileInfo, 0)
	directory = strings.TrimPrefix(directory, workspaceMountPath)
	directory = strings.TrimPrefix(directory, "/")
	for path, mode := range f.directories {
		if pathDirectory(strings.TrimPrefix(path, "/workspace/")) == directory {
			entries = append(entries, sdk.FileInfo{Path: path, Type: "directory", Mode: mode})
		}
	}
	for path, file := range f.files {
		if pathDirectory(strings.TrimPrefix(path, "/workspace/")) == directory {
			entries = append(entries, sdk.FileInfo{Path: path, Type: "file", Mode: file.mode, Size: int64(len(file.content))})
		}
	}
	return entries, nil
}

func (f *testWorkspaceFilesystem) DownloadFile(_ context.Context, path, _ string, _ ...sdk.DownloadFileOptions) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.files[path].content)), nil
}

func (f *testWorkspaceFilesystem) UploadFile(_ context.Context, content io.Reader, options sdk.UploadFileOptions) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	if f.files == nil {
		f.files = make(map[string]testWorkspaceFile)
	}
	f.files[options.Metadata.Path] = testWorkspaceFile{content: data, mode: options.Metadata.Mode}
	return nil
}

func (f *testWorkspaceFilesystem) CreateDirectory(_ context.Context, path string, mode int) error {
	if f.directories == nil {
		f.directories = make(map[string]int)
	}
	f.directories[path] = mode
	return nil
}

func (f *testWorkspaceFilesystem) DeleteFiles(_ context.Context, paths []string) error {
	for _, path := range paths {
		delete(f.files, path)
	}
	return nil
}

func (f *testWorkspaceFilesystem) DeleteDirectory(_ context.Context, path string) error {
	delete(f.directories, path)
	for filePath := range f.files {
		if strings.HasPrefix(filePath, path+"/") {
			delete(f.files, filePath)
		}
	}
	return nil
}

func TestWorkspacePathAcceptsEinoRelativePrefix(t *testing.T) {
	for _, value := range []string{"challenge.yaml", "./challenge.yaml", "nodes/host/checks.sh", "./nodes/host/checks.sh"} {
		if err := ValidateWorkspacePath(value); err != nil {
			t.Fatalf("ValidateWorkspacePath(%q): %v", value, err)
		}
	}
	if got, want := WorkspacePath("./challenge.yaml"), "/workspace/challenge.yaml"; got != want {
		t.Fatalf("WorkspacePath(./challenge.yaml) = %q, want %q", got, want)
	}
}

func TestWorkspacePathRejectsEscapesAndAmbiguousRelativePaths(t *testing.T) {
	for _, value := range []string{"", ".", "./", "././challenge.yaml", "../challenge.yaml", "checks/../challenge.yaml", "/etc/passwd", `checks\\bad`} {
		if err := ValidateWorkspacePath(value); err == nil {
			t.Fatalf("ValidateWorkspacePath(%q) unexpectedly succeeded", value)
		}
	}
}
