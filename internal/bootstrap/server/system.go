package server

import (
	"context"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/buildinfo"
	"github.com/breakfix/breakfix/internal/transport/httpapi"
)

// systemReportProvider assembles the admin system report from state only the
// bootstrap owns: build metadata, the deployment configuration, and the
// background service registry. Catalog integrity stays in the HTTP layer so
// the check runs against the request context.
type systemReportProvider struct {
	cfg           config.Config
	registry      *serviceRegistry
	documentation *docsource.Library
}

func newSystemReportProvider(cfg config.Config, registry *serviceRegistry, documentation *docsource.Library) *systemReportProvider {
	return &systemReportProvider{cfg: cfg, registry: registry, documentation: documentation}
}

func (p *systemReportProvider) Report(context.Context) (httpapi.SystemReport, error) {
	statuses := p.registry.snapshot()
	services := make([]httpapi.BackgroundServiceStatus, 0, len(statuses))
	for _, status := range statuses {
		services = append(services, httpapi.BackgroundServiceStatus{
			Name: status.Name, StartedAt: status.StartedAt, LastTickAt: status.LastTickAt, LastError: status.LastError,
		})
	}
	report := httpapi.SystemReport{
		Version:                 buildinfo.Version,
		Commit:                  buildinfo.Commit,
		BuildTime:               buildinfo.BuildTime,
		CatalogReleaseReference: p.cfg.Catalog.ReleaseReference,
		Services:                services,
	}
	if p.documentation != nil {
		documentation := p.cfg.Documentation
		parserVersion, upstreamCommit := p.documentation.Identity()
		report.Documentation = &httpapi.SystemDocumentationReport{
			SourceID:       documentation.SourceID,
			Repository:     documentation.Repository,
			Revision:       documentation.Revision,
			Version:        documentation.Version,
			Language:       documentation.Language,
			PagePath:       documentation.PagePath,
			Anchor:         documentation.Anchor,
			ParserVersion:  parserVersion,
			UpstreamCommit: upstreamCommit,
		}
	}
	return report, nil
}
