package incus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewReconnectableClientRejectsDisabledConfig(t *testing.T) {
	if _, err := NewReconnectableClient(Config{}, RoleRuntime); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewReconnectableClient(disabled) = %v, want invalid", err)
	}
	// A nil client is the disabled form: every operation must fail with the
	// explicit "not configured" classification instead of panicking.
	var client *ReconnectableClient
	client.Close()
	if _, err := client.Preflight(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled Preflight() = %v, want unavailable", err)
	}
	if _, err := client.BuildNodeImage(context.Background(), BuildNodeImageRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled BuildNodeImage() = %v, want unavailable", err)
	}
}

func TestReconnectableClientKeepsDeterministicIdentityOffline(t *testing.T) {
	client, err := NewReconnectableClient(offlineReconnectConfig(t), RoleController)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	identity, err := client.NodeEnvironmentIdentity("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", []string{"client", "app"})
	if err != nil {
		t.Fatalf("derive identity while offline: %v", err)
	}
	if identity.Project == "" || len(identity.Nodes) != 2 || identity.Nodes[0].InstanceName == "" {
		t.Fatalf("offline identity = %#v", identity)
	}
}

func TestReconnectableClientClassifiesOfflineProviderAsRetryable(t *testing.T) {
	client, err := NewReconnectableClient(offlineReconnectConfig(t), RoleController)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.ProvisionNodeEnvironment(ctx, ProvisionNodeEnvironmentRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("offline provider error = %v, want ErrUnavailable", err)
	}
}

func TestReconnectableClientPreflightClassifiesOfflineProviderAsRetryable(t *testing.T) {
	client, err := NewReconnectableClient(offlineReconnectConfig(t), RoleController)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Preflight(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("offline preflight error = %v, want ErrUnavailable", err)
	}
}

func offlineReconnectConfig(t *testing.T) Config {
	t.Helper()
	certificate, key := reconnectTestCertificate(t)
	root := t.TempDir()
	serverCertPath := filepath.Join(root, "server.crt")
	clientCertPath := filepath.Join(root, "client.crt")
	clientKeyPath := filepath.Join(root, "client.key")
	for path, content := range map[string][]byte{
		serverCertPath: certificate,
		clientCertPath: certificate,
		clientKeyPath:  key,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Config{
		Endpoint: "https://127.0.0.1:1",
		TLS: TLSConfig{
			ServerCertificateFile: serverCertPath,
			ClientCertificateFile: clientCertPath,
			ClientKeyFile:         clientKeyPath,
		},
		StoragePool: "local", NetworkDriver: "bridge", BuildProject: "breakfix-build", ImageProject: "breakfix-images",
		BaseImageAlias: "node-systemd-base-v1", BaseImageFingerprint: strings.Repeat("a", 64),
		NamePrefix: "bf", MaxNodesPerEnvironment: 4, NodeCPU: "1", NodeMemory: "512MiB", NodeProcesses: 512, NodeRootDisk: "5GiB",
		NodeNetworkPool: "10.240.0.0/16", NodeNetworkPrefix: 24,
		BlockedEgressCIDRs: []string{"10.0.0.0/8"},
	}
}

func reconnectTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "breakfix reconnect test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Minute),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
