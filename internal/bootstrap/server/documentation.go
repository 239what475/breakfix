package server

import (
	"errors"
	"fmt"
	"strings"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	"github.com/breakfix/breakfix/internal/adapter/llm"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const (
	documentationActionTimeoutSeconds int64 = 900
	documentationCreateTimeoutSeconds int64 = 1800
	documentationResetTimeoutSeconds  int64 = 900
	documentationStopTimeoutSeconds   int64 = 300
	documentationReapTimeoutSeconds   int64 = 300
	documentationIdleTTLSeconds       int64 = 900
	documentationMaxLifetimeSeconds   int64 = 1800
)

// documentationProfiles is deliberately K8s-only for the first fixed Pod
// lifecycle practice. It exposes no caller-selected runtime or boundary.
type documentationProfiles struct {
	constraint domain.RuntimeConstraint
	profile    runnable.RuntimeProfile
	lifecycle  runnable.LifecyclePolicy
}

func newDocumentationProfiles(cfg config.Config) (documentationProfiles, error) {
	if err := cfg.Runtime.Validate(); err != nil {
		return documentationProfiles{}, err
	}
	memory, err := quantityValue(cfg.Runtime.K8s.Resources.WorkloadMemory)
	if err != nil {
		return documentationProfiles{}, fmt.Errorf("parse documentation workload memory: %w", err)
	}
	disk, err := quantityValue(cfg.Runtime.K8s.Resources.WorkloadEphemeralStorage)
	if err != nil {
		return documentationProfiles{}, fmt.Errorf("parse documentation workload storage: %w", err)
	}
	resources := runnable.ResourceLimits{
		CPU: cfg.Runtime.K8s.Resources.WorkloadCPU, MemoryBytes: memory, EphemeralBytes: disk,
		MaxProcesses: 256, MaxConcurrentTasks: 1,
	}
	constraint := domain.RuntimeConstraint{
		Runtime: runnable.RuntimeK8s, BaseImage: cfg.Runtime.K8s.BaseImageDigest, Resources: resources,
		Network: runnable.NetworkIsolated, Topology: "single-kubernetes-cluster",
	}
	target := runnable.TargetLocation{Kind: "management", ID: "cluster"}
	profile := runnable.RuntimeProfile{
		Runtime: runnable.RuntimeK8s, ProfileRevision: cfg.Runtime.K8s.ProfileRevision,
		BaseImage:        cfg.Runtime.K8s.BaseImageDigest,
		SoftwareVersions: map[string]string{"kubernetes": cfg.Runtime.K8s.Version},
		Resources:        resources, Network: runnable.NetworkIsolated, Topology: constraint.Topology,
		ExecutionBoundaries: []runnable.ExecutionBoundary{
			{ID: "management-write", Target: target, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkIsolated, MaxTimeout: documentationActionTimeoutSeconds},
			{ID: "management-read", Target: target, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkIsolated, MaxTimeout: documentationActionTimeoutSeconds},
		},
	}
	lifecycle := runnable.LifecyclePolicy{
		CreateTimeoutSeconds: documentationCreateTimeoutSeconds,
		ResetTimeoutSeconds:  documentationResetTimeoutSeconds,
		StopTimeoutSeconds:   documentationStopTimeoutSeconds,
		ReapTimeoutSeconds:   documentationReapTimeoutSeconds,
		IdleTTLSeconds:       documentationIdleTTLSeconds,
		MaxLifetimeSeconds:   documentationMaxLifetimeSeconds,
	}
	if err := constraint.Validate(); err != nil {
		return documentationProfiles{}, err
	}
	if err := profile.Validate(); err != nil {
		return documentationProfiles{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return documentationProfiles{}, err
	}
	return documentationProfiles{constraint: constraint, profile: profile, lifecycle: lifecycle}, nil
}

func (p documentationProfiles) DocumentationRuntimeConstraints() []domain.RuntimeConstraint {
	return []domain.RuntimeConstraint{p.constraint}
}

func (p documentationProfiles) DocumentationLifecyclePolicy() runnable.LifecyclePolicy {
	return p.lifecycle
}

func (p documentationProfiles) ResolveDocumentationRuntimeProfile(constraint domain.RuntimeConstraint) (runnable.RuntimeProfile, error) {
	if constraint != p.constraint {
		return runnable.RuntimeProfile{}, errors.New("documentation runtime constraint is not allowed by Server policy")
	}
	return p.profile, nil
}

// documentationLibraryIdentity is the opened library's self-described parser
// identity surfaced in the admin system report. It is empty on the legacy
// rendered-snapshot path.
type documentationLibraryIdentity struct {
	parserVersion  string
	upstreamCommit string
}

func newDocumentationPipeline(cfg config.Config, database *postgres.Store) (*app.AgentPipeline, documentationLibraryIdentity, error) {
	if database == nil || !cfg.Documentation.Enabled() {
		return nil, documentationLibraryIdentity{}, nil
	}
	identity := documentationLibraryIdentity{}
	context := domain.DocumentContext{
		FormatVersion: domain.FormatVersion, SourceID: cfg.Documentation.SourceID, Repository: cfg.Documentation.Repository,
		Commit: cfg.Documentation.Revision, Version: cfg.Documentation.Version, Language: cfg.Documentation.Language,
		License: cfg.Documentation.License, PagePath: cfg.Documentation.PagePath,
		Anchor: cfg.Documentation.Anchor,
	}
	library, err := docsource.NewPinnedLibrary(context, cfg.Documentation.LibraryRoot)
	if err != nil {
		return nil, identity, fmt.Errorf("load pinned documentation library: %w", err)
	}
	identity.parserVersion, identity.upstreamCommit = library.Identity()
	reader := app.Reader(library)
	profiles, err := newDocumentationProfiles(cfg)
	if err != nil {
		return nil, identity, err
	}
	evidence, err := llm.NewDocumentReviewer(cfg.Agent, "evidence")
	if err != nil {
		return nil, identity, err
	}
	value, err := llm.NewDocumentReviewer(cfg.Agent, "value")
	if err != nil {
		return nil, identity, err
	}
	safety, err := llm.NewDocumentReviewer(cfg.Agent, "safety")
	if err != nil {
		return nil, identity, err
	}
	consistency, err := llm.NewDocumentReviewer(cfg.Agent, "consistency")
	if err != nil {
		return nil, identity, err
	}
	verification, err := llm.NewDocumentReviewer(cfg.Agent, "verification")
	if err != nil {
		return nil, identity, err
	}
	service, err := app.NewService(database.DocumentPractice, database.Runnable)
	if err != nil {
		return nil, identity, err
	}
	pipeline, err := app.NewAgentPipeline(
		service, reader, llm.NewDocumentPlanner(cfg.Agent),
		[]app.PlanReviewRole{evidence, value}, llm.NewDocumentGenerator(cfg.Agent),
		[]app.CandidateReviewRole{safety, consistency}, []app.VerificationReviewRole{verification}, profiles,
		app.AgentPipelineConfig{Model: strings.TrimSpace(cfg.Agent.Model), PromptVersion: "document-prompt-v2", ToolVersion: "document-tools-v2", PolicyVersion: "document-policy-v2"},
	)
	if err != nil {
		return nil, identity, err
	}
	return pipeline, identity, nil
}
