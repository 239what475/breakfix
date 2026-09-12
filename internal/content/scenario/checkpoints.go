package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Content struct {
	Problem  string            `json:"problem"`
	Solution string            `json:"solution"`
	Hints    map[string]string `json:"hints"`
}

func ReadContent(entry *Entry) (*Content, error) {
	if entry == nil {
		return nil, fmt.Errorf("scenario is nil")
	}
	problem, err := os.ReadFile(filepath.Join(entry.Dir, "problem.md"))
	if err != nil {
		return nil, fmt.Errorf("read problem.md: %w", err)
	}
	solution, err := os.ReadFile(filepath.Join(entry.Dir, "solution.md"))
	if err != nil {
		return nil, fmt.Errorf("read solution.md: %w", err)
	}
	content := &Content{
		Problem:  string(problem),
		Solution: string(solution),
		Hints:    make(map[string]string),
	}
	for _, checkpoint := range entry.Checkpoints {
		if checkpoint.Hint == "" {
			continue
		}
		path, err := safeScenarioPath(entry.Dir, checkpoint.Hint)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %q hint: %w", checkpoint.ID, err)
		}
		hint, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read checkpoint %q hint: %w", checkpoint.ID, err)
		}
		content.Hints[checkpoint.ID] = string(hint)
	}
	return content, nil
}

func safeScenarioPath(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("must be a non-empty relative path")
	}
	path := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("must remain inside the scenario directory")
	}
	return path, nil
}
