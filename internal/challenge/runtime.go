package challenge

import "strings"

const (
	RuntimeContainer = "container"
	RuntimeVCluster  = "vcluster"
	TypeScript       = "script"
)

func NormalizeRuntime(runtime string) string {
	switch strings.TrimSpace(runtime) {
	case "", RuntimeContainer:
		return RuntimeContainer
	case RuntimeVCluster:
		return RuntimeVCluster
	default:
		return strings.TrimSpace(runtime)
	}
}
