#!/bin/sh
set -eu

# One documentation prepare serves the whole regression chain. Admin runs on
# the fresh target that prepare creates (its first registration must elect the
# bootstrap admin); the database-only reset restores the counted state the
# documentation practice chain asserts; k8s and ui append without another
# prepare; node and recovery join only under the full profile because their
# scenarios drive Incus-backed environments.

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
docs_prepare=$repo_root/scripts/kind/e2e-documentation-prepare.sh
reset_database=$repo_root/scripts/kind/e2e-reset-database.sh
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

DOCS_PROJECT_VERSION=${DOCS_PROJECT_VERSION:-docs-project-v10} "$docs_prepare"

"$runner" admin
"$reset_database"
"$runner" documentation
"$runner" k8s
"$runner" ui
if [ "$profile" = full ]; then
	"$runner" node
	"$runner" recovery
fi

printf 'Breakfix E2E regression (%s profile) passed.\n' "$profile"
