package generator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"gopkg.in/yaml.v3"
)

func (g *Generator) validateChallengeManifest(chalDir string) error {
	path := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read challenge.yaml: %w", err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse challenge.yaml: %w", err)
	}
	if spec == nil {
		spec = map[string]any{}
	}

	delete(spec, "id")
	delete(spec, "image")

	var errs []string
	if scalarString(spec["type"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 type")
	} else if scalarString(spec["type"]) != challenge.TypeScript {
		errs = append(errs, fmt.Sprintf("challenge.yaml type 必须为 %s，当前为 %q", challenge.TypeScript, scalarString(spec["type"])))
	}
	switch challenge.NormalizeRuntime(scalarString(spec["runtime"])) {
	case challenge.RuntimeContainer, challenge.RuntimeVCluster:
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml runtime 必须为 container/vcluster，当前为 %q", scalarString(spec["runtime"])))
	}
	if scalarString(spec["title"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 title")
	}
	switch scalarString(spec["difficulty"]) {
	case "":
		errs = append(errs, "challenge.yaml 缺少 difficulty")
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
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	normalized, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal challenge.yaml: %w", err)
	}
	if err := os.WriteFile(path, normalized, 0644); err != nil {
		return fmt.Errorf("write challenge.yaml: %w", err)
	}
	return nil
}

func (g *Generator) validateChallengeSemantics(chalDir string) error {
	entry, err := loadChallengeEntry(chalDir)
	if err != nil {
		return err
	}
	dockerfileData, err := os.ReadFile(filepath.Join(chalDir, "Dockerfile"))
	if err != nil {
		return fmt.Errorf("read Dockerfile: %w", err)
	}

	checkpointPath := filepath.Join(chalDir, "checks", "checkpoints.sh")
	checkpointData, err := os.ReadFile(checkpointPath)
	if err != nil {
		return fmt.Errorf("read checks/checkpoints.sh: %w", err)
	}
	checkpointText := string(checkpointData)

	var errs []string
	if err := validateDockerfileNoBuildNetwork(string(dockerfileData)); err != nil {
		errs = append(errs, err.Error())
	}
	if challenge.NormalizeRuntime(entry.Runtime) == challenge.RuntimeVCluster {
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
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateDockerfileNoBuildNetwork(dockerfile string) error {
	normalized := strings.ToLower(dockerfile)
	for _, pattern := range []string{
		"apt-get", "apt install", "apk add", "yum install", "dnf install",
		"pip ", "npm install", "go install", "curl", "wget", "git clone", "add http",
	} {
		if strings.Contains(normalized, pattern) {
			return fmt.Errorf("Dockerfile 不得在构建期使用 %q 联网安装或下载内容；VerifyTask 构建没有外网。请只使用基础镜像已有工具，并把题目文件随 artifact 提供", pattern)
		}
	}
	return nil
}

func loadChallengeEntry(chalDir string) (*challenge.Entry, error) {
	return challenge.ValidateSubmissionDir(chalDir)
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
			return fmt.Errorf("checks/checkpoints.sh 对 `kubectl get pods --no-headers` 的 READY/STATUS 列顺序判断错误：检测到 %q，这会把 `1/1   Running` 误判为失败；应按 `1/1` 在前、`Running` 在后设计匹配", pattern)
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
			return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应依赖 `kubectl run` 拉外部探测镜像或临时 Pod；这会引入镜像可用性和时序不稳定，请改用现有工作负载、Service、endpoints 或 port-forward 等平台内可闭环的验证方式")
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
	return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应通过遍历标签下的所有 Pod 并硬判 `Running 1/1` 来验收；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或只检查最终目标 Pod 集，并显式忽略 deletionTimestamp 不为空的旧 Pod")
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
	return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应通过 `kubectl get pods ... | grep -v ... 1/1 ... Running` 这类全量 Pod 过滤方式直接判失败；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或显式过滤 deletionTimestamp 不为空的旧 Pod")
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
