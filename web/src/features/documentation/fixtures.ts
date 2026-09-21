import type {
	DocumentationPageResponse,
	DocumentationTreeResponse,
} from "../../api/generated";

// Small-scale mirrors of the pinned library fixtures the E2E reader walks:
// the outline path down to the pod-lifecycle page and a page whose markdown
// carries the generated content shapes (heading anchor, alert, code fence).

export const treeRoots: DocumentationTreeResponse = {
	nodes: [{ title: "Concepts", path: "/docs/concepts", has_children: true }],
};

export const treeConcepts: DocumentationTreeResponse = {
	nodes: [{ title: "Workloads", path: "/docs/concepts/workloads", has_children: true }],
};

export const treeWorkloads: DocumentationTreeResponse = {
	nodes: [{ title: "Pods", path: "/docs/concepts/workloads/pods", has_children: true }],
};

export const treePods: DocumentationTreeResponse = {
	nodes: [{ title: "Pod Lifecycle", path: "/docs/concepts/workloads/pods/pod-lifecycle", has_children: false }],
};

export function treeAt(path?: string): DocumentationTreeResponse {
	switch (path) {
		case undefined:
			return treeRoots;
		case "/docs/concepts":
			return treeConcepts;
		case "/docs/concepts/workloads":
			return treeWorkloads;
		case "/docs/concepts/workloads/pods":
			return treePods;
		default:
			return { nodes: [] };
	}
}

export const podLifecyclePath = "docs/concepts/workloads/pods/pod-lifecycle";
export const podLifecycleUrlPath = "/docs/concepts/workloads/pods/pod-lifecycle/";

export const podLifecyclePage: DocumentationPageResponse = {
	path: podLifecyclePath,
	page_kind: "content",
	title: "Pod Lifecycle",
	digest: "sha256:fixture",
	markdown: [
		"# Pod Lifecycle",
		"",
		"This page describes the Pod lifecycle.",
		"",
		"> [!NOTE]",
		"> kubelet restarts the container when it exits.",
		"",
		"## Pod Lifetime",
		"",
		"A Pod tracks a phase over its lifetime.",
		"",
		"```bash",
		"kubectl get pods",
		"```",
		"",
	].join("\n"),
	anchors: [{ id: "pod-lifetime", level: 2, title: "Pod Lifetime" }],
	assets: [],
};
