package scenario

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
// never confused with a runtime artifact or scenario identity.
func ValidatePortableDir(dir string) (*Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read scenario.yaml: %w", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse scenario.yaml: %w", err)
	}
	if spec == nil {
		return nil, errors.New("scenario.yaml must be a mapping")
	}

	var errs []string
	for _, field := range []string{"id", "source_slug", "image", "content_revision", "published_at"} {
		if _, exists := spec[field]; exists {
			errs = append(errs, fmt.Sprintf("scenario.yaml 不得包含平台托管字段 %q", field))
		}
	}
	runtime := manifestScalar(spec["runtime"])
	if runtime != RuntimeNode && runtime != RuntimeK8s {
		errs = append(errs, fmt.Sprintf("scenario.yaml runtime 必须明确为 node 或 k8s，当前为 %q", runtime))
	}
	if manifestScalar(spec["title"]) == "" {
		errs = append(errs, "scenario.yaml 缺少 title")
	}
	typeValue := NormalizeScenarioType(manifestScalar(spec["type"]))
	if !typeValue.Valid() {
		errs = append(errs, fmt.Sprintf("scenario.yaml type 必须为 operations-scenario，当前为 %q", typeValue))
	}
	if tags, exists := spec["tags"]; exists {
		var values []string
		encoded, marshalErr := yaml.Marshal(tags)
		if marshalErr != nil {
			errs = append(errs, fmt.Sprintf("scenario.yaml tags 无效: %v", marshalErr))
		} else if unmarshalErr := yaml.Unmarshal(encoded, &values); unmarshalErr != nil {
			errs = append(errs, fmt.Sprintf("scenario.yaml tags 必须是字符串数组: %v", unmarshalErr))
		} else if _, tagErr := NormalizeTags(values); tagErr != nil {
			errs = append(errs, fmt.Sprintf("scenario.yaml tags 无效: %v", tagErr))
		}
	}
	if manifestScalar(spec["description"]) == "" {
		errs = append(errs, "scenario.yaml 缺少 description")
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	var typed Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&typed); err != nil {
		return nil, fmt.Errorf("parse scenario.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("scenario.yaml must contain one YAML document")
		}
		return nil, fmt.Errorf("parse scenario.yaml: %w", err)
	}
	return ValidateCandidateDir(dir)
}

func manifestScalar(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
