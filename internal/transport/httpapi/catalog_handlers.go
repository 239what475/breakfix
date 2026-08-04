package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListChallenges(c *gin.Context) {
	user := h.getUser(c)

	challenges, err := h.catalog.List(c.Request.Context())
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
		s.Domain = toAPIRoadmapReference(published.Roadmap.Domain)
		s.Topic = toAPIRoadmapReference(roadmap.Ref{ID: published.Roadmap.Topic.ID, SourceRef: published.Roadmap.Topic.SourceRef, Title: published.Roadmap.Topic.Title})
		s.Tags = toAPIRoadmapReferences(published.Roadmap.Tags)
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ChallengeList{Challenges: summaries})
}

func (h *Handler) catalogEntries(ctx context.Context) (map[string]challenge.Entry, error) {
	published, err := h.catalog.List(ctx)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]challenge.Entry, len(published))
	for _, item := range published {
		entries[item.Entry.ID] = item.Entry
	}
	return entries, nil
}

func (h *Handler) GetChallengeContent(c *gin.Context, id string) {
	if h.requireUser(c) == nil {
		return
	}
	published, err := h.catalog.Find(c.Request.Context(), id)
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
		Roadmap:     toAPIChallengeRoadmap(published.Roadmap),
	})
}

func toAPIRoadmapReference(value roadmap.Ref) api.RoadmapReference {
	return api.RoadmapReference{Id: value.ID, SourceRef: value.SourceRef, Title: value.Title}
}

func toAPIRoadmapReferences(values []roadmap.Tag) []api.RoadmapReference {
	result := make([]api.RoadmapReference, 0, len(values))
	for _, value := range values {
		result = append(result, toAPIRoadmapReference(roadmap.Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}))
	}
	return result
}

func toAPIRoadmapTag(value roadmap.Tag) api.RoadmapTag {
	return api.RoadmapTag{Id: value.ID, SourceRef: value.SourceRef, Title: value.Title, Description: value.Description}
}

func toAPIRoadmapTopic(value roadmap.Topic) api.RoadmapTopic {
	return api.RoadmapTopic{
		Id: value.ID, SourceRef: value.SourceRef, Title: value.Title, Domain: toAPIRoadmapReference(value.Domain),
		Definition: value.Definition, Scope: value.Scope, NonGoals: value.NonGoals, ChallengeGuidance: value.ChallengeGuidance,
	}
}

func toAPIRoadmapEdges(values []roadmap.Edge) []api.RoadmapEdge {
	result := make([]api.RoadmapEdge, 0, len(values))
	for _, value := range values {
		result = append(result, api.RoadmapEdge{
			Source: toAPIRoadmapReference(value.Source), Target: toAPIRoadmapReference(value.Target),
			Relation: api.RoadmapEdgeRelation(value.Relation), Reason: value.Reason,
		})
	}
	return result
}

func toAPIChallengeRoadmap(value appcatalog.ChallengeRoadmap) api.ChallengeRoadmap {
	tags := make([]api.RoadmapTag, 0, len(value.Tags))
	for _, tag := range value.Tags {
		tags = append(tags, toAPIRoadmapTag(tag))
	}
	return api.ChallengeRoadmap{
		Revision: value.Revision, Domain: toAPIRoadmapReference(value.Domain), Topic: toAPIRoadmapTopic(value.Topic), Tags: tags,
		TopicNeighbors: toAPIRoadmapEdges(value.TopicNeighbors), ChallengeNeighbors: toAPIRoadmapEdges(value.ChallengeNeighbors),
	}
}

func (h *Handler) GetChallengeProgress(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	entry, err := h.catalog.Entry(c.Request.Context(), id)
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
