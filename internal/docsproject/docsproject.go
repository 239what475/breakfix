// Package docsproject projects a rendered documentation site into a
// deterministic, offline document library.
package docsproject

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultRoot    = "docs-site/public"
	DefaultOut     = "docs-site/documents"
	DefaultWorkers = 8
)

// Config controls one offline projection run.
type Config struct {
	Root    string
	Out     string
	Workers int
	Version string
	Resume  bool
	Pages   []string
}

// DefaultConfig supplies the command's documented defaults.
func DefaultConfig() Config {
	return Config{Root: DefaultRoot, Out: DefaultOut, Workers: DefaultWorkers}
}

// InputError identifies a bad input tree or invocation. The command maps it
// to exit status 2, leaving exit status 1 for page-level extraction failures.
type InputError struct {
	Err error
}

func (e *InputError) Error() string { return e.Err.Error() }
func (e *InputError) Unwrap() error { return e.Err }

// ExitCode maps projection errors to the stable command contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var input *InputError
	if errors.As(err, &input) {
		return 2
	}
	return 1
}

// Run validates the invocation. Subsequent implementation stages extend this
// function without adding any runtime, database, or network dependencies.
func Run(config Config) error {
	if strings.TrimSpace(config.Version) == "" {
		return &InputError{Err: errors.New("generator version is required")}
	}
	if strings.TrimSpace(config.Root) == "" {
		return &InputError{Err: errors.New("rendered root is required")}
	}
	if strings.TrimSpace(config.Out) == "" {
		return &InputError{Err: errors.New("output root is required")}
	}
	if config.Workers < 1 {
		return &InputError{Err: errors.New("workers must be at least 1")}
	}
	info, err := os.Stat(filepath.Clean(config.Root))
	if err != nil {
		return &InputError{Err: fmt.Errorf("read rendered root: %w", err)}
	}
	if !info.IsDir() {
		return &InputError{Err: errors.New("rendered root is not a directory")}
	}
	state, err := loadTree(filepath.Clean(config.Root), config.Pages)
	if err != nil {
		return err
	}
	return runProjection(config, state)
}
