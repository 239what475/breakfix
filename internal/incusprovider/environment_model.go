package incusprovider

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/breakfix/breakfix/internal/terminal"
)

type NodeEnvironmentResources struct {
	CPU       string
	Memory    string
	Processes int64
	RootDisk  string
}

type NodeIdentity struct {
	LogicalName  string
	InstanceName string
	Address      string
}

type NodeEnvironmentIdentity struct {
	Project string
	Network string
	ACL     string
	Profile string
	Nodes   []NodeIdentity
}

type ProvisionNodeEnvironmentRequest struct {
	EnvironmentUID        string
	Revision              string
	ImageFingerprint      string
	ProfileRevision       string
	NetworkPolicyRevision string
	Identity              NodeEnvironmentIdentity
	Resources             NodeEnvironmentResources
}

type NodeInitialization struct {
	Complete bool
	Failed   bool
	ExitCode int
	Message  string
}

type NodeObservation struct {
	Node           NodeIdentity
	Running        bool
	Initialization NodeInitialization
}

type NodeEnvironmentObservation struct {
	Identity NodeEnvironmentIdentity
	Nodes    []NodeObservation
	Ready    bool
}

type ExecNodeRequest struct {
	EnvironmentUID string
	Revision       string
	Identity       NodeEnvironmentIdentity
	LogicalName    string
	Command        []string
	Environment    map[string]string
	WorkingDir     string
}

type ExecNodeResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type ExecNodePTYRequest struct {
	EnvironmentUID string
	Revision       string
	Identity       NodeEnvironmentIdentity
	LogicalName    string
	SessionName    string
	WindowName     string
	Stdin          io.Reader
	Stdout         io.Writer
	Resize         <-chan terminal.Size
}

type CloseNodePTYWindowRequest struct {
	EnvironmentUID string
	Revision       string
	Identity       NodeEnvironmentIdentity
	LogicalName    string
	SessionName    string
	WindowName     string
}

var (
	logicalNodeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	fullFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func IdentityForNodeEnvironment(prefix, environmentUID string, logicalNames []string) (NodeEnvironmentIdentity, error) {
	names, err := NamesForEnvironment(prefix, environmentUID)
	if err != nil {
		return NodeEnvironmentIdentity{}, err
	}
	if len(logicalNames) == 0 {
		return NodeEnvironmentIdentity{}, fmt.Errorf("%w: at least one logical node is required", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(logicalNames))
	identity := NodeEnvironmentIdentity{
		Project: names.Project,
		Network: names.Network,
		ACL:     names.ACL,
		Profile: names.Profile,
		Nodes:   make([]NodeIdentity, 0, len(logicalNames)),
	}
	for _, logicalName := range logicalNames {
		logicalName = strings.TrimSpace(logicalName)
		if !logicalNodeNamePattern.MatchString(logicalName) {
			return NodeEnvironmentIdentity{}, fmt.Errorf("%w: invalid logical node name %q", ErrInvalid, logicalName)
		}
		if _, duplicate := seen[logicalName]; duplicate {
			return NodeEnvironmentIdentity{}, fmt.Errorf("%w: duplicate logical node name %q", ErrInvalid, logicalName)
		}
		instanceName, err := NameForNode(prefix, environmentUID, logicalName)
		if err != nil {
			return NodeEnvironmentIdentity{}, err
		}
		seen[logicalName] = struct{}{}
		identity.Nodes = append(identity.Nodes, NodeIdentity{LogicalName: logicalName, InstanceName: instanceName})
	}
	return identity, nil
}

func (c *Client) NodeEnvironmentIdentity(environmentUID string, logicalNames []string) (NodeEnvironmentIdentity, error) {
	if c == nil {
		return NodeEnvironmentIdentity{}, fmt.Errorf("%w: connected Incus client is required", ErrInvalid)
	}
	return IdentityForNodeEnvironment(c.config.NamePrefix, environmentUID, logicalNames)
}

func (i NodeEnvironmentIdentity) node(logicalName string) (NodeIdentity, bool) {
	index := slices.IndexFunc(i.Nodes, func(node NodeIdentity) bool {
		return node.LogicalName == logicalName
	})
	if index < 0 {
		return NodeIdentity{}, false
	}
	return i.Nodes[index], true
}
