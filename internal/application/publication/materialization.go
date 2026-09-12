// Package publication owns the Server-side tail of content publication.
package publication

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
)

const (
	DefaultMaterializationInterval   = 5 * time.Minute
	DefaultMaterializationStagingAge = 10 * time.Minute
)

type MaterializationStore interface {
	RetainedMaterializationPaths(context.Context) ([]string, error)
}

type MaterializationReconcilerConfig struct {
	ScenariosDir  string
	Interval      time.Duration
	StagingMaxAge time.Duration
}

// MaterializationReconciler removes only scenario directories that have no
// durable publication reference. PostgreSQL remains the sole retention
// authority; the filesystem never becomes a second publication index.
type MaterializationReconciler struct {
	scenariosDir  string
	interval      time.Duration
	stagingMaxAge time.Duration
	store         MaterializationStore
	now           func() time.Time
}

func NewMaterializationReconciler(store MaterializationStore, config MaterializationReconcilerConfig) (*MaterializationReconciler, error) {
	if store == nil {
		return nil, errors.New("materialization store is required")
	}
	root := strings.TrimSpace(config.ScenariosDir)
	if root == "" {
		return nil, errors.New("scenarios directory is required")
	}
	if config.Interval <= 0 {
		config.Interval = DefaultMaterializationInterval
	}
	if config.StagingMaxAge <= 0 {
		config.StagingMaxAge = DefaultMaterializationStagingAge
	}
	return &MaterializationReconciler{
		scenariosDir:  filepath.Clean(root),
		interval:      config.Interval,
		stagingMaxAge: config.StagingMaxAge,
		store:         store,
		now:           func() time.Time { return time.Now().UTC() },
	}, nil
}

// Recover performs the startup pass before publication loops begin. No
// materializer can still own a staging directory from the previous process,
// so every leftover staging directory is reclaimable immediately.
func (r *MaterializationReconciler) Recover(ctx context.Context) error {
	return r.reconcile(ctx, true)
}

// Run performs low-frequency convergence after startup. Deletion failures are
// retried from a newly derived PostgreSQL retention set on the next pass.
func (r *MaterializationReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.reconcile(ctx, false); err != nil && ctx.Err() == nil {
				slog.Warn("reconcile scenario materializations", "err", err)
			}
		}
	}
}

func (r *MaterializationReconciler) reconcile(ctx context.Context, removeAllStaging bool) error {
	paths, err := r.store.RetainedMaterializationPaths(ctx)
	if err != nil {
		return fmt.Errorf("derive retained scenario materializations: %w", err)
	}
	retained := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
		parts := strings.Split(value, "/")
		if len(parts) != 2 || scenario.ValidateMaterializedPath(value, parts[0], parts[1]) != nil {
			return fmt.Errorf("database returned invalid materialized scenario path %q", value)
		}
		retained[value] = struct{}{}
	}

	entries, err := os.ReadDir(r.scenariosDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read scenarios directory: %w", err)
	}
	var cleanupErrors []error
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			if removeAllStaging || r.stagingExpired(entry) {
				if err := os.RemoveAll(filepath.Join(r.scenariosDir, entry.Name())); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("remove staging materialization %q: %w", entry.Name(), err))
				}
			}
			continue
		}
		if !entry.IsDir() || !scenario.ValidSourceSlug(entry.Name()) {
			continue
		}
		parent := filepath.Join(r.scenariosDir, entry.Name())
		revisions, readErr := os.ReadDir(parent)
		if readErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("read materialization parent %q: %w", entry.Name(), readErr))
			continue
		}
		for _, revision := range revisions {
			if !revision.IsDir() || !scenario.ValidRevisionID(revision.Name()) {
				continue
			}
			relative := filepath.ToSlash(filepath.Join(entry.Name(), revision.Name()))
			if _, keep := retained[relative]; keep {
				continue
			}
			if err := os.RemoveAll(filepath.Join(parent, revision.Name())); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove orphaned materialization %q: %w", relative, err))
			}
		}
		if err := os.Remove(parent); err != nil && !errors.Is(err, os.ErrNotExist) && !isDirectoryNotEmpty(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove empty materialization parent %q: %w", entry.Name(), err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (r *MaterializationReconciler) stagingExpired(entry os.DirEntry) bool {
	info, err := entry.Info()
	if err != nil {
		return false
	}
	return !info.ModTime().After(r.now().Add(-r.stagingMaxAge))
}

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY)
}
