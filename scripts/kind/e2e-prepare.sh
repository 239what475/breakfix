#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}
target_id=${BREAKFIX_E2E_TARGET:-e2e}
fixture_source=${BREAKFIX_E2E_CATALOG_SOURCE:-$repo_root/test/fixtures/catalog-release}
fixture_title='Node 运行时验收'
fixture_runtime=node
fixture_domain=platform-runtime
fixture_topic=platform-runtime/node-environment-validation
fixture_tag=runtime-fixture
state_dir=${BREAKFIX_E2E_STATE_DIR:-$repo_root/.local/e2e/$target_id}
fixture_archive=$state_dir/catalog-release.oci.tar
catalog_tag=${BREAKFIX_E2E_CATALOG_TAG:-e2e-$target_id}
prepare_timeout_seconds=${BREAKFIX_E2E_PREPARE_TIMEOUT_SECONDS:-900}
target_script=$repo_root/scripts/kind/e2e-target.sh
port_forward_pid=
port_forward_log=

fail() {
	printf 'Breakfix E2E prepare: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

secret_value() {
	kubectl -n "$namespace" get secret "$runtime_secret" -o json |
		jq -r --arg key "$1" 'if .data[$key] == null then "" else .data[$key] | @base64d end'
}

encode() {
	printf '%s' "$1" | base64 | tr -d '\n'
}

stop_port_forward() {
	if [ -n "$port_forward_pid" ] && kill -0 "$port_forward_pid" >/dev/null 2>&1; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" >/dev/null 2>&1 || true
	fi
	port_forward_pid=
}

start_port_forward() {
	local_port=$1
	port_forward_log=$state_dir/prepare-port-forward.log
	: >"$port_forward_log"
	case "$local_port" in
		"") port_mapping=:9090 ;;
		*) port_mapping=$local_port:9090 ;;
	esac
	kubectl -n "$namespace" port-forward --address 127.0.0.1 service/breakfix-server "$port_mapping" >"$port_forward_log" 2>&1 &
	port_forward_pid=$!
}

wait_for_port_forward() {
	attempt=0
	while [ "$attempt" -lt 120 ]; do
		attempt=$((attempt + 1))
		base_port=$(sed -n 's/.*127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$port_forward_log" | head -n 1)
		if [ -n "$base_port" ]; then
			printf '%s\n' "$base_port"
			return
		fi
		if ! kill -0 "$port_forward_pid" >/dev/null 2>&1; then
			cat "$port_forward_log" >&2 || true
			fail "could not establish a Server port-forward"
		fi
		sleep 1
	done
	fail "timed out waiting for a Server port-forward"
}

dump_diagnostics() {
	result=$1
	mkdir -p "$state_dir"
	diagnostics=$state_dir/prepare-failure-$(date -u +%Y%m%dT%H%M%SZ)
	mkdir -p "$diagnostics"
	kubectl -n "$namespace" get deployments,pods,persistentvolumeclaims,nodeenvironments,vk8senvironments -o wide >"$diagnostics/resources.txt" 2>&1 || true
	for deployment in breakfix-server breakfix-controller breakfix-runtime-worker breakfix-registry; do
		kubectl -n "$namespace" logs deployment/"$deployment" --all-containers --tail=300 >"$diagnostics/$deployment.log" 2>&1 || true
	done
	if kubectl -n "$namespace" get pod breakfix-postgresql-0 >/dev/null 2>&1; then
		kubectl -n "$namespace" exec breakfix-postgresql-0 -- sh -ec \
			'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT id, state, bundle_digest, last_error FROM catalog_releases ORDER BY created_at;" -c "SELECT id, state, source_ref, last_error FROM catalog_release_entries ORDER BY created_at;"' \
			>"$diagnostics/catalog.sql.txt" 2>&1 || true
	fi
	if [ -n "$port_forward_log" ] && [ -f "$port_forward_log" ]; then
		cp "$port_forward_log" "$diagnostics/port-forward.log" || true
	fi
	printf 'E2E prepare failed; diagnostics retained in %s\n' "$diagnostics" >&2
}

cleanup() {
	result=$?
	stop_port_forward
	if [ "$result" -ne 0 ]; then
		dump_diagnostics "$result"
	fi
	exit "$result"
}

trap cleanup EXIT HUP INT TERM

for tool in base64 curl go jq kubectl make sed tr; do require_command "$tool"; done
[ -d "$fixture_source" ] || fail "fixture source does not exist: $fixture_source"
[ -f "$fixture_source/release.yaml" ] || fail "fixture source does not contain release.yaml: $fixture_source"

# Validate all dependencies before the reset changes the selected E2E target.
"$target_script" preflight

# Build only after the target has passed the Kind/Incus read-only checks. This
# keeps an accidental invocation on another context from changing Docker state
# or attempting network image resolution.
make -C "$repo_root" images
"$target_script" mark
"$target_script" ensure-incus
"$target_script" configure-runtime
# Bring up the current Controller before cleanup. A stale target can contain
# Environment finalizers, and those must be handled by the current code rather
# than by an assumed old Deployment.
"$repo_root/scripts/kind/runtime.sh"
"$target_script" reset
# reset deliberately removes its marker and Secret snapshot. Mark the clean
# target again so this prepare owns a fresh restore point for its deployment.
"$target_script" mark
"$target_script" ensure-incus
"$target_script" configure-runtime

"$repo_root/scripts/kind/runtime.sh"

registry_repository=$(secret_value registry_repository)
registry_username=$(secret_value registry_username)
registry_password=$(secret_value registry_password)
[ -n "$registry_repository" ] || fail "runtime Secret must provide registry_repository after Kind Registry preparation"
[ -n "$registry_username" ] && [ -n "$registry_password" ] ||
	fail "runtime Secret must provide Registry credentials for the fixture publication"
registry_ca=${BREAKFIX_E2E_REGISTRY_TRUST_BUNDLE_FILE:-$repo_root/.local/kind-registry/ca.crt}
[ -r "$registry_ca" ] || fail "Kind Registry CA bundle is unavailable: $registry_ca"

mkdir -p "$state_dir"
rm -f "$state_dir/ui-origin-port"
catalog_reference=$(
	BREAKFIX_REGISTRY_USERNAME="$registry_username" \
	BREAKFIX_REGISTRY_PASSWORD="$registry_password" \
	BREAKFIX_REGISTRY_TRUST_BUNDLE_FILE="$registry_ca" \
	go run ./cmd/catalog-release \
		-source "$fixture_source" \
		-output "$fixture_archive" \
		-reference "$registry_repository/catalog/$catalog_tag"
)
case "$catalog_reference" in
	"$registry_repository"/catalog/*@sha256:*)
		;;
	*)
		fail "fixture publication returned an unexpected immutable reference: $catalog_reference"
		;;
esac

# Terminal WebSockets enforce one exact browser Origin. Select a free local
# port now, bind it into this prepared target's Server configuration, and let
# every later suite reconnect a port-forward on the same target-local address.
# The port is dynamic per prepare and never falls back to localhost:9090.
start_port_forward ""
base_port=$(wait_for_port_forward)
stop_port_forward
ui_origin=http://127.0.0.1:$base_port
printf '%s\n' "$base_port" >"$state_dir/ui-origin-port"
patch=$(jq -cn \
	--arg reference "$(encode "$catalog_reference")" \
	--arg origin "$(encode "$ui_origin")" \
	'{data: {catalog_release_reference: $reference, ui_origin: $origin}}')
kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch "$patch" >/dev/null
kubectl -n "$namespace" rollout restart deployment/breakfix-server >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null

start_port_forward "$base_port"
base_port=$(wait_for_port_forward)
base_url=http://127.0.0.1:$base_port

deadline=$(( $(date +%s) + prepare_timeout_seconds ))
catalog_json=$state_dir/catalog-projection.json
while [ "$(date +%s)" -lt "$deadline" ]; do
	if curl --fail --silent --show-error "$base_url/api/challenges" >"$catalog_json" 2>/dev/null &&
		jq -e \
			--arg title "$fixture_title" \
			--arg runtime "$fixture_runtime" \
			--arg domain "$fixture_domain" \
			--arg topic "$fixture_topic" \
			--arg tag "$fixture_tag" '
				(.challenges | length) == 1 and
				.challenges[0].title == $title and
				.challenges[0].runtime == $runtime and
				.challenges[0].domain.source_ref == $domain and
				.challenges[0].topic.source_ref == $topic and
				([.challenges[0].tags[].source_ref] | sort) == [$tag]
			' "$catalog_json" >/dev/null; then
		break
	fi
	sleep 2
done
jq -e \
	--arg title "$fixture_title" \
	--arg runtime "$fixture_runtime" \
	--arg domain "$fixture_domain" \
	--arg topic "$fixture_topic" \
	--arg tag "$fixture_tag" '
		(.challenges | length) == 1 and
		.challenges[0].title == $title and
		.challenges[0].runtime == $runtime and
		.challenges[0].domain.source_ref == $domain and
		.challenges[0].topic.source_ref == $topic and
		([.challenges[0].tags[].source_ref] | sort) == [$tag]
	' "$catalog_json" >/dev/null || fail "fixture Catalog did not reach the expected public projection before timeout"

"$target_script" mark-prepared "$catalog_reference" "$ui_origin"

printf 'Prepared Breakfix E2E target %s with fixture %s.\n' "$target_id" "$catalog_reference"
