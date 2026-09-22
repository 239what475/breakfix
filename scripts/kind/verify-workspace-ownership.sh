#!/bin/sh
set -eu

# Verify the GeneratorWorkspace ownership transfer against a prepared Kind
# target, without OpenSandbox credentials: everything asserted here lives on
# the Kubernetes side. Two proofs:
#
#   1. Kubernetes garbage collection drops an owner-referenced PVC together
#      with its GeneratorWorkspace CR.
#   2. Deleting the projection row (the destructive-schema-migration case)
#      converges through the Server reconciler: the CR is adopted into a
#      deleting row, the external resources are dropped, the owner is deleted
#      with its finalizer, the PVC follows through the cascade, and the row
#      completes as deleted.
#
# The OpenSandbox behavior of the drop and the leak sanitizer is covered by
# unit tests with fakes; this script never calls the provider.

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
workspace_namespace=${BREAKFIX_OPENSANDBOX_NAMESPACE:-opensandbox}
converge_seconds=${BREAKFIX_WORKSPACE_OWNERSHIP_CONVERGE_SECONDS:-300}
run_id=$(date +%s)-$$
cr_name=generator-workspace-cascade-$run_id
adopt_id=generator-workspace-adopt-$run_id

fail() {
	printf 'workspace ownership acceptance: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

for tool in kubectl; do
	require_command "$tool"
done

case $(kubectl config current-context) in
kind-*) ;;
*) fail "requires a Kind context, current context is $(kubectl config current-context 2>/dev/null || echo unset)" ;;
esac

cleanup() {
	status=$?
	kubectl -n "$workspace_namespace" delete generatorworkspace "$cr_name" --wait=false --ignore-not-found >/dev/null 2>&1 || true
	kubectl -n "$workspace_namespace" delete generatorworkspace "$adopt_id" --wait=false --ignore-not-found >/dev/null 2>&1 || true
	kubectl -n "$workspace_namespace" delete pvc "$cr_name-pvc" "$adopt_id-pvc" --wait=false --ignore-not-found >/dev/null 2>&1 || true
	kubectl -n "$namespace" exec breakfix-postgresql-0 -c postgresql -- psql -U breakfix -d breakfix -tAc \
		"DELETE FROM generator_workspaces WHERE workspace_id IN ('$cr_name', '$adopt_id')" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup 0 HUP INT TERM

# An operator may run this against a target deployed before the CRD existed.
kubectl apply -f "$repo_root/deploy/crds/breakfix.dev_generatorworkspaces.yaml" >/dev/null
attempt=0
until kubectl get generatorworkspaces >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	[ "$attempt" -lt 30 ] || fail "generatorworkspaces CRD did not become available"
	sleep 2
done

kubectl get namespace "$workspace_namespace" >/dev/null 2>&1 ||
	kubectl create namespace "$workspace_namespace" >/dev/null

server_ready=$(kubectl -n "$namespace" get deploy breakfix-server -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)
[ "${server_ready:-0}" -ge 1 ] || fail "breakfix-server in $namespace is not ready; run make e2e-prepare first"

psql() {
	kubectl -n "$namespace" exec breakfix-postgresql-0 -c postgresql -- psql -U breakfix -d breakfix -tAc "$1"
}

# --- Proof 1: the garbage collector drops an owned claim with its CR --------

# The status subresource ignores status fields carried by a plain apply, so
# every CR writes its ownership facts through an explicit status patch. The
# optional fourth argument carries extra metadata lines, e.g. a finalizer.
apply_workspace_cr() {
	cr=$1
	workflow=$2
	pvc=$3
	extra=${4:-}
	kubectl -n "$workspace_namespace" apply -f - >/dev/null <<EOF
apiVersion: breakfix.dev/v2
kind: GeneratorWorkspace
metadata:
  name: $cr
$extra
spec: {}
EOF
	kubectl -n "$workspace_namespace" patch generatorworkspace "$cr" --subresource=status --type=merge -p \
		"{\"status\":{\"workflowID\":\"$workflow\",\"namespace\":\"$workspace_namespace\",\"pvcName\":\"$pvc\",\"phase\":\"Active\"}}" >/dev/null
}

apply_workspace_cr "$cr_name" workflow-cascade "$cr_name-pvc"

kubectl -n "$workspace_namespace" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $cr_name-pvc
  ownerReferences:
    - apiVersion: breakfix.dev/v2
      kind: GeneratorWorkspace
      name: $cr_name
      uid: $(kubectl -n "$workspace_namespace" get generatorworkspace "$cr_name" -o jsonpath='{.metadata.uid}')
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
EOF

kubectl -n "$workspace_namespace" delete generatorworkspace "$cr_name" --wait=true --timeout=60s >/dev/null
kubectl -n "$workspace_namespace" wait --for=delete pvc/"$cr_name-pvc" --timeout=60s >/dev/null 2>&1 ||
	fail "PVC $cr_name-pvc survived its owner CR"

printf 'Cascade proof: PVC %s was garbage-collected with its CR\n' "$cr_name-pvc"

# --- Proof 2: a CR survives its row and still converges to full cleanup -----

apply_workspace_cr "$adopt_id" workflow-adopt "$adopt_id-pvc" "  finalizers:
    - breakfix.dev/workspace-cleanup"

kubectl -n "$workspace_namespace" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $adopt_id-pvc
  ownerReferences:
    - apiVersion: breakfix.dev/v2
      kind: GeneratorWorkspace
      name: $adopt_id
      uid: $(kubectl -n "$workspace_namespace" get generatorworkspace "$adopt_id" -o jsonpath='{.metadata.uid}')
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
EOF

# Simulate the destructive schema migration: the CR exists, the database has
# no projection row for it.
adopted_row=$(psql "SELECT state FROM generator_workspaces WHERE workspace_id = '$adopt_id'")
[ "$adopted_row" = "" ] || fail "row for $adopt_id unexpectedly exists before adoption"

printf 'Waiting up to %ss for the reconciler to adopt and drop %s...\n' "$converge_seconds" "$adopt_id"
deadline=$(( $(date +%s) + converge_seconds ))
while :; do
	cr_state=$(kubectl -n "$workspace_namespace" get generatorworkspace "$adopt_id" -o jsonpath='{.status.phase}' 2>/dev/null || echo gone)
	row_state=$(psql "SELECT state FROM generator_workspaces WHERE workspace_id = '$adopt_id'")
	if [ "$cr_state" = "gone" ] && [ "$row_state" = "deleted" ]; then
		break
	fi
	if [ "$(date +%s)" -ge "$deadline" ]; then
		fail "converged to cr=$cr_state row=${row_state:-absent}; expected a deleted row without a CR (is the target running the reconciler-enabled Server?)"
	fi
	sleep 5
done

kubectl -n "$workspace_namespace" wait --for=delete pvc/"$adopt_id-pvc" --timeout=60s >/dev/null 2>&1 ||
	fail "PVC $adopt_id-pvc survived the adopted CR"

printf 'Adoption proof: CR %s was adopted, dropped, and its row completed as deleted\n' "$adopt_id"
printf 'Workspace ownership acceptance passed.\n'
