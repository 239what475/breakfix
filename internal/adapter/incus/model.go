package incus

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const SupportedServerVersion = "7.0.1"

var (
	ErrNotFound    = errors.New("incus resource not found")
	ErrConflict    = errors.New("incus resource conflict")
	ErrUnavailable = errors.New("incus provider unavailable")
	ErrInvalid     = errors.New("invalid incus request")
	ErrInvariant   = errors.New("incus resource invariant violated")
)

type TLSConfig struct {
	ServerCertificateFile string `yaml:"server_certificate_file"`
	ClientCertificateFile string `yaml:"client_certificate_file"`
	ClientKeyFile         string `yaml:"client_key_file"`
}

type Config struct {
	Endpoint               string    `yaml:"endpoint"`
	TLS                    TLSConfig `yaml:"tls"`
	StoragePool            string    `yaml:"storage_pool"`
	NetworkDriver          string    `yaml:"network_driver"`
	BuildProject           string    `yaml:"build_project"`
	ImageProject           string    `yaml:"image_project"`
	BaseImageAlias         string    `yaml:"base_image_alias"`
	BaseImageFingerprint   string    `yaml:"base_image_fingerprint"`
	NamePrefix             string    `yaml:"name_prefix"`
	MaxNodesPerEnvironment int       `yaml:"max_nodes_per_environment"`
	NodeCPU                string    `yaml:"node_cpu"`
	NodeMemory             string    `yaml:"node_memory"`
	NodeProcesses          int64     `yaml:"node_processes"`
	NodeRootDisk           string    `yaml:"node_root_disk"`
	NodeNetworkPool        string    `yaml:"node_network_pool"`
	NodeNetworkPrefix      int       `yaml:"node_network_prefix"`
	BlockedEgressCIDRs     []string  `yaml:"blocked_egress_cidrs"`
}

func (c Config) Validate() error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(c.Endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("%w: incus endpoint must be an absolute HTTPS origin", ErrInvalid)
	}
	if strings.TrimSpace(c.TLS.ServerCertificateFile) == "" || strings.TrimSpace(c.TLS.ClientCertificateFile) == "" || strings.TrimSpace(c.TLS.ClientKeyFile) == "" {
		return fmt.Errorf("%w: incus server certificate, client certificate, and client key files are required", ErrInvalid)
	}
	for field, value := range map[string]string{
		"storage_pool":      c.StoragePool,
		"build_project":     c.BuildProject,
		"image_project":     c.ImageProject,
		"base_image_alias":  c.BaseImageAlias,
		"name_prefix":       c.NamePrefix,
		"node_memory":       c.NodeMemory,
		"node_root_disk":    c.NodeRootDisk,
		"node_network_pool": c.NodeNetworkPool,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: incus %s is required", ErrInvalid, field)
		}
	}
	if c.BuildProject == c.ImageProject || c.BuildProject == "default" || c.ImageProject == "default" {
		return fmt.Errorf("%w: incus build and image projects must be distinct and non-default", ErrInvalid)
	}
	if c.NetworkDriver != "bridge" {
		return fmt.Errorf("%w: incus network_driver must be bridge", ErrInvalid)
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,5}$`).MatchString(c.NamePrefix) {
		return fmt.Errorf("%w: incus name_prefix must be a short lowercase name", ErrInvalid)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.BaseImageFingerprint) {
		return fmt.Errorf("%w: incus base_image_fingerprint must be a full lowercase SHA-256 fingerprint", ErrInvalid)
	}
	cpu, err := strconv.Atoi(c.NodeCPU)
	if err != nil || cpu <= 0 {
		return fmt.Errorf("%w: incus node_cpu must be a positive integer", ErrInvalid)
	}
	if c.MaxNodesPerEnvironment < 1 || c.NodeProcesses < 1 {
		return fmt.Errorf("%w: incus max_nodes_per_environment and node_processes must be positive", ErrInvalid)
	}
	pool, err := c.nodeNetworkPool()
	if err != nil {
		return err
	}
	addresses := uint64(1) << uint(32-c.NodeNetworkPrefix)
	if addresses <= uint64(10+c.MaxNodesPerEnvironment) {
		return fmt.Errorf("%w: incus node_network_prefix is too small for max_nodes_per_environment", ErrInvalid)
	}
	if pool.Bits() >= c.NodeNetworkPrefix {
		return fmt.Errorf("%w: incus node_network_prefix must be longer than node_network_pool", ErrInvalid)
	}
	if len(c.BlockedEgressCIDRs) == 0 {
		return fmt.Errorf("%w: incus blocked_egress_cidrs must not be empty", ErrInvalid)
	}
	for _, value := range c.BlockedEgressCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%w: invalid blocked egress CIDR %q", ErrInvalid, value)
		}
	}
	return nil
}

func (c Config) nodeNetworkPool() (netip.Prefix, error) {
	pool, err := netip.ParsePrefix(strings.TrimSpace(c.NodeNetworkPool))
	if err != nil || !pool.Addr().Is4() || pool != pool.Masked() {
		return netip.Prefix{}, fmt.Errorf("%w: incus node_network_pool must be a masked IPv4 CIDR", ErrInvalid)
	}
	if c.NodeNetworkPrefix < 1 || c.NodeNetworkPrefix > 30 {
		return netip.Prefix{}, fmt.Errorf("%w: incus node_network_prefix must be between 1 and 30", ErrInvalid)
	}
	if !privateIPv4Pool(pool) {
		return netip.Prefix{}, fmt.Errorf("%w: incus node_network_pool must be contained in an RFC1918 IPv4 range", ErrInvalid)
	}
	return pool, nil
}

func privateIPv4Pool(pool netip.Prefix) bool {
	for _, private := range []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	} {
		if pool.Bits() >= private.Bits() && private.Contains(pool.Addr()) {
			return true
		}
	}
	return false
}

type Role string

const (
	RoleController Role = "controller"
	RoleServer     Role = "server"
	RoleRuntime    Role = "runtime"
)

func (r Role) Valid() bool {
	switch r {
	case RoleController, RoleServer, RoleRuntime:
		return true
	default:
		return false
	}
}

type PreflightResult struct {
	ServerVersion        string
	MemberName           string
	StoragePool          string
	BaseImageFingerprint string
}
