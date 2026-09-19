export const nodeRuntimeFixture = {
	title: "Node 运行时验收",
	searchTerm: "运行时",
	answerCommand: "/bin/bash /opt/breakfix/runnable/nodes/host/actions/apply.sh",
} as const;

export const k8sReproductionCoreFixture = {
	title: "Kubernetes 复现核心验收",
	searchTerm: "Kubernetes",
} as const;

// The core profile publishes a K8s-only Catalog: the Node fixture entry
// cannot materialize without an Incus provider. Browsing assertions use the
// fixture the prepared target actually carries.
const coreProfile = process.env.BREAKFIX_E2E_PROFILE === "core";
export const browsableFixture = coreProfile ? k8sReproductionCoreFixture : nodeRuntimeFixture;
