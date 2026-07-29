package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

var commonRequiredFiles = []string{"challenge.yaml", "problem.md", "solution.md"}

const MaxChallengeNodes = 4

// MaterializeWithSlug promotes a published challenge into a human-readable
// source directory while keeping its opaque identity in challenge.yaml.
func MaterializeWithSlug(root, id, sourceSlug string, populate func(dst string) error) (*Entry, error) {
	if !ValidSourceSlug(sourceSlug) {
		return nil, fmt.Errorf("invalid challenge source slug %q", sourceSlug)
	}
	return materialize(root, id, sourceSlug, populate)
}

func materialize(root, id, directoryName string, populate func(dst string) error) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid challenge id %q", id)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create challenges dir: %w", err)
	}

	staging := filepath.Join(root, ".tmp-"+directoryName+"-"+randomSuffix())
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

	challenge, err := ValidateDir(staging)
	if err != nil {
		return nil, err
	}
	if challenge.ID != id {
		return nil, fmt.Errorf("challenge id mismatch: expected %s got %s", id, challenge.ID)
	}
	if challenge.SourceSlug != directoryName {
		return nil, fmt.Errorf("challenge source slug mismatch: expected %s got %s", directoryName, challenge.SourceSlug)
	}

	target := filepath.Join(root, directoryName)
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("challenge %s already exists", id)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat challenge dir: %w", err)
	}

	if err := os.Rename(staging, target); err != nil {
		return nil, fmt.Errorf("promote challenge dir: %w", err)
	}
	promoted = true
	challenge.Dir = target
	return challenge, nil
}

func ValidateDir(dir string) (*Entry, error) {
	challenge, err := LoadDir(dir)
	if err != nil {
		return nil, err
	}
	if !ValidID(challenge.ID) {
		return nil, fmt.Errorf("invalid challenge id %q", challenge.ID)
	}
	if !ValidSourceSlug(challenge.SourceSlug) {
		return nil, fmt.Errorf("invalid challenge source slug %q", challenge.SourceSlug)
	}
	if err := validateChallengeFiles(challenge, dir); err != nil {
		return nil, err
	}
	if strings.TrimSpace(challenge.Title) == "" {
		return nil, fmt.Errorf("challenge title is required")
	}
	switch challenge.Runtime {
	case RuntimeNode, RuntimeK8s:
	default:
		return nil, fmt.Errorf("unsupported challenge runtime %q", challenge.Runtime)
	}
	switch strings.TrimSpace(challenge.Difficulty) {
	case "easy", "medium", "hard":
	default:
		return nil, fmt.Errorf("challenge difficulty must be easy, medium, or hard")
	}
	if strings.TrimSpace(challenge.Description) == "" {
		return nil, fmt.Errorf("challenge description is required")
	}
	if err := validateCheckpoints(challenge, dir); err != nil {
		return nil, err
	}
	if err := validateTeachingAssets(challenge, dir); err != nil {
		return nil, err
	}
	return challenge, nil
}

func ValidateSubmissionDir(dir string) (*Entry, error) {
	challenge, err := LoadSubmissionDir(dir)
	if err != nil {
		return nil, err
	}
	if err := validateChallengeFiles(challenge, dir); err != nil {
		return nil, err
	}
	if strings.TrimSpace(challenge.Title) == "" {
		return nil, fmt.Errorf("challenge title is required")
	}
	switch challenge.Runtime {
	case RuntimeNode, RuntimeK8s:
	default:
		return nil, fmt.Errorf("unsupported challenge runtime %q", challenge.Runtime)
	}
	switch strings.TrimSpace(challenge.Difficulty) {
	case "easy", "medium", "hard":
	default:
		return nil, fmt.Errorf("challenge difficulty must be easy, medium, or hard")
	}
	if strings.TrimSpace(challenge.Description) == "" {
		return nil, fmt.Errorf("challenge description is required")
	}
	if err := validateCheckpoints(challenge, dir); err != nil {
		return nil, err
	}
	if err := validateTeachingAssets(challenge, dir); err != nil {
		return nil, err
	}
	return challenge, nil
}

func validateCheckpoints(challenge *Entry, dir string) error {
	if len(challenge.Checkpoints) == 0 {
		return fmt.Errorf("challenge checkpoints are required")
	}
	known := make(map[string]struct{}, len(challenge.Checkpoints))
	nodes := make(map[string]struct{}, len(challenge.Nodes))
	for _, node := range challenge.Nodes {
		nodes[node.Name] = struct{}{}
	}
	for _, checkpoint := range challenge.Checkpoints {
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
		path, err := safeChallengePath(dir, checkpoint.Hint)
		if err != nil {
			return fmt.Errorf("checkpoint %q hint: %w", id, err)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("checkpoint %q hint is not a file", id)
		}
		if challenge.Runtime == RuntimeNode {
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

func validateChallengeFiles(challenge *Entry, dir string) error {
	for _, name := range commonRequiredFiles {
		if err := requireRegularFile(dir, name); err != nil {
			return err
		}
	}
	for _, legacy := range []string{"Dockerfile", "generate.sh", "answer.sh", "checks"} {
		if _, err := os.Lstat(filepath.Join(dir, legacy)); err == nil {
			return fmt.Errorf("legacy challenge asset %s is not allowed", legacy)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect legacy challenge asset %s: %w", legacy, err)
		}
	}

	switch challenge.Runtime {
	case RuntimeNode:
		if len(challenge.Nodes) == 0 {
			return fmt.Errorf("node challenge requires at least one node")
		}
		if len(challenge.Nodes) > MaxChallengeNodes {
			return fmt.Errorf("node challenge exceeds the %d node limit", MaxChallengeNodes)
		}
		declared := make(map[string]struct{}, len(challenge.Nodes))
		checkpointNodes := make(map[string]struct{})
		for _, checkpoint := range challenge.Checkpoints {
			checkpointNodes[checkpoint.Node] = struct{}{}
		}
		for _, node := range challenge.Nodes {
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
				return fmt.Errorf("nodes/%s is not declared in challenge.yaml", entry.Name())
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, "k8s")); err == nil {
			return fmt.Errorf("node challenge must not contain k8s assets")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect k8s assets: %w", err)
		}
	case RuntimeK8s:
		if len(challenge.Nodes) != 0 {
			return fmt.Errorf("k8s challenge must not declare nodes")
		}
		for _, script := range []string{"generate.sh", "answer.sh", "checks.sh"} {
			if err := requireRegularFile(dir, filepath.Join("k8s", script)); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, "nodes")); err == nil {
			return fmt.Errorf("k8s challenge must not contain node assets")
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
