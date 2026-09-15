export const nodeRuntimeFixture = {
	title: "Node 运行时验收",
	searchTerm: "运行时",
	answerCommand: "/bin/bash /opt/breakfix/runnable/nodes/host/actions/apply.sh",
} as const;

export const k8sReproductionCoreFixture = {
	title: "Kubernetes 复现核心验收",
	searchTerm: "Kubernetes",
} as const;
