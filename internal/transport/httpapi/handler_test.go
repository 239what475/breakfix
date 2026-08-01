package httpapi

import (
	"context"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/taxonomy"
)

type readyCatalogGate struct{}

func (readyCatalogGate) HasReadyRelease(context.Context) (bool, error) { return true, nil }

func newHandlerForTest(database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	handler, err := NewHandlerWithDependencies(database, client, cfg, Dependencies{
		TaxonomyStore: taxonomy.NewStore(cfg.DataDir), CatalogGate: readyCatalogGate{},
	})
	if err != nil {
		panic(err)
	}
	return handler
}
