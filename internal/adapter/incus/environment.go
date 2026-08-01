package incus

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/units"
)

const (
	environmentUIDKey        = "user.breakfix.environment_uid"
	revisionKey              = "user.breakfix.revision"
	resourceKindKey          = "user.breakfix.resource_kind"
	logicalNodeKey           = "user.breakfix.logical_node"
	profileRevisionKey       = "user.breakfix.profile_revision"
	networkPolicyRevisionKey = "user.breakfix.network_policy_revision"
	runtimeInitResultPath    = "/var/lib/breakfix/runtime-init/result"
)

func (c *Client) ProvisionNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) (NodeEnvironmentObservation, error) {
	if err := c.validateNodeEnvironmentRequest(request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	defaultServer, err := c.scoped(ctx, api.ProjectDefaultName)
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	if err := c.ensureEnvironmentACL(defaultServer, request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	network, err := c.ensureEnvironmentNetwork(defaultServer, request)
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	identity, err := identityWithNetworkAddresses(request.Identity, network.Config["ipv4.address"])
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	request.Identity = identity
	if err := c.ensureEnvironmentProject(defaultServer, request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	projectServer, err := c.scoped(ctx, identity.Project)
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	if err := c.ensureEnvironmentImage(ctx, projectServer, request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	if err := c.ensureEnvironmentProfile(projectServer, request); err != nil {
		return NodeEnvironmentObservation{}, err
	}

	topology := topologyCredential(identity.Nodes)
	type pendingStart struct {
		name string
		op   incus.Operation
	}
	starts := make([]pendingStart, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		instance, err := c.ensureEnvironmentInstance(ctx, projectServer, request, node, topology)
		if err != nil {
			return NodeEnvironmentObservation{}, err
		}
		switch instance.Status {
		case "Running":
			continue
		case "Stopped":
			op, err := projectServer.UpdateInstanceState(node.InstanceName, api.InstanceStatePut{Action: "start", Timeout: 60}, "")
			if err != nil {
				return NodeEnvironmentObservation{}, classify("start environment node", identity.Project+"/"+node.InstanceName, err)
			}
			starts = append(starts, pendingStart{name: node.InstanceName, op: op})
		default:
			return NodeEnvironmentObservation{}, fmt.Errorf("%w: environment node %q is in unexpected state %q", ErrInvariant, node.InstanceName, instance.Status)
		}
	}
	for _, start := range starts {
		if err := waitOperation(ctx, "start environment node", identity.Project+"/"+start.name, start.op); err != nil {
			return NodeEnvironmentObservation{}, err
		}
	}
	return c.observeNodeEnvironment(ctx, request)
}

func (c *Client) ObserveNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) (NodeEnvironmentObservation, error) {
	if err := c.validateNodeEnvironmentRequest(request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	defaultServer, err := c.scoped(ctx, api.ProjectDefaultName)
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	network, _, err := defaultServer.GetNetwork(request.Identity.Network)
	if err != nil {
		return NodeEnvironmentObservation{}, classify("get environment network", request.Identity.Network, err)
	}
	if err := c.validateEnvironmentNetwork(network, request); err != nil {
		return NodeEnvironmentObservation{}, err
	}
	identity, err := identityWithNetworkAddresses(request.Identity, network.Config["ipv4.address"])
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	request.Identity = identity
	return c.observeNodeEnvironment(ctx, request)
}

func (c *Client) observeNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) (NodeEnvironmentObservation, error) {
	projectServer, err := c.scoped(ctx, request.Identity.Project)
	if err != nil {
		return NodeEnvironmentObservation{}, err
	}
	observation := NodeEnvironmentObservation{
		Identity: request.Identity,
		Nodes:    make([]NodeObservation, 0, len(request.Identity.Nodes)),
		Ready:    true,
	}
	for _, node := range request.Identity.Nodes {
		instance, _, err := projectServer.GetInstance(node.InstanceName)
		if err != nil {
			return NodeEnvironmentObservation{}, classify("get environment node", request.Identity.Project+"/"+node.InstanceName, err)
		}
		if err := validateEnvironmentInstance(instance, request, node, topologyCredential(request.Identity.Nodes)); err != nil {
			return NodeEnvironmentObservation{}, err
		}
		nodeObservation := NodeObservation{Node: node, Running: instance.Status == "Running"}
		if nodeObservation.Running {
			nodeObservation.Initialization, err = c.readNodeInitialization(ctx, projectServer, request, node)
			if err != nil {
				return NodeEnvironmentObservation{}, err
			}
		}
		if !nodeObservation.Running || !nodeObservation.Initialization.Complete || nodeObservation.Initialization.Failed {
			observation.Ready = false
		}
		observation.Nodes = append(observation.Nodes, nodeObservation)
	}
	return observation, nil
}

func (c *Client) DeleteNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) error {
	if err := c.validateNodeEnvironmentRequest(request); err != nil {
		return err
	}
	identity := request.Identity
	projectServer, err := c.scoped(ctx, identity.Project)
	if err != nil {
		return err
	}
	projectExists := true
	project, _, err := projectServer.GetProject(identity.Project)
	if err != nil {
		classified := classify("get environment project", identity.Project, err)
		if !errors.Is(classified, ErrNotFound) {
			return classified
		}
		projectExists = false
	} else if err := validateOwner(project.Config, request.EnvironmentUID, request.Revision, "project", identity.Project); err != nil {
		return err
	}

	if projectExists {
		if err := c.deleteEnvironmentInstances(ctx, projectServer, request.EnvironmentUID, request.Revision, identity); err != nil {
			return err
		}
		if err := deleteImageIfPresent(ctx, projectServer, identity.Project, request.ImageFingerprint); err != nil {
			return err
		}
		if err := deleteProfileIfPresent(projectServer, request.EnvironmentUID, request.Revision, identity.Profile); err != nil {
			return err
		}
		if err := projectServer.DeleteProject(identity.Project); err != nil {
			classified := classify("delete environment project", identity.Project, err)
			if !errors.Is(classified, ErrNotFound) {
				return classified
			}
		}
	}

	defaultServer, err := c.scoped(ctx, api.ProjectDefaultName)
	if err != nil {
		return err
	}
	if err := deleteNetworkIfPresent(defaultServer, request.EnvironmentUID, request.Revision, identity.Network); err != nil {
		return err
	}
	if err := deleteACLIfPresent(defaultServer, request.EnvironmentUID, request.Revision, identity.ACL); err != nil {
		return err
	}
	return nil
}

func (c *Client) validateNodeEnvironmentRequest(request ProvisionNodeEnvironmentRequest) error {
	if strings.TrimSpace(request.EnvironmentUID) == "" || strings.TrimSpace(request.Revision) == "" {
		return fmt.Errorf("%w: environment UID and revision are required", ErrInvalid)
	}
	if !fullFingerprintPattern.MatchString(request.ImageFingerprint) {
		return fmt.Errorf("%w: a full image fingerprint is required", ErrInvalid)
	}
	if strings.TrimSpace(request.ProfileRevision) == "" || strings.TrimSpace(request.NetworkPolicyRevision) == "" {
		return fmt.Errorf("%w: profile and network policy revisions are required", ErrInvalid)
	}
	if len(request.Identity.Nodes) > c.config.MaxNodesPerEnvironment {
		return fmt.Errorf("%w: node count exceeds provider limit", ErrInvalid)
	}
	if request.Resources != (NodeEnvironmentResources{
		CPU: c.config.NodeCPU, Memory: c.config.NodeMemory, Processes: c.config.NodeProcesses, RootDisk: c.config.NodeRootDisk,
	}) {
		return fmt.Errorf("%w: node resources do not match the configured profile", ErrInvalid)
	}
	return c.validateNodeEnvironmentIdentity(request.EnvironmentUID, request.Identity)
}

func (c *Client) validateNodeEnvironmentIdentity(environmentUID string, identity NodeEnvironmentIdentity) error {
	logicalNames := make([]string, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		logicalNames = append(logicalNames, node.LogicalName)
	}
	expected, err := IdentityForNodeEnvironment(c.config.NamePrefix, environmentUID, logicalNames)
	if err != nil {
		return err
	}
	if identity.Project != expected.Project || identity.Network != expected.Network || identity.ACL != expected.ACL || identity.Profile != expected.Profile {
		return fmt.Errorf("%w: environment resource identity does not match its UID", ErrInvalid)
	}
	for index := range expected.Nodes {
		if identity.Nodes[index].LogicalName != expected.Nodes[index].LogicalName || identity.Nodes[index].InstanceName != expected.Nodes[index].InstanceName {
			return fmt.Errorf("%w: environment node identity does not match its UID", ErrInvalid)
		}
	}
	return nil
}

func (c *Client) ensureEnvironmentACL(server incus.InstanceServer, request ProvisionNodeEnvironmentRequest) error {
	expected := environmentACL(request, c.config.BlockedEgressCIDRs)
	current, _, err := server.GetNetworkACL(request.Identity.ACL)
	if err == nil {
		return validateEnvironmentACL(current, expected, request)
	}
	classified := classify("get environment ACL", request.Identity.ACL, err)
	if !errors.Is(classified, ErrNotFound) {
		return classified
	}
	if err := server.CreateNetworkACL(expected); err != nil {
		classified = classify("create environment ACL", request.Identity.ACL, err)
		if !errors.Is(classified, ErrConflict) {
			return classified
		}
	}
	current, _, err = server.GetNetworkACL(request.Identity.ACL)
	if err != nil {
		return classify("get created environment ACL", request.Identity.ACL, err)
	}
	return validateEnvironmentACL(current, expected, request)
}

func (c *Client) ensureEnvironmentNetwork(server incus.InstanceServer, request ProvisionNodeEnvironmentRequest) (*api.Network, error) {
	current, _, err := server.GetNetwork(request.Identity.Network)
	if err == nil {
		return current, c.validateEnvironmentNetwork(current, request)
	}
	classified := classify("get environment network", request.Identity.Network, err)
	if !errors.Is(classified, ErrNotFound) {
		return nil, classified
	}

	slots, err := c.nodeNetworkSlotCount()
	if err != nil {
		return nil, err
	}
	for attempts := uint64(0); attempts < slots; attempts++ {
		subnet, err := c.selectEnvironmentNetworkSubnet(server, request.EnvironmentUID)
		if err != nil {
			return nil, err
		}
		expected := environmentNetwork(request, subnetGatewayPrefix(subnet).String())
		if err := server.CreateNetwork(expected); err != nil {
			classified = classify("create environment network", request.Identity.Network, err)
			if !errors.Is(classified, ErrConflict) {
				return nil, classified
			}
			current, _, getErr := server.GetNetwork(request.Identity.Network)
			if getErr == nil {
				return current, c.validateEnvironmentNetwork(current, request)
			}
			if classifiedGet := classify("get conflicted environment network", request.Identity.Network, getErr); !errors.Is(classifiedGet, ErrNotFound) {
				return nil, classifiedGet
			}
			continue
		}
		current, _, err = server.GetNetwork(request.Identity.Network)
		if err != nil {
			return nil, classify("get created environment network", request.Identity.Network, err)
		}
		return current, c.validateEnvironmentNetwork(current, request)
	}
	return nil, fmt.Errorf("%w: configured node network pool has no free subnet", ErrUnavailable)
}

func (c *Client) ensureEnvironmentProject(server incus.InstanceServer, request ProvisionNodeEnvironmentRequest) error {
	expected, err := c.environmentProject(request)
	if err != nil {
		return err
	}
	current, _, err := server.GetProject(request.Identity.Project)
	if err == nil {
		return validateEnvironmentProject(current, expected, request)
	}
	classified := classify("get environment project", request.Identity.Project, err)
	if !errors.Is(classified, ErrNotFound) {
		return classified
	}
	if err := server.CreateProject(expected); err != nil {
		classified = classify("create environment project", request.Identity.Project, err)
		if !errors.Is(classified, ErrConflict) {
			return classified
		}
	}
	current, _, err = server.GetProject(request.Identity.Project)
	if err != nil {
		return classify("get created environment project", request.Identity.Project, err)
	}
	return validateEnvironmentProject(current, expected, request)
}

func (c *Client) ensureEnvironmentImage(ctx context.Context, server incus.InstanceServer, request ProvisionNodeEnvironmentRequest) error {
	current, _, err := server.GetImage(request.ImageFingerprint)
	if err == nil {
		return validateEnvironmentImage(current, request.ImageFingerprint)
	}
	classified := classify("get environment image", request.Identity.Project+"/"+request.ImageFingerprint, err)
	if !errors.Is(classified, ErrNotFound) {
		return classified
	}
	op, err := server.CreateImage(api.ImagesPost{
		Source: &api.ImagesPostSource{
			ImageSource: api.ImageSource{Protocol: "incus"},
			Type:        "image",
			Fingerprint: request.ImageFingerprint,
			Project:     c.config.ImageProject,
		},
	}, nil)
	if err != nil {
		return classify("copy environment image", request.Identity.Project+"/"+request.ImageFingerprint, err)
	}
	if err := waitOperation(ctx, "copy environment image", request.Identity.Project+"/"+request.ImageFingerprint, op); err != nil {
		return err
	}
	current, _, err = server.GetImage(request.ImageFingerprint)
	if err != nil {
		return classify("get copied environment image", request.Identity.Project+"/"+request.ImageFingerprint, err)
	}
	return validateEnvironmentImage(current, request.ImageFingerprint)
}

func (c *Client) ensureEnvironmentProfile(server incus.InstanceServer, request ProvisionNodeEnvironmentRequest) error {
	expected := c.environmentProfile(request)
	current, _, err := server.GetProfile(request.Identity.Profile)
	if err == nil {
		return validateEnvironmentProfile(current, expected, request)
	}
	classified := classify("get environment profile", request.Identity.Project+"/"+request.Identity.Profile, err)
	if !errors.Is(classified, ErrNotFound) {
		return classified
	}
	if err := server.CreateProfile(expected); err != nil {
		classified = classify("create environment profile", request.Identity.Project+"/"+request.Identity.Profile, err)
		if !errors.Is(classified, ErrConflict) {
			return classified
		}
	}
	current, _, err = server.GetProfile(request.Identity.Profile)
	if err != nil {
		return classify("get created environment profile", request.Identity.Project+"/"+request.Identity.Profile, err)
	}
	return validateEnvironmentProfile(current, expected, request)
}

func (c *Client) ensureEnvironmentInstance(ctx context.Context, server incus.InstanceServer, request ProvisionNodeEnvironmentRequest, node NodeIdentity, topology string) (*api.Instance, error) {
	expected := environmentInstance(request, node, topology)
	current, _, err := server.GetInstance(node.InstanceName)
	if err == nil {
		return current, validateEnvironmentInstance(current, request, node, topology)
	}
	classified := classify("get environment node", request.Identity.Project+"/"+node.InstanceName, err)
	if !errors.Is(classified, ErrNotFound) {
		return nil, classified
	}
	op, err := server.CreateInstance(expected)
	if err != nil {
		return nil, classify("create environment node", request.Identity.Project+"/"+node.InstanceName, err)
	}
	if err := waitOperation(ctx, "create environment node", request.Identity.Project+"/"+node.InstanceName, op); err != nil {
		return nil, err
	}
	current, _, err = server.GetInstance(node.InstanceName)
	if err != nil {
		return nil, classify("get created environment node", request.Identity.Project+"/"+node.InstanceName, err)
	}
	return current, validateEnvironmentInstance(current, request, node, topology)
}

func (c *Client) readNodeInitialization(ctx context.Context, server incus.InstanceServer, request ProvisionNodeEnvironmentRequest, node NodeIdentity) (NodeInitialization, error) {
	// The Incus SDK file endpoint does not attach the client context to its
	// request. Use the same context-bound exec path as checkpoint execution so
	// a lost lease or reconciler cancellation can always stop this observation.
	result, err := c.execNode(ctx, server, node.InstanceName, []string{"cat", runtimeInitResultPath}, nil, "")
	if err != nil {
		return NodeInitialization{}, fmt.Errorf("read runtime initialization result: %w", err)
	}
	if result.ExitCode == 1 {
		return NodeInitialization{}, nil
	}
	if result.ExitCode != 0 {
		return NodeInitialization{}, fmt.Errorf("%w: read runtime initialization result exited with %d: %s", ErrInvariant, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	initialization, err := parseNodeInitialization([]byte(result.Stdout))
	if err != nil {
		return NodeInitialization{}, err
	}
	if initialization.Failed {
		result, execErr := c.execNode(ctx, server, node.InstanceName, []string{"journalctl", "--unit", "breakfix-runtime-init.service", "--no-pager", "--lines", "200"}, nil, "")
		if execErr == nil {
			initialization.Message = strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
		}
	}
	return initialization, nil
}

func parseNodeInitialization(data []byte) (NodeInitialization, error) {
	fields := make(map[string]string, 2)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "state" && key != "exit_code") {
			return NodeInitialization{}, fmt.Errorf("%w: malformed runtime initialization result", ErrInvariant)
		}
		if _, duplicate := fields[key]; duplicate {
			return NodeInitialization{}, fmt.Errorf("%w: duplicate runtime initialization result field %q", ErrInvariant, key)
		}
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		return NodeInitialization{}, fmt.Errorf("read runtime initialization result: %w", err)
	}
	exitCode, err := strconv.Atoi(fields["exit_code"])
	if err != nil || exitCode < 0 {
		return NodeInitialization{}, fmt.Errorf("%w: invalid runtime initialization exit code", ErrInvariant)
	}
	switch fields["state"] {
	case "success":
		if exitCode != 0 {
			return NodeInitialization{}, fmt.Errorf("%w: successful runtime initialization has non-zero exit code", ErrInvariant)
		}
		return NodeInitialization{Complete: true}, nil
	case "failure":
		if exitCode == 0 {
			return NodeInitialization{}, fmt.Errorf("%w: failed runtime initialization has zero exit code", ErrInvariant)
		}
		return NodeInitialization{Complete: true, Failed: true, ExitCode: exitCode}, nil
	default:
		return NodeInitialization{}, fmt.Errorf("%w: invalid runtime initialization state", ErrInvariant)
	}
}

func (c *Client) deleteEnvironmentInstances(ctx context.Context, server incus.InstanceServer, environmentUID, revision string, identity NodeEnvironmentIdentity) error {
	type pendingOperation struct {
		name string
		op   incus.Operation
	}
	stops := make([]pendingOperation, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		instance, _, err := server.GetInstance(node.InstanceName)
		if err != nil {
			classified := classify("get environment node for deletion", identity.Project+"/"+node.InstanceName, err)
			if errors.Is(classified, ErrNotFound) {
				continue
			}
			return classified
		}
		if err := validateOwner(instance.Config, environmentUID, revision, "node", node.InstanceName); err != nil {
			return err
		}
		if instance.IsActive() {
			op, err := server.UpdateInstanceState(node.InstanceName, api.InstanceStatePut{Action: "stop", Timeout: 30, Force: true}, "")
			if err != nil {
				return classify("stop environment node", identity.Project+"/"+node.InstanceName, err)
			}
			stops = append(stops, pendingOperation{name: node.InstanceName, op: op})
		}
	}
	for _, stop := range stops {
		if err := waitOperation(ctx, "stop environment node", identity.Project+"/"+stop.name, stop.op); err != nil {
			return err
		}
	}
	deletes := make([]pendingOperation, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		if _, _, err := server.GetInstance(node.InstanceName); err != nil {
			classified := classify("get stopped environment node", identity.Project+"/"+node.InstanceName, err)
			if errors.Is(classified, ErrNotFound) {
				continue
			}
			return classified
		}
		op, err := server.DeleteInstance(node.InstanceName)
		if err != nil {
			return classify("delete environment node", identity.Project+"/"+node.InstanceName, err)
		}
		deletes = append(deletes, pendingOperation{name: node.InstanceName, op: op})
	}
	for _, deletion := range deletes {
		if err := waitOperation(ctx, "delete environment node", identity.Project+"/"+deletion.name, deletion.op); err != nil {
			return err
		}
	}
	return nil
}

func deleteImageIfPresent(ctx context.Context, server incus.InstanceServer, project, fingerprint string) error {
	if _, _, err := server.GetImage(fingerprint); err != nil {
		classified := classify("get environment image for deletion", project+"/"+fingerprint, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	op, err := server.DeleteImage(fingerprint)
	if err != nil {
		return classify("delete environment image", project+"/"+fingerprint, err)
	}
	return waitOperation(ctx, "delete environment image", project+"/"+fingerprint, op)
}

func deleteProfileIfPresent(server incus.InstanceServer, environmentUID, revision, name string) error {
	profile, _, err := server.GetProfile(name)
	if err != nil {
		classified := classify("get environment profile for deletion", name, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	if err := validateOwner(profile.Config, environmentUID, revision, "profile", name); err != nil {
		return err
	}
	if err := server.DeleteProfile(name); err != nil {
		classified := classify("delete environment profile", name, err)
		if !errors.Is(classified, ErrNotFound) {
			return classified
		}
	}
	return nil
}

func deleteNetworkIfPresent(server incus.InstanceServer, environmentUID, revision, name string) error {
	network, _, err := server.GetNetwork(name)
	if err != nil {
		classified := classify("get environment network for deletion", name, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	if err := validateOwner(network.Config, environmentUID, revision, "network", name); err != nil {
		return err
	}
	if err := server.DeleteNetwork(name); err != nil {
		classified := classify("delete environment network", name, err)
		if !errors.Is(classified, ErrNotFound) {
			return classified
		}
	}
	return nil
}

func deleteACLIfPresent(server incus.InstanceServer, environmentUID, revision, name string) error {
	acl, _, err := server.GetNetworkACL(name)
	if err != nil {
		classified := classify("get environment ACL for deletion", name, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	if err := validateOwner(acl.Config, environmentUID, revision, "acl", name); err != nil {
		return err
	}
	if err := server.DeleteNetworkACL(name); err != nil {
		classified := classify("delete environment ACL", name, err)
		if !errors.Is(classified, ErrNotFound) {
			return classified
		}
	}
	return nil
}

func environmentACL(request ProvisionNodeEnvironmentRequest, blockedCIDRs []string) api.NetworkACLsPost {
	egress := make([]api.NetworkACLRule, 0, len(blockedCIDRs)+1)
	for _, cidr := range blockedCIDRs {
		egress = append(egress, api.NetworkACLRule{Action: "reject", Destination: cidr, State: "enabled", Description: "Block platform and private networks"})
	}
	egress = append(egress, api.NetworkACLRule{Action: "allow", State: "enabled", Description: "Allow public egress"})
	return api.NetworkACLsPost{
		NetworkACLPost: api.NetworkACLPost{Name: request.Identity.ACL},
		NetworkACLPut: api.NetworkACLPut{
			Description: "Breakfix managed NodeEnvironment egress policy",
			Config:      ownerConfig(request.EnvironmentUID, request.Revision, "acl"),
			Egress:      egress,
		},
	}
}

func environmentNetwork(request ProvisionNodeEnvironmentRequest, gateway string) api.NetworksPost {
	config := ownerConfig(request.EnvironmentUID, request.Revision, "network")
	config[networkPolicyRevisionKey] = request.NetworkPolicyRevision
	config["ipv4.address"] = gateway
	config["ipv4.nat"] = "true"
	config["ipv6.address"] = "none"
	config["security.acls"] = request.Identity.ACL
	config["security.acls.default.ingress.action"] = "reject"
	config["security.acls.default.egress.action"] = "reject"
	return api.NetworksPost{
		Name: request.Identity.Network,
		Type: "bridge",
		NetworkPut: api.NetworkPut{
			Description: "Breakfix managed NodeEnvironment network",
			Config:      config,
		},
	}
}

func (c *Client) environmentProject(request ProvisionNodeEnvironmentRequest) (api.ProjectsPost, error) {
	nodeCount := int64(len(request.Identity.Nodes))
	cpu, err := strconv.ParseInt(request.Resources.CPU, 10, 64)
	if err != nil || cpu <= 0 || cpu > math.MaxInt64/nodeCount {
		return api.ProjectsPost{}, fmt.Errorf("%w: invalid aggregate node CPU", ErrInvalid)
	}
	memory, err := units.ParseByteSizeString(request.Resources.Memory)
	if err != nil || memory <= 0 || memory > math.MaxInt64/nodeCount {
		return api.ProjectsPost{}, fmt.Errorf("%w: invalid aggregate node memory", ErrInvalid)
	}
	rootDisk, err := units.ParseByteSizeString(request.Resources.RootDisk)
	if err != nil || rootDisk <= 0 || rootDisk > math.MaxInt64/(nodeCount+1) {
		return api.ProjectsPost{}, fmt.Errorf("%w: invalid aggregate node disk", ErrInvalid)
	}
	if request.Resources.Processes <= 0 || request.Resources.Processes > math.MaxInt64/nodeCount {
		return api.ProjectsPost{}, fmt.Errorf("%w: invalid aggregate node process limit", ErrInvalid)
	}
	config := ownerConfig(request.EnvironmentUID, request.Revision, "project")
	for key, value := range map[string]string{
		"features.images":                          "true",
		"features.networks":                        "false",
		"features.profiles":                        "true",
		"features.storage.buckets":                 "false",
		"features.storage.volumes":                 "true",
		"restricted":                               "true",
		"restricted.backups":                       "block",
		"restricted.snapshots":                     "block",
		"restricted.cluster.target":                "block",
		"restricted.containers.lowlevel":           "block",
		"restricted.containers.nesting":            "block",
		"restricted.containers.privilege":          "isolated",
		"restricted.devices.disk":                  "managed",
		"restricted.devices.gpu":                   "block",
		"restricted.devices.nic":                   "managed",
		"restricted.devices.pci":                   "block",
		"restricted.devices.proxy":                 "block",
		"restricted.devices.unix-block":            "block",
		"restricted.devices.unix-char":             "block",
		"restricted.devices.unix-hotplug":          "block",
		"restricted.devices.usb":                   "block",
		"restricted.networks.access":               request.Identity.Network,
		"restricted.storage-pools.access":          c.config.StoragePool,
		"limits.instances":                         strconv.FormatInt(nodeCount, 10),
		"limits.containers":                        strconv.FormatInt(nodeCount, 10),
		"limits.virtual-machines":                  "0",
		"limits.cpu":                               strconv.FormatInt(cpu*nodeCount, 10),
		"limits.memory":                            strconv.FormatInt(memory*nodeCount, 10) + "B",
		"limits.processes":                         strconv.FormatInt(request.Resources.Processes*nodeCount, 10),
		"limits.disk.pool." + c.config.StoragePool: strconv.FormatInt(rootDisk*(nodeCount+1), 10) + "B",
	} {
		config[key] = value
	}
	return api.ProjectsPost{
		Name: request.Identity.Project,
		ProjectPut: api.ProjectPut{
			Description: "Breakfix managed NodeEnvironment project",
			Config:      config,
		},
	}, nil
}

func (c *Client) environmentProfile(request ProvisionNodeEnvironmentRequest) api.ProfilesPost {
	config := ownerConfig(request.EnvironmentUID, request.Revision, "profile")
	config[profileRevisionKey] = request.ProfileRevision
	config["security.privileged"] = "false"
	config["security.idmap.isolated"] = "true"
	config["limits.cpu"] = request.Resources.CPU
	config["limits.memory"] = request.Resources.Memory
	config["limits.processes"] = strconv.FormatInt(request.Resources.Processes, 10)
	return api.ProfilesPost{
		Name: request.Identity.Profile,
		ProfilePut: api.ProfilePut{
			Description: "Breakfix managed NodeEnvironment profile",
			Config:      config,
			Devices: api.DevicesMap{
				"root": {
					"type": "disk", "pool": c.config.StoragePool, "path": "/", "size": request.Resources.RootDisk,
				},
			},
		},
	}
}

func environmentInstance(request ProvisionNodeEnvironmentRequest, node NodeIdentity, topology string) api.InstancesPost {
	config := ownerConfig(request.EnvironmentUID, request.Revision, "node")
	config[logicalNodeKey] = node.LogicalName
	config["systemd.credential.breakfix.node"] = node.LogicalName
	config["systemd.credential.breakfix.topology"] = topology
	return api.InstancesPost{
		Name:   node.InstanceName,
		Type:   api.InstanceTypeContainer,
		Start:  false,
		Source: api.InstanceSource{Type: "image", Fingerprint: request.ImageFingerprint},
		InstancePut: api.InstancePut{
			Description: "Breakfix managed NodeEnvironment node",
			Config:      config,
			Profiles:    []string{request.Identity.Profile},
			Devices: api.DevicesMap{
				"eth0": {
					"type": "nic", "network": request.Identity.Network, "name": "eth0", "ipv4.address": node.Address, "security.ipv4_filtering": "true",
				},
			},
		},
	}
}

func validateEnvironmentACL(current *api.NetworkACL, expected api.NetworkACLsPost, request ProvisionNodeEnvironmentRequest) error {
	if current == nil || current.Name != expected.Name || current.Description != expected.Description {
		return fmt.Errorf("%w: environment ACL %q differs from expected identity", ErrInvariant, request.Identity.ACL)
	}
	if !equalStringMap(current.Config, expected.Config) || len(current.Ingress) != 0 || !equalACLRules(current.Egress, expected.Egress) {
		return fmt.Errorf("%w: environment ACL %q differs from expected policy", ErrInvariant, request.Identity.ACL)
	}
	return nil
}

func (c *Client) validateEnvironmentNetwork(current *api.Network, request ProvisionNodeEnvironmentRequest) error {
	if current == nil || current.Name != request.Identity.Network || current.Type != "bridge" || !current.Managed || current.Status != api.NetworkStatusCreated {
		return fmt.Errorf("%w: environment network %q has unexpected metadata", ErrInvariant, request.Identity.Network)
	}
	gateway, err := c.validateEnvironmentNetworkGateway(current.Config["ipv4.address"])
	if err != nil {
		return fmt.Errorf("%w: environment network %q has invalid IPv4 subnet: %v", ErrInvariant, request.Identity.Network, err)
	}
	required := environmentNetwork(request, gateway.String()).Config
	for key, expected := range required {
		if current.Config[key] != expected {
			return fmt.Errorf("%w: environment network %q differs at %s", ErrInvariant, request.Identity.Network, key)
		}
	}
	for key := range current.Config {
		if _, ok := required[key]; ok || strings.HasPrefix(key, "volatile.") {
			continue
		}
		return fmt.Errorf("%w: environment network %q has unexpected config %s", ErrInvariant, request.Identity.Network, key)
	}
	return nil
}

func (c *Client) selectEnvironmentNetworkSubnet(server incus.InstanceServer, environmentUID string) (netip.Prefix, error) {
	pool, err := c.config.nodeNetworkPool()
	if err != nil {
		return netip.Prefix{}, err
	}
	slots, err := c.nodeNetworkSlotCount()
	if err != nil {
		return netip.Prefix{}, err
	}
	networks, err := server.GetNetworks()
	if err != nil {
		return netip.Prefix{}, classify("list environment networks", pool.String(), err)
	}
	occupied := make([]netip.Prefix, 0, len(networks))
	for _, network := range networks {
		prefix, parseErr := netip.ParsePrefix(network.Config["ipv4.address"])
		if parseErr == nil && prefix.Addr().Is4() {
			occupied = append(occupied, prefix.Masked())
		}
	}
	digest := sha256.Sum256([]byte(environmentUID))
	start := uint64(binary.BigEndian.Uint32(digest[:4])) % slots
	for offset := uint64(0); offset < slots; offset++ {
		candidate := childSubnet(pool, c.config.NodeNetworkPrefix, (start+offset)%slots)
		if !overlapsAnyPrefix(candidate, occupied) {
			return candidate, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("%w: configured node network pool %q has no free /%d subnet", ErrUnavailable, pool, c.config.NodeNetworkPrefix)
}

func (c *Client) nodeNetworkSlotCount() (uint64, error) {
	pool, err := c.config.nodeNetworkPool()
	if err != nil {
		return 0, err
	}
	if c.config.NodeNetworkPrefix <= pool.Bits() {
		return 0, fmt.Errorf("%w: node network prefix is not inside configured pool", ErrInvalid)
	}
	return uint64(1) << uint(c.config.NodeNetworkPrefix-pool.Bits()), nil
}

func (c *Client) validateEnvironmentNetworkGateway(value string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != c.config.NodeNetworkPrefix {
		return netip.Prefix{}, errors.New("gateway is not a configured IPv4 subnet")
	}
	pool, err := c.config.nodeNetworkPool()
	if err != nil {
		return netip.Prefix{}, err
	}
	masked := prefix.Masked()
	if !pool.Contains(masked.Addr()) || subnetGateway(masked) != prefix.Addr() {
		return netip.Prefix{}, errors.New("gateway is outside the configured node network pool")
	}
	return prefix, nil
}

func childSubnet(pool netip.Prefix, bits int, slot uint64) netip.Prefix {
	base := binary.BigEndian.Uint32(pool.Masked().Addr().AsSlice())
	address := base + uint32(slot<<uint(32-bits))
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], address)
	return netip.PrefixFrom(netip.AddrFrom4(raw), bits)
}

func subnetGateway(prefix netip.Prefix) netip.Addr {
	return prefix.Masked().Addr().Next()
}

func subnetGatewayPrefix(prefix netip.Prefix) netip.Prefix {
	return netip.PrefixFrom(subnetGateway(prefix), prefix.Bits())
}

func overlapsAnyPrefix(candidate netip.Prefix, occupied []netip.Prefix) bool {
	for _, prefix := range occupied {
		if candidate.Contains(prefix.Addr()) || prefix.Contains(candidate.Addr()) {
			return true
		}
	}
	return false
}

func validateEnvironmentProject(current *api.Project, expected api.ProjectsPost, request ProvisionNodeEnvironmentRequest) error {
	if current == nil || current.Name != expected.Name || current.Description != expected.Description || !equalStringMap(current.Config, expected.Config) {
		return fmt.Errorf("%w: environment project %q differs from expected policy", ErrInvariant, request.Identity.Project)
	}
	return nil
}

func validateEnvironmentImage(current *api.Image, fingerprint string) error {
	if current == nil || current.Fingerprint != fingerprint || !current.ExpiresAt.IsZero() || current.Public || current.Type != string(api.InstanceTypeContainer) {
		return fmt.Errorf("%w: environment image %q has unexpected metadata", ErrInvariant, fingerprint)
	}
	return nil
}

func validateEnvironmentProfile(current *api.Profile, expected api.ProfilesPost, request ProvisionNodeEnvironmentRequest) error {
	if current == nil || current.Name != expected.Name || current.Description != expected.Description || !equalStringMap(current.Config, expected.Config) || !equalDevices(current.Devices, expected.Devices) {
		return fmt.Errorf("%w: environment profile %q differs from expected policy", ErrInvariant, request.Identity.Profile)
	}
	return nil
}

func validateEnvironmentInstance(current *api.Instance, request ProvisionNodeEnvironmentRequest, node NodeIdentity, topology string) error {
	expected := environmentInstance(request, node, topology)
	if current == nil || current.Name != node.InstanceName || current.Type != string(api.InstanceTypeContainer) || current.Description != expected.Description || !slices.Equal(current.Profiles, expected.Profiles) || !equalDevices(current.Devices, expected.Devices) {
		return fmt.Errorf("%w: environment node %q differs from expected identity", ErrInvariant, node.InstanceName)
	}
	for key, value := range expected.Config {
		if current.Config[key] != value {
			return fmt.Errorf("%w: environment node %q differs at %s", ErrInvariant, node.InstanceName, key)
		}
	}
	for key := range current.Config {
		if _, ok := expected.Config[key]; ok || strings.HasPrefix(key, "volatile.") || strings.HasPrefix(key, "image.") {
			continue
		}
		return fmt.Errorf("%w: environment node %q has unexpected config %s", ErrInvariant, node.InstanceName, key)
	}
	baseImage := current.Config["volatile.base_image"]
	if baseImage == "" {
		baseImage = current.ExpandedConfig["volatile.base_image"]
	}
	if baseImage != request.ImageFingerprint {
		return fmt.Errorf("%w: environment node %q was not created from the requested image", ErrInvariant, node.InstanceName)
	}
	return nil
}

func validateOwner(config map[string]string, environmentUID, revision, kind, name string) error {
	if config[environmentUIDKey] != environmentUID || config[revisionKey] != revision || config[resourceKindKey] != kind {
		return fmt.Errorf("%w: refusing to use or delete %s %q with mismatched owner", ErrInvariant, kind, name)
	}
	return nil
}

func ownerConfig(environmentUID, revision, kind string) api.ConfigMap {
	return api.ConfigMap{
		environmentUIDKey: environmentUID,
		revisionKey:       revision,
		resourceKindKey:   kind,
	}
}

func identityWithNetworkAddresses(identity NodeEnvironmentIdentity, subnet string) (NodeEnvironmentIdentity, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil || !prefix.Addr().Is4() {
		return NodeEnvironmentIdentity{}, fmt.Errorf("%w: invalid environment IPv4 subnet %q", ErrInvariant, subnet)
	}
	base := prefix.Masked().Addr()
	result := identity
	result.Nodes = append([]NodeIdentity(nil), identity.Nodes...)
	for index := range result.Nodes {
		address := base
		for offset := 0; offset < 10+index; offset++ {
			address = address.Next()
		}
		if !address.IsValid() || !prefix.Contains(address) || !prefix.Contains(address.Next()) {
			return NodeEnvironmentIdentity{}, fmt.Errorf("%w: environment subnet %q is too small", ErrInvariant, subnet)
		}
		if result.Nodes[index].Address != "" && result.Nodes[index].Address != address.String() {
			return NodeEnvironmentIdentity{}, fmt.Errorf("%w: environment node %q address differs from its network", ErrInvariant, result.Nodes[index].LogicalName)
		}
		result.Nodes[index].Address = address.String()
	}
	return result, nil
}

func topologyCredential(nodes []NodeIdentity) string {
	lines := make([]string, 0, len(nodes))
	for _, node := range nodes {
		lines = append(lines, node.Address+" "+node.LogicalName)
	}
	return strings.Join(lines, "\n")
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func equalDevices(left, right api.DevicesMap) bool {
	if len(left) != len(right) {
		return false
	}
	for name, device := range left {
		if !equalStringMap(device, right[name]) {
			return false
		}
	}
	return true
}

func equalACLRules(left, right []api.NetworkACLRule) bool {
	left = append([]api.NetworkACLRule(nil), left...)
	right = append([]api.NetworkACLRule(nil), right...)
	for index := range left {
		left[index].Normalise()
	}
	for index := range right {
		right[index].Normalise()
	}
	key := func(rule api.NetworkACLRule) string {
		return strings.Join([]string{rule.Action, rule.Source, rule.Destination, rule.Protocol, rule.SourcePort, rule.DestinationPort, rule.ICMPType, rule.ICMPCode, rule.Description, rule.State}, "\x00")
	}
	slices.SortFunc(left, func(a, b api.NetworkACLRule) int { return strings.Compare(key(a), key(b)) })
	slices.SortFunc(right, func(a, b api.NetworkACLRule) int { return strings.Compare(key(a), key(b)) })
	return slices.Equal(left, right)
}
