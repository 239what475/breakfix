#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-${BREAKFIX_E2E_NAMESPACE:-breakfix-system}}
deadline=${BREAKFIX_E2E_AUTHORING_DEADLINE:-60s}
target_id=${BREAKFIX_E2E_TARGET:-e2e}
state_dir=${BREAKFIX_E2E_STATE_DIR:-$repo_root/.local/e2e/$target_id}
target_script=$repo_root/scripts/kind/e2e-target.sh
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/breakfix-authoring-interruption.XXXXXX")
original_config=$temporary_dir/config.json
restored=0

fail() {
	printf 'Breakfix authoring interruption E2E: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

case "${BREAKFIX_E2E_PROFILE:-full}" in
	core)
		fail 'the authoring interruption chain drives Node runtime environments and requires the full profile (BREAKFIX_E2E_PROFILE=full)'
		;;
	full)
		;;
	*)
		fail "BREAKFIX_E2E_PROFILE must be core or full, got \"${BREAKFIX_E2E_PROFILE}\""
		;;
esac

patch_config() {
	value=$1
	config=$(jq -r '.data["config.yaml"] // empty' "$original_config")
	[ -n "$config" ] || fail 'prepared Server ConfigMap has no config.yaml'
	patched=$(printf '%s\n' "$config" | awk -v deadline="$value" '
		/^[[:space:]]*authoring_run_deadline:[[:space:]]*/ {
			print "  authoring_run_deadline: " deadline
			found++
			next
		}
		{ print }
		END { if (found != 1) exit 1 }
	') || fail 'Server ConfigMap does not contain exactly one authoring_run_deadline'
	patch=$(jq -cn --arg config "$patched" '{data: {"config.yaml": $config}}')
	kubectl -n "$namespace" patch configmap "$configmap" --type merge --patch "$patch" >/dev/null
}

restore() {
	result=$?
	if [ "$restored" -eq 0 ] && [ -s "$original_config" ]; then
		set +e
		original=$(jq -r '.data["config.yaml"] // empty' "$original_config")
		if [ -n "$original" ]; then
			patch=$(jq -cn --arg config "$original" '{data: {"config.yaml": $config}}')
			kubectl -n "$namespace" patch configmap "$configmap" --type merge --patch "$patch" >/dev/null
			kubectl -n "$namespace" rollout restart deployment/breakfix-server >/dev/null
			kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null
		else
			printf 'Breakfix authoring interruption E2E: could not restore empty Server config\n' >&2
			result=1
		fi
		set -e
		restored=1
	fi
	rm -rf "$temporary_dir"
	exit "$result"
}

for tool in awk jq kubectl npm; do
	require_command "$tool"
done

"$target_script" assert-prepared
configmap=$(kubectl -n "$namespace" get deployment breakfix-server -o json |
	jq -r '.spec.template.spec.volumes[] | select(.name == "config") | .configMap.name')
[ -n "$configmap" ] && [ "$configmap" != "null" ] || fail 'breakfix-server has no config ConfigMap'
kubectl -n "$namespace" get configmap "$configmap" -o json >"$original_config"
trap restore EXIT HUP INT TERM

patch_config "$deadline"
kubectl -n "$namespace" rollout restart deployment/breakfix-server >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-server --timeout=3m >/dev/null

RUN_AGENT_LIVE_E2E=1 "$repo_root/scripts/kind/run-e2e.sh" acceptance-interruption
