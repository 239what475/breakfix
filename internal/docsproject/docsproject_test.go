package docsproject

import (
	"errors"
	"testing"
)

func TestRunRejectsInvalidInvocationWithInputExitCode(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		config Config
	}{
		{name: "version", config: Config{Root: root, Out: "out", Workers: 1}},
		{name: "root", config: Config{Out: "out", Version: "v1", Workers: 1}},
		{name: "output", config: Config{Root: root, Version: "v1", Workers: 1}},
		{name: "workers", config: Config{Root: root, Out: "out", Version: "v1"}},
		{name: "missing root", config: Config{Root: root + "/missing", Out: "out", Version: "v1", Workers: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Run(test.config)
			var input *InputError
			if !errors.As(err, &input) || ExitCode(err) != 2 {
				t.Fatalf("Run() error = %v, exit code = %d; want input error and 2", err, ExitCode(err))
			}
		})
	}
}

func TestRunAcceptsDocumentedMinimumConfig(t *testing.T) {
	config := DefaultConfig()
	config.Root = t.TempDir()
	config.Out = t.TempDir()
	config.Version = "docs-project-v1"
	if err := Run(config); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestExitCodeUsesOneForPageFailures(t *testing.T) {
	if got := ExitCode(errors.New("page failure")); got != 1 {
		t.Fatalf("ExitCode() = %d, want 1", got)
	}
}
