// Package operations owns Operations-specific translation into the public
// runnable contract. It is the only place where the historical scenario file
// layout and observation/repair terminology are translated to generic phases,
// actions, and assertions.
package operations

import (
	"fmt"
	"maps"
	"strings"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const operationsContentKind = "operations"

type RuntimeProfileConfig struct {
	ProfileRevision  string
	BaseImage        string
	SoftwareVersions map[string]string
	Resources        runnable.ResourceLimits
	Network          runnable.NetworkScope
	MaxActionTimeout int64
}

type Config struct {
	MaxNodes  int
	Node      RuntimeProfileConfig
	K8s       RuntimeProfileConfig
	Lifecycle runnable.LifecyclePolicy
}

// Input identifies an existing immutable Operations revision and its exact
// archive. Archive bytes are already frozen by the content workflow; this
// adapter never reads a provider or mutable content directory.
type Input struct {
	ContentID       string
	ContentRevision string
	Entry           scenario.Entry
	Source          runnable.SourceArchive
}

func Compile(input Input, config Config) (runnable.RunnableSpec, error) {
	if err := scenario.RequireOperationsScenario(&input.Entry); err != nil {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations revision: %w", err)
	}
	if strings.TrimSpace(input.ContentID) == "" || strings.TrimSpace(input.ContentRevision) == "" {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations revision: immutable content identity is required")
	}
	if err := input.Source.Validate(); err != nil {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations revision source: %w", err)
	}
	if config.MaxNodes <= 0 || config.MaxNodes > scenario.MaxScenarioNodes {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations revision: max nodes is outside scenario platform limits")
	}

	profileConfig, runtime, err := profileFor(input.Entry.Runtime, config)
	if err != nil {
		return runnable.RunnableSpec{}, err
	}
	if runtime == runnable.RuntimeNode && len(input.Entry.Nodes) > config.MaxNodes {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations revision: node count %d exceeds platform limit %d", len(input.Entry.Nodes), config.MaxNodes)
	}
	profile, locations, err := compileProfile(runtime, input.Entry, profileConfig)
	if err != nil {
		return runnable.RunnableSpec{}, err
	}
	initialization, err := compileInitialization(input.Entry, locations)
	if err != nil {
		return runnable.RunnableSpec{}, err
	}
	plan, err := compileValidationPlan(input.Entry, locations)
	if err != nil {
		return runnable.RunnableSpec{}, err
	}

	spec := runnable.RunnableSpec{
		FormatVersion: runnable.FormatVersion,
		Identity: runnable.ContentIdentity{
			Kind: operationsContentKind, ID: input.ContentID, Revision: input.ContentRevision,
		},
		RuntimeProfile:  profile,
		Source:          input.Source,
		Initialization:  initialization,
		ValidationPlan:  plan,
		LifecyclePolicy: config.Lifecycle,
	}
	if err := spec.Validate(); err != nil {
		return runnable.RunnableSpec{}, fmt.Errorf("compile operations runnable spec: %w", err)
	}
	return spec, nil
}

func profileFor(runtime string, config Config) (RuntimeProfileConfig, runnable.Runtime, error) {
	switch runtime {
	case scenario.RuntimeNode:
		return config.Node, runnable.RuntimeNode, nil
	case scenario.RuntimeK8s:
		return config.K8s, runnable.RuntimeK8s, nil
	default:
		return RuntimeProfileConfig{}, "", fmt.Errorf("compile operations revision: unsupported runtime %q", runtime)
	}
}

type location struct {
	target        runnable.TargetLocation
	writeBoundary string
	readBoundary  string
}

func compileProfile(runtime runnable.Runtime, entry scenario.Entry, config RuntimeProfileConfig) (runnable.RuntimeProfile, map[string]location, error) {
	versions := maps.Clone(config.SoftwareVersions)
	if versions == nil {
		versions = make(map[string]string, len(entry.Versions))
	}
	for _, version := range entry.Versions {
		component := strings.ToLower(strings.TrimSpace(version.Component))
		if err := runnableID(component); err != nil {
			return runnable.RuntimeProfile{}, nil, fmt.Errorf("compile operations version %q: %w", version.Component, err)
		}
		if current, exists := versions[component]; exists && current != version.Version {
			return runnable.RuntimeProfile{}, nil, fmt.Errorf("compile operations version %q conflicts with runtime profile", version.Component)
		}
		versions[component] = version.Version
	}

	locations := make(map[string]location)
	boundaries := make([]runnable.ExecutionBoundary, 0, len(entry.Nodes)*2)
	if runtime == runnable.RuntimeNode {
		for _, node := range entry.Nodes {
			item := locationFor("node", node.Name)
			locations[node.Name] = item
			boundaries = append(boundaries,
				runnable.ExecutionBoundary{ID: item.writeBoundary, Target: item.target, Permission: runnable.PermissionReadWrite, Network: config.Network, MaxTimeout: config.MaxActionTimeout},
				runnable.ExecutionBoundary{ID: item.readBoundary, Target: item.target, Permission: runnable.PermissionReadOnly, Network: config.Network, MaxTimeout: config.MaxActionTimeout},
			)
		}
	} else {
		item := locationFor("management", "cluster")
		locations[""] = item
		boundaries = append(boundaries,
			runnable.ExecutionBoundary{ID: item.writeBoundary, Target: item.target, Permission: runnable.PermissionReadWrite, Network: config.Network, MaxTimeout: config.MaxActionTimeout},
			runnable.ExecutionBoundary{ID: item.readBoundary, Target: item.target, Permission: runnable.PermissionReadOnly, Network: config.Network, MaxTimeout: config.MaxActionTimeout},
		)
	}
	profile := runnable.RuntimeProfile{
		Runtime: runtime, ProfileRevision: config.ProfileRevision, BaseImage: config.BaseImage,
		SoftwareVersions: versions, Resources: config.Resources, Network: config.Network,
		Topology: entry.Topology, ExecutionBoundaries: boundaries,
	}
	if err := profile.Validate(); err != nil {
		return runnable.RuntimeProfile{}, nil, fmt.Errorf("compile operations runtime profile: %w", err)
	}
	return profile, locations, nil
}

func locationFor(kind, id string) location {
	prefix := kind + "-" + id
	return location{
		target:        runnable.TargetLocation{Kind: kind, ID: id},
		writeBoundary: prefix + "-write",
		readBoundary:  prefix + "-read",
	}
}

func compileInitialization(entry scenario.Entry, locations map[string]location) ([]runnable.ActionSpec, error) {
	if entry.Runtime == scenario.RuntimeK8s {
		item := locations[""]
		return []runnable.ActionSpec{action("initialize", "k8s/generate.sh", item)}, nil
	}
	result := make([]runnable.ActionSpec, 0, len(entry.Nodes))
	for _, node := range entry.Nodes {
		item, exists := locations[node.Name]
		if !exists {
			return nil, fmt.Errorf("compile operations initialization: node %q has no execution location", node.Name)
		}
		result = append(result, action("initialize-"+node.Name, "nodes/"+node.Name+"/generate.sh", item))
	}
	return result, nil
}

func compileValidationPlan(entry scenario.Entry, locations map[string]location) (runnable.ValidationPlan, error) {
	initial := runnable.ValidationPhase{ID: "initial-observation", TimeoutSeconds: 1800, Execution: runnable.PhaseSequential}
	for _, evidence := range entry.Reproduction.Evidence {
		item, err := locationForScenario(entry.Runtime, evidence.Node, locations)
		if err != nil {
			return runnable.ValidationPlan{}, fmt.Errorf("compile operations observation %q: %w", evidence.ID, err)
		}
		initial.Assertions = append(initial.Assertions, assertion(evidence.ID, reproductionEntrypoint(entry.Runtime, evidence.Node), item))
	}
	plan := runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{initial}}
	if !entry.HasReferenceRepair {
		return plan, nil
	}
	final := runnable.ValidationPhase{ID: "final-observation", TimeoutSeconds: 1800, Execution: runnable.PhaseSequential}
	if entry.Runtime == scenario.RuntimeK8s {
		item := locations[""]
		final.Actions = append(final.Actions, action("apply-change", "k8s/answer.sh", item))
	} else {
		for _, node := range entry.Nodes {
			final.Actions = append(final.Actions, action("apply-change-"+node.Name, "nodes/"+node.Name+"/answer.sh", locations[node.Name]))
		}
	}
	for _, checkpoint := range entry.Checkpoints {
		item, err := locationForScenario(entry.Runtime, checkpoint.Node, locations)
		if err != nil {
			return runnable.ValidationPlan{}, fmt.Errorf("compile operations conclusion %q: %w", checkpoint.ID, err)
		}
		final.Assertions = append(final.Assertions, assertion(checkpoint.ID, checksEntrypoint(entry.Runtime, checkpoint.Node), item))
	}
	plan.Phases = append(plan.Phases, final)
	return plan, nil
}

func locationForScenario(runtime, node string, locations map[string]location) (location, error) {
	if runtime == scenario.RuntimeK8s {
		item, exists := locations[""]
		if !exists {
			return location{}, fmt.Errorf("management execution location is absent")
		}
		return item, nil
	}
	item, exists := locations[node]
	if !exists {
		return location{}, fmt.Errorf("node %q has no execution location", node)
	}
	return item, nil
}

func action(id, entrypoint string, item location) runnable.ActionSpec {
	return runnable.ActionSpec{ID: id, Entrypoint: entrypoint, Target: item.target, BoundaryID: item.writeBoundary, TimeoutSeconds: 900, ExpectedExitCodes: []int{0}}
}

func assertion(id, entrypoint string, item location) runnable.AssertionSpec {
	return runnable.AssertionSpec{ID: id, Entrypoint: entrypoint, Target: item.target, BoundaryID: item.readBoundary, TimeoutSeconds: 900}
}

func reproductionEntrypoint(runtime, node string) string {
	if runtime == scenario.RuntimeK8s {
		return "k8s/reproduce.sh"
	}
	return "nodes/" + node + "/reproduce.sh"
}

func checksEntrypoint(runtime, node string) string {
	if runtime == scenario.RuntimeK8s {
		return "k8s/checks.sh"
	}
	return "nodes/" + node + "/checks.sh"
}

func runnableID(value string) error {
	if value == "" || len(value) > runnable.MaxIDLength {
		return fmt.Errorf("must be a stable lowercase identifier")
	}
	for index, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' || (index == 0 && (character < 'a' || character > 'z')) {
			return fmt.Errorf("must be a stable lowercase identifier")
		}
	}
	return nil
}
