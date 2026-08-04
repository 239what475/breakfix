package challenge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidatePortableDir validates candidate source that can be built on any
// target platform. Publication fields remain forbidden so source identity is
// never confused with a runtime artifact or challenge identity.
func ValidatePortableDir(dir string) (*Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, "challenge.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read challenge.yaml: %w", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse challenge.yaml: %w", err)
	}
	if spec == nil {
		return nil, errors.New("challenge.yaml must be a mapping")
	}

	var errs []string
	for _, field := range []string{"id", "source_slug", "image", "content_revision", "published_at"} {
		if _, exists := spec[field]; exists {
			errs = append(errs, fmt.Sprintf("challenge.yaml 不得包含平台托管字段 %q", field))
		}
	}
	runtime := manifestScalar(spec["runtime"])
	if runtime != RuntimeNode && runtime != RuntimeK8s {
		errs = append(errs, fmt.Sprintf("challenge.yaml runtime 必须明确为 node 或 k8s，当前为 %q", runtime))
	}
	if manifestScalar(spec["title"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 title")
	}
	switch manifestScalar(spec["difficulty"]) {
	case "easy", "medium", "hard":
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml difficulty 必须为 easy/medium/hard，当前为 %q", manifestScalar(spec["difficulty"])))
	}
	if _, exists := spec["tags"]; exists {
		errs = append(errs, "challenge.yaml 不得包含 tags；分类由 Roadmap 维护")
	}
	if manifestScalar(spec["description"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 description")
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	var typed Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&typed); err != nil {
		return nil, fmt.Errorf("parse challenge.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("challenge.yaml must contain one YAML document")
		}
		return nil, fmt.Errorf("parse challenge.yaml: %w", err)
	}
	return ValidateCandidateDir(dir)
}

func manifestScalar(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
