package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/content/scenario"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListScenarios(c *gin.Context) {
	user := h.getUser(c)

	scenarios, err := h.catalog.ListOperations(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	completed := make(map[string]struct{})
	active := make(map[string]activeEnvironment)
	if user != nil {
		completed, err = h.db.Environment.ListCompletedScenarioIDs(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
		envs, err := h.listActiveEnvironments(c.Request.Context(), user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("list active environments: %v", err)})
			return
		}
		for _, env := range envs {
			if env.Phase == breakfixv1.EnvironmentCompleted {
				// The controller has already established the completion fact. Expose it
				// immediately instead of waiting for the asynchronous SQL projector.
				completed[env.ScenarioRef] = struct{}{}
				continue
			}
			if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining {
				continue
			}
			current, exists := active[env.ScenarioRef]
			if !exists || passedCheckpointCount(env.Checkpoints) > passedCheckpointCount(current.Checkpoints) {
				active[env.ScenarioRef] = env
			}
		}
	}

	summaries := make([]api.ScenarioSummary, 0, len(scenarios))
	for _, published := range scenarios {
		ch := published.Entry
		s := api.ScenarioSummary{
			Id:           ch.ID,
			Title:        ch.Title,
			Runtime:      scenarioSummaryRuntime(ch.Runtime),
			ScenarioTags: append([]string(nil), published.Catalog.Tags...),
			Description:  ch.Description,
		}
		publishedAt := ch.PublishedAt.UTC()
		s.PublishedAt = publishedAt
		if user != nil {
			_, solved := completed[ch.ID]
			solvedVal := solved
			s.Solved = &solvedVal
			activeEnv, isActive := active[ch.ID]
			activeVal := isActive
			s.Active = &activeVal
			if isActive {
				progress := checkpointProgressSummary(activeEnv.Checkpoints, len(ch.Checkpoints))
				s.Progress = &progress
			}
		}
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ScenarioList{Scenarios: summaries})
}

func (h *Handler) catalogEntries(ctx context.Context) (map[string]scenario.Entry, error) {
	published, err := h.catalog.ListOperations(ctx)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]scenario.Entry, len(published))
	for _, item := range published {
		entries[item.Entry.ID] = item.Entry
	}
	return entries, nil
}

func (h *Handler) GetScenarioContent(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	var entry *scenario.Entry
	published, err := h.catalog.FindOperations(c.Request.Context(), id)
	if err == nil {
		entry = &published.Entry
		// Prefer the revision pinned by an existing Environment. If there is
		// no Environment, the public current revision remains readable without
		// requiring a user to start one first.
		if h.k8s != nil {
			fixedEntry, _, environmentErr := h.resolveEnvironmentScenario(c.Request.Context(), user.ID, id, true)
			switch {
			case environmentErr == nil:
				entry = fixedEntry
			case errors.Is(environmentErr, errNoMatchingEnvironment):
			case errors.Is(environmentErr, errAmbiguousEnvironment):
				c.JSON(http.StatusConflict, api.ErrorResponse{Error: scenarioEnvironmentError(environmentErr).Error()})
				return
			default:
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: environmentErr.Error()})
				return
			}
		}
	} else if errors.Is(err, scenario.ErrNotFound) {
		entry, _, err = h.resolveEnvironmentScenario(c.Request.Context(), user.ID, id, true)
		if err != nil {
			if errors.Is(err, errAmbiguousEnvironment) {
				c.JSON(http.StatusConflict, api.ErrorResponse{Error: scenarioEnvironmentError(err).Error()})
				return
			}
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "scenario not found"})
			return
		}
		// A deprecated Scenario has no active Catalog projection. Its durable
		// content remains readable for an existing Environment.
	} else {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	content, err := scenario.ReadContent(entry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	checkpoints := toAPICheckpoints(entry.Checkpoints)
	hints := content.Hints
	c.JSON(http.StatusOK, api.ScenarioContent{
		Id:                    entry.ID,
		Title:                 entry.Title,
		Description:           entry.Description,
		Runtime:               api.ScenarioContentRuntime(entry.Runtime),
		ScenarioTags:          append([]string(nil), entry.Tags...),
		Nodes:                 toAPIScenarioNodes(entry.Nodes),
		Versions:              toAPIScenarioVersions(entry.Versions),
		Topology:              entry.Topology,
		Initialization:        entry.Initialization,
		ReproductionObjective: entry.Reproduction.Objective,
		ReproductionEvidence:  toAPIReproductionEvidence(entry.Reproduction.Evidence),
		Problem:               content.Problem,
		Solution:              content.Solution,
		Hints:                 hints,
		Checkpoints:           checkpoints,
	})
}

func toAPIScenarioVersions(versions []scenario.Version) []api.ScenarioVersion {
	result := make([]api.ScenarioVersion, 0, len(versions))
	for _, version := range versions {
		result = append(result, api.ScenarioVersion{Component: version.Component, Version: version.Version})
	}
	return result
}

func toAPIReproductionEvidence(evidence []scenario.ReproductionEvidence) []api.ScenarioReproductionEvidence {
	result := make([]api.ScenarioReproductionEvidence, 0, len(evidence))
	for _, item := range evidence {
		result = append(result, api.ScenarioReproductionEvidence{Id: item.ID, Description: item.Description, Node: optionalString(item.Node)})
	}
	return result
}

func (h *Handler) GetScenarioProgress(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	_, env, err := h.resolveEnvironmentScenario(c.Request.Context(), user.ID, id, true)
	if err != nil {
		if errors.Is(err, errAmbiguousEnvironment) {
			c.JSON(http.StatusConflict, api.ErrorResponse{Error: scenarioEnvironmentError(err).Error()})
			return
		}
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this scenario"})
		return
	}
	if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining && env.Phase != breakfixv1.EnvironmentCompleted {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "environment is not ready for checkpoint checks"})
		return
	}
	if env.Checkpoints == nil {
		checks := []api.CheckpointResult{}
		c.JSON(http.StatusOK, api.ScenarioProgress{Checks: checks})
		return
	}
	if env.Checkpoints.Error != "" {
		c.JSON(http.StatusUnprocessableEntity, api.ErrorResponse{Error: env.Checkpoints.Error})
		return
	}
	checks := toAPICheckStatusResults(env.Checkpoints.Results)
	c.JSON(http.StatusOK, api.ScenarioProgress{Checks: checks})
}

func passedCheckpointCount(status *breakfixv1.CheckpointStatus) int {
	if status == nil {
		return 0
	}
	passed := 0
	for _, result := range status.Results {
		if result.Passed {
			passed++
		}
	}
	return passed
}

func checkpointProgressSummary(status *breakfixv1.CheckpointStatus, total int) api.CheckpointProgressSummary {
	passed := passedCheckpointCount(status)
	if passed > total {
		passed = total
	}
	return api.CheckpointProgressSummary{Passed: passed, Total: total}
}

func scenarioSummaryRuntime(runtime string) api.ScenarioSummaryRuntime {
	return api.ScenarioSummaryRuntime(scenario.NormalizeRuntime(runtime))
}

func toAPICheckpoints(checkpoints []scenario.Checkpoint) []api.ScenarioCheckpoint {
	result := make([]api.ScenarioCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		var hint *string
		if checkpoint.Hint != "" {
			value := checkpoint.Hint
			hint = &value
		}
		var node *string
		if checkpoint.Node != "" {
			value := checkpoint.Node
			node = &value
		}
		result = append(result, api.ScenarioCheckpoint{
			Id:          checkpoint.ID,
			Title:       checkpoint.Title,
			Description: checkpoint.Description,
			Hint:        hint,
			Node:        node,
		})
	}
	return result
}

func toAPIScenarioNodes(nodes []scenario.Node) []api.ScenarioNode {
	result := make([]api.ScenarioNode, len(nodes))
	for index, node := range nodes {
		result[index] = api.ScenarioNode{Name: node.Name, Title: node.Title}
	}
	return result
}

func toAPICheckStatusResults(checks []breakfixv1.CheckpointResultStatus) []api.CheckpointResult {
	result := make([]api.CheckpointResult, 0, len(checks))
	for _, check := range checks {
		details := check.Details
		var firstPassedAt *time.Time
		if check.FirstPassedAt != nil && !check.FirstPassedAt.IsZero() {
			value := check.FirstPassedAt.UTC()
			firstPassedAt = &value
		}
		result = append(result, api.CheckpointResult{
			Id:            check.ID,
			Passed:        check.Passed,
			FirstPassedAt: firstPassedAt,
			Summary:       check.Summary,
			Details:       &details,
		})
	}
	return result
}
