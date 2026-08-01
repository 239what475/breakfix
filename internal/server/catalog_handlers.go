package server

import (
	"fmt"
	"net/http"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
	"github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListChallenges(c *gin.Context) {
	user := h.getUser(c)

	challenges, err := h.publishedChallenges()
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	completed := make(map[string]struct{})
	active := make(map[string]activeEnvironment)
	if user != nil {
		completed, err = h.db.Environment.ListCompletedChallengeIDs(c.Request.Context(), user.ID)
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
				completed[env.ChallengeRef] = struct{}{}
				continue
			}
			if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining {
				continue
			}
			current, exists := active[env.ChallengeRef]
			if !exists || passedCheckpointCount(env.Checkpoints) > passedCheckpointCount(current.Checkpoints) {
				active[env.ChallengeRef] = env
			}
		}
	}

	summaries := make([]api.ChallengeSummary, 0, len(challenges))
	for _, published := range challenges {
		ch := published.Entry
		s := api.ChallengeSummary{
			Id:          ch.ID,
			Title:       ch.Title,
			Runtime:     challengeSummaryRuntime(ch.Runtime),
			Difficulty:  api.ChallengeSummaryDifficulty(ch.Difficulty),
			Description: ch.Description,
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
		s.Tags = toAPITaxonomyReferences(published.Taxonomy.Tags)
		s.PrimaryOutcome = toAPITaxonomyReference(published.Taxonomy.PrimaryOutcome)
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ChallengeList{Challenges: summaries})
}

func (h *Handler) GetChallengeContent(c *gin.Context, id string) {
	if h.requireUser(c) == nil {
		return
	}
	published, err := h.publishedChallengeWithTaxonomy(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	content, err := challenge.ReadContent(&published.Entry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	checkpoints := toAPICheckpoints(published.Entry.Checkpoints)
	hints := content.Hints
	c.JSON(http.StatusOK, api.ChallengeContent{
		Id:          published.Entry.ID,
		Title:       published.Entry.Title,
		Runtime:     api.ChallengeContentRuntime(published.Entry.Runtime),
		Nodes:       toAPIChallengeNodes(published.Entry.Nodes),
		Problem:     content.Problem,
		Solution:    content.Solution,
		Hints:       hints,
		Checkpoints: checkpoints,
		Taxonomy:    toAPIChallengeTaxonomy(published.Taxonomy),
	})
}

func toAPITaxonomyReference(value taxonomy.Ref) api.TaxonomyReference {
	return api.TaxonomyReference{Id: value.ID, Title: value.Title}
}

func toAPITaxonomyReferences(values []taxonomy.Ref) []api.TaxonomyReference {
	result := make([]api.TaxonomyReference, 0, len(values))
	for _, value := range values {
		result = append(result, toAPITaxonomyReference(value))
	}
	return result
}

func toAPIChallengeTaxonomy(value challengeTaxonomy) api.ChallengeTaxonomy {
	entrySkills := make([]api.ChallengeEntrySkill, 0, len(value.EntrySkills))
	for _, entrySkill := range value.EntrySkills {
		entrySkills = append(entrySkills, api.ChallengeEntrySkill{
			Id:       entrySkill.Ref.ID,
			Title:    entrySkill.Ref.Title,
			Requires: toAPITaxonomyReferences(entrySkill.Requires),
		})
	}
	outcomes := make([]api.ChallengeOutcome, 0, len(value.Outcomes))
	for _, outcome := range value.Outcomes {
		outcomes = append(outcomes, api.ChallengeOutcome{Id: outcome.ID, Title: outcome.Title, Primary: outcome.Primary})
	}
	return api.ChallengeTaxonomy{
		Revision:       value.Revision,
		Tags:           toAPITaxonomyReferences(value.Tags),
		PrimaryOutcome: toAPITaxonomyReference(value.PrimaryOutcome),
		Outcomes:       outcomes,
		EntrySkills:    entrySkills,
	}
}

func (h *Handler) GetChallengeProgress(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	entry, err := h.publishedChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	env, err := h.findProgressEnvironment(c.Request.Context(), user.ID, entry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining && env.Phase != breakfixv1.EnvironmentCompleted {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "environment is not ready for checkpoint checks"})
		return
	}
	if env.Checkpoints == nil {
		checks := []api.CheckpointResult{}
		c.JSON(http.StatusOK, api.ChallengeProgress{Checks: checks})
		return
	}
	if env.Checkpoints.Error != "" {
		c.JSON(http.StatusUnprocessableEntity, api.ErrorResponse{Error: env.Checkpoints.Error})
		return
	}
	checks := toAPICheckStatusResults(env.Checkpoints.Results)
	c.JSON(http.StatusOK, api.ChallengeProgress{Checks: checks})
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

func challengeSummaryRuntime(runtime string) api.ChallengeSummaryRuntime {
	return api.ChallengeSummaryRuntime(challenge.NormalizeRuntime(runtime))
}

func toAPICheckpoints(checkpoints []challenge.Checkpoint) []api.ChallengeCheckpoint {
	result := make([]api.ChallengeCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		hint := checkpoint.Hint
		node := checkpoint.Node
		result = append(result, api.ChallengeCheckpoint{
			Id:          checkpoint.ID,
			Title:       checkpoint.Title,
			Description: checkpoint.Description,
			Hint:        &hint,
			Node:        &node,
		})
	}
	return result
}

func toAPIChallengeNodes(nodes []challenge.Node) []api.ChallengeNode {
	result := make([]api.ChallengeNode, len(nodes))
	for index, node := range nodes {
		result[index] = api.ChallengeNode{Name: node.Name, Title: node.Title}
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
