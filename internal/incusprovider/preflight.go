package incusprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
)

var requiredExtensions = []string{
	"clustering",
	"container_edit_metadata",
	"image_source_project",
	"instance_file_head",
	"instance_systemd_credentials",
	"network_acl",
	"network_bridge_acl_devices",
	"operation_wait",
	"projects",
	"projects_images_remote_cache_expiry",
	"projects_limits",
	"projects_limits_disk",
	"projects_limits_instances",
	"projects_networks",
	"projects_restrictions",
	"projects_restricted_cluster_target",
	"projects_restricted_storage_pool_access",
	"storage_api_project",
}

func (c *Client) Preflight(ctx context.Context, role Role) (PreflightResult, error) {
	if !role.Valid() {
		return PreflightResult{}, fmt.Errorf("%w: invalid Incus provider role %q", ErrInvalid, role)
	}
	server, err := c.scoped(ctx, "default")
	if err != nil {
		return PreflightResult{}, err
	}
	info, _, err := server.GetServer()
	if err != nil {
		return PreflightResult{}, classify("get server", c.config.Endpoint, err)
	}
	if info.Environment.ServerVersion != SupportedServerVersion {
		return PreflightResult{}, fmt.Errorf("%w: Incus server version %q, require %q", ErrInvariant, info.Environment.ServerVersion, SupportedServerVersion)
	}
	missing := make([]string, 0)
	for _, extension := range requiredExtensions {
		if !server.HasExtension(extension) {
			missing = append(missing, extension)
		}
	}
	if len(missing) > 0 {
		return PreflightResult{}, fmt.Errorf("%w: Incus server is missing API extensions: %s", ErrInvariant, strings.Join(missing, ", "))
	}
	members, err := server.GetClusterMembers()
	if err != nil {
		return PreflightResult{}, classify("list cluster members", c.config.Endpoint, err)
	}
	if len(members) != 1 || members[0].Status != "Online" {
		return PreflightResult{}, fmt.Errorf("%w: bridge mode requires exactly one online Incus member", ErrInvariant)
	}
	pool, _, err := server.GetStoragePool(c.config.StoragePool)
	if err != nil {
		return PreflightResult{}, classify("get storage pool", c.config.StoragePool, err)
	}
	if pool.Status != "Created" {
		return PreflightResult{}, fmt.Errorf("%w: Incus storage pool %q is %q", ErrInvariant, pool.Name, pool.Status)
	}

	projects := projectsForRole(role, c.config)
	for _, project := range projects {
		if err := c.checkPlatformProject(ctx, project); err != nil {
			return PreflightResult{}, err
		}
	}
	baseFingerprint := ""
	if len(projects) > 0 {
		baseFingerprint, err = c.checkBaseImage(ctx, projects[0])
		if err != nil {
			return PreflightResult{}, err
		}
		for _, project := range projects[1:] {
			fingerprint, err := c.checkBaseImage(ctx, project)
			if err != nil {
				return PreflightResult{}, err
			}
			if fingerprint != baseFingerprint {
				return PreflightResult{}, fmt.Errorf("%w: base image differs between Incus platform projects", ErrInvariant)
			}
		}
	}
	return PreflightResult{
		ServerVersion:        info.Environment.ServerVersion,
		MemberName:           members[0].ServerName,
		StoragePool:          pool.Name,
		BaseImageFingerprint: baseFingerprint,
	}, nil
}

func projectsForRole(role Role, config Config) []string {
	switch role {
	case RoleBuilder:
		return []string{config.BuildProject}
	case RolePublisher:
		return []string{config.BuildProject, config.ImageProject}
	case RoleController, RoleVerifier:
		return []string{config.ImageProject}
	case RoleServer:
		return nil
	default:
		return nil
	}
}

func (c *Client) checkPlatformProject(ctx context.Context, projectName string) error {
	server, err := c.scoped(ctx, "default")
	if err != nil {
		return err
	}
	project, _, err := server.GetProject(projectName)
	if err != nil {
		return classify("get platform project", projectName, err)
	}
	required := map[string]string{
		"features.images":          "true",
		"features.networks":        "false",
		"features.profiles":        "true",
		"features.storage.volumes": "true",
	}
	for key, value := range required {
		if project.Config[key] != value {
			return fmt.Errorf("%w: Incus project %q requires %s=%s", ErrInvariant, projectName, key, value)
		}
	}
	return nil
}

func (c *Client) checkBaseImage(ctx context.Context, projectName string) (string, error) {
	server, err := c.scoped(ctx, projectName)
	if err != nil {
		return "", err
	}
	alias, _, err := server.GetImageAlias(c.config.BaseImageAlias)
	if err != nil {
		return "", classify("get base image alias", projectName+"/"+c.config.BaseImageAlias, err)
	}
	if alias.Target != c.config.BaseImageFingerprint {
		return "", fmt.Errorf("%w: Incus base alias %q in project %q points to %q instead of %q", ErrInvariant, c.config.BaseImageAlias, projectName, alias.Target, c.config.BaseImageFingerprint)
	}
	image, _, err := server.GetImage(c.config.BaseImageFingerprint)
	if err != nil {
		return "", classify("get base image", projectName+"/"+c.config.BaseImageFingerprint, err)
	}
	if image.Fingerprint != c.config.BaseImageFingerprint || !image.ExpiresAt.IsZero() || image.Public || image.Type != string(api.InstanceTypeContainer) {
		return "", fmt.Errorf("%w: Incus base image %q in project %q has unexpected metadata", ErrInvariant, c.config.BaseImageFingerprint, projectName)
	}
	return image.Fingerprint, nil
}
