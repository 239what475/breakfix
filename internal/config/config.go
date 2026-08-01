package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

type Config struct {
	Port                 int                `yaml:"port"`
	HealthPort           int                `yaml:"health_port"`
	DataDir              string             `yaml:"data_dir"`
	DatabaseURL          string             `yaml:"database_url"`
	Kubeconfig           string             `yaml:"kubeconfig"`
	Registry             RegistryConfig     `yaml:"registry"`
	VClusterBinary       string             `yaml:"vcluster_binary"`
	VClusterChartRepo    string             `yaml:"vcluster_chart_repo"`
	VClusterChartVersion string             `yaml:"vcluster_chart_version"`
	UIOrigin             string             `yaml:"ui_origin"`
	Namespace            string             `yaml:"namespace"`
	CRDNamespace         string             `yaml:"crd_namespace"`
	CooldownMinutes      int                `yaml:"cooldown_minutes"`
	JWTSecret            string             `yaml:"jwt_secret"`
	InternalWorkers      InternalWorkerKeys `yaml:"internal_workers"`
	Worker               WorkerConfig       `yaml:"worker"`
	Agent                AgentConfig        `yaml:"agent"`
	OpenSandbox          OpenSandboxConfig  `yaml:"opensandbox"`
	Incus                incus.Config       `yaml:"incus"`
	Runtime              RuntimeConfig      `yaml:"runtime"`
}

// RegistryConfig separates the OCI reference root from the HTTPS endpoint used
// by control-plane clients. Address is embedded in immutable image references
// and must be reachable by Kubernetes nodes. ClientAddress is an authority
// used by Server and Generate Worker HTTP calls; production normally sets it
// to Address's authority, while Kind uses the Registry Service DNS name.
type RegistryConfig struct {
	Address         string `yaml:"address"`
	ClientAddress   string `yaml:"client_address"`
	PullSecret      string `yaml:"pull_secret"`
	TrustBundleFile string `yaml:"trust_bundle_file"`
	Username        string `yaml:"-"`
	Password        string `yaml:"-"`
}

func (c RegistryConfig) Validate() error {
	address := strings.TrimRight(strings.TrimSpace(c.Address), "/")
	if address == "" || strings.Contains(address, "://") || strings.ContainsAny(address, " \t\r\n@") {
		return fmt.Errorf("registry address must be an OCI repository root")
	}
	parts := strings.Split(address, "/")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return fmt.Errorf("registry address must include a hostname and repository namespace")
	}
	for _, part := range parts[1:] {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("registry address contains an empty repository component")
		}
	}
	if strings.LastIndex(address, ":") > strings.LastIndex(address, "/") {
		return fmt.Errorf("registry address must not contain an image tag")
	}
	clientAddress := strings.TrimSpace(c.ClientAddress)
	if clientAddress == "" || strings.Contains(clientAddress, "://") || strings.ContainsAny(clientAddress, " \t\r\n/@") {
		return fmt.Errorf("registry client_address must be an HTTPS authority")
	}
	clientURL, err := url.Parse("https://" + clientAddress)
	if err != nil || clientURL.Host == "" || clientURL.Host != clientAddress || clientURL.Path != "" || clientURL.RawQuery != "" || clientURL.Fragment != "" || clientURL.User != nil {
		return fmt.Errorf("registry client_address must be an HTTPS authority")
	}
	if (strings.TrimSpace(c.Username) == "") != (strings.TrimSpace(c.Password) == "") {
		return fmt.Errorf("registry username and password must be set together")
	}
	return nil
}

// RuntimeConfig contains the immutable platform profile used when Server
// creates learning environments and CandidateRevision execution snapshots.
// Artifact references come from the published challenge or candidate; these
// values describe only the platform-owned runtime around that artifact.
type RuntimeConfig struct {
	Node NodeRuntimeConfig `yaml:"node"`
	K8s  K8sRuntimeConfig  `yaml:"k8s"`
}

type NodeRuntimeConfig struct {
	ProfileRevision       string `yaml:"profile_revision"`
	NetworkPolicyRevision string `yaml:"network_policy_revision"`
}

type K8sRuntimeConfig struct {
	BaseImageDigest         string            `yaml:"base_image_digest"`
	ProfileRevision         string            `yaml:"profile_revision"`
	Version                 string            `yaml:"version"`
	ManagementTerminalImage string            `yaml:"management_terminal_image"`
	Resources               K8sResourceConfig `yaml:"resources"`
}

type K8sResourceConfig struct {
	ControlPlaneCPU              string `yaml:"control_plane_cpu"`
	ControlPlaneMemory           string `yaml:"control_plane_memory"`
	ControlPlaneEphemeralStorage string `yaml:"control_plane_ephemeral_storage"`
	WorkloadCPU                  string `yaml:"workload_cpu"`
	WorkloadMemory               string `yaml:"workload_memory"`
	WorkloadEphemeralStorage     string `yaml:"workload_ephemeral_storage"`
	QuotaCPU                     string `yaml:"quota_cpu"`
	QuotaMemory                  string `yaml:"quota_memory"`
	QuotaEphemeralStorage        string `yaml:"quota_ephemeral_storage"`
}

type AgentConfig struct {
	BaseURL        string `yaml:"base_url"`
	APIKeyEnv      string `yaml:"api_key_env"`
	Model          string `yaml:"model"`
	RequestTimeout string `yaml:"request_timeout"`
	APIKey         string `yaml:"-"`
}

type WorkerConfig struct {
	ServerURL string `yaml:"server_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	APIKey    string `yaml:"-"`
}

// InternalWorkerRole identifies one fixed worker pool. These identities are
// intentionally independent: compromising one worker must not grant access to
// another worker's Server endpoints.
type InternalWorkerRole string

const (
	InternalWorkerGenerate InternalWorkerRole = "generate"
	InternalWorkerTaxonomy InternalWorkerRole = "taxonomy"
)

// InternalWorkerKeys are read only by Server. Every fixed worker receives its
// own API key through WorkerConfig.APIKeyEnv instead of this complete set.
type InternalWorkerKeys struct {
	Generate string `yaml:"generate"`
	Taxonomy string `yaml:"taxonomy"`
}

func (k InternalWorkerKeys) Key(role InternalWorkerRole) string {
	switch role {
	case InternalWorkerGenerate:
		return k.Generate
	case InternalWorkerTaxonomy:
		return k.Taxonomy
	default:
		return ""
	}
}

func (k InternalWorkerKeys) Validate() error {
	keys := []struct {
		role InternalWorkerRole
		key  string
	}{
		{InternalWorkerGenerate, k.Generate},
		{InternalWorkerTaxonomy, k.Taxonomy},
	}
	seen := make(map[string]InternalWorkerRole, len(keys))
	for _, item := range keys {
		value := strings.TrimSpace(item.key)
		if value == "" {
			return fmt.Errorf("internal_workers.%s is required", item.role)
		}
		if previous, duplicate := seen[value]; duplicate {
			return fmt.Errorf("internal_workers.%s and internal_workers.%s must use different keys", previous, item.role)
		}
		seen[value] = item.role
	}
	return nil
}

// OpenSandboxConfig describes the Server-owned Generator workspace plane.
// Its lifecycle key intentionally never appears in a Generate Worker config.
type OpenSandboxConfig struct {
	BaseURL                   string `yaml:"base_url"`
	APIKeyEnv                 string `yaml:"api_key_env"`
	Namespace                 string `yaml:"namespace"`
	WorkspaceImage            string `yaml:"workspace_image"`
	WorkspaceStorage          string `yaml:"workspace_storage"`
	WorkspaceCPU              string `yaml:"workspace_cpu"`
	WorkspaceMemory           string `yaml:"workspace_memory"`
	WorkspaceProvisionTimeout string `yaml:"workspace_provision_timeout"`
	APIKey                    string `yaml:"-"`
}

func (c AgentConfig) Timeout() (time.Duration, error) {
	value, err := time.ParseDuration(c.RequestTimeout)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("agent request_timeout must be a positive duration")
	}
	return value, nil
}

func (c OpenSandboxConfig) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.APIKeyEnv) == "" || strings.TrimSpace(c.Namespace) == "" {
		return fmt.Errorf("opensandbox base_url, api_key_env, and namespace are required")
	}
	if strings.TrimSpace(c.WorkspaceImage) == "" || strings.TrimSpace(c.WorkspaceStorage) == "" || strings.TrimSpace(c.WorkspaceCPU) == "" || strings.TrimSpace(c.WorkspaceMemory) == "" {
		return fmt.Errorf("opensandbox workspace_image, workspace_storage, workspace_cpu, and workspace_memory are required")
	}
	for name, value := range map[string]string{"workspace_storage": c.WorkspaceStorage, "workspace_cpu": c.WorkspaceCPU, "workspace_memory": c.WorkspaceMemory} {
		quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("opensandbox %s must be a positive resource quantity", name)
		}
	}
	if _, err := c.ProvisionTimeout(); err != nil {
		return err
	}
	return nil
}

// ProvisionTimeout is the maximum time Server waits for a newly created
// Generator Sandbox to become usable. The Agent Run deadline remains the
// outer bound for retries and model execution.
func (c OpenSandboxConfig) ProvisionTimeout() (time.Duration, error) {
	timeout, err := time.ParseDuration(strings.TrimSpace(c.WorkspaceProvisionTimeout))
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("opensandbox workspace_provision_timeout must be a positive duration")
	}
	return timeout, nil
}

func (c RuntimeConfig) Validate() error {
	if strings.TrimSpace(c.Node.ProfileRevision) == "" || strings.TrimSpace(c.Node.NetworkPolicyRevision) == "" {
		return fmt.Errorf("runtime node profile_revision and network_policy_revision are required")
	}
	for name, value := range map[string]string{
		"base_image_digest":         c.K8s.BaseImageDigest,
		"profile_revision":          c.K8s.ProfileRevision,
		"version":                   c.K8s.Version,
		"management_terminal_image": c.K8s.ManagementTerminalImage,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("runtime k8s %s is required", name)
		}
	}
	if !immutableOCIReference(c.K8s.BaseImageDigest) || !immutableOCIReference(c.K8s.ManagementTerminalImage) {
		return fmt.Errorf("runtime k8s base_image_digest and management_terminal_image must be immutable OCI digest references")
	}
	if err := (environment.VK8sResources{
		ControlPlaneCPU: c.K8s.Resources.ControlPlaneCPU, ControlPlaneMemory: c.K8s.Resources.ControlPlaneMemory,
		ControlPlaneEphemeralStorage: c.K8s.Resources.ControlPlaneEphemeralStorage,
		WorkloadCPU:                  c.K8s.Resources.WorkloadCPU, WorkloadMemory: c.K8s.Resources.WorkloadMemory,
		WorkloadEphemeralStorage: c.K8s.Resources.WorkloadEphemeralStorage,
		QuotaCPU:                 c.K8s.Resources.QuotaCPU, QuotaMemory: c.K8s.Resources.QuotaMemory,
		QuotaEphemeralStorage: c.K8s.Resources.QuotaEphemeralStorage,
	}).Validate(); err != nil {
		return fmt.Errorf("runtime k8s resources: %w", err)
	}
	return nil
}

func immutableOCIReference(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "@sha256:")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || len(parts[1]) != 64 {
		return false
	}
	for _, character := range parts[1] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func Load(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	cfg.DatabaseURL = os.ExpandEnv(cfg.DatabaseURL)
	cfg.JWTSecret = os.ExpandEnv(cfg.JWTSecret)
	cfg.InternalWorkers.Generate = os.ExpandEnv(cfg.InternalWorkers.Generate)
	cfg.InternalWorkers.Taxonomy = os.ExpandEnv(cfg.InternalWorkers.Taxonomy)
	cfg.Worker.ServerURL = os.ExpandEnv(cfg.Worker.ServerURL)
	cfg.Registry.Address = os.ExpandEnv(cfg.Registry.Address)
	cfg.Registry.ClientAddress = os.ExpandEnv(cfg.Registry.ClientAddress)
	cfg.Registry.PullSecret = os.ExpandEnv(cfg.Registry.PullSecret)
	cfg.Registry.TrustBundleFile = os.ExpandEnv(cfg.Registry.TrustBundleFile)
	cfg.OpenSandbox.BaseURL = os.ExpandEnv(cfg.OpenSandbox.BaseURL)
	cfg.OpenSandbox.Namespace = os.ExpandEnv(cfg.OpenSandbox.Namespace)
	cfg.UIOrigin = os.ExpandEnv(cfg.UIOrigin)
	cfg.Incus.Endpoint = os.ExpandEnv(cfg.Incus.Endpoint)
	cfg.Incus.BaseImageFingerprint = os.ExpandEnv(cfg.Incus.BaseImageFingerprint)
	cfg.Incus.TLS.ServerCertificateFile = os.ExpandEnv(cfg.Incus.TLS.ServerCertificateFile)
	cfg.Incus.TLS.ClientCertificateFile = os.ExpandEnv(cfg.Incus.TLS.ClientCertificateFile)
	cfg.Incus.TLS.ClientKeyFile = os.ExpandEnv(cfg.Incus.TLS.ClientKeyFile)
	cfg.Runtime.K8s.BaseImageDigest = os.ExpandEnv(cfg.Runtime.K8s.BaseImageDigest)
	cfg.Runtime.K8s.ManagementTerminalImage = os.ExpandEnv(cfg.Runtime.K8s.ManagementTerminalImage)
	cfg.Kubeconfig = expandKubeconfigPath(os.ExpandEnv(cfg.Kubeconfig))
	cfg.Agent.APIKey = os.Getenv(cfg.Agent.APIKeyEnv)
	cfg.OpenSandbox.APIKey = os.Getenv(cfg.OpenSandbox.APIKeyEnv)
	cfg.Worker.APIKey = os.Getenv(cfg.Worker.APIKeyEnv)
	cfg.Registry.Username = os.Getenv("BREAKFIX_REGISTRY_USERNAME")
	cfg.Registry.Password = os.Getenv("BREAKFIX_REGISTRY_PASSWORD")
	if (strings.TrimSpace(cfg.Registry.Username) == "") != (strings.TrimSpace(cfg.Registry.Password) == "") {
		return cfg, fmt.Errorf("BREAKFIX_REGISTRY_USERNAME and BREAKFIX_REGISTRY_PASSWORD must be set together")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return cfg, fmt.Errorf("parse config: multiple YAML documents are not supported")
		}
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func expandKubeconfigPath(value string) string {
	value = strings.TrimSpace(value)
	if value != "~" && !strings.HasPrefix(value, "~/") {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	if value == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(value, "~/"))
}

// ValidateServer checks the complete dependency contract of the Server
// process. Other processes intentionally validate only the configuration they
// consume, so a Generate Worker never needs a Kubernetes or OpenSandbox secret.
func (c Config) ValidateServer() error {
	if c.Port <= 0 || strings.TrimSpace(c.DataDir) == "" || strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("server port, data_dir, and database_url are required")
	}
	if strings.TrimSpace(c.JWTSecret) == "" {
		return fmt.Errorf("server jwt_secret is required")
	}
	if err := c.InternalWorkers.Validate(); err != nil {
		return fmt.Errorf("server %w", err)
	}
	if err := c.Registry.Validate(); err != nil {
		return fmt.Errorf("server registry: %w", err)
	}
	if strings.TrimSpace(c.Namespace) == "" || strings.TrimSpace(c.CRDNamespace) == "" || c.CooldownMinutes <= 0 {
		return fmt.Errorf("server namespace, crd_namespace, and positive cooldown_minutes are required")
	}
	if strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("server agent model is required")
	}
	if err := c.OpenSandbox.Validate(); err != nil {
		return fmt.Errorf("server opensandbox: %w", err)
	}
	if strings.TrimSpace(c.OpenSandbox.APIKey) == "" {
		return fmt.Errorf("server opensandbox lifecycle API key is required")
	}
	if _, err := c.ParsedUIOrigin(); err != nil {
		return fmt.Errorf("server ui_origin: %w", err)
	}
	if err := c.Incus.Validate(); err != nil {
		return fmt.Errorf("server incus: %w", err)
	}
	if err := c.Runtime.Validate(); err != nil {
		return err
	}
	return nil
}

func (c Config) ValidateController() error {
	if c.HealthPort <= 0 {
		return fmt.Errorf("controller health_port is required")
	}
	if strings.TrimSpace(c.Namespace) == "" || strings.TrimSpace(c.CRDNamespace) == "" || c.CooldownMinutes <= 0 {
		return fmt.Errorf("controller namespace, crd_namespace, and positive cooldown_minutes are required")
	}
	if strings.TrimSpace(c.VClusterBinary) == "" || strings.TrimSpace(c.VClusterChartRepo) == "" || strings.TrimSpace(c.VClusterChartVersion) == "" {
		return fmt.Errorf("controller vcluster configuration is required")
	}
	if err := c.Incus.Validate(); err != nil {
		return fmt.Errorf("controller incus: %w", err)
	}
	if err := c.Runtime.Validate(); err != nil {
		return err
	}
	return nil
}

func (c Config) ValidateGenerateWorker() error {
	if err := c.validateWorker(); err != nil {
		return fmt.Errorf("generate worker: %w", err)
	}
	if strings.TrimSpace(c.Agent.BaseURL) == "" || strings.TrimSpace(c.Agent.APIKeyEnv) == "" || strings.TrimSpace(c.Agent.APIKey) == "" || strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("generate worker base_url, api_key_env, API key, and model are required")
	}
	if _, err := c.Agent.Timeout(); err != nil {
		return err
	}
	if err := c.Registry.Validate(); err != nil {
		return fmt.Errorf("generate worker registry: %w", err)
	}
	if strings.TrimSpace(c.CRDNamespace) == "" {
		return fmt.Errorf("generate worker crd_namespace is required")
	}
	if err := c.Incus.Validate(); err != nil {
		return fmt.Errorf("generate worker incus: %w", err)
	}
	return nil
}

func (c Config) ValidateTaxonomyWorker() error {
	if err := c.validateWorker(); err != nil {
		return fmt.Errorf("taxonomy worker: %w", err)
	}
	if strings.TrimSpace(c.Agent.BaseURL) == "" || strings.TrimSpace(c.Agent.APIKeyEnv) == "" || strings.TrimSpace(c.Agent.APIKey) == "" || strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("taxonomy worker base_url, api_key_env, API key, and model are required")
	}
	if _, err := c.Agent.Timeout(); err != nil {
		return err
	}
	return nil
}

func (c Config) validateWorker() error {
	if c.HealthPort <= 0 || strings.TrimSpace(c.Worker.ServerURL) == "" || strings.TrimSpace(c.Worker.APIKeyEnv) == "" || strings.TrimSpace(c.Worker.APIKey) == "" {
		return fmt.Errorf("health_port, worker.server_url, worker.api_key_env, and worker API key are required")
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(c.Worker.ServerURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("worker.server_url must be an absolute HTTP(S) URL")
	}
	return nil
}

// ParsedUIOrigin accepts exactly one browser origin. Terminal WebSockets use
// it to reject every other Origin before the ticket is consumed.
func (c Config) ParsedUIOrigin() (*url.URL, error) {
	value := strings.TrimSpace(c.UIOrigin)
	if value == "" {
		return nil, fmt.Errorf("ui_origin is required")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("must be an absolute http or https origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("must not include credentials, path, query, or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}

func (c Config) ChallengesDir() string { return filepath.Join(c.DataDir, "challenges") }
