package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var commonRequiredFiles = []string{"scenario.yaml", "problem.md", "solution.md"}

var (
	nodeImageFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	k8sImageDigestPattern       = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
	contentRevisionPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

const MaxScenarioNodes = 4

// MaterializeWithPath promotes one immutable revision into a relative path.
// The path is never reused by a later revision.
func MaterializeWithPath(root, id, revisionID, relativePath string, populate func(dst string) error) (*Entry, error) {
	if !ValidRevisionID(revisionID) || !validMaterializedRelativePath(relativePath) {
		return nil, fmt.Errorf("invalid materialized scenario path %q", relativePath)
	}
	return materializeAt(root, id, revisionID, relativePath, populate)
}

func validMaterializedRelativePath(value string) bool {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(value)) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func materializeAt(root, id, revisionID, relativePath string, populate func(dst string) error) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid scenario id %q", id)
	}
	if !validMaterializedRelativePath(relativePath) {
		return nil, fmt.Errorf("invalid materialized scenario path %q", relativePath)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create scenarios dir: %w", err)
	}

	staging := filepath.Join(root, ".tmp-"+strings.ReplaceAll(relativePath, "/", "-")+"-"+randomSuffix())
	if err := os.MkdirAll(staging, 0755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := populate(staging); err != nil {
		return nil, err
	}

	scenario, err := ValidateDir(staging)
	if err != nil {
		return nil, err
	}
	if scenario.ID != id {
		return nil, fmt.Errorf("scenario id mismatch: expected %s got %s", id, scenario.ID)
	}
	actualPath := filepath.ToSlash(filepath.Join(scenario.SourceSlug, scenario.RevisionID))
	if actualPath != relativePath {
		return nil, fmt.Errorf("scenario materialized path mismatch: expected %s got %s", relativePath, actualPath)
	}
	if scenario.RevisionID != revisionID {
		return nil, fmt.Errorf("scenario revision mismatch: expected %s got %s", revisionID, scenario.RevisionID)
	}

	target := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return nil, fmt.Errorf("create scenario parent: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("scenario %s already exists", id)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat scenario dir: %w", err)
	}

	if err := os.Rename(staging, target); err != nil {
		return nil, fmt.Errorf("promote scenario dir: %w", err)
	}
	promoted = true
	scenario.Dir = target
	return scenario, nil
}

func ValidateDir(dir string) (*Entry, error) {
	scenario, err := LoadDir(dir)
	if err != nil {
		return nil, err
	}
	if !ValidID(scenario.ID) {
		return nil, fmt.Errorf("invalid scenario id %q", scenario.ID)
	}
	if !ValidRevisionID(scenario.RevisionID) {
		return nil, fmt.Errorf("invalid scenario revision id %q", scenario.RevisionID)
	}
	if !ValidSourceSlug(scenario.SourceSlug) {
		return nil, fmt.Errorf("invalid scenario source slug %q", scenario.SourceSlug)
	}
	if !contentRevisionPattern.MatchString(scenario.ContentRevision) {
		return nil, fmt.Errorf("scenario content_revision must be a lowercase sha256 digest")
	}
	if err := validateScenarioFiles(scenario, dir); err != nil {
		return nil, err
	}
	if strings.TrimSpace(scenario.Title) == "" {
		return nil, fmt.Errorf("scenario title is required")
	}
	if !NormalizeScenarioType(string(scenario.Type)).Valid() {
		return nil, fmt.Errorf("unsupported scenario type %q", scenario.Type)
	}
	if tags, err := NormalizeTags(scenario.Tags); err != nil {
		return nil, err
	} else if scenario.Type == ScenarioDocumentationExample && len(tags) > 0 {
		return nil, fmt.Errorf("documentation-example must not contain tags")
	}
	switch scenario.Runtime {
	case RuntimeNode, RuntimeK8s:
	default:
		return nil, fmt.Errorf("unsupported scenario runtime %q", scenario.Runtime)
	}
	if err := validatePublishedImage(scenario); err != nil {
		return nil, err
	}
	if strings.TrimSpace(scenario.Description) == "" {
		return nil, fmt.Errorf("scenario description is required")
	}
	if err := validateCheckpoints(scenario, dir); err != nil {
		return nil, err
	}
	if err := validateTeachingAssets(scenario, dir); err != nil {
		return nil, err
	}
	return scenario, nil
}

func validatePublishedImage(scenario *Entry) error {
	switch scenario.Runtime {
	case RuntimeNode:
		if !nodeImageFingerprintPattern.MatchString(scenario.Image) {
			return fmt.Errorf("node scenario image must be a full immutable Incus fingerprint")
		}
	case RuntimeK8s:
		if !k8sImageDigestPattern.MatchString(scenario.Image) {
			return fmt.Errorf("k8s scenario image must be an immutable OCI digest reference")
		}
	}
	return nil
}

// ValidateCandidateDir validates an unpublished CandidateRevision directory.
// It deliberately accepts no platform-owned publication identity or image.
func ValidateCandidateDir(dir string) (*Entry, error) {
	scenario, err := LoadCandidateDir(dir)
	if err != nil {
		return nil, err
	}
	if err := validateScenarioFiles(scenario, dir); err != nil {
		return nil, err
	}
	if strings.TrimSpace(scenario.Title) == "" {
		return nil, fmt.Errorf("scenario title is required")
	}
	if !NormalizeScenarioType(string(scenario.Type)).Valid() {
		return nil, fmt.Errorf("unsupported scenario type %q", scenario.Type)
	}
	if tags, err := NormalizeTags(scenario.Tags); err != nil {
		return nil, err
	} else if scenario.Type == ScenarioDocumentationExample && len(tags) > 0 {
		return nil, fmt.Errorf("documentation-example must not contain tags")
	}
	switch scenario.Runtime {
	case RuntimeNode, RuntimeK8s:
	default:
		return nil, fmt.Errorf("unsupported scenario runtime %q", scenario.Runtime)
	}
	if strings.TrimSpace(scenario.Description) == "" {
		return nil, fmt.Errorf("scenario description is required")
	}
	if err := validateCheckpoints(scenario, dir); err != nil {
		return nil, err
	}
	if err := validateTeachingAssets(scenario, dir); err != nil {
		return nil, err
	}
	return scenario, nil
}

func validateCheckpoints(scenario *Entry, dir string) error {
	if len(scenario.Checkpoints) == 0 {
		return fmt.Errorf("scenario checkpoints are required")
	}
	known := make(map[string]struct{}, len(scenario.Checkpoints))
	nodes := make(map[string]struct{}, len(scenario.Nodes))
	for _, node := range scenario.Nodes {
		nodes[node.Name] = struct{}{}
	}
	for _, checkpoint := range scenario.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" || !ValidID(id) {
			return fmt.Errorf("invalid checkpoint id %q", checkpoint.ID)
		}
		if _, ok := known[id]; ok {
			return fmt.Errorf("duplicate checkpoint id %q", id)
		}
		if strings.TrimSpace(checkpoint.Title) == "" {
			return fmt.Errorf("checkpoint %q title is required", id)
		}
		if strings.TrimSpace(checkpoint.Description) == "" {
			return fmt.Errorf("checkpoint %q description is required", id)
		}
		if strings.TrimSpace(checkpoint.Hint) == "" {
			return fmt.Errorf("checkpoint %q hint is required", id)
		}
		path, err := safeScenarioPath(dir, checkpoint.Hint)
		if err != nil {
			return fmt.Errorf("checkpoint %q hint: %w", id, err)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("checkpoint %q hint is not a file", id)
		}
		if scenario.Runtime == RuntimeNode {
			if _, ok := nodes[checkpoint.Node]; !ok {
				return fmt.Errorf("checkpoint %q references unknown node %q", id, checkpoint.Node)
			}
		} else if strings.TrimSpace(checkpoint.Node) != "" {
			return fmt.Errorf("k8s checkpoint %q must not declare a node", id)
		}
		known[id] = struct{}{}
	}
	return nil
}

func validateScenarioFiles(scenario *Entry, dir string) error {
	for _, name := range commonRequiredFiles {
		if err := requireRegularFile(dir, name); err != nil {
			return err
		}
	}
	switch scenario.Runtime {
	case RuntimeNode:
		if len(scenario.Nodes) == 0 {
			return fmt.Errorf("node scenario requires at least one node")
		}
		if len(scenario.Nodes) > MaxScenarioNodes {
			return fmt.Errorf("node scenario exceeds the %d node limit", MaxScenarioNodes)
		}
		declared := make(map[string]struct{}, len(scenario.Nodes))
		checkpointNodes := make(map[string]struct{})
		for _, checkpoint := range scenario.Checkpoints {
			checkpointNodes[checkpoint.Node] = struct{}{}
		}
		for _, node := range scenario.Nodes {
			name := strings.TrimSpace(node.Name)
			if !ValidID(name) {
				return fmt.Errorf("invalid node name %q", node.Name)
			}
			if _, reserved := reservedNodeNames[name]; reserved {
				return fmt.Errorf("node name %q is reserved", name)
			}
			if _, duplicate := declared[name]; duplicate {
				return fmt.Errorf("duplicate node name %q", name)
			}
			if strings.TrimSpace(node.Title) == "" {
				return fmt.Errorf("node %q title is required", name)
			}
			declared[name] = struct{}{}
			for _, script := range []string{"generate.sh", "answer.sh"} {
				if err := requireRegularFile(dir, filepath.Join("nodes", name, script)); err != nil {
					return err
				}
			}
			if _, hasChecks := checkpointNodes[name]; hasChecks {
				if err := requireRegularFile(dir, filepath.Join("nodes", name, "checks.sh")); err != nil {
					return err
				}
			}
		}
		entries, err := os.ReadDir(filepath.Join(dir, "nodes"))
		if err != nil {
			return fmt.Errorf("read nodes directory: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				return fmt.Errorf("nodes/%s must be a declared node directory", entry.Name())
			}
			if _, ok := declared[entry.Name()]; !ok {
				return fmt.Errorf("nodes/%s is not declared in scenario.yaml", entry.Name())
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, "k8s")); err == nil {
			return fmt.Errorf("node scenario must not contain k8s assets")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect k8s assets: %w", err)
		}
	case RuntimeK8s:
		if len(scenario.Nodes) != 0 {
			return fmt.Errorf("k8s scenario must not declare nodes")
		}
		for _, script := range []string{"generate.sh", "answer.sh", "checks.sh"} {
			if err := requireRegularFile(dir, filepath.Join("k8s", script)); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, "nodes")); err == nil {
			return fmt.Errorf("k8s scenario must not contain node assets")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect node assets: %w", err)
		}
	}
	return nil
}

var reservedNodeNames = map[string]struct{}{
	"breakfix":  {},
	"gateway":   {},
	"localhost": {},
}

func requireRegularFile(root, relative string) error {
	info, err := os.Lstat(filepath.Join(root, relative))
	if err != nil {
		return fmt.Errorf("missing %s: %w", filepath.ToSlash(relative), err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", filepath.ToSlash(relative))
	}
	return nil
}

func ValidID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 && i < len(id)-1 {
			continue
		}
		return false
	}
	return true
}

// ValidSourceSlug allows a readable Unicode directory name while rejecting
// path syntax. It is not an identity and must never be used as a lookup key.
func ValidSourceSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return false
	}
	for _, r := range slug {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' {
			continue
		}
		return false
	}
	return true
}

func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
