package vcluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBinaryPath     = "vcluster"
	DefaultCreateTimeout  = 2 * time.Minute
	DefaultDeleteTimeout  = 2 * time.Minute
	DefaultVersionTimeout = 10 * time.Second
)

type ErrorCode string

const (
	ErrBinaryNotFound ErrorCode = "binary_not_found"
	ErrTimeout        ErrorCode = "timeout"
	ErrAlreadyExists  ErrorCode = "already_exists"
	ErrNotFound       ErrorCode = "not_found"
	ErrUnauthorized   ErrorCode = "unauthorized"
	ErrInvalidConfig  ErrorCode = "invalid_config"
	ErrCommandFailed  ErrorCode = "command_failed"
)

type Error struct {
	Op       string
	Code     ErrorCode
	Args     []string
	Output   string
	ExitCode int
	Err      error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if strings.TrimSpace(e.Output) != "" {
		return fmt.Sprintf("vcluster %s failed (%s): %s", e.Op, e.Code, strings.TrimSpace(e.Output))
	}
	if e.Err != nil {
		return fmt.Sprintf("vcluster %s failed (%s): %v", e.Op, e.Code, e.Err)
	}
	return fmt.Sprintf("vcluster %s failed (%s)", e.Op, e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsCode(err error, code ErrorCode) bool {
	var cliErr *Error
	return errors.As(err, &cliErr) && cliErr.Code == code
}

type VersionInfo struct {
	BinaryPath string
	Version    string
	RawOutput  string
}

type Result struct {
	Command  string
	Args     []string
	Output   string
	ExitCode int
}

type Client struct {
	BinaryPath string
	Env        []string
}

type CreateOptions struct {
	Name            string
	Namespace       string
	Connect         bool
	BackgroundProxy bool
	Upgrade         bool
	ChartName       string
	ChartRepo       string
	ChartVersion    string
	ValuesFiles     []string
	SetValues       []string
	ExtraArgs       []string
	Timeout         time.Duration
}

type DeleteOptions struct {
	Name      string
	Namespace string
	ExtraArgs []string
	Timeout   time.Duration
}

func (c *Client) Validate(ctx context.Context) (*VersionInfo, error) {
	result, err := c.run(ctx, "version", []string{"version"}, DefaultVersionTimeout)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(result.Output)
	if raw == "" {
		return nil, &Error{
			Op:     "validate",
			Code:   ErrCommandFailed,
			Args:   []string{"version"},
			Output: raw,
		}
	}
	return &VersionInfo{
		BinaryPath: result.Command,
		Version:    parseVersion(raw),
		RawOutput:  raw,
	}, nil
}

func (c *Client) Create(ctx context.Context, opts CreateOptions) (*Result, error) {
	if strings.TrimSpace(opts.Name) == "" || strings.TrimSpace(opts.Namespace) == "" {
		return nil, &Error{Op: "create", Code: ErrInvalidConfig, Err: fmt.Errorf("name and namespace are required")}
	}

	args := []string{
		"create", opts.Name,
		"-n", opts.Namespace,
		fmt.Sprintf("--connect=%t", opts.Connect),
		fmt.Sprintf("--background-proxy=%t", opts.BackgroundProxy),
	}
	if opts.Upgrade {
		args = append(args, "--upgrade")
	}
	if v := strings.TrimSpace(opts.ChartName); v != "" {
		args = append(args, "--chart-name", v)
	}
	if v := strings.TrimSpace(opts.ChartRepo); v != "" {
		args = append(args, "--chart-repo", v)
	}
	if v := strings.TrimSpace(opts.ChartVersion); v != "" {
		args = append(args, "--chart-version", v)
	}
	for _, file := range opts.ValuesFiles {
		if v := strings.TrimSpace(file); v != "" {
			args = append(args, "--values", v)
		}
	}
	for _, setValue := range opts.SetValues {
		if v := strings.TrimSpace(setValue); v != "" {
			args = append(args, "--set", v)
		}
	}
	args = append(args, opts.ExtraArgs...)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCreateTimeout
	}
	result, err := c.run(ctx, "create", args, timeout)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) Delete(ctx context.Context, opts DeleteOptions) (*Result, error) {
	if strings.TrimSpace(opts.Name) == "" || strings.TrimSpace(opts.Namespace) == "" {
		return nil, &Error{Op: "delete", Code: ErrInvalidConfig, Err: fmt.Errorf("name and namespace are required")}
	}

	args := []string{"delete", opts.Name, "-n", opts.Namespace}
	args = append(args, opts.ExtraArgs...)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultDeleteTimeout
	}
	result, err := c.run(ctx, "delete", args, timeout)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) run(ctx context.Context, op string, args []string, timeout time.Duration) (*Result, error) {
	binaryPath, err := c.resolveBinary()
	if err != nil {
		return nil, &Error{Op: op, Code: ErrBinaryNotFound, Args: append([]string{}, args...), Err: err}
	}

	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, binaryPath, args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	output := strings.TrimSpace(joinOutput(stdout.String(), stderr.String()))
	result := &Result{
		Command: binaryPath,
		Args:    append([]string{}, args...),
		Output:  output,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr == nil {
		return result, nil
	}

	code := classifyError(op, output, runErr, runCtx)
	return result, &Error{
		Op:       op,
		Code:     code,
		Args:     append([]string{}, args...),
		Output:   output,
		ExitCode: result.ExitCode,
		Err:      runErr,
	}
}

func (c *Client) resolveBinary() (string, error) {
	path := c.BinaryPath
	if strings.TrimSpace(path) == "" {
		path = DefaultBinaryPath
	}
	return exec.LookPath(path)
}

func classifyError(op, output string, runErr error, runCtx context.Context) ErrorCode {
	if runCtx.Err() == context.DeadlineExceeded || errors.Is(runErr, context.DeadlineExceeded) {
		return ErrTimeout
	}
	lower := strings.ToLower(stripANSI(output))
	switch {
	case op == "create" && strings.Contains(lower, "already exists"):
		return ErrAlreadyExists
	case op == "delete" && (strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "couldn't find vcluster") || strings.Contains(lower, "could not find vcluster")):
		return ErrNotFound
	case strings.Contains(lower, "forbidden"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "permission denied"):
		return ErrUnauthorized
	default:
		return ErrCommandFailed
	}
}

func joinOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

var versionPattern = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.\-]+)?)\b`)
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func parseVersion(raw string) string {
	match := versionPattern.FindStringSubmatch(raw)
	if len(match) > 1 {
		return match[1]
	}
	return strings.TrimSpace(raw)
}

func stripANSI(raw string) string {
	return ansiPattern.ReplaceAllString(raw, "")
}
