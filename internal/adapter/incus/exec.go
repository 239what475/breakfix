package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/gorilla/websocket"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

const nodeTMUXHistoryLimit = 10000

func (c *Client) ExecNode(ctx context.Context, request ExecNodeRequest) (ExecNodeResult, error) {
	if strings.TrimSpace(request.EnvironmentUID) == "" || strings.TrimSpace(request.Revision) == "" {
		return ExecNodeResult{}, fmt.Errorf("%w: environment UID and revision are required", ErrInvalid)
	}
	if err := c.validateNodeEnvironmentIdentity(request.EnvironmentUID, request.Identity); err != nil {
		return ExecNodeResult{}, err
	}
	if len(request.Command) == 0 || strings.TrimSpace(request.Command[0]) == "" {
		return ExecNodeResult{}, fmt.Errorf("%w: exec command is required", ErrInvalid)
	}
	node, ok := request.Identity.node(request.LogicalName)
	if !ok {
		return ExecNodeResult{}, fmt.Errorf("%w: logical node %q does not exist", ErrInvalid, request.LogicalName)
	}
	server, err := c.scoped(ctx, request.Identity.Project)
	if err != nil {
		return ExecNodeResult{}, err
	}
	instance, _, err := server.GetInstance(node.InstanceName)
	if err != nil {
		return ExecNodeResult{}, classify("get environment node for exec", request.Identity.Project+"/"+node.InstanceName, err)
	}
	if err := validateOwner(instance.Config, request.EnvironmentUID, request.Revision, "node", node.InstanceName); err != nil {
		return ExecNodeResult{}, err
	}
	if instance.Config[logicalNodeKey] != node.LogicalName {
		return ExecNodeResult{}, fmt.Errorf("%w: environment node %q has mismatched logical name", ErrInvariant, node.InstanceName)
	}
	return c.execNode(ctx, server, node.InstanceName, request.Command, request.Environment, request.WorkingDir)
}

// ExecNodePTY attaches a browser stream to one persistent tmux window in an
// Incus system container. Closing the attach leaves tmux running; deleting the
// Environment remains the only operation that destroys the node itself.
func (c *Client) ExecNodePTY(ctx context.Context, request ExecNodePTYRequest) error {
	if request.Stdin == nil || request.Stdout == nil {
		return fmt.Errorf("%w: PTY stdin and stdout are required", ErrInvalid)
	}
	if !logicalNodeNamePattern.MatchString(request.SessionName) || !logicalNodeNamePattern.MatchString(request.WindowName) {
		return fmt.Errorf("%w: invalid tmux session or window name", ErrInvalid)
	}
	server, node, err := c.nodeForExec(ctx, request.EnvironmentUID, request.Revision, request.Identity, request.LogicalName)
	if err != nil {
		return err
	}
	command := fmt.Sprintf(`export TERM=xterm-256color
if ! tmux has-session -t %[1]s 2>/dev/null; then
  tmux new-session -d -s %[1]s -n %[2]s
elif ! tmux list-windows -t %[1]s -F '#W' | grep -Fx -- %[2]s >/dev/null; then
  tmux new-window -d -t %[1]s -n %[2]s
fi
tmux set-option -q -t %[1]s history-limit %[4]d
exec tmux attach-session -t %[3]s`, shellQuote(request.SessionName), shellQuote(request.WindowName), shellQuote(request.SessionName+":"+request.WindowName), nodeTMUXHistoryLimit)

	dataDone := make(chan bool)
	op, err := server.ExecInstance(node.InstanceName, api.InstanceExecPost{
		Command: []string{"/bin/bash", "-lc", command}, WaitForWS: true, Interactive: true,
		Environment: map[string]string{"TERM": "xterm-256color"}, Width: 80, Height: 24,
	}, &incus.InstanceExecArgs{
		Stdin: request.Stdin, Stdout: request.Stdout, Stderr: io.Discard, DataDone: dataDone,
		Control: func(connection *websocket.Conn) {
			forwardTerminalResize(ctx, connection, request.Resize)
		},
	})
	if err != nil {
		return classify("attach environment node PTY", request.Identity.Project+"/"+node.InstanceName, err)
	}
	if err := waitOperation(ctx, "wait for environment node PTY", request.Identity.Project+"/"+node.InstanceName, op); err != nil {
		return err
	}
	select {
	case <-dataDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) CloseNodePTYWindow(ctx context.Context, request CloseNodePTYWindowRequest) error {
	if !logicalNodeNamePattern.MatchString(request.SessionName) || !logicalNodeNamePattern.MatchString(request.WindowName) {
		return fmt.Errorf("%w: invalid tmux session or window name", ErrInvalid)
	}
	result, err := c.ExecNode(ctx, ExecNodeRequest{
		EnvironmentUID: request.EnvironmentUID, Revision: request.Revision, Identity: request.Identity,
		LogicalName: request.LogicalName,
		Command:     []string{"/bin/bash", "-lc", "tmux kill-window -t " + shellQuote(request.SessionName+":"+request.WindowName) + " 2>/dev/null || true"},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%w: close node tmux window exited with %d: %s", ErrInvariant, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (c *Client) nodeForExec(ctx context.Context, environmentUID, revision string, identity NodeEnvironmentIdentity, logicalName string) (incus.InstanceServer, NodeIdentity, error) {
	if strings.TrimSpace(environmentUID) == "" || strings.TrimSpace(revision) == "" {
		return nil, NodeIdentity{}, fmt.Errorf("%w: environment UID and revision are required", ErrInvalid)
	}
	if err := c.validateNodeEnvironmentIdentity(environmentUID, identity); err != nil {
		return nil, NodeIdentity{}, err
	}
	node, ok := identity.node(logicalName)
	if !ok {
		return nil, NodeIdentity{}, fmt.Errorf("%w: logical node %q does not exist", ErrInvalid, logicalName)
	}
	server, err := c.scoped(ctx, identity.Project)
	if err != nil {
		return nil, NodeIdentity{}, err
	}
	instance, _, err := server.GetInstance(node.InstanceName)
	if err != nil {
		return nil, NodeIdentity{}, classify("get environment node for exec", identity.Project+"/"+node.InstanceName, err)
	}
	if err := validateOwner(instance.Config, environmentUID, revision, "node", node.InstanceName); err != nil {
		return nil, NodeIdentity{}, err
	}
	if instance.Config[logicalNodeKey] != node.LogicalName {
		return nil, NodeIdentity{}, fmt.Errorf("%w: environment node %q has mismatched logical name", ErrInvariant, node.InstanceName)
	}
	return server, node, nil
}

func forwardTerminalResize(ctx context.Context, connection *websocket.Conn, resize <-chan environment.Size) {
	defer func() { _ = connection.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case size, ok := <-resize:
			if !ok {
				return
			}
			if size.Width == 0 || size.Height == 0 {
				continue
			}
			if err := connection.WriteJSON(api.InstanceExecControl{
				Command: "window-resize",
				Args:    map[string]string{"width": strconv.Itoa(int(size.Width)), "height": strconv.Itoa(int(size.Height))},
			}); err != nil {
				return
			}
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func (c *Client) execNode(ctx context.Context, server incus.InstanceServer, instanceName string, command []string, environment map[string]string, workingDir string) (ExecNodeResult, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dataDone := make(chan bool)
	op, err := server.ExecInstance(instanceName, api.InstanceExecPost{
		Command:     append([]string(nil), command...),
		WaitForWS:   true,
		Interactive: false,
		Environment: environment,
		Cwd:         workingDir,
	}, &incus.InstanceExecArgs{
		Stdout:   &stdout,
		Stderr:   &stderr,
		DataDone: dataDone,
	})
	if err != nil {
		return ExecNodeResult{}, classify("exec environment node", instanceName, err)
	}
	if err := waitOperation(ctx, "wait for environment node exec", instanceName, op); err != nil {
		return ExecNodeResult{}, err
	}
	select {
	case <-dataDone:
	case <-ctx.Done():
		return ExecNodeResult{}, ctx.Err()
	}
	exitCode, err := operationExitCode(op.Get().Metadata)
	if err != nil {
		return ExecNodeResult{}, err
	}
	return ExecNodeResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}

func operationExitCode(metadata map[string]any) (int, error) {
	value, ok := metadata["return"]
	if !ok {
		return 0, fmt.Errorf("%w: exec operation has no return code", ErrInvariant)
	}
	var exitCode int64
	var err error
	switch value := value.(type) {
	case int:
		exitCode = int64(value)
	case int64:
		exitCode = value
	case float64:
		exitCode = int64(value)
		if float64(exitCode) != value {
			err = fmt.Errorf("non-integer return code")
		}
	case json.Number:
		exitCode, err = value.Int64()
	case string:
		exitCode, err = strconv.ParseInt(value, 10, 32)
	default:
		err = fmt.Errorf("unsupported return code type %T", value)
	}
	if err != nil || exitCode < 0 || exitCode > 255 {
		return 0, fmt.Errorf("%w: invalid exec return code: %v", ErrInvariant, err)
	}
	return int(exitCode), nil
}
