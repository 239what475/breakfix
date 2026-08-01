package oci

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Credentials are the Registry V2 basic-auth credentials held only by
// platform control-plane processes. They never describe an image pull secret.
type Credentials struct {
	Username string
	Password string
}

func (c Credentials) Validate() error {
	if (strings.TrimSpace(c.Username) == "") != (strings.TrimSpace(c.Password) == "") {
		return fmt.Errorf("registry username and password must be set together")
	}
	return nil
}

func (c Credentials) apply(request *http.Request) {
	if strings.TrimSpace(c.Username) != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
}

// ClientOptions configures one HTTPS-only Registry client. Endpoint is the
// Registry authority used for control-plane HTTP calls. It is separate from
// the authority embedded in an OCI image reference so Kubernetes nodes can
// pull through their own reachable endpoint while in-cluster clients use the
// Registry Service. TrustBundleFile is optional: public CAs use the normal
// system trust store, while an internal Registry can append an operator-
// provided CA bundle without disabling TLS verification.
type ClientOptions struct {
	Endpoint        string
	Credentials     Credentials
	TrustBundleFile string
}

type Client struct {
	Credentials        Credentials
	endpoint           string
	rootCAs            *x509.CertPool
	httpClientOverride *http.Client
}

// NewClient constructs an HTTPS-only Registry client. It always verifies the
// server certificate through the system trust store, optionally augmented by
// an operator-provided internal CA bundle.
func NewClient(options ClientOptions) (Client, error) {
	if err := options.Credentials.Validate(); err != nil {
		return Client{}, err
	}
	endpoint, err := registryEndpoint(options.Endpoint)
	if err != nil {
		return Client{}, err
	}
	var roots *x509.CertPool
	if path := strings.TrimSpace(options.TrustBundleFile); path != "" {
		bundle, err := os.ReadFile(path)
		if err != nil {
			return Client{}, fmt.Errorf("read registry trust bundle: %w", err)
		}
		roots, err = x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(bundle) {
			return Client{}, fmt.Errorf("registry trust bundle contains no certificates")
		}
	}
	return Client{Credentials: options.Credentials, endpoint: endpoint, rootCAs: roots}, nil
}

// Ping verifies Registry V2 availability and the configured credentials.
func (c Client) Ping(ctx context.Context) error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registryURL("/v2/"), nil)
	if err != nil {
		return err
	}
	c.Credentials.apply(request)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("ping registry: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ping registry: status %d", response.StatusCode)
	}
	return nil
}

// DeleteImage removes an image by digest. Registry V2 does not reliably
// support deleting a tag directly, so resolve the tag's manifest first.
func (c Client) DeleteImage(ctx context.Context, imageName string) error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	_, repository, reference, err := imageReference(imageName)
	if err != nil {
		return err
	}
	manifestURL := c.registryURL("/v2/" + repositoryPath(repository) + "/manifests/" + url.PathEscape(reference))
	client := c.httpClient()

	head, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest request: %w", err)
	}
	head.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	c.Credentials.apply(head)
	response, err := client.Do(head)
	if err != nil {
		return fmt.Errorf("resolve image manifest: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("resolve image manifest: status %d", response.StatusCode)
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return fmt.Errorf("resolve image manifest: registry did not return Docker-Content-Digest")
	}

	remove, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.registryURL("/v2/"+repositoryPath(repository)+"/manifests/"+url.PathEscape(digest)), nil)
	if err != nil {
		return fmt.Errorf("create image delete request: %w", err)
	}
	c.Credentials.apply(remove)
	response, err = client.Do(remove)
	if err != nil {
		return fmt.Errorf("delete image manifest: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete image manifest: status %d", response.StatusCode)
}

func imageReference(imageName string) (registry, repository, reference string, err error) {
	parts := strings.SplitN(strings.TrimSpace(imageName), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	registry, repository = parts[0], parts[1]
	if index := strings.LastIndex(repository, "@"); index >= 0 {
		reference = repository[index+1:]
		repository = repository[:index]
	} else if index := strings.LastIndex(repository, ":"); index > strings.LastIndex(repository, "/") {
		reference = repository[index+1:]
		repository = repository[:index]
	} else {
		reference = "latest"
	}
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(reference) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	return registry, repository, reference, nil
}

func repositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func registryEndpoint(value string) (string, error) {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" || strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, " \t\r\n/@") {
		return "", fmt.Errorf("registry client endpoint must be an HTTPS authority")
	}
	parsed, err := url.Parse("https://" + endpoint)
	if err != nil || parsed.Host == "" || parsed.Host != endpoint || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("registry client endpoint must be an HTTPS authority")
	}
	return parsed.Host, nil
}
