#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}
runtime_snapshot_secret=${BREAKFIX_E2E_RUNTIME_SNAPSHOT_SECRET:-breakfix-e2e-runtime-original}
target_id=${BREAKFIX_E2E_TARGET:-e2e}
kind_cluster=${BREAKFIX_E2E_KIND_CLUSTER:-breakfix-e2e}
incus_remote=${BREAKFIX_E2E_INCUS_REMOTE:-incus-cluster}
build_project=${BREAKFIX_E2E_INCUS_BUILD_PROJECT:-breakfix-e2e-build}
image_project=${BREAKFIX_E2E_INCUS_IMAGE_PROJECT:-breakfix-e2e-images}
name_prefix=${BREAKFIX_E2E_INCUS_NAME_PREFIX:-e2e}
source_build_project=${BREAKFIX_E2E_SOURCE_INCUS_BUILD_PROJECT:-breakfix-build}
source_image_project=${BREAKFIX_E2E_SOURCE_INCUS_IMAGE_PROJECT:-breakfix-images}
base_image_alias=${BREAKFIX_E2E_INCUS_BASE_IMAGE_ALIAS:-node-systemd-base-v1}
build_profile=${BREAKFIX_E2E_INCUS_BUILD_PROFILE:-breakfix-bootstrap}
storage_pool=${BREAKFIX_E2E_INCUS_STORAGE_POOL:-local}
build_network=${BREAKFIX_E2E_INCUS_BUILD_NETWORK:-bf-bootstrap}
opensandbox_service=${BREAKFIX_E2E_OPENSANDBOX_SERVICE:-opensandbox-server}
profile=${BREAKFIX_E2E_PROFILE:-full}
marker_name=breakfix-e2e-target
prepared_marker_name=breakfix-e2e-prepared
target_label_key=breakfix.dev/e2e-target
command=${1:-}
opensandbox_port_forward_pid=
opensandbox_port_forward_log=
opensandbox_port=

fail() {
	printf 'Breakfix E2E target: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_kind_target() {
	context=$(kubectl config current-context 2>/dev/null || true)
	[ "$context" = "kind-$kind_cluster" ] || {
		fail "requires context kind-$kind_cluster; current context is ${context:-unset}. Set BREAKFIX_E2E_KIND_CLUSTER only for a dedicated Kind target"
	}
}

validate_configuration() {
	case "$target_id" in
		'' | *[!a-z0-9-]* | -* | *-)
			fail "BREAKFIX_E2E_TARGET must be a lowercase target identifier"
			;;
	esac
	case "$kind_cluster" in
		'' | *[!a-z0-9-]* | -* | *-)
			fail "BREAKFIX_E2E_KIND_CLUSTER must be a Kind cluster name"
			;;
	esac
	printf '%s\n' "$name_prefix" | grep -Eq '^[a-z][a-z0-9-]{0,5}$' ||
		fail "BREAKFIX_E2E_INCUS_NAME_PREFIX must be 1-6 lowercase letters, digits, or hyphens and begin with a letter"
	printf '%s\n' "$opensandbox_service" | grep -Eq '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' ||
		fail "BREAKFIX_E2E_OPENSANDBOX_SERVICE must be a Kubernetes Service name"
	for project in "$build_project" "$image_project"; do
		case "$project" in
			'' | default | "$source_build_project" | "$source_image_project")
				fail "E2E Incus projects must be explicit projects distinct from default and shared projects"
				;;
		esac
	done
	[ "$build_project" != "$image_project" ] || fail "E2E build and image projects must be distinct"
}

secret_value() {
	kubectl -n "$namespace" get secret "$runtime_secret" -o json |
		jq -r --arg key "$1" 'if .data[$key] == null then "" else .data[$key] | @base64d end'
}

encode() {
	printf '%s' "$1" | base64 | tr -d '\n'
}

snapshot_runtime_secret() {
	if kubectl -n "$namespace" get secret "$runtime_snapshot_secret" >/dev/null 2>&1; then
		owner=$(kubectl -n "$namespace" get secret "$runtime_snapshot_secret" -o json |
			jq -r --arg key "$target_label_key" '.metadata.labels[$key] // ""')
		[ "$owner" = "$target_id" ] ||
			fail "refusing to overwrite runtime Secret snapshot $namespace/$runtime_snapshot_secret owned by target ${owner:-unknown}"
		return
	fi
	kubectl -n "$namespace" get secret "$runtime_secret" -o json |
		jq --arg name "$runtime_snapshot_secret" --arg target "$target_id" --arg label "$target_label_key" '
			{
				apiVersion: "v1",
				kind: "Secret",
				metadata: {
					name: $name,
					namespace: .metadata.namespace,
					labels: {($label): $target}
				},
				type: (.type // "Opaque"),
				data: (.data // {})
			}' | kubectl apply -f - >/dev/null
}

verify_runtime_snapshot() {
	kubectl -n "$namespace" get secret "$runtime_snapshot_secret" >/dev/null 2>&1 ||
		fail "missing runtime Secret snapshot $namespace/$runtime_snapshot_secret; refusing to continue without a restore point"
	owner=$(kubectl -n "$namespace" get secret "$runtime_snapshot_secret" -o json |
		jq -r --arg key "$target_label_key" '.metadata.labels[$key] // ""')
	[ "$owner" = "$target_id" ] ||
		fail "runtime Secret snapshot $namespace/$runtime_snapshot_secret belongs to target ${owner:-unknown}, not $target_id"
}

mark_target() {
	require_kind_target
	validate_configuration
	require_runtime_secret
	if ! kubectl -n "$namespace" get configmap "$marker_name" >/dev/null 2>&1 &&
		kubectl -n "$namespace" get configmap "$prepared_marker_name" >/dev/null 2>&1; then
		fail "found prepared marker $namespace/$prepared_marker_name without ownership marker $namespace/$marker_name; inspect the target before continuing"
	fi
	if kubectl -n "$namespace" get configmap "$marker_name" >/dev/null 2>&1; then
		verify_marker
		verify_runtime_snapshot
		return
	fi
	initial_ui_origin=$(secret_value ui_origin)
	[ -n "$initial_ui_origin" ] || fail "runtime Secret must provide ui_origin before preparing an E2E target"
	snapshot_runtime_secret
	kubectl -n "$namespace" create configmap "$marker_name" \
		--from-literal=target_id="$target_id" \
		--from-literal=kind_cluster="$kind_cluster" \
		--from-literal=incus_build_project="$build_project" \
		--from-literal=incus_image_project="$image_project" \
		--from-literal=incus_name_prefix="$name_prefix" \
		--from-literal=opensandbox_service="$opensandbox_service" \
		--from-literal=initial_ui_origin="$initial_ui_origin" \
		--from-literal=runtime_snapshot_secret="$runtime_snapshot_secret" \
		--dry-run=client -o yaml | kubectl apply -f - >/dev/null
	printf 'Marked Kind target %s for Breakfix E2E resources.\n' "$kind_cluster"
}

mark_prepared() {
	require_kind_target
	validate_configuration
	require_runtime_secret
	[ "$#" -eq 2 ] || fail "mark-prepared requires an immutable catalog reference and exact UI origin"
	catalog_reference=$1
	ui_origin=$2
	printf '%s\n' "$catalog_reference" | grep -Eq '.+@sha256:[a-f0-9]{64}$' ||
		fail "prepared Catalog reference must be an immutable OCI digest"
	case "$ui_origin" in
		http://127.0.0.1:[0-9]*|https://127.0.0.1:[0-9]*)
			;;
		*)
			fail "prepared UI origin must be a loopback HTTP(S) origin with an explicit port"
			;;
	esac
	verify_marker
	current_reference=$(secret_value catalog_release_reference)
	[ "$current_reference" = "$catalog_reference" ] ||
		fail "runtime Secret catalog_release_reference does not match the prepared Catalog reference"
	current_origin=$(secret_value ui_origin)
	[ "$current_origin" = "$ui_origin" ] ||
		fail "runtime Secret ui_origin does not match the prepared UI origin"
	kubectl -n "$namespace" create configmap "$prepared_marker_name" \
		--from-literal=target_id="$target_id" \
		--from-literal=kind_cluster="$kind_cluster" \
		--from-literal=incus_build_project="$build_project" \
		--from-literal=incus_image_project="$image_project" \
		--from-literal=incus_name_prefix="$name_prefix" \
		--from-literal=catalog_release_reference="$catalog_reference" \
		--from-literal=ui_origin="$ui_origin" \
		--dry-run=client -o yaml | kubectl apply -f - >/dev/null
	printf 'Marked Breakfix E2E target %s as prepared with %s.\n' "$target_id" "$catalog_reference"
}

marker_value() {
	kubectl -n "$namespace" get configmap "$marker_name" -o json |
		jq -r --arg key "$1" '.data[$key] // ""'
}

verify_marker() {
	require_kind_target
	validate_configuration
	kubectl -n "$namespace" get configmap "$marker_name" >/dev/null 2>&1 ||
		fail "missing E2E target marker $namespace/$marker_name; run make e2e-prepare first"
	for item in \
		"target_id:$target_id" \
		"kind_cluster:$kind_cluster" \
		"incus_build_project:$build_project" \
		"incus_image_project:$image_project" \
		"incus_name_prefix:$name_prefix" \
		"opensandbox_service:$opensandbox_service"; do
		key=${item%%:*}
		expected=${item#*:}
		actual=$(marker_value "$key")
		[ "$actual" = "$expected" ] ||
			fail "E2E target marker $namespace/$marker_name has $key=$actual, expected $expected"
	done
	[ -n "$(marker_value initial_ui_origin)" ] ||
		fail "E2E target marker $namespace/$marker_name is missing initial_ui_origin"
	[ "$(marker_value runtime_snapshot_secret)" = "$runtime_snapshot_secret" ] ||
		fail "E2E target marker $namespace/$marker_name is missing the expected runtime Secret snapshot name"
	verify_runtime_snapshot
}

verify_prepared_marker() {
	verify_marker
	kubectl -n "$namespace" get configmap "$prepared_marker_name" >/dev/null 2>&1 ||
		fail "missing prepared E2E target marker $namespace/$prepared_marker_name; run make e2e-prepare first"
	for item in \
		"target_id:$target_id" \
		"kind_cluster:$kind_cluster" \
		"incus_build_project:$build_project" \
		"incus_image_project:$image_project" \
		"incus_name_prefix:$name_prefix"; do
		key=${item%%:*}
		expected=${item#*:}
		actual=$(kubectl -n "$namespace" get configmap "$prepared_marker_name" -o json |
			jq -r --arg key "$key" '.data[$key] // ""')
		[ "$actual" = "$expected" ] ||
			fail "prepared E2E target marker $namespace/$prepared_marker_name has $key=$actual, expected $expected"
	done
	prepared_reference=$(kubectl -n "$namespace" get configmap "$prepared_marker_name" -o json |
		jq -r '.data.catalog_release_reference // ""')
	prepared_origin=$(kubectl -n "$namespace" get configmap "$prepared_marker_name" -o json |
		jq -r '.data.ui_origin // ""')
	printf '%s\n' "$prepared_reference" | grep -Eq '.+@sha256:[a-f0-9]{64}$' ||
		fail "prepared E2E target marker has no immutable Catalog reference"
	[ "$prepared_reference" = "$(secret_value catalog_release_reference)" ] ||
		fail "runtime Secret Catalog reference differs from prepared target marker"
	[ "$prepared_origin" = "$(secret_value ui_origin)" ] ||
		fail "runtime Secret UI origin differs from prepared target marker"
}

require_runtime_secret() {
	kubectl -n "$namespace" get secret "$runtime_secret" >/dev/null 2>&1 ||
		fail "runtime Secret $namespace/$runtime_secret is required"
}

project_exists() {
	incus project show "$incus_remote:$1" >/dev/null 2>&1
}

project_target() {
	incus project get "$incus_remote:$1" user.breakfix.e2e.target 2>/dev/null || true
}

verify_owned_project() {
	project=$1
	if ! project_exists "$project"; then
		return
	fi
	owner=$(project_target "$project")
	[ "$owner" = "$target_id" ] ||
		fail "refusing to modify Incus project $project because it is not marked for E2E target $target_id"
}

ensure_project() {
	project=$1
	if project_exists "$project"; then
		verify_owned_project "$project"
	else
		incus project create "$incus_remote:$project" \
			--description "Breakfix disposable E2E target $target_id" \
			-c features.images=true \
			-c features.networks=false \
			-c features.profiles=true \
			-c features.storage.buckets=true \
			-c features.storage.volumes=true >/dev/null
	fi
	incus project set "$incus_remote:$project" \
		features.images=true \
		features.networks=false \
		features.profiles=true \
		features.storage.buckets=true \
		features.storage.volumes=true \
		restricted=true \
		restricted.cluster.target=block \
		restricted.containers.lowlevel=block \
		restricted.containers.nesting=block \
		restricted.containers.privilege=isolated \
		restricted.devices.disk=managed \
		restricted.devices.nic=managed \
		restricted.networks.access="$build_network" \
		restricted.storage-pools.access="$storage_pool" \
		user.breakfix.e2e.target="$target_id" >/dev/null
}

image_fingerprint() {
	incus image info "$incus_remote:$1" --project "$2" 2>/dev/null |
		awk '/^Fingerprint:/ { print $2; exit }'
}

ensure_image_copy() {
	source_project=$1
	target_project=$2
	fingerprint=$3
	incus image info "$incus_remote:$fingerprint" --project "$source_project" >/dev/null 2>&1 ||
		fail "source image $fingerprint is unavailable in shared Incus project $source_project"
	if ! incus image info "$incus_remote:$fingerprint" --project "$target_project" >/dev/null 2>&1; then
		incus image copy "$incus_remote:$fingerprint" "$incus_remote:" \
			--project "$source_project" --target-project "$target_project" >/dev/null
	fi
	current=$(image_fingerprint "$base_image_alias" "$target_project" || true)
	if [ "$current" != "$fingerprint" ]; then
		if [ -n "$current" ]; then
			incus image alias delete "$incus_remote:$base_image_alias" --project "$target_project" >/dev/null
		fi
		incus image alias create "$incus_remote:$base_image_alias" "$fingerprint" --project "$target_project" >/dev/null
	fi
}

ensure_build_profile() {
	incus profile show "$incus_remote:$build_profile" --project "$source_build_project" >/dev/null 2>&1 ||
		fail "source build profile $source_build_project/$build_profile is unavailable"
	if ! incus profile show "$incus_remote:$build_profile" --project "$build_project" >/dev/null 2>&1; then
		incus profile copy "$incus_remote:$build_profile" "$incus_remote:$build_profile" \
			--project "$source_build_project" --target-project "$build_project" >/dev/null
	fi
}

base_fingerprint() {
	fingerprint=${BREAKFIX_E2E_INCUS_BASE_IMAGE_FINGERPRINT:-}
	if [ -z "$fingerprint" ]; then
		fingerprint=$(secret_value incus_base_image_fingerprint)
	fi
	printf '%s\n' "$fingerprint"
}

preflight() {
	preflight_tools="awk curl docker grep jq kind kubectl make npm openssl sha256sum"
	if [ "$profile" = full ]; then
		preflight_tools="$preflight_tools incus"
	fi
	for tool in $preflight_tools; do require_command "$tool"; done
	require_kind_target
	validate_configuration
	require_runtime_secret
	if [ "$profile" = core ]; then
		# Stale Incus fields would silently re-enable the Node provider on a
		# Node-less target; refuse them instead of discovering this mid-suite.
		[ -z "$(secret_value incus_endpoint)" ] ||
			fail "core profile requires a runtime Secret without incus_endpoint"
		return
	fi
	[ -n "$(secret_value incus_endpoint)" ] ||
		fail "full profile requires the runtime Secret to provide incus_endpoint"
	fingerprint=$(base_fingerprint)
	printf '%s\n' "$fingerprint" | grep -Eq '^[a-f0-9]{64}$' ||
		fail "runtime Secret must provide a full lowercase incus_base_image_fingerprint"
	incus project show "$incus_remote:$source_build_project" >/dev/null 2>&1 ||
		fail "shared Incus build project $source_build_project is unavailable through $incus_remote"
	incus project show "$incus_remote:$source_image_project" >/dev/null 2>&1 ||
		fail "shared Incus image project $source_image_project is unavailable through $incus_remote"
	incus image info "$incus_remote:$fingerprint" --project "$source_build_project" >/dev/null 2>&1 ||
		fail "shared Incus build project does not contain base image $fingerprint"
	incus image info "$incus_remote:$fingerprint" --project "$source_image_project" >/dev/null 2>&1 ||
		fail "shared Incus image project does not contain base image $fingerprint"
	incus profile show "$incus_remote:$build_profile" --project "$source_build_project" >/dev/null 2>&1 ||
		fail "shared Incus build project does not contain profile $build_profile"
}

ensure_incus() {
	if [ "$profile" = core ]; then
		printf 'Skipped Incus E2E preparation: target %s runs the core profile.\n' "$target_id"
		return
	fi
	for tool in incus jq kubectl; do require_command "$tool"; done
	verify_marker
	require_runtime_secret
	fingerprint=$(base_fingerprint)
	printf '%s\n' "$fingerprint" | grep -Eq '^[a-f0-9]{64}$' ||
		fail "runtime Secret must provide a full lowercase incus_base_image_fingerprint"
	ensure_project "$build_project"
	ensure_project "$image_project"
	ensure_image_copy "$source_build_project" "$build_project" "$fingerprint"
	ensure_image_copy "$source_image_project" "$image_project" "$fingerprint"
	ensure_build_profile
	printf 'Prepared Incus E2E projects %s and %s for target %s.\n' \
		"$build_project" "$image_project" "$target_id"
}

configure_runtime() {
	for tool in base64 jq kubectl tr; do require_command "$tool"; done
	verify_marker
	require_runtime_secret
	if [ "$profile" = core ]; then
		patch=$(jq -cn --arg catalog "$(encode "")" '{data: {catalog_release_reference: $catalog}}')
		kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch "$patch" >/dev/null
		return
	fi
	verify_owned_project "$build_project"
	verify_owned_project "$image_project"
	patch=$(jq -cn \
		--arg build "$(encode "$build_project")" \
		--arg image "$(encode "$image_project")" \
		--arg prefix "$(encode "$name_prefix")" \
		--arg catalog "$(encode "")" \
		'{data: {
			incus_build_project: $build,
			incus_image_project: $image,
			incus_name_prefix: $prefix,
			catalog_release_reference: $catalog
		}}')
	kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch "$patch" >/dev/null
}

restore_runtime_secret() {
	verify_runtime_snapshot
	snapshot=$(kubectl -n "$namespace" get secret "$runtime_snapshot_secret" -o json |
		jq -c '{type: (.type // "Opaque"), data: (.data // {})}')
	kubectl -n "$namespace" get secret "$runtime_secret" -o json |
		jq --argjson snapshot "$snapshot" '
			.data = $snapshot.data |
			.type = $snapshot.type |
			del(.metadata.managedFields, .metadata.creationTimestamp, .metadata.uid, .metadata.generation)
		' | kubectl replace -f - >/dev/null
}

scale_down() {
	kind=$1
	name=$2
	if kubectl -n "$namespace" get "$kind" "$name" >/dev/null 2>&1; then
		kubectl -n "$namespace" scale "$kind" "$name" --replicas=0 >/dev/null
	fi
}

wait_for_pods_to_stop() {
	selector=$1
	attempt=0
	while :; do
		pods=$(kubectl -n "$namespace" get pods -l "$selector" -o name) ||
			fail "could not list Pods matching $selector while resetting the E2E target"
		[ -z "$pods" ] && return
		attempt=$((attempt + 1))
		if [ "$attempt" -gt 180 ]; then
			kubectl -n "$namespace" get pods -l "$selector" -o wide >&2 || true
			fail "timed out waiting for Pods matching $selector to stop"
		fi
		sleep 1
	done
}

resource_available() {
	resources=$(kubectl api-resources --verbs=list -o name) ||
		fail "could not query Kubernetes API resources while resetting the E2E target"
	printf '%s\n' "$resources" |
		awk -F. -v resource="$1" '$1 == resource { found = 1 } END { exit !found }'
}

wait_for_environment_deletion() {
	resource=$1
	attempt=0
	while :; do
		resources=$(kubectl -n "$namespace" get "$resource" -o name) ||
			fail "could not list $resource while waiting for E2E Environment finalizers"
		[ -z "$resources" ] && return
		attempt=$((attempt + 1))
		if [ "$attempt" -gt 300 ]; then
			kubectl -n "$namespace" get "$resource" -o yaml >&2 || true
			fail "timed out waiting for $resource finalizers in the E2E target"
		fi
		sleep 1
	done
}

delete_environments() {
	state_dir=$repo_root/.local/e2e/$target_id
	mkdir -p "$state_dir"
	for resource in runtimeenvironments; do
		if ! resource_available "$resource"; then
			continue
		fi
		kubectl -n "$namespace" get "$resource" -o yaml >"$state_dir/reset-$resource.yaml" 2>/dev/null || true
		if kubectl -n "$namespace" get "$resource" -o name 2>/dev/null | grep -q .; then
			kubectl -n "$namespace" delete "$resource" --all --wait=false >/dev/null
			wait_for_environment_deletion "$resource"
		fi
	done
}

list_generator_workspaces() {
	pod=breakfix-postgresql-0
	pvc=$(kubectl -n "$namespace" get persistentvolumeclaim data-breakfix-postgresql-0 --ignore-not-found -o name) ||
		fail "could not determine whether PostgreSQL state exists while cleaning Generator workspaces"
	[ -n "$pvc" ] || return
	pod_name=$(kubectl -n "$namespace" get pod "$pod" --ignore-not-found -o name) ||
		fail "could not determine whether PostgreSQL Pod exists while cleaning Generator workspaces"
	if [ -z "$pod_name" ]; then
		fail "PostgreSQL state exists but $namespace/$pod is unavailable; refusing to delete state before Generator workspace cleanup"
	fi
	kubectl -n "$namespace" exec "$pod" -- sh -ec \
		'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "SELECT workspace_id, namespace, pvc_name, sandbox_id FROM generator_workspaces WHERE state <> '\''deleted'\'' ORDER BY workspace_id;"'
}

stop_opensandbox_port_forward() {
	if [ -n "${opensandbox_port_forward_pid:-}" ] && kill -0 "$opensandbox_port_forward_pid" >/dev/null 2>&1; then
		kill "$opensandbox_port_forward_pid" >/dev/null 2>&1 || true
		wait "$opensandbox_port_forward_pid" >/dev/null 2>&1 || true
	fi
	opensandbox_port_forward_pid=
	opensandbox_port=
	if [ -n "${opensandbox_port_forward_log:-}" ]; then
		rm -f "$opensandbox_port_forward_log"
	fi
	opensandbox_port_forward_log=
}

start_opensandbox_port_forward() {
	workspace_namespace=$1
	stop_opensandbox_port_forward
	opensandbox_port_forward_log=$(mktemp)
	kubectl -n "$workspace_namespace" port-forward --address 127.0.0.1 \
		"service/$opensandbox_service" :80 >"$opensandbox_port_forward_log" 2>&1 &
	opensandbox_port_forward_pid=$!
	attempt=0
	while [ "$attempt" -lt 60 ]; do
		attempt=$((attempt + 1))
		opensandbox_port=$(sed -n 's/.*127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$opensandbox_port_forward_log" | head -n 1)
		if [ -n "$opensandbox_port" ]; then
			return
		fi
		if ! kill -0 "$opensandbox_port_forward_pid" >/dev/null 2>&1; then
			cat "$opensandbox_port_forward_log" >&2 || true
			stop_opensandbox_port_forward
			fail "could not establish an OpenSandbox Lifecycle API port-forward in namespace $workspace_namespace"
		fi
		sleep 1
	done
	cat "$opensandbox_port_forward_log" >&2 || true
	stop_opensandbox_port_forward
	fail "timed out waiting for an OpenSandbox Lifecycle API port-forward in namespace $workspace_namespace"
}

opensandbox_request() {
	method=$1
	path=$2
	response_file=$3
	if ! opensandbox_status=$(curl --silent --show-error --max-time 15 \
		-X "$method" \
		-H "OPEN-SANDBOX-API-KEY: $opensandbox_api_key" \
		-o "$response_file" -w '%{http_code}' \
		"http://127.0.0.1:$opensandbox_port/v1$path"); then
		fail "OpenSandbox Lifecycle API request $method $path failed"
	fi
}

find_opensandbox_workspace() {
	workspace_id=$1
	response_file=$(mktemp)
	metadata_query=$(jq -nr --arg value "breakfix.generator_workspace_id=$workspace_id" '$value | @uri')
	opensandbox_request GET "/sandboxes?metadata=$metadata_query&page=1&pageSize=2" "$response_file"
	[ "$opensandbox_status" = 200 ] || {
		cat "$response_file" >&2 || true
		rm -f "$response_file"
		fail "could not find OpenSandbox workspace $workspace_id"
	}
	count=$(jq -r '.items | length' "$response_file") || {
		rm -f "$response_file"
		fail "OpenSandbox returned an invalid workspace list for $workspace_id"
	}
	[ "$count" -le 1 ] || {
		rm -f "$response_file"
		fail "Generator workspace $workspace_id owns multiple OpenSandbox sandboxes"
	}
	if [ "$count" -eq 1 ]; then
		resolved_sandbox_id=$(jq -r '.items[0].id // ""' "$response_file")
		[ -n "$resolved_sandbox_id" ] || {
			rm -f "$response_file"
			fail "OpenSandbox returned a workspace without an id for $workspace_id"
		}
	else
		resolved_sandbox_id=
	fi
	rm -f "$response_file"
}

delete_opensandbox_workspace() {
	workspace_id=$1
	workspace_namespace=$2
	sandbox_id=$3
	opensandbox_api_key=$(secret_value opensandbox_api_key)
	[ -n "$opensandbox_api_key" ] ||
		fail "OpenSandbox API key is required while cleaning Generator workspace $workspace_id"
	start_opensandbox_port_forward "$workspace_namespace"
	if [ -z "$sandbox_id" ]; then
		find_opensandbox_workspace "$workspace_id"
		sandbox_id=$resolved_sandbox_id
	fi
	if [ -z "$sandbox_id" ]; then
		stop_opensandbox_port_forward
		return
	fi

	encoded_sandbox_id=$(jq -nr --arg value "$sandbox_id" '$value | @uri')
	response_file=$(mktemp)
	opensandbox_request DELETE "/sandboxes/$encoded_sandbox_id" "$response_file"
	case "$opensandbox_status" in
		2?? | 404)
			;;
		*)
			cat "$response_file" >&2 || true
			rm -f "$response_file"
			stop_opensandbox_port_forward
			fail "could not delete OpenSandbox workspace $sandbox_id"
			;;
	esac
	rm -f "$response_file"
	if [ "$opensandbox_status" != 404 ]; then
		attempt=0
		terminated=false
		while [ "$attempt" -lt 180 ]; do
			attempt=$((attempt + 1))
			response_file=$(mktemp)
			opensandbox_request GET "/sandboxes/$encoded_sandbox_id" "$response_file"
			case "$opensandbox_status" in
				404 | 410)
					rm -f "$response_file"
					terminated=true
					break
					;;
				2??)
					state=$(jq -r '.status.state // ""' "$response_file") || {
						rm -f "$response_file"
						stop_opensandbox_port_forward
						fail "OpenSandbox returned an invalid workspace state for $sandbox_id"
					}
					rm -f "$response_file"
					case "$state" in
						Terminated | Failed)
							terminated=true
							break
							;;
					esac
					;;
				*)
					cat "$response_file" >&2 || true
					rm -f "$response_file"
					stop_opensandbox_port_forward
					fail "could not observe deletion of OpenSandbox workspace $sandbox_id"
					;;
			esac
			if [ "$terminated" != true ]; then
				sleep 1
			fi
		done
		[ "$terminated" = true ] || {
			stop_opensandbox_port_forward
			fail "timed out waiting for OpenSandbox workspace $sandbox_id to terminate"
		}
	fi
	stop_opensandbox_port_forward
}

cleanup_generator_workspaces() {
	workspace_file=$(mktemp)
	list_generator_workspaces >"$workspace_file"
	if [ ! -s "$workspace_file" ]; then
		rm -f "$workspace_file"
		return
	fi
	while IFS='|' read -r workspace_id workspace_namespace pvc_name sandbox_id; do
		[ -n "$workspace_id" ] && [ -n "$workspace_namespace" ] && [ -n "$pvc_name" ] ||
			fail "Generator workspace record is incomplete; refusing to delete PostgreSQL state"
		delete_opensandbox_workspace "$workspace_id" "$workspace_namespace" "$sandbox_id"
		kubectl -n "$workspace_namespace" delete persistentvolumeclaim "$pvc_name" \
			--ignore-not-found --wait=true >/dev/null
		# The GeneratorWorkspace CR is the ownership record: the reset drops it
		# with its resources. The Server is scaled down here, so the cleanup
		# finalizer must be lifted explicitly for the delete to complete.
		kubectl -n "$workspace_namespace" patch generatorworkspace "$workspace_id" --type=merge \
			-p '{"metadata":{"finalizers":null}}' --ignore-not-found >/dev/null 2>&1 || true
		kubectl -n "$workspace_namespace" delete generatorworkspace "$workspace_id" \
			--ignore-not-found --wait=true >/dev/null
	done <"$workspace_file"
	rm -f "$workspace_file"
}

delete_owned_projects() {
	for project in "$build_project" "$image_project"; do
		if project_exists "$project"; then
			verify_owned_project "$project"
			# Incus 7 still asks for an explicit confirmation for remote project
			# deletion; --force controls contained resources, not that prompt.
			printf 'yes\n' | incus project delete "$incus_remote:$project" --force >/dev/null
		fi
	done
}

delete_state_pvc() {
	name=$1
	kubectl -n "$namespace" delete persistentvolumeclaim "$name" --ignore-not-found --wait=true >/dev/null
}

dump_reset_diagnostics() {
	result=$1
	diagnostics=$repo_root/.local/e2e/$target_id/reset-failure-$(date -u +%Y%m%dT%H%M%SZ)
	mkdir -p "$diagnostics"
	kubectl -n "$namespace" get deployments,statefulsets,pods,persistentvolumeclaims,configmaps \
		-o wide >"$diagnostics/resources.txt" 2>&1 || true
	kubectl -n "$namespace" get events --sort-by=.lastTimestamp >"$diagnostics/events.txt" 2>&1 || true
	kubectl -n "$namespace" get runtimeenvironments -o name >"$diagnostics/environment-identities.txt" 2>&1 || true
	if kubectl -n "$namespace" get pod breakfix-postgresql-0 >/dev/null 2>&1; then
		kubectl -n "$namespace" exec breakfix-postgresql-0 -- sh -ec \
			'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT workspace_id, workflow_id, namespace, pvc_name, sandbox_id, state FROM generator_workspaces WHERE state <> '\''deleted'\'' ORDER BY workspace_id;"' \
			>"$diagnostics/generator-workspaces.sql.txt" 2>&1 || true
	fi
	if [ "$profile" = full ]; then
		for project in "$build_project" "$image_project"; do
			incus project show "$incus_remote:$project" >"$diagnostics/incus-$project.project.yaml" 2>&1 || true
			incus list "$incus_remote:" --project "$project" --format yaml >"$diagnostics/incus-$project.instances.yaml" 2>&1 || true
			incus image list "$incus_remote:" --project "$project" --format yaml >"$diagnostics/incus-$project.images.yaml" 2>&1 || true
		done
	fi
	printf '%s\n' "$result" >"$diagnostics/exit-status.txt"
	printf 'E2E reset failed; diagnostics retained in %s\n' "$diagnostics" >&2
}

reset_target() {
	trap 'result=$?; stop_opensandbox_port_forward; if [ "$result" -ne 0 ]; then dump_reset_diagnostics "$result"; fi; exit "$result"' EXIT HUP INT TERM
	reset_tools="awk base64 cat curl grep head jq kubectl mktemp openssl sed sha256sum sleep tr"
	if [ "$profile" = full ]; then
		reset_tools="$reset_tools incus"
	fi
	for tool in $reset_tools; do require_command "$tool"; done
	verify_marker
	require_runtime_secret
	verify_runtime_snapshot

	# Stop every writer before asking Controller to run exact provider finalizers.
	for deployment in breakfix-server breakfix-runtime-worker breakfix-registry; do
		scale_down deployment "$deployment"
	done
	wait_for_pods_to_stop app.kubernetes.io/name=breakfix-server
	wait_for_pods_to_stop app.kubernetes.io/name=breakfix-runtime-worker
	wait_for_pods_to_stop app.kubernetes.io/name=breakfix-registry

	delete_environments
	cleanup_generator_workspaces
	if [ "$profile" = full ]; then
		delete_owned_projects
	fi

	# Controller has finished finalizers. It is now safe to stop the remaining
	# control-plane writers and remove only this target's durable state.
	scale_down deployment breakfix-controller
	scale_down statefulset breakfix-postgresql
	wait_for_pods_to_stop app.kubernetes.io/name=breakfix-controller
	wait_for_pods_to_stop app.kubernetes.io/name=breakfix-postgresql
	restore_runtime_secret
	delete_state_pvc breakfix-server-data
	delete_state_pvc data-breakfix-postgresql-0
	delete_state_pvc breakfix-registry-data
	kubectl -n "$namespace" delete configmap "$marker_name" --wait=true >/dev/null
	kubectl -n "$namespace" delete configmap "$prepared_marker_name" --ignore-not-found --wait=true >/dev/null
	kubectl -n "$namespace" delete secret "$runtime_snapshot_secret" --wait=true >/dev/null
	rm -f "$repo_root/.local/e2e/$target_id/ui-origin-port"
	trap - EXIT HUP INT TERM

	printf 'Reset Breakfix E2E target %s. Shared Incus projects and base images were not modified.\n' "$target_id"
}

case "$profile" in
	core|full)
		;;
	*)
		fail "BREAKFIX_E2E_PROFILE must be core or full, got \"$profile\""
		;;
esac

case "$command" in
	preflight)
		preflight
		;;
	mark)
		for tool in jq kubectl; do require_command "$tool"; done
		mark_target
		;;
	mark-prepared)
		for tool in grep jq kubectl; do require_command "$tool"; done
		shift
		mark_prepared "$@"
		;;
	ensure-incus)
		ensure_incus
		;;
	configure-runtime)
		configure_runtime
		;;
	reset)
		reset_target
		;;
	assert)
		for tool in jq kubectl; do require_command "$tool"; done
		verify_marker
		;;
	assert-prepared)
		for tool in grep jq kubectl; do require_command "$tool"; done
		verify_prepared_marker
		;;
	*)
		printf 'Usage: %s {preflight|mark|mark-prepared|ensure-incus|configure-runtime|reset|assert|assert-prepared} [catalog-reference ui-origin]\n' "$0" >&2
		exit 2
		;;
esac
