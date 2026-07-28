package challenge

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var checkpointMarkerPattern = regexp.MustCompile(`<!--\s*checkpoint:\s*([a-z0-9-]+)\s*-->`)

// validateTeachingAssets keeps instructional material tied to the same
// immutable artifact as the executable checks without prescribing a repair.
func validateTeachingAssets(challenge *Entry, dir string) error {
	solution, err := os.ReadFile(filepath.Join(dir, "solution.md"))
	if err != nil {
		return fmt.Errorf("read solution.md: %w", err)
	}
	if err := validateSolutionMarkers(challenge, solution); err != nil {
		return err
	}

	markdown := []string{"problem.md", "solution.md"}
	for _, checkpoint := range challenge.Checkpoints {
		markdown = append(markdown, checkpoint.Hint)
	}
	for _, relative := range markdown {
		if err := validateMarkdownLinks(dir, relative); err != nil {
			return err
		}
	}
	return nil
}

func validateSolutionMarkers(challenge *Entry, solution []byte) error {
	counts := make(map[string]int, len(challenge.Checkpoints))
	for _, marker := range checkpointMarkerPattern.FindAllSubmatch(solution, -1) {
		counts[string(marker[1])]++
	}
	for _, checkpoint := range challenge.Checkpoints {
		count := counts[checkpoint.ID]
		if count != 1 {
			return fmt.Errorf("solution.md must contain exactly one <!-- checkpoint: %s --> marker, found %d", checkpoint.ID, count)
		}
		delete(counts, checkpoint.ID)
	}
	for id, count := range counts {
		return fmt.Errorf("solution.md contains marker for unknown checkpoint %q (%d occurrences)", id, count)
	}
	return nil
}

func validateMarkdownLinks(root, relative string) error {
	path, err := safeChallengePath(root, relative)
	if err != nil {
		return fmt.Errorf("teaching asset %q: %w", relative, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read teaching asset %q: %w", relative, err)
	}
	document := goldmark.DefaultParser().Parse(text.NewReader(content))
	var validationErr error
	err = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || validationErr != nil {
			return ast.WalkContinue, validationErr
		}
		switch value := node.(type) {
		case *ast.Link:
			validationErr = validateMarkdownLink(root, relative, string(value.Destination))
		case *ast.Image:
			validationErr = validateMarkdownLink(root, relative, string(value.Destination))
		}
		return ast.WalkContinue, validationErr
	})
	if err != nil {
		return err
	}
	return validationErr
}

func validateMarkdownLink(root, source, destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.HasPrefix(destination, "#") {
		return nil
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return fmt.Errorf("%s contains invalid Markdown link %q: %w", source, destination, err)
	}
	if parsed.Scheme != "" {
		if parsed.Scheme == "https" && parsed.Host != "" {
			return nil
		}
		return fmt.Errorf("%s link %q must be a relative bundled resource or an HTTPS URL", source, destination)
	}
	if parsed.Host != "" {
		return fmt.Errorf("%s link %q must not use an absolute network path", source, destination)
	}
	resource, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return fmt.Errorf("%s link %q has an invalid path: %w", source, destination, err)
	}
	if resource == "" {
		return nil
	}
	if filepath.IsAbs(resource) {
		return fmt.Errorf("%s link %q: must be a non-empty relative path", source, destination)
	}
	path, err := safeChallengePath(root, filepath.Join(filepath.Dir(source), resource))
	if err != nil {
		return fmt.Errorf("%s link %q: %w", source, destination, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s link %q does not resolve to a bundled resource: %w", source, destination, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return fmt.Errorf("%s link %q must resolve to a regular bundled file", source, destination)
	}
	return nil
}
