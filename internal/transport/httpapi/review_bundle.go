package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/domain/generation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const reviewBundleSchemaVersion = 1

// GetGeneratorReviewBundle returns an immutable, version-bound review package.
// The package contains only author-visible projections; provider paths and
// lifecycle identities never cross this HTTP boundary.
func (h *Handler) GetGeneratorReviewBundle(c *gin.Context, workflowID string, params api.GetGeneratorReviewBundleParams) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	service, ok := h.requireGenerator(c)
	if !ok {
		return
	}
	view, err := service.GetGeneration(c.Request.Context(), user.ID, workflowID)
	if err != nil {
		h.writeGeneratorError(c, err)
		return
	}
	if view.Candidate == nil {
		h.writeGeneratorError(c, generation.ErrCandidateNotFound)
		return
	}

	kind := reviewBundleKind(params.Kind)
	var payload []byte
	switch kind {
	case reviewBundleContent:
		if view.Workflow.State != generation.StateNeedsAuthorReview {
			h.writeGeneratorError(c, generation.ErrCandidateInvalidState)
			return
		}
		payload, err = h.buildContentReviewPayload(c.Request.Context(), view.Workflow, view.Candidate)
	default:
		h.writeGeneratorError(c, errors.New("review bundle kind must be content"))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "review bundle is temporarily unavailable"})
		return
	}
	manifest := api.GeneratorReviewManifest{
		SchemaVersion:          reviewBundleSchemaVersion,
		Kind:                   api.GeneratorReviewManifestKind(kind),
		WorkflowId:             view.Workflow.ID,
		WorkflowState:          string(view.Workflow.State),
		CandidateRevisionId:    view.Candidate.ID,
		CandidateArchiveDigest: view.Candidate.ArchiveDigest,
		PayloadSha256:          candidate.Digest(payload),
		ExportedAt:             time.Now().UTC(),
	}
	c.JSON(http.StatusOK, api.GeneratorReviewBundle{
		Manifest: manifest,
		Payload:  base64.StdEncoding.EncodeToString(payload),
	})
}

type reviewBundleKind string

const (
	reviewBundleContent reviewBundleKind = "content"
)

func (h *Handler) buildContentReviewPayload(ctx context.Context, workflow generation.Workflow, revision *generation.Revision) ([]byte, error) {
	projection, err := h.toAPIGeneratorGeneration(ctx, workflow, revision)
	if err != nil {
		return nil, err
	}
	entries := make(map[string][]byte)
	entries["overview.md"] = []byte(contentOverviewMarkdown(projection))
	entries["judge.md"] = []byte(judgeReviewMarkdown(workflow))
	for _, checkpoint := range verifiedCheckpoints(projection.Verified) {
		entries["checkpoints/"+checkpoint.Id+".md"] = []byte(verifiedCheckpointMarkdown(checkpoint))
	}
	for _, asset := range projection.Assets {
		name, err := reviewPath("candidate", asset.Path)
		if err != nil {
			return nil, err
		}
		entries[name] = []byte(asset.Content)
	}
	for _, diff := range projection.Diff {
		name, err := reviewPath("diff", diff.Path+".diff")
		if err != nil {
			return nil, err
		}
		entries[name] = []byte(diff.Diff)
	}
	return writeDeterministicReviewArchive(entries)
}

func contentOverviewMarkdown(value api.GeneratorGeneration) string {
	if value.Verified == nil {
		return "# Candidate\n\n当前 candidate 已通过内容审核流程，但没有可显示的场景摘要。\n"
	}
	metadata := value.Verified.Metadata
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", metadata.Title)
	fmt.Fprintf(&out, "**运行时**：%s\n\n", metadata.Runtime)
	fmt.Fprintf(&out, "## 场景简介\n\n%s\n\n", metadata.Description)
	out.WriteString("## 检查点\n\n")
	for _, checkpoint := range value.Verified.Checkpoints {
		fmt.Fprintf(&out, "### %s\n\n%s\n\n", checkpoint.Title, checkpoint.Description)
		if checkpoint.Node != nil && *checkpoint.Node != "" {
			fmt.Fprintf(&out, "**节点**：`%s`\n\n", *checkpoint.Node)
		}
		if checkpoint.Hint != nil && *checkpoint.Hint != "" {
			fmt.Fprintf(&out, "**提示文件**：`%s`\n\n", *checkpoint.Hint)
		}
	}
	return out.String()
}

func verifiedCheckpoints(value *api.VerifiedScenario) []api.VerifiedCheckpoint {
	if value == nil {
		return nil
	}
	return value.Checkpoints
}

func verifiedCheckpointMarkdown(value api.VerifiedCheckpoint) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", value.Title)
	fmt.Fprintf(&out, "%s\n\n", value.Description)
	if value.Node != nil && *value.Node != "" {
		fmt.Fprintf(&out, "**节点**：`%s`\n\n", *value.Node)
	}
	if value.Hint != nil && *value.Hint != "" {
		fmt.Fprintf(&out, "**提示文件**：`%s`\n", *value.Hint)
	}
	return out.String()
}

func judgeReviewMarkdown(workflow generation.Workflow) string {
	var out strings.Builder
	out.WriteString("# Judge\n\n")
	fmt.Fprintf(&out, "**工作流状态**：`%s`\n\n", workflow.State)
	if strings.TrimSpace(workflow.LastError) == "" {
		out.WriteString("Judge 未记录需要作者处理的错误。\n")
	} else {
		out.WriteString("## 反馈\n\n")
		out.WriteString(workflow.LastError)
		out.WriteString("\n")
	}
	return out.String()
}

func reviewPath(prefix, value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || strings.HasPrefix(value, "../") || value == ".." {
		return "", fmt.Errorf("review bundle path is invalid: %q", value)
	}
	name := path.Join(prefix, value)
	if name == prefix || strings.HasPrefix(name, "../") || path.IsAbs(name) {
		return "", fmt.Errorf("review bundle path escapes prefix: %q", value)
	}
	return name, nil
}

func writeDeterministicReviewArchive(entries map[string][]byte) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("review bundle is empty")
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		if _, err := reviewPath(".", name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	gzipWriter.ModTime = time.Unix(0, 0).UTC()
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range names {
		content := entries[name]
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC()}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write review entry %s: %w", name, err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			return nil, fmt.Errorf("write review content %s: %w", name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close review archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close review gzip: %w", err)
	}
	return data.Bytes(), nil
}
