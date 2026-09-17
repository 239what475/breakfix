package httpapi

import (
	"context"
	"time"
)

// BackgroundServiceStatus mirrors the bootstrap's in-memory service registry:
// one entry per started Server background service with its last observed pass.
// It is process-local by design and never survives a restart.
type BackgroundServiceStatus struct {
	Name       string
	StartedAt  time.Time
	LastTickAt *time.Time
	LastError  string
}

// SystemDocumentationReport summarizes the pinned documentation deployment.
type SystemDocumentationReport struct {
	SourceID   string
	Repository string
	Revision   string
	Version    string
	Language   string
	PagePath   string
	Anchor     string
}

// SystemReport carries the bootstrap-owned half of the admin system status.
// The handler adds the live catalog integrity verdict.
type SystemReport struct {
	Version                 string
	Commit                  string
	BuildTime               string
	CatalogReleaseReference string
	Documentation           *SystemDocumentationReport
	Services                []BackgroundServiceStatus
}

// SystemReportProvider assembles the process-scoped report. Bootstrap owns
// the build metadata and the service registry; HTTP never sees either directly.
type SystemReportProvider func(context.Context) (SystemReport, error)
