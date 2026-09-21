#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}
state_dir=${BREAKFIX_E2E_STATE_DIR:-$repo_root/.local/e2e/${BREAKFIX_E2E_TARGET:-e2e}}
docs_fixture_root=$repo_root/test/fixtures/docs-project
library_dir=$state_dir/document-library
target_script=$repo_root/scripts/kind/e2e-target.sh

fail() { printf 'Breakfix documentation E2E prepare: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"; }

for tool in base64 curl docker go jq kubectl make sed tr; do require_command "$tool"; done
[ -f "$docs_fixture_root/build-info.json" ] || fail "docs-project fixture is incomplete; run make docs-fixture"

port_forward_pid=
port_forward_log=$state_dir/doc-prepare-port-forward.log

start_port_forward() {
	local_port=$1
	: >"$port_forward_log"
	kubectl -n "$namespace" port-forward --address 127.0.0.1 service/breakfix-server "$local_port:9090" >"$port_forward_log" 2>&1 &
	port_forward_pid=$!
}

stop_port_forward() {
	if [ -n "$port_forward_pid" ] && kill -0 "$port_forward_pid" >/dev/null 2>&1; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" >/dev/null 2>&1 || true
	fi
	port_forward_pid=
}

trap stop_port_forward EXIT HUP INT TERM

# Prepare the ordinary disposable target first so this suite inherits its
# isolated database, Registry, Incus projects, and immutable runtime snapshot.
# The Server rollout is deferred: this script patches documentation config
# into the deployment afterwards and owns the single final rollout, the
# fixture Catalog projection wait, and the prepared marker.
BREAKFIX_E2E_DEFER_SERVER_RESTART=1 make -C "$repo_root" --no-print-directory e2e-prepare

# The verification pods run inside the vcluster environments hosted on the
# Kind node; landing the small workload image in the node's containerd keeps
# the first wait short. `kind load` rejects the multi-arch busybox manifest
# because the local store only holds the host platform, and the node cannot
# reach docker.io directly - so the host platform blobs travel over through
# docker save instead.
kind_cluster=${BREAKFIX_E2E_KIND_CLUSTER:-breakfix-e2e}
kind_node=${BREAKFIX_E2E_KIND_NODE:-${kind_cluster}-control-plane}
docker image inspect busybox:1.36.1 >/dev/null 2>&1 || docker pull busybox:1.36.1 >/dev/null
docker save busybox:1.36.1 | docker exec -i "$kind_node" ctr --namespace=k8s.io images import - >/dev/null

# The server consumes a real docs-project library, not a hand-made snapshot:
# the committed rendered fixture pages are projected by the same generator that
# produces the full library, so page digests match docs-site/documents exactly.
generator_version=${DOCS_PROJECT_VERSION:-docs-project-v10}
library_pages="docs/concepts/workloads/pods/pod-lifecycle/,docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/,docs/concepts/services-networking/ingress/"
rm -rf "$library_dir"
go run "$repo_root/cmd/docs-project" -root "$docs_fixture_root" -out "$library_dir" \
	-version "$generator_version" -site-origin https://kubernetes.io \
	-pages "$library_pages" || fail "docs-project library generation failed"
[ -s "$library_dir/manifest.json" ] || fail "generated documentation library is incomplete"

# ConfigMap keys are flat; deploy/manifests/server.yaml maps them back to
# library paths with volume items. Server-side apply avoids the client-side
# last-applied annotation, which would exceed the 256KiB annotation limit for
# a library of this size.
library_args=""
for file in $(find "$library_dir" -type f | sort); do
	key=$(printf '%s' "${file#"$library_dir"/}" | tr '/' '_')
	library_args="$library_args --from-file=$key=$file"
done
# shellcheck disable=SC2086
kubectl -n "$namespace" create configmap breakfix-documentation-library $library_args \
	--dry-run=client -o yaml | kubectl apply --server-side --force-conflicts -f - >/dev/null

# The deployment mounts the full library through the dedicated library image by
# default. The E2E target keeps the mini library on its ConfigMap path: swap
# only the documentation-library volume source back to the ConfigMap and leave
# every other volume untouched.
patched_volumes=$(kubectl -n "$namespace" get deployment breakfix-server -o json | jq -c '
	.spec.template.spec.volumes | map(
		if .name == "documentation-library" then
			{
				name: "documentation-library",
				configMap: {
					name: "breakfix-documentation-library",
					items: [
						{key: "manifest.json", path: "manifest.json"},
						{key: "docs_concepts_workloads_pods_pod-lifecycle_index.md", path: "docs/concepts/workloads/pods/pod-lifecycle/index.md"},
						{key: "docs_concepts_workloads_pods_pod-lifecycle_index.json", path: "docs/concepts/workloads/pods/pod-lifecycle/index.json"},
						{key: "docs_concepts_workloads_autoscaling_horizontal-pod-autoscale_index.md", path: "docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/index.md"},
						{key: "docs_concepts_workloads_autoscaling_horizontal-pod-autoscale_index.json", path: "docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/index.json"},
						{key: "docs_concepts_services-networking_ingress_index.md", path: "docs/concepts/services-networking/ingress/index.md"},
						{key: "docs_concepts_services-networking_ingress_index.json", path: "docs/concepts/services-networking/ingress/index.json"},
						{key: "images_docs_pod.svg", path: "images/docs/pod.svg"}
					]
				}
			}
		else . end
	)')
[ -n "$patched_volumes" ] && [ "$patched_volumes" != "null" ] || fail "Breakfix Server deployment has no volumes to patch"
kubectl -n "$namespace" patch deployment breakfix-server --type merge \
	-p "$(jq -cn --argjson volumes "$patched_volumes" '{spec:{template:{spec:{volumes:$volumes}}}}')" >/dev/null

config_name=$(kubectl -n "$namespace" get deployment breakfix-server -o json |
	jq -r '.spec.template.spec.volumes[] | select(.name == "config") | .configMap.name // empty')
[ -n "$config_name" ] || fail "Breakfix Server deployment has no config ConfigMap volume"
config=$(kubectl -n "$namespace" get configmap "$config_name" -o json | jq -r '.data["config.yaml"]')
[ -n "$config" ] && [ "$config" != "null" ] || fail "config ConfigMap $config_name has no config.yaml"
# The playground capacity gate is pinned to a single concurrent session: the
# suite's second user must be rejected while the first session occupies the
# only slot. The value stays a quoted string — the config parser expects one.
config=$(printf '%s\n' "$config" | sed \
	-e 's#^  library_root: .*#  library_root: /var/lib/breakfix/documentation/library#' \
	-e 's#^  max_active: .*#  max_active: "1"#')
kubectl -n "$namespace" patch configmap "$config_name" --type merge --patch "$(jq -cn --arg config "$config" '{data:{"config.yaml":$config}}')" >/dev/null

# The blank practice scenario needs no model credential: the documentation
# surface is the parsed library plus real vk8s environments. Restart the
# Server once so the patched configuration takes effect.
kubectl -n "$namespace" rollout restart deployment/breakfix-server >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null

# The deferred e2e-prepare handed its fixture reference over; finish its job
# now that the Server runs with every documentation patch in place.
catalog_reference=$(sed -n '1p' "$state_dir/catalog-reference")
[ -n "$catalog_reference" ] || fail "deferred e2e-prepare left no catalog reference in $state_dir/catalog-reference"
case "$catalog_reference" in
	*/catalog/*@sha256:*) ;;
	*) fail "deferred catalog reference has an unexpected shape: $catalog_reference" ;;
esac
configured_port=$(sed -n '1p' "$state_dir/ui-origin-port")
case "$configured_port" in
	''|*[!0-9]*) fail "prepared target is missing a valid ui-origin-port" ;;
esac
ui_origin=http://127.0.0.1:$configured_port

start_port_forward "$configured_port"
base_url=$ui_origin
fixture_title='Node 运行时验收'
fixture_runtime=node
fixture_count=2
expect_node=1
if [ "${BREAKFIX_E2E_PROFILE:-full}" = core ]; then
	fixture_count=1
	expect_node=0
fi
catalog_json=$state_dir/catalog-projection.json
deadline=$(( $(date +%s) + ${BREAKFIX_E2E_PREPARE_TIMEOUT_SECONDS:-900} ))
while [ "$(date +%s)" -lt "$deadline" ]; do
	if curl --fail --silent --show-error "$base_url/readyz" >/dev/null 2>&1 &&
		curl --fail --silent --show-error "$base_url/api/operations/scenarios" >"$catalog_json" 2>/dev/null &&
		jq -e \
			--arg title "$fixture_title" \
			--arg runtime "$fixture_runtime" \
			--argjson count "$fixture_count" \
			--argjson expect_node "$expect_node" \
			'
				(.scenarios | length) == $count and
				(($expect_node == 1 and any(.scenarios[]; .title == $title and .runtime == $runtime and (.scenario_tags | sort) == ["linux", "runtime-fixture"])) or $expect_node == 0) and
				any(.scenarios[]; .title == "Kubernetes 复现核心验收" and .runtime == "k8s" and (.scenario_tags | sort) == ["kubernetes", "runtime-fixture"])
			' "$catalog_json" >/dev/null; then
		break
	fi
	sleep 2
done
jq -e \
	--arg title "$fixture_title" \
	--arg runtime "$fixture_runtime" \
	--argjson count "$fixture_count" \
	--argjson expect_node "$expect_node" \
	'
		(.scenarios | length) == $count and
		(($expect_node == 1 and any(.scenarios[]; .title == $title and .runtime == $runtime and (.scenario_tags | sort) == ["linux", "runtime-fixture"])) or $expect_node == 0) and
		any(.scenarios[]; .title == "Kubernetes 复现核心验收" and .runtime == "k8s" and (.scenario_tags | sort) == ["kubernetes", "runtime-fixture"])
	' "$catalog_json" >/dev/null || fail "fixture Catalog did not reach the expected public projection before timeout"
stop_port_forward
"$target_script" mark-prepared "$catalog_reference" "$ui_origin"

printf 'Prepared documentation fixture on Kind target %s.\n' "$kind_cluster"
