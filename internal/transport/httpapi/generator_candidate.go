package httpapi

import (
	"github.com/breakfix/breakfix/internal/adapter/incus"
	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/bootstrap/runtimesnapshot"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func candidateExecutionSnapshot(entry challenge.Entry, runtime config.RuntimeConfig, incus incus.Config) (generation.ExecutionSnapshot, error) {
	return appexecution.Freeze(entry, runtimesnapshot.From(runtime, incus))
}
