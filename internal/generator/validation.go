package generator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"gopkg.in/yaml.v3"
)

// Candidate is the immutable archive inspected by the Generator and Judge
// before it can be submitted for real verification. The Worker only holds it
// in memory; Server is the authority that persists a passed candidate.
type Candidate struct {
	Archive []byte
	Entry   challenge.Entry
	Files   []CandidateFile
}

// CandidateFile is deliberately limited to regular workspace files. It is
// used as untrusted data in the Judge prompt, never as instruction text.
type CandidateFile struct {
	Path    string
	Content string
}

// InspectCandidateArchive validates exactly the archive returned by the
// workspace. It makes no metadata substitutions or normalizations: platform
// fields are rejected so a model protocol mistake cannot silently change the
// candidate that reaches VerifyTask.
func InspectCandidateArchive(archive []byte) (*Candidate, error) {
	if len(archive) == 0 {
		return nil, errors.New("generator candidate archive is empty")
	}
	dir, err := os.MkdirTemp("", "breakfix-generator-candidate-")
	if err != nil {
		return nil, fmt.Errorf("create candidate staging: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	if err := challenge.ExtractTarGz(dir, bytes.NewReader(archive)); err != nil {
		return nil, fmt.Errorf("extract generator candidate: %w", err)
	}
	entry, err := ValidateCandidateDir(dir)
	if err != nil {
		return nil, err
	}
	if err := ValidateCandidateSemantics(dir); err != nil {
		return nil, err
	}
	files, err := candidateFiles(dir)
	if err != nil {
		return nil, err
	}
	return &Candidate{Archive: append([]byte(nil), archive...), Entry: *entry, Files: files}, nil
}

// ValidateCandidateDir validates a generator-owned challenge directory. In
// contrast with a published catalog entry, it must not contain platform-owned
// id, image, or publication metadata. They are added only by publication.
func ValidateCandidateDir(chalDir string) (*challenge.Entry, error) {
	path := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(path)
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
	for _, field := range []string{"id", "image", "published_at"} {
		if _, exists := spec[field]; exists {
			errs = append(errs, fmt.Sprintf("challenge.yaml 不得包含平台托管字段 %q", field))
		}
	}
	runtime := scalarString(spec["runtime"])
	if runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s {
		errs = append(errs, fmt.Sprintf("challenge.yaml runtime 必须明确为 node 或 k8s，当前为 %q", runtime))
	}
	if scalarString(spec["title"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 title")
	}
	switch scalarString(spec["difficulty"]) {
	case "easy", "medium", "hard":
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml difficulty 必须为 easy/medium/hard，当前为 %q", scalarString(spec["difficulty"])))
	}
	if _, exists := spec["tags"]; exists {
		errs = append(errs, "challenge.yaml 不得包含 tags；分类由 taxonomy workflow 维护")
	}
	if scalarString(spec["description"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 description")
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	entry, err := challenge.ValidateSubmissionDir(chalDir)
	if err != nil {
		return nil, fmt.Errorf("validate challenge structure: %w", err)
	}
	return entry, nil
}

// ValidateCandidateSemantics contains deterministic policy checks that cannot
// be delegated to the Judge model. It never writes the workspace.
func ValidateCandidateSemantics(chalDir string) error {
	entry, err := ValidateCandidateDir(chalDir)
	if err != nil {
		return err
	}
	return validateCandidateSemantics(chalDir, entry)
}

func candidateFiles(root string) ([]CandidateFile, error) {
	files := make([]CandidateFile, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("candidate contains unsupported file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, CandidateFile{Path: filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read candidate files: %w", err)
	}
	return files, nil
}

func validateCandidateSemantics(chalDir string, entry *challenge.Entry) error {
	var errs []string
	if entry.Runtime == challenge.RuntimeK8s {
		checkpointData, err := os.ReadFile(filepath.Join(chalDir, "k8s", "checks.sh"))
		if err != nil {
			return fmt.Errorf("read k8s/checks.sh: %w", err)
		}
		checkpointText := string(checkpointData)
		if err := validateKubectlPodReadinessPattern(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoEphemeralProbePods(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoNaivePodHealthLoop(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoNaivePodGrepFilter(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
	} else {
		for _, node := range entry.Nodes {
			for _, script := range []string{"generate.sh", "answer.sh", "checks.sh"} {
				path := filepath.Join(chalDir, "nodes", node.Name, script)
				data, err := os.ReadFile(path)
				if os.IsNotExist(err) && script == "checks.sh" {
					continue
				}
				if err != nil {
					return fmt.Errorf("read nodes/%s/%s: %w", node.Name, script, err)
				}
				if err := validateNodeScriptBoundary(filepath.ToSlash(filepath.Join("nodes", node.Name, script)), string(data)); err != nil {
					errs = append(errs, err.Error())
				}
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateNodeScriptBoundary(path, script string) error {
	normalized := strings.ToLower(script)
	for _, forbidden := range []string{"kubectl", "kubeconfig", "vcluster", "incus "} {
		if strings.Contains(normalized, forbidden) {
			return fmt.Errorf("%s 不得使用 Kubernetes、vcluster 或 Incus API（检测到 %q）", path, forbidden)
		}
	}
	return nil
}

func validateKubectlPodReadinessPattern(verifyText string) error {
	normalized := strings.ReplaceAll(verifyText, " ", "")
	normalized = strings.ReplaceAll(normalized, "\t", "")

	badPatterns := []string{
		"Running\\s+1/1",
		"Running[[:space:]]+1/1",
		"Running.*1/1",
	}
	for _, pattern := range badPatterns {
		if strings.Contains(normalized, strings.ReplaceAll(pattern, " ", "")) {
			return fmt.Errorf("k8s/checks.sh 对 `kubectl get pods --no-headers` 的 READY/STATUS 列顺序判断错误：检测到 %q，这会把 `1/1   Running` 误判为失败；应按 `1/1` 在前、`Running` 在后设计匹配", pattern)
		}
	}
	return nil
}

func validateVClusterCheckpointNoEphemeralProbePods(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	badSnippets := []string{
		"kubectl run",
		"busybox:1.36",
		"busybox:stable",
		"--rm -i --restart=never --image=",
	}
	for _, snippet := range badSnippets {
		if strings.Contains(normalized, snippet) {
			return fmt.Errorf("runtime=k8s 的 k8s/checks.sh 不应依赖 `kubectl run` 拉外部探测镜像或临时 Pod；这会引入镜像可用性和时序不稳定，请改用现有工作负载、Service、endpoints 或 port-forward 等平台内可闭环的验证方式")
		}
	}
	return nil
}

func validateVClusterCheckpointNoNaivePodHealthLoop(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	requiredSignals := []string{
		"kubectl get pods",
		"while ifs= read -r",
		"awk '{print $3}'",
		"awk '{print $2}'",
		"!= \"running\"",
		"!= \"1/1\"",
	}
	for _, signal := range requiredSignals {
		if !strings.Contains(normalized, signal) {
			return nil
		}
	}
	if strings.Contains(normalized, "deletiontimestamp") || strings.Contains(normalized, "ownerreferences") || strings.Contains(normalized, "rollout status") {
		return nil
	}
	return fmt.Errorf("runtime=k8s 的 k8s/checks.sh 不应通过遍历标签下的所有 Pod 并硬判 `Running 1/1` 来验收；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或只检查最终目标 Pod 集，并显式忽略 deletionTimestamp 不为空的旧 Pod")
}

func validateVClusterCheckpointNoNaivePodGrepFilter(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	if !strings.Contains(normalized, "kubectl get pods") {
		return nil
	}
	if !strings.Contains(normalized, "grep -v") {
		return nil
	}
	if !strings.Contains(normalized, "1/1") || !strings.Contains(normalized, "running") {
		return nil
	}
	if strings.Contains(normalized, "deletiontimestamp") || strings.Contains(normalized, "ownerreferences") || strings.Contains(normalized, "rollout status") {
		return nil
	}
	return fmt.Errorf("runtime=k8s 的 k8s/checks.sh 不应通过 `kubectl get pods ... | grep -v ... 1/1 ... Running` 这类全量 Pod 过滤方式直接判失败；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或显式过滤 deletionTimestamp 不为空的旧 Pod")
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
