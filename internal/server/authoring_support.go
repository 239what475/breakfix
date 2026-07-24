package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

func (h *Handler) generatorImage() string {
	if h.registryAddr == "" {
		return "breakfix-generator:latest"
	}
	return h.registryAddr + "/breakfix-generator:latest"
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (h *Handler) checkRegistryReady(ctx context.Context) error {
	addr := strings.TrimSpace(h.registryAddr)
	if addr == "" {
		return fmt.Errorf("registry is not configured")
	}
	registry := addr
	if idx := strings.IndexByte(registry, '/'); idx >= 0 {
		registry = registry[:idx]
	}
	url := "http://" + registry + "/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build registry health request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("registry %s is unavailable: %w", registry, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("registry %s health check failed: status %d", registry, resp.StatusCode)
	}
	return nil
}

func (h *Handler) internalServerURL() string {
	host := strings.TrimSpace(h.serverHost)
	if host == "" {
		host = "172.18.0.1"
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", h.port))
	return "http://" + addr
}
