package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/catalogseed"
	"github.com/breakfix/breakfix/internal/incusprovider"
)

func main() {
	challengeDir := flag.String("challenge-dir", "data/challenges/cleanup-logs", "published Node challenge directory")
	tlsDir := flag.String("tls-dir", ".local/incus", "directory containing role-specific Incus TLS identities")
	endpoint := flag.String("endpoint", "", "Incus HTTPS endpoint")
	serverCertificate := flag.String("server-certificate", "", "trusted Incus server certificate")
	storagePool := flag.String("storage-pool", "local", "Incus storage pool")
	buildProject := flag.String("build-project", "breakfix-build", "Incus Node build project")
	imageProject := flag.String("image-project", "breakfix-images", "Incus Node image project")
	baseImageAlias := flag.String("base-image-alias", "node-systemd-base-v1", "trusted Node base image alias")
	baseImageFingerprint := flag.String("base-image-fingerprint", "", "trusted Node base image fingerprint")
	namePrefix := flag.String("name-prefix", "bf", "Incus resource name prefix")
	maxNodes := flag.Int("max-nodes", 4, "maximum NodeEnvironment node count")
	nodeCPU := flag.String("node-cpu", "1", "fixed NodeEnvironment CPU limit")
	nodeMemory := flag.String("node-memory", "512MiB", "fixed NodeEnvironment memory limit")
	nodeProcesses := flag.Int64("node-processes", 512, "fixed NodeEnvironment process limit")
	nodeRootDisk := flag.String("node-root-disk", "5GiB", "fixed NodeEnvironment root disk limit")
	nodeNetworkPool := flag.String("node-network-pool", "10.240.0.0/16", "dedicated NodeEnvironment IPv4 pool")
	nodeNetworkPrefix := flag.Int("node-network-prefix", 24, "NodeEnvironment bridge IPv4 prefix length")
	blockedEgressCIDRs := flag.String("blocked-egress-cidrs", "10.0.0.0/8,100.64.0.0/10,169.254.0.0/16,172.16.0.0/12,192.168.0.0/16", "comma-separated NodeEnvironment blocked egress CIDRs")
	replaceExistingImage := flag.Bool("replace-existing-image", false, "replace the manifest's current formal image when it differs")
	timeout := flag.Duration("timeout", 20*time.Minute, "maximum seed duration")
	flag.Parse()

	if *timeout <= 0 {
		fatal(fmt.Errorf("timeout must be positive"))
	}
	cfg := incusprovider.Config{
		Endpoint:               *endpoint,
		StoragePool:            *storagePool,
		NetworkDriver:          "bridge",
		BuildProject:           *buildProject,
		ImageProject:           *imageProject,
		BaseImageAlias:         *baseImageAlias,
		BaseImageFingerprint:   *baseImageFingerprint,
		NamePrefix:             *namePrefix,
		MaxNodesPerEnvironment: *maxNodes,
		NodeCPU:                *nodeCPU,
		NodeMemory:             *nodeMemory,
		NodeProcesses:          *nodeProcesses,
		NodeRootDisk:           *nodeRootDisk,
		NodeNetworkPool:        *nodeNetworkPool,
		NodeNetworkPrefix:      *nodeNetworkPrefix,
		BlockedEgressCIDRs:     splitCIDRs(*blockedEgressCIDRs),
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	provider, err := connectRole(ctx, cfg, *serverCertificate, *tlsDir, incusprovider.RoleGenerate)
	if err != nil {
		fatal(err)
	}
	defer provider.Close()
	if _, err := provider.Preflight(ctx, incusprovider.RoleGenerate); err != nil {
		fatal(fmt.Errorf("preflight generate Incus identity: %w", err))
	}

	result, err := catalogseed.PublishNode(ctx, &seedProvider{client: provider}, catalogseed.NodeOptions{
		ChallengeDir:         *challengeDir,
		ReplaceExistingImage: *replaceExistingImage,
	})
	if err != nil {
		fatal(err)
	}
	if result.Reused {
		fmt.Printf("Catalog Node image already matches %s: %s\n", result.Revision, result.Fingerprint)
		return
	}
	fmt.Printf("Published catalog Node image for %s: %s\n", result.ChallengeID, result.Fingerprint)
}

func connectRole(ctx context.Context, cfg incusprovider.Config, serverCertificate, tlsDir string, role incusprovider.Role) (*incusprovider.Client, error) {
	roleDir := filepath.Join(tlsDir, string(role))
	cfg.TLS = incusprovider.TLSConfig{
		ServerCertificateFile: serverCertificate,
		ClientCertificateFile: filepath.Join(roleDir, "client.crt"),
		ClientKeyFile:         filepath.Join(roleDir, "client.key"),
	}
	client, err := incusprovider.Connect(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect %s Incus identity: %w", role, err)
	}
	return client, nil
}

func splitCIDRs(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

type seedProvider struct {
	client *incusprovider.Client
}

func (p *seedProvider) BuildNodeImage(ctx context.Context, request incusprovider.BuildNodeImageRequest) (incusprovider.BuildNodeImageResult, error) {
	return p.client.BuildNodeImage(ctx, request)
}

func (p *seedProvider) PublishNodeImage(ctx context.Context, request incusprovider.PublishNodeImageRequest) (incusprovider.PublishNodeImageResult, error) {
	return p.client.PublishNodeImage(ctx, request)
}

func (p *seedProvider) PublishChallengeNodeImage(ctx context.Context, request incusprovider.PublishChallengeNodeImageRequest) (incusprovider.PublishNodeImageResult, error) {
	return p.client.PublishChallengeNodeImage(ctx, request)
}

func (p *seedProvider) FindChallengeNodeImage(ctx context.Context, challengeID, revision string) (incusprovider.PublishNodeImageResult, bool, error) {
	return p.client.FindChallengeNodeImage(ctx, challengeID, revision)
}

func (p *seedProvider) DeleteCandidateNodeImage(ctx context.Context, candidateID, fingerprint string) error {
	return p.client.DeleteCandidateNodeImage(ctx, candidateID, fingerprint)
}

func (p *seedProvider) DeleteBuildNodeImage(ctx context.Context, build incusprovider.BuildNodeImageResult) error {
	return p.client.DeleteBuildNodeImage(ctx, build)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "catalog seed failed:", err)
	os.Exit(1)
}
