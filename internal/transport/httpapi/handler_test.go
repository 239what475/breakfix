package httpapi

import (
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

func newHandlerForTest(database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	return NewHandlerWithDependencies(database, client, cfg, Dependencies{TaxonomyStore: taxonomy.NewStore(cfg.DataDir)})
}
