// Package taxonomyworker assembles the Taxonomy Worker process.
package taxonomyworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/transport/health"
	"github.com/breakfix/breakfix/internal/worker/taxonomy"
)

// DefaultWorkerID derives a stable local identity when Kubernetes has not
// injected POD_NAME.
func DefaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-taxonomy-worker"
}

// Run owns Taxonomy Worker dependency assembly and lifecycle.
func Run(ctx context.Context, configPath, workerID string) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("taxonomy worker ID is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateTaxonomyWorker(); err != nil {
		return fmt.Errorf("validate Taxonomy Worker configuration: %w", err)
	}
	client, err := internalapi.NewTaxonomyWorkflowClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		return fmt.Errorf("create taxonomy workflow client: %w", err)
	}
	runner, err := taxonomy.New(client, cfg.Agent, taxonomy.Config{WorkerID: workerID})
	if err != nil {
		return fmt.Errorf("create Taxonomy Worker: %w", err)
	}
	return health.Run(ctx, health.Config{Port: cfg.HealthPort, Component: "taxonomy"}, runner.Run)
}
