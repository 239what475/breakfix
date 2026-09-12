package scenario

import "strings"

const (
	RuntimeNode = "node"
	RuntimeK8s  = "k8s"
)

func NormalizeRuntime(runtime string) string {
	return strings.TrimSpace(runtime)
}
