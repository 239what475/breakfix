#!/bin/sh
set -eu

# Between-suite reset for a prepared E2E target: recreate the database and
# let Server reinstall the recorded Catalog digest on one restart. Everything
# a prepare owns stays in place - the deployment (including the documentation
# library patch), the Registry with its immutable fixture artifacts, the
# runtime Secret, and the prepared markers - so a regression run pays for one
# prepare instead of one per suite.

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
target_id=${BREAKFIX_E2E_TARGET:-e2e}
state_dir=${BREAKFIX_E2E_STATE_DIR:-$repo_root/.local/e2e/$target_id}
target_script=$repo_root/scripts/kind/e2e-target.sh
profile=${BREAKFIX_E2E_PROFILE:-full}
port_forward_pid=
port_forward_log=

fail() {
	printf 'Breakfix E2E database reset: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

case "$profile" in
	core|full)
		;;
	*)
		fail "BREAKFIX_E2E_PROFILE must be core or full, got \"$profile\""
		;;
esac

for tool in curl jq kubectl sed; do require_command "$tool"; done

stop_port_forward() {
	if [ -n "$port_forward_pid" ] && kill -0 "$port_forward_pid" >/dev/null 2>&1; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" >/dev/null 2>&1 || true
	fi
	port_forward_pid=
}
trap stop_port_forward EXIT HUP INT TERM

"$target_script" assert-prepared

# A suite may have parked the Runtime Worker at zero replicas; later suites
# and the Controller finalizers below both need it claiming again.
kubectl -n "$namespace" scale deployment/breakfix-runtime-worker --replicas=1 >/dev/null

# Environments from earlier suites are cluster state backed by database rows.
# Remove them first so Controller finalizers reap the runtime resources while
# the old database rows still describe them.
if kubectl -n "$namespace" get runtimeenvironments -o name 2>/dev/null | grep -q .; then
	kubectl -n "$namespace" delete runtimeenvironments --all --wait=true --timeout=180s >/dev/null
fi

kubectl -n "$namespace" exec breakfix-postgresql-0 -- sh -ec '
	psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
		-c "DROP DATABASE IF EXISTS \"$POSTGRES_DB\" WITH (FORCE);" \
		-c "CREATE DATABASE \"$POSTGRES_DB\" OWNER \"$POSTGRES_USER\";"
' >/dev/null

# Server and Controller both validate the database schema marker: one restart
# each recreates the schema, and Server reinstalls the Catalog digest recorded
# in the runtime Secret without any further prepare work.
kubectl -n "$namespace" rollout restart deployment/breakfix-server deployment/breakfix-controller >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-controller --timeout=3m >/dev/null

configured_port=$(sed -n '1p' "$state_dir/ui-origin-port" 2>/dev/null || true)
case "$configured_port" in
	''|*[!0-9]*)
		fail "prepared target is missing a valid ui-origin-port"
		;;
esac
port_forward_log=$state_dir/reset-database-port-forward.log
: >"$port_forward_log"
kubectl -n "$namespace" port-forward --address 127.0.0.1 service/breakfix-server "$configured_port:9090" >"$port_forward_log" 2>&1 &
port_forward_pid=$!

base_url=http://127.0.0.1:$configured_port
fixture_title='Node 运行时验收'
fixture_runtime=node
fixture_count=2
expect_node=1
if [ "$profile" = core ]; then
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
	' "$catalog_json" >/dev/null || fail "fixture Catalog did not come back before timeout"

printf 'Reset the E2E database for target %s; the fixture Catalog was reinstalled from the recorded digest.\n' "$target_id"
