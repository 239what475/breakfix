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
encode() { printf '%s' "$1" | base64 | tr -d '\n'; }

for tool in base64 docker go jq kubectl make tr; do require_command "$tool"; done
[ -f "$docs_fixture_root/build-info.json" ] || fail "docs-project fixture is incomplete; run make docs-fixture"

# Prepare the ordinary disposable target first so this suite inherits its
# isolated database, Registry, Incus projects, and immutable runtime snapshot.
make -C "$repo_root" --no-print-directory e2e-prepare
"$target_script" assert-prepared

fixture_build_dir=$state_dir/document-agent-fixture-build
mkdir -p "$fixture_build_dir"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$fixture_build_dir/document-agent-fixture" "$repo_root/cmd/document-agent-fixture"
docker build --platform linux/amd64 --provenance=false -t breakfix/document-agent-fixture:e2e \
	-f "$repo_root/build/images/document-agent-fixture/Dockerfile" "$fixture_build_dir" >/dev/null
kind_cluster=${BREAKFIX_E2E_KIND_CLUSTER:-breakfix-e2e}
kind load docker-image --name "$kind_cluster" breakfix/document-agent-fixture:e2e >/dev/null
kubectl -n "$namespace" apply -f "$repo_root/test/kind/document-agent-fixture.yaml" >/dev/null
# The fixture image is rebuilt under a stable E2E tag. Restart its Deployment
# so Kubernetes does not keep serving an earlier locally loaded image.
kubectl -n "$namespace" rollout restart deployment/document-agent-fixture >/dev/null
kubectl -n "$namespace" rollout status deployment/document-agent-fixture --timeout=2m >/dev/null

# The server consumes a real docs-project library, not a hand-made snapshot:
# the committed rendered fixture pages are projected by the same generator that
# produces the full library, so page digests match docs-site/documents exactly.
generator_version=${DOCS_PROJECT_VERSION:-docs-project-v10}
library_page=docs/concepts/workloads/pods/pod-lifecycle/
rm -rf "$library_dir"
go run "$repo_root/cmd/docs-project" -root "$docs_fixture_root" -out "$library_dir" \
	-version "$generator_version" -site-origin https://kubernetes.io \
	-pages "$library_page" || fail "docs-project library generation failed"
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
config=$(printf '%s\n' "$config" | sed \
	-e 's#^  library_root: .*#  library_root: /var/lib/breakfix/documentation/library#' \
	-e 's#base_url: https://api.deepseek.com#base_url: http://document-agent-fixture:8080#' \
	-e 's#model: deepseek-v4-pro#model: documentation-fixture#')
kubectl -n "$namespace" patch configmap "$config_name" --type merge --patch "$(jq -cn --arg config "$config" '{data:{"config.yaml":$config}}')" >/dev/null

# The fixture implements the same authenticated chat-completions wire contract,
# but does not need a real model credential. The target restore point returns
# this Secret to its original bytes during reset.
kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch \
	"$(jq -cn --arg value "documentation-fixture-key" --arg encoded "$(encode documentation-fixture-key)" '{data:{deepseek_api_key:$encoded}}')" >/dev/null
kubectl -n "$namespace" rollout restart deployment/breakfix-server >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null

printf 'Prepared documentation fixture on Kind target %s.\n' "$kind_cluster"
