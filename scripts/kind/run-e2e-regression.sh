#!/bin/sh
set -eu

# One prepare serves the whole chain; k8s and ui append without another
# prepare, and node and recovery join only under the full profile because
# their scenarios drive Incus-backed environments. The documentation and
# admin suites are live acceptance (real model) and run through their own
# entry points, not this chain.

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
prepare=$repo_root/scripts/kind/e2e-prepare.sh
runner=$repo_root/scripts/kind/run-e2e.sh
profile=${BREAKFIX_E2E_PROFILE:-full}

case "$profile" in
	core|full)
		;;
	*)
		printf 'Breakfix E2E regression: BREAKFIX_E2E_PROFILE must be core or full, got "%s"\n' "$profile" >&2
		exit 2
		;;
esac

"$prepare"

"$runner" k8s
"$runner" ui
if [ "$profile" = full ]; then
	"$runner" node
	"$runner" recovery
fi

printf 'Breakfix E2E regression (%s profile) passed.\n' "$profile"
