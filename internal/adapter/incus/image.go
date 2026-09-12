package incus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

const (
	buildProfileName   = "breakfix-bootstrap"
	workflowIDKey      = "user.breakfix.workflow_id"
	workflowAttemptKey = "user.breakfix.workflow_attempt"
	candidateIDKey     = "user.breakfix.candidate_revision_id"
	scenarioBundleDir  = "/opt/breakfix/scenario"
)

func (c *Client) BuildNodeImage(ctx context.Context, request BuildNodeImageRequest) (BuildNodeImageResult, error) {
	names, files, err := c.validateBuildNodeImageRequest(request)
	if err != nil {
		return BuildNodeImageResult{}, err
	}
	result := BuildNodeImageResult{
		WorkflowID: request.WorkflowID, CandidateRevisionID: request.CandidateRevisionID,
		Attempt: request.Attempt, InstanceName: names.Instance, Alias: names.Alias,
	}
	server, err := c.scoped(ctx, c.config.BuildProject)
	if err != nil {
		return BuildNodeImageResult{}, err
	}
	for attempt := int64(1); attempt < request.Attempt; attempt++ {
		if err := c.deleteBuildNodeImageAttempt(ctx, server, request.WorkflowID, request.CandidateRevisionID, attempt); err != nil {
			return BuildNodeImageResult{}, fmt.Errorf("clean earlier Node build attempt %d: %w", attempt, err)
		}
	}
	alias, _, err := server.GetImageAlias(names.Alias)
	if err == nil {
		result.Fingerprint = alias.Target
		if err := validateBuiltImage(server, request, result); err == nil {
			if err := c.deleteBuildInstanceIfPresent(ctx, server, request, names.Instance); err != nil {
				return BuildNodeImageResult{}, err
			}
			return result, nil
		} else if !errors.Is(err, ErrInvariant) {
			return BuildNodeImageResult{}, err
		}
		if err := c.deleteBuildNodeImageAttempt(ctx, server, request.WorkflowID, request.CandidateRevisionID, request.Attempt); err != nil {
			return BuildNodeImageResult{}, fmt.Errorf("replace stale Node build attempt %d: %w", request.Attempt, err)
		}
	} else {
		classified := classify("get build image alias", c.config.BuildProject+"/"+names.Alias, err)
		if !errors.Is(classified, ErrNotFound) {
			return BuildNodeImageResult{}, classified
		}
	}

	if err := c.deleteBuildInstanceIfPresent(ctx, server, request, names.Instance); err != nil {
		return BuildNodeImageResult{}, err
	}
	instanceRequest := buildInstance(request, names, c.config.BaseImageFingerprint)
	op, err := server.CreateInstance(instanceRequest)
	if err != nil {
		return BuildNodeImageResult{}, classify("create node image build instance", names.Instance, err)
	}
	if err := waitOperation(ctx, "create node image build instance", names.Instance, op); err != nil {
		return BuildNodeImageResult{}, err
	}
	for _, directory := range imageDirectories(files) {
		if err := server.CreateInstanceFile(names.Instance, path.Join(scenarioBundleDir, directory), incus.InstanceFileArgs{
			UID: 0, GID: 0, Mode: 0o755, Type: "directory", WriteMode: "overwrite",
		}); err != nil {
			return BuildNodeImageResult{}, classify("create node image bundle directory", directory, err)
		}
	}
	for _, file := range files {
		if err := server.CreateInstanceFile(names.Instance, path.Join(scenarioBundleDir, file.Path), incus.InstanceFileArgs{
			Content: bytes.NewReader(file.Content), UID: 0, GID: 0, Mode: file.Mode, Type: "file", WriteMode: "overwrite",
		}); err != nil {
			return BuildNodeImageResult{}, classify("write node image bundle file", file.Path, err)
		}
	}
	if err := clearImageMetadataExpiry(server, names.Instance); err != nil {
		return BuildNodeImageResult{}, err
	}

	op, err = server.CreateImage(api.ImagesPost{
		ImagePut: api.ImagePut{
			Public: false,
			Properties: map[string]string{
				resourceKindKey:    "build-image",
				workflowIDKey:      request.WorkflowID,
				candidateIDKey:     request.CandidateRevisionID,
				workflowAttemptKey: strconv.FormatInt(request.Attempt, 10),
				revisionKey:        request.Revision,
			},
		},
		Source:  &api.ImagesPostSource{Type: "instance", Name: names.Instance},
		Aliases: []api.ImageAlias{{Name: names.Alias, Description: "Breakfix candidate build image"}},
	}, nil)
	if err != nil {
		return BuildNodeImageResult{}, classify("publish node build image", names.Alias, err)
	}
	if err := waitOperation(ctx, "publish node build image", names.Alias, op); err != nil {
		return BuildNodeImageResult{}, err
	}
	alias, _, err = server.GetImageAlias(names.Alias)
	if err != nil {
		return BuildNodeImageResult{}, classify("get published node build image", names.Alias, err)
	}
	result.Fingerprint = alias.Target
	if err := validateBuiltImage(server, request, result); err != nil {
		return BuildNodeImageResult{}, err
	}
	if err := c.deleteBuildInstanceIfPresent(ctx, server, request, names.Instance); err != nil {
		return BuildNodeImageResult{}, err
	}
	return result, nil
}

func (c *Client) PublishNodeImage(ctx context.Context, request PublishNodeImageRequest) (PublishNodeImageResult, error) {
	if strings.TrimSpace(request.CandidateRevisionID) == "" || strings.TrimSpace(request.Revision) == "" {
		return PublishNodeImageResult{}, fmt.Errorf("%w: candidate revision ID and revision are required", ErrInvalid)
	}
	names, err := NamesForBuildAttempt(c.config.NamePrefix, request.Build.WorkflowID, request.Build.CandidateRevisionID, request.Build.Attempt)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	if request.Build.CandidateRevisionID != request.CandidateRevisionID || request.Build.InstanceName != names.Instance || request.Build.Alias != names.Alias || !fullFingerprintPattern.MatchString(request.Build.Fingerprint) {
		return PublishNodeImageResult{}, fmt.Errorf("%w: build image identity is invalid", ErrInvalid)
	}
	candidateAlias, err := AliasForCandidate(c.config.NamePrefix, request.CandidateRevisionID)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	result := PublishNodeImageResult{Alias: candidateAlias, Fingerprint: request.Build.Fingerprint}

	buildServer, err := c.scoped(ctx, c.config.BuildProject)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	if err := validateBuiltImage(buildServer, BuildNodeImageRequest{
		WorkflowID: request.Build.WorkflowID, CandidateRevisionID: request.Build.CandidateRevisionID,
		Attempt: request.Build.Attempt, Revision: request.Revision,
	}, request.Build); err != nil {
		return PublishNodeImageResult{}, err
	}
	imageServer, err := c.scoped(ctx, c.config.ImageProject)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	image, _, err := imageServer.GetImage(result.Fingerprint)
	if err != nil {
		classified := classify("get staging node image", c.config.ImageProject+"/"+result.Fingerprint, err)
		if !errors.Is(classified, ErrNotFound) {
			return PublishNodeImageResult{}, classified
		}
		op, err := imageServer.CreateImage(api.ImagesPost{
			Source: &api.ImagesPostSource{
				ImageSource: api.ImageSource{Protocol: "incus"},
				Type:        "image", Fingerprint: result.Fingerprint, Project: c.config.BuildProject,
			},
		}, nil)
		if err != nil {
			return PublishNodeImageResult{}, classify("copy staging node image", result.Fingerprint, err)
		}
		if err := waitOperation(ctx, "copy staging node image", result.Fingerprint, op); err != nil {
			return PublishNodeImageResult{}, err
		}
		image, _, err = imageServer.GetImage(result.Fingerprint)
		if err != nil {
			return PublishNodeImageResult{}, classify("get copied staging node image", result.Fingerprint, err)
		}
	}
	if err := validateEnvironmentImage(image, result.Fingerprint); err != nil {
		return PublishNodeImageResult{}, err
	}
	alias, _, err := imageServer.GetImageAlias(candidateAlias)
	if err == nil {
		if alias.Target != result.Fingerprint {
			return PublishNodeImageResult{}, fmt.Errorf("%w: candidate image alias %q points to another image", ErrInvariant, candidateAlias)
		}
		return result, nil
	}
	classified := classify("get candidate image alias", candidateAlias, err)
	if !errors.Is(classified, ErrNotFound) {
		return PublishNodeImageResult{}, classified
	}
	if err := imageServer.CreateImageAlias(api.ImageAliasesPost{ImageAliasesEntry: api.ImageAliasesEntry{
		Name: candidateAlias,
		ImageAliasesEntryPut: api.ImageAliasesEntryPut{
			Target: result.Fingerprint, Description: "Breakfix candidate staging image",
		},
	}}); err != nil {
		return PublishNodeImageResult{}, classify("create candidate image alias", candidateAlias, err)
	}
	return result, nil
}

func (c *Client) DeleteBuildNodeImage(ctx context.Context, result BuildNodeImageResult) error {
	names, err := NamesForBuildAttempt(c.config.NamePrefix, result.WorkflowID, result.CandidateRevisionID, result.Attempt)
	if err != nil {
		return err
	}
	if result.InstanceName != names.Instance || result.Alias != names.Alias || !fullFingerprintPattern.MatchString(result.Fingerprint) {
		return fmt.Errorf("%w: build image identity is invalid", ErrInvalid)
	}
	server, err := c.scoped(ctx, c.config.BuildProject)
	if err != nil {
		return err
	}
	request := BuildNodeImageRequest{WorkflowID: result.WorkflowID, CandidateRevisionID: result.CandidateRevisionID, Attempt: result.Attempt}
	if err := c.deleteBuildInstanceIfPresent(ctx, server, request, names.Instance); err != nil {
		return err
	}
	return deleteOwnedImageAlias(ctx, server, names.Alias, result.Fingerprint, func(image *api.Image) error {
		return validateBuildImageOwner(image, result.WorkflowID, result.CandidateRevisionID, result.Attempt)
	})
}

// DeleteBuildNodeImageAttempt removes one exact attempt without requiring a
// previously committed fingerprint. Ownership is verified from Incus metadata
// before any instance, alias, or image is removed.
func (c *Client) DeleteBuildNodeImageAttempt(ctx context.Context, workflowID, candidateRevisionID string, attempt int64) error {
	server, err := c.scoped(ctx, c.config.BuildProject)
	if err != nil {
		return err
	}
	return c.deleteBuildNodeImageAttempt(ctx, server, workflowID, candidateRevisionID, attempt)
}

func (c *Client) deleteBuildNodeImageAttempt(ctx context.Context, server incus.InstanceServer, workflowID, candidateRevisionID string, attempt int64) error {
	names, err := NamesForBuildAttempt(c.config.NamePrefix, workflowID, candidateRevisionID, attempt)
	if err != nil {
		return err
	}
	request := BuildNodeImageRequest{WorkflowID: workflowID, CandidateRevisionID: candidateRevisionID, Attempt: attempt}
	if err := c.deleteBuildInstanceIfPresent(ctx, server, request, names.Instance); err != nil {
		return err
	}
	alias, _, err := server.GetImageAlias(names.Alias)
	if err != nil {
		classified := classify("get Node build image alias for cleanup", names.Alias, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	return deleteOwnedImageAlias(ctx, server, names.Alias, alias.Target, func(image *api.Image) error {
		return validateBuildImageOwner(image, workflowID, candidateRevisionID, attempt)
	})
}

func (c *Client) PublishScenarioNodeImage(ctx context.Context, request PublishScenarioNodeImageRequest) (PublishNodeImageResult, error) {
	candidateAlias, err := AliasForCandidate(c.config.NamePrefix, request.CandidateRevisionID)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	scenarioAlias, err := AliasForScenario(c.config.NamePrefix, request.ScenarioID, request.ScenarioRevisionID)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	if request.Staging.Alias != candidateAlias || !fullFingerprintPattern.MatchString(request.Staging.Fingerprint) {
		return PublishNodeImageResult{}, fmt.Errorf("%w: candidate image identity is invalid", ErrInvalid)
	}
	server, err := c.scoped(ctx, c.config.ImageProject)
	if err != nil {
		return PublishNodeImageResult{}, err
	}
	staging, _, err := server.GetImageAlias(candidateAlias)
	if err != nil {
		return PublishNodeImageResult{}, classify("get candidate image alias", candidateAlias, err)
	}
	if staging.Target != request.Staging.Fingerprint {
		return PublishNodeImageResult{}, fmt.Errorf("%w: candidate image alias %q has unexpected target", ErrInvariant, candidateAlias)
	}
	image, _, err := server.GetImage(request.Staging.Fingerprint)
	if err != nil {
		return PublishNodeImageResult{}, classify("get candidate image", request.Staging.Fingerprint, err)
	}
	if err := validateEnvironmentImage(image, request.Staging.Fingerprint); err != nil {
		return PublishNodeImageResult{}, err
	}
	formal, _, err := server.GetImageAlias(scenarioAlias)
	if err == nil {
		if formal.Target != request.Staging.Fingerprint {
			return PublishNodeImageResult{}, fmt.Errorf("%w: scenario image alias %q points to another immutable revision", ErrInvariant, scenarioAlias)
		}
		return PublishNodeImageResult{Alias: scenarioAlias, Fingerprint: request.Staging.Fingerprint}, nil
	}
	classified := classify("get scenario image alias", scenarioAlias, err)
	if !errors.Is(classified, ErrNotFound) {
		return PublishNodeImageResult{}, classified
	}
	if err := server.CreateImageAlias(api.ImageAliasesPost{ImageAliasesEntry: api.ImageAliasesEntry{
		Name: scenarioAlias,
		ImageAliasesEntryPut: api.ImageAliasesEntryPut{
			Target: request.Staging.Fingerprint, Description: "Breakfix published scenario image",
		},
	}}); err != nil {
		return PublishNodeImageResult{}, classify("create scenario image alias", scenarioAlias, err)
	}
	return PublishNodeImageResult{Alias: scenarioAlias, Fingerprint: request.Staging.Fingerprint}, nil
}

func (c *Client) DeleteCandidateNodeImage(ctx context.Context, candidateRevisionID, fingerprint string) error {
	expectedAlias, err := AliasForCandidate(c.config.NamePrefix, candidateRevisionID)
	if err != nil {
		return err
	}
	if !fullFingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("%w: published image identity is invalid", ErrInvalid)
	}
	server, err := c.scoped(ctx, c.config.ImageProject)
	if err != nil {
		return err
	}
	return deleteOwnedImageAlias(ctx, server, expectedAlias, fingerprint, func(image *api.Image) error {
		return validateEnvironmentImage(image, fingerprint)
	})
}

func (c *Client) DeleteScenarioNodeImage(ctx context.Context, scenarioID, scenarioRevisionID, fingerprint string) error {
	expectedAlias, err := AliasForScenario(c.config.NamePrefix, scenarioID, scenarioRevisionID)
	if err != nil {
		return err
	}
	if !fullFingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("%w: published scenario image identity is invalid", ErrInvalid)
	}
	server, err := c.scoped(ctx, c.config.ImageProject)
	if err != nil {
		return err
	}
	return deleteOwnedImageAlias(ctx, server, expectedAlias, fingerprint, func(image *api.Image) error {
		return validateEnvironmentImage(image, fingerprint)
	})
}

func (c *Client) validateBuildNodeImageRequest(request BuildNodeImageRequest) (BuildNames, []ImageFile, error) {
	names, err := NamesForBuildAttempt(c.config.NamePrefix, request.WorkflowID, request.CandidateRevisionID, request.Attempt)
	if err != nil {
		return BuildNames{}, nil, err
	}
	if strings.TrimSpace(request.Revision) == "" || len(request.Files) == 0 {
		return BuildNames{}, nil, fmt.Errorf("%w: revision and image files are required", ErrInvalid)
	}
	files := append([]ImageFile(nil), request.Files...)
	seen := make(map[string]struct{}, len(files))
	for index := range files {
		clean := path.Clean(strings.TrimSpace(files[index].Path))
		if clean == "." || clean == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return BuildNames{}, nil, fmt.Errorf("%w: invalid image file path %q", ErrInvalid, files[index].Path)
		}
		if _, duplicate := seen[clean]; duplicate {
			return BuildNames{}, nil, fmt.Errorf("%w: duplicate image file path %q", ErrInvalid, clean)
		}
		if files[index].Mode < 0o400 || files[index].Mode > 0o777 {
			return BuildNames{}, nil, fmt.Errorf("%w: invalid image file mode for %q", ErrInvalid, clean)
		}
		files[index].Path = clean
		files[index].Content = append([]byte(nil), files[index].Content...)
		seen[clean] = struct{}{}
	}
	slices.SortFunc(files, func(a, b ImageFile) int { return strings.Compare(a.Path, b.Path) })
	return names, files, nil
}

func buildInstance(request BuildNodeImageRequest, names BuildNames, baseFingerprint string) api.InstancesPost {
	config := api.ConfigMap{
		resourceKindKey:    "build-instance",
		workflowIDKey:      request.WorkflowID,
		candidateIDKey:     request.CandidateRevisionID,
		workflowAttemptKey: strconv.FormatInt(request.Attempt, 10),
		revisionKey:        request.Revision,
	}
	return api.InstancesPost{
		Name:   names.Instance,
		Type:   api.InstanceTypeContainer,
		Start:  false,
		Source: api.InstanceSource{Type: "image", Fingerprint: baseFingerprint},
		InstancePut: api.InstancePut{
			Description: "Breakfix stopped node image builder",
			Config:      config,
			Profiles:    []string{buildProfileName},
		},
	}
}

func (c *Client) deleteBuildInstanceIfPresent(ctx context.Context, server incus.InstanceServer, request BuildNodeImageRequest, name string) error {
	instance, _, err := server.GetInstance(name)
	if err != nil {
		classified := classify("get node image build instance", name, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	if err := validateBuildInstanceOwner(instance, request.WorkflowID, request.CandidateRevisionID, request.Attempt); err != nil {
		return err
	}
	if instance.IsActive() {
		return fmt.Errorf("%w: node image build instance %q was started", ErrInvariant, name)
	}
	op, err := server.DeleteInstance(name)
	if err != nil {
		return classify("delete node image build instance", name, err)
	}
	return waitOperation(ctx, "delete node image build instance", name, op)
}

func validateBuildInstanceOwner(instance *api.Instance, workflowID, candidateRevisionID string, attempt int64) error {
	if instance == nil || instance.Type != string(api.InstanceTypeContainer) || instance.Config[resourceKindKey] != "build-instance" || instance.Config[workflowIDKey] != workflowID || instance.Config[candidateIDKey] != candidateRevisionID || instance.Config[workflowAttemptKey] != strconv.FormatInt(attempt, 10) {
		return fmt.Errorf("%w: node image build instance has mismatched owner", ErrInvariant)
	}
	return nil
}

// Instances inherit metadata.yaml from their source image. Clear an upstream
// expiry before publishing so every candidate image is a permanent artifact.
func clearImageMetadataExpiry(server incus.InstanceServer, instance string) error {
	metadata, etag, err := server.GetInstanceMetadata(instance)
	if err != nil {
		return classify("get node image build metadata", instance, err)
	}
	if metadata.ExpiryDate == 0 {
		return nil
	}
	metadata.ExpiryDate = 0
	if err := server.UpdateInstanceMetadata(instance, *metadata, etag); err != nil {
		return classify("clear node image build metadata expiry", instance, err)
	}
	return nil
}

func validateBuiltImage(server incus.InstanceServer, request BuildNodeImageRequest, result BuildNodeImageResult) error {
	alias, _, err := server.GetImageAlias(result.Alias)
	if err != nil {
		return classify("get node build image alias", result.Alias, err)
	}
	if alias.Target != result.Fingerprint {
		return fmt.Errorf("%w: node build image alias %q has unexpected target", ErrInvariant, result.Alias)
	}
	image, _, err := server.GetImage(result.Fingerprint)
	if err != nil {
		return classify("get node build image", result.Fingerprint, err)
	}
	return validateBuiltImageProperties(image, request)
}

func validateBuiltImageProperties(image *api.Image, request BuildNodeImageRequest) error {
	if err := validateBuildImageOwner(image, request.WorkflowID, request.CandidateRevisionID, request.Attempt); err != nil {
		return err
	}
	if image.Properties[revisionKey] != request.Revision {
		return fmt.Errorf("%w: node build image has mismatched owner", ErrInvariant)
	}
	return nil
}

func validateBuildImageOwner(image *api.Image, workflowID, candidateRevisionID string, attempt int64) error {
	if image == nil || !image.ExpiresAt.IsZero() || image.Public || image.Type != string(api.InstanceTypeContainer) || image.Properties[resourceKindKey] != "build-image" || image.Properties[workflowIDKey] != workflowID || image.Properties[candidateIDKey] != candidateRevisionID || image.Properties[workflowAttemptKey] != strconv.FormatInt(attempt, 10) {
		return fmt.Errorf("%w: node build image has mismatched owner", ErrInvariant)
	}
	return nil
}

func imageDirectories(files []ImageFile) []string {
	directories := make(map[string]struct{})
	for _, file := range files {
		for directory := path.Dir(file.Path); directory != "."; directory = path.Dir(directory) {
			directories[directory] = struct{}{}
		}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	slices.SortFunc(result, func(a, b string) int {
		depthA := strings.Count(a, "/")
		depthB := strings.Count(b, "/")
		if depthA != depthB {
			return depthA - depthB
		}
		return strings.Compare(a, b)
	})
	return result
}

func deleteOwnedImageAlias(ctx context.Context, server incus.InstanceServer, aliasName, fingerprint string, validate func(*api.Image) error) error {
	alias, _, err := server.GetImageAlias(aliasName)
	if err != nil {
		classified := classify("get image alias for deletion", aliasName, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	if alias.Target != fingerprint {
		return fmt.Errorf("%w: refusing to delete image alias %q with unexpected target", ErrInvariant, aliasName)
	}
	image, _, err := server.GetImage(fingerprint)
	if err != nil {
		return classify("get image for deletion", fingerprint, err)
	}
	if err := validate(image); err != nil {
		return err
	}
	if err := server.DeleteImageAlias(aliasName); err != nil {
		return classify("delete image alias", aliasName, err)
	}
	aliases, err := server.GetImageAliases()
	if err != nil {
		return classify("list image aliases after deletion", fingerprint, err)
	}
	if slices.ContainsFunc(aliases, func(alias api.ImageAliasesEntry) bool { return alias.Target == fingerprint }) {
		return nil
	}
	op, err := server.DeleteImage(fingerprint)
	if err != nil {
		classified := classify("delete unreferenced image", fingerprint, err)
		if errors.Is(classified, ErrNotFound) {
			return nil
		}
		return classified
	}
	return waitOperation(ctx, "delete unreferenced image", fingerprint, op)
}
