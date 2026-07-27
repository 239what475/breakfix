package server

import (
	"fmt"
	"net/http"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/challenge"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
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
		completed, err = h.db.ListCompletedChallengeIDs(c.Request.Context(), user.ID)
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
			Type:        ch.Type,
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
		tags := mappingTagTitles(published.Mapping)
		s.Tags = tags
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ChallengeList{Challenges: summaries})
}

func (h *Handler) GetChallengeContent(c *gin.Context, id string) {
	if h.requireUser(c) == nil {
		return
	}
	entry, err := h.publishedChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	content, err := challenge.ReadContent(entry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	checkpoints := toAPICheckpoints(entry.Checkpoints)
	hints := content.Hints
	c.JSON(http.StatusOK, api.ChallengeContent{
		Id:          entry.ID,
		Title:       entry.Title,
		Problem:     content.Problem,
		Solution:    content.Solution,
		Hints:       hints,
		Checkpoints: checkpoints,
	})
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
		dependsOn := append([]string{}, checkpoint.DependsOn...)
		result = append(result, api.ChallengeCheckpoint{
			Id:          checkpoint.ID,
			Title:       checkpoint.Title,
			Description: checkpoint.Description,
			Hint:        &hint,
			DependsOn:   &dependsOn,
		})
	}
	return result
}

func toAPICheckResults(checks []challenge.CheckResult) []api.CheckpointResult {
	result := make([]api.CheckpointResult, 0, len(checks))
	for _, check := range checks {
		details := check.Details
		result = append(result, api.CheckpointResult{
			Id:      check.ID,
			Passed:  check.Passed,
			Summary: check.Summary,
			Details: &details,
		})
	}
	return result
}

func toAPICheckStatusResults(checks []breakfixv1.CheckpointResultStatus) []api.CheckpointResult {
	result := make([]api.CheckpointResult, 0, len(checks))
	for _, check := range checks {
		details := check.Details
		result = append(result, api.CheckpointResult{
			Id:      check.ID,
			Passed:  check.Passed,
			Summary: check.Summary,
			Details: &details,
		})
	}
	return result
}
