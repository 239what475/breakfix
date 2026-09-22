#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-${BREAKFIX_E2E_NAMESPACE:-breakfix-system}}
target_id=${BREAKFIX_E2E_TARGET:-e2e}
state_dir=${BREAKFIX_E2E_STATE_DIR:-$repo_root/.local/e2e/$target_id}
target_script=$repo_root/scripts/kind/e2e-target.sh
suite=${1:-}
profile=${BREAKFIX_E2E_PROFILE:-full}
port_forward_pid=
supervisor_pid=
port_file=$state_dir/$suite-port
control_file=$state_dir/$suite-port-forward.active
child_file=$state_dir/$suite-port-forward.child
port_forward_log=$state_dir/$suite-port-forward.log
base_url=

fail() {
	printf 'Breakfix E2E runner: %s\n' "$*" >&2
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

case "$suite" in
	ui|node|k8s|recovery|playground|admin|acceptance-node|acceptance-mcp|acceptance-k8s|acceptance-interruption|agent-assistant|agent-soak)
		;;
	*)
		printf 'Usage: %s {ui|node|k8s|recovery|playground|acceptance-node|acceptance-mcp|acceptance-k8s|acceptance-interruption|agent-assistant|agent-soak}\n' "$0" >&2
		exit 2
		;;
esac

# These suites drive Node runtime environments (Incus system containers) or
# live Node acceptance; the core profile has no Node provider at all.
case "$suite" in
	node|recovery|acceptance-node|acceptance-mcp|acceptance-interruption|agent-assistant|agent-soak)
		if [ "$profile" != full ]; then
			fail "suite \"$suite\" requires the full profile (BREAKFIX_E2E_PROFILE=full): it drives Node runtime environments"
		fi
		;;
esac

runner_tools="cp curl kubectl npm sed"
if [ "$profile" = full ]; then
	runner_tools="$runner_tools incus"
fi
for tool in $runner_tools; do
	require_command "$tool"
done

mkdir -p "$state_dir"
"$target_script" assert-prepared

diagnostics_dir=

dump_diagnostics() {
	result=$1
	diagnostics_dir=$state_dir/$suite-failure-$(date -u +%Y%m%dT%H%M%SZ)
	mkdir -p "$diagnostics_dir"

	kubectl -n "$namespace" get deployments,pods,persistentvolumeclaims,runtimeenvironments \
		-o wide >"$diagnostics_dir/resources.txt" 2>&1 || true
	kubectl -n "$namespace" get events --sort-by=.lastTimestamp >"$diagnostics_dir/events.txt" 2>&1 || true
	kubectl -n "$namespace" get runtimeenvironments -o name >"$diagnostics_dir/environment-identities.txt" 2>&1 || true

	for deployment in breakfix-server breakfix-controller breakfix-runtime-worker breakfix-registry; do
		kubectl -n "$namespace" logs "deployment/$deployment" --all-containers --prefix --tail=500 \
			>"$diagnostics_dir/$deployment.log" 2>&1 || true
	done
	if kubectl -n "$namespace" get pod breakfix-postgresql-0 >/dev/null 2>&1; then
		kubectl -n "$namespace" logs pod/breakfix-postgresql-0 --all-containers --tail=500 \
			>"$diagnostics_dir/postgresql.log" 2>&1 || true
	fi

	while IFS= read -r identity; do
		[ -n "$identity" ] || continue
		file=$(printf '%s' "$identity" | sed 's#[^A-Za-z0-9_.-]#_#g')
		kubectl -n "$namespace" get "$identity" -o yaml >"$diagnostics_dir/$file.yaml" 2>&1 || true
	done <"$diagnostics_dir/environment-identities.txt"

	if [ "$profile" = full ]; then
		for project in \
			"${BREAKFIX_E2E_INCUS_BUILD_PROJECT:-breakfix-e2e-build}" \
			"${BREAKFIX_E2E_INCUS_IMAGE_PROJECT:-breakfix-e2e-images}"; do
			incus project show "${BREAKFIX_E2E_INCUS_REMOTE:-incus-cluster}:$project" \
				>"$diagnostics_dir/incus-$project.project.yaml" 2>&1 || true
			incus list "${BREAKFIX_E2E_INCUS_REMOTE:-incus-cluster}:" --project "$project" --format yaml \
				>"$diagnostics_dir/incus-$project.instances.yaml" 2>&1 || true
			incus image list "${BREAKFIX_E2E_INCUS_REMOTE:-incus-cluster}:" --project "$project" --format yaml \
				>"$diagnostics_dir/incus-$project.images.yaml" 2>&1 || true
		done
	fi
	for artifact in \
		"$repo_root/test/results/$suite" \
		"$repo_root/test/report/$suite"; do
		[ -e "$artifact" ] || continue
		name=$(basename "$artifact")
		cp -a "$artifact" "$diagnostics_dir/playwright-$name" || true
	done
	if kubectl -n "$namespace" get pod breakfix-postgresql-0 >/dev/null 2>&1; then
		kubectl -n "$namespace" exec breakfix-postgresql-0 -- sh -ec \
			'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT id, state, candidate_revision_id, last_error FROM generation_workflows ORDER BY updated_at DESC LIMIT 100;" -c "SELECT workspace_id, workflow_id, namespace, pvc_name, sandbox_id, state FROM generator_workspaces WHERE state <> '\''deleted'\'' ORDER BY updated_at DESC;"' \
			>"$diagnostics_dir/generation.sql.txt" 2>&1 || true
	fi
	printf '%s\n' "$base_url" >"$diagnostics_dir/base-url.txt"
	printf '%s\n' "$result" >"$diagnostics_dir/exit-status.txt"
	printf 'E2E %s failed; diagnostics retained in %s\n' "$suite" "$diagnostics_dir" >&2
}

stop_forward_child() {
	child=
	if [ -f "$child_file" ]; then
		child=$(sed -n '1p' "$child_file" 2>/dev/null || true)
	fi
	case "$child" in
		''|*[!0-9]*) ;;
		*) kill "$child" >/dev/null 2>&1 || true ;;
	esac
}

stop_port_forward() {
	rm -f "$control_file"
	stop_forward_child
	if [ -n "$supervisor_pid" ]; then
		kill "$supervisor_pid" >/dev/null 2>&1 || true
		wait "$supervisor_pid" >/dev/null 2>&1 || true
	fi
	rm -f "$child_file" "$port_file"
	supervisor_pid=
}

forward_supervisor() {
	configured_port=$(sed -n '1p' "$state_dir/ui-origin-port" 2>/dev/null || true)
	case "$configured_port" in
		''|*[!0-9]*)
			printf 'prepared E2E target is missing a valid ui-origin-port; run make e2e-prepare first\n' >&2
			exit 2
			;;
		*)
			[ "$configured_port" -ge 1024 ] && [ "$configured_port" -le 65535 ] || {
				printf 'prepared E2E ui-origin-port must be between 1024 and 65535, got %s\n' "$configured_port" >&2
				exit 2
			}
			;;
	esac
	port=$configured_port
	child=
	trap 'if [ -n "${child:-}" ]; then kill "$child" >/dev/null 2>&1 || true; fi; exit 0' INT TERM HUP EXIT

	while [ -f "$control_file" ]; do
		: >"$port_forward_log"
		kubectl -n "$namespace" port-forward --address 127.0.0.1 \
			"service/breakfix-server" "$port:9090" >>"$port_forward_log" 2>&1 &
		child=$!
		printf '%s\n' "$child" >"$child_file"

		printf '%s\n' "$port" >"$port_file"

		wait "$child" >/dev/null 2>&1 || true
		child=
		rm -f "$child_file"
		[ -f "$control_file" ] || break
		sleep 1
	done
}

wait_for_ready() {
	deadline=$(( $(date +%s) + 45 ))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		if curl --fail --silent --show-error "$base_url/readyz" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

start_port_forward() {
	rm -f "$port_file" "$child_file"
	: >"$port_forward_log"
	touch "$control_file"
	forward_supervisor &
	supervisor_pid=$!

	deadline=$(( $(date +%s) + 60 ))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		if [ -s "$port_file" ]; then
			port=$(sed -n '1p' "$port_file")
			base_url="http://127.0.0.1:$port"
			if wait_for_ready; then
				return 0
			fi
		fi
		if ! kill -0 "$supervisor_pid" >/dev/null 2>&1; then
			cat "$port_forward_log" >&2 || true
			return 1
		fi
		sleep 1
	done
	return 1
}

run_suite() {
	case "$suite" in
		ui)
			npm run test:e2e --prefix "$repo_root/test"
			;;
		node)
			npm run test:e2e:node --prefix "$repo_root/test"
			;;
		k8s)
			npm run test:e2e:k8s --prefix "$repo_root/test"
			;;
		recovery)
			npm run test:e2e:recovery --prefix "$repo_root/test"
			;;
		playground)
			# The playground provisions real vk8s environments and needs no
			# model; the same prepared target serves the aggregation smoke.
			npm run test:e2e:playground --prefix "$repo_root/test"
			;;
		acceptance-node)
			[ "${RUN_AGENT_LIVE_E2E:-}" = 1 ] ||
				fail 'acceptance-node requires RUN_AGENT_LIVE_E2E=1; this suite calls the real model and OpenSandbox'
			npm run test:acceptance:node --prefix "$repo_root/test"
			;;
		acceptance-mcp)
			[ "${RUN_AGENT_LIVE_E2E:-}" = 1 ] ||
				fail 'acceptance-mcp requires RUN_AGENT_LIVE_E2E=1; this suite calls the real model and OpenSandbox'
			npm run test:acceptance:mcp --prefix "$repo_root/test"
			;;
		acceptance-k8s)
			[ "${RUN_AGENT_LIVE_E2E:-}" = 1 ] ||
				fail 'acceptance-k8s requires RUN_AGENT_LIVE_E2E=1; this suite calls the real model and OpenSandbox'
			npm run test:agent-live:k8s --prefix "$repo_root/test"
			;;
		acceptance-interruption)
			[ "${RUN_AGENT_LIVE_E2E:-}" = 1 ] ||
				fail 'acceptance-interruption requires RUN_AGENT_LIVE_E2E=1; this suite calls the real model and OpenSandbox'
			npm run test:acceptance:interruption --prefix "$repo_root/test"
			;;
		agent-assistant)
			[ "${RUN_AGENT_LIVE_E2E:-}" = 1 ] ||
				fail 'agent-assistant requires RUN_AGENT_LIVE_E2E=1; this suite calls the real model'
			npm run test:agent-live:assistant --prefix "$repo_root/test"
			;;
		agent-soak)
			[ "${RUN_AGENT_SOAK_E2E:-}" = 1 ] ||
				fail 'agent-soak requires RUN_AGENT_SOAK_E2E=1; this suite calls the real model repeatedly'
			npm run test:agent-live:soak --prefix "$repo_root/test"
			;;
	esac
}

result=0
trap 'stop_port_forward; exit 130' INT TERM HUP
if ! start_port_forward; then
	dump_diagnostics 1
	stop_port_forward
	exit 1
fi

export BREAKFIX_E2E_BASE_URL="$base_url"
set +e
run_suite
result=$?
set -e
if [ "$result" -ne 0 ]; then
	dump_diagnostics "$result"
fi
stop_port_forward
exit "$result"
