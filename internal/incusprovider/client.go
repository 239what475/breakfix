package incusprovider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type contextServer interface {
	WithContext(context.Context) incus.InstanceServer
}

type Client struct {
	config Config
	base   incus.InstanceServer
}

func Connect(ctx context.Context, config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	serverCertificate, err := readCredential(config.TLS.ServerCertificateFile, "server certificate")
	if err != nil {
		return nil, err
	}
	clientCertificate, err := readCredential(config.TLS.ClientCertificateFile, "client certificate")
	if err != nil {
		return nil, err
	}
	clientKey, err := readCredential(config.TLS.ClientKeyFile, "client key")
	if err != nil {
		return nil, err
	}
	base, err := incus.ConnectIncusWithContext(ctx, strings.TrimRight(config.Endpoint, "/"), &incus.ConnectionArgs{
		TLSServerCert:        serverCertificate,
		TLSClientCert:        clientCertificate,
		TLSClientKey:         clientKey,
		IdenticalCertificate: true,
		InsecureSkipVerify:   false,
		SkipGetEvents:        true,
		UserAgent:            "breakfix/incus-provider",
	})
	if err != nil {
		return nil, classify("connect", config.Endpoint, err)
	}
	probe := base.UseProject("default")
	if _, ok := probe.(contextServer); !ok {
		base.Disconnect()
		return nil, fmt.Errorf("%w: Incus client does not support request contexts", ErrInvariant)
	}
	return &Client{config: config, base: base}, nil
}

func (c *Client) Close() {
	if c != nil && c.base != nil {
		c.base.Disconnect()
	}
}

func (c *Client) scoped(ctx context.Context, project string) (incus.InstanceServer, error) {
	if c == nil || c.base == nil || strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: connected Incus client and project are required", ErrInvalid)
	}
	clone := c.base.UseProject(project)
	contextual, ok := clone.(contextServer)
	if !ok {
		return nil, fmt.Errorf("%w: Incus project client does not support request contexts", ErrInvariant)
	}
	return contextual.WithContext(ctx), nil
}

func readCredential(path, label string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Incus %s: %w", label, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%w: Incus %s is empty", ErrInvalid, label)
	}
	return string(data), nil
}

func waitOperation(ctx context.Context, operation, resource string, op incus.Operation) error {
	if op == nil {
		return fmt.Errorf("%w: %s returned no operation for %s", ErrInvariant, operation, resource)
	}
	if err := op.WaitContext(ctx); err != nil {
		if ctx.Err() != nil {
			_ = op.Cancel()
		}
		return classify(operation, resource, err)
	}
	return nil
}

func classify(operation, resource string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s %s: %w", operation, resource, err)
	}
	status, hasStatus := api.StatusErrorMatch(err)
	var kind error
	switch {
	case hasStatus && status == http.StatusNotFound:
		kind = ErrNotFound
	case hasStatus && status == http.StatusConflict:
		kind = ErrConflict
	case hasStatus && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusForbidden || status == http.StatusUnauthorized):
		kind = ErrInvalid
	case hasStatus && (status == http.StatusTooManyRequests || status >= http.StatusInternalServerError):
		kind = ErrUnavailable
	default:
		var networkError net.Error
		if errors.As(err, &networkError) {
			kind = ErrUnavailable
		} else {
			kind = ErrUnavailable
		}
	}
	return fmt.Errorf("%s %s: %w: %v", operation, resource, kind, err)
}
