#!/bin/sh
set -eu

namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
workspace_namespace=${BREAKFIX_OPENSANDBOX_NAMESPACE:-opensandbox}
context=$(kubectl config current-context)

case "$context" in
  kind-*)
    ;;
  *)
    if [ "${BREAKFIX_ALLOW_NON_KIND_RESET:-}" != "1" ]; then
      printf 'refusing to reset state outside a Kind context: %s\n' "$context" >&2
      printf 'set BREAKFIX_ALLOW_NON_KIND_RESET=1 only after intentionally selecting a disposable cluster\n' >&2
      exit 2
    fi
    ;;
esac

for command in kubectl sleep; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

scale_down() {
  kind=$1
  name=$2
  if kubectl -n "$namespace" get "$kind" "$name" >/dev/null 2>&1; then
    kubectl -n "$namespace" scale "$kind" "$name" --replicas=0 >/dev/null
  fi
}

wait_for_pods() {
  selector=$1
  attempts=0
  while kubectl -n "$namespace" get pods -l "$selector" -o name 2>/dev/null | grep -q .; do
    attempts=$((attempts + 1))
    if [ "$attempts" -gt 120 ]; then
      printf 'timed out waiting for Pods matching %s to stop\n' "$selector" >&2
      exit 1
    fi
    sleep 1
  done
}

# The current schema deliberately has no migration from the former development
# database. Stop every process that can hold either RWO volume before removal.
for deployment in breakfix-server breakfix-generate-worker breakfix-taxonomy-worker; do
  scale_down deployment "$deployment"
done
scale_down statefulset breakfix-postgresql

wait_for_pods app.kubernetes.io/name=breakfix-server
wait_for_pods app.kubernetes.io/name=breakfix-generate-worker
wait_for_pods app.kubernetes.io/name=breakfix-taxonomy-worker
wait_for_pods app.kubernetes.io/name=breakfix-postgresql

# Runtime test and catalog-sync Pods can retain the Server RWO claim after a
# previous interrupted test. They are disposable local helpers, not product
# environments.
kubectl -n "$namespace" delete pod -l app.kubernetes.io/name=breakfix-runtime-candidate-data \
  --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$namespace" delete pod breakfix-catalog-sync --ignore-not-found --wait=true >/dev/null 2>&1 || true

# Generator workspaces are Server-owned but materialized by OpenSandbox in a
# separate namespace. A disposable Kind reset must remove both the provider
# resource and its explicitly labeled PVCs after stopping every Breakfix
# process, otherwise interrupted live tests pollute the next baseline.
if kubectl get namespace "$workspace_namespace" >/dev/null 2>&1; then
  kubectl -n "$workspace_namespace" delete batchsandboxes -l breakfix.generator_run_id \
    --ignore-not-found --wait=true >/dev/null
  kubectl -n "$workspace_namespace" delete persistentvolumeclaims -l app.kubernetes.io/part-of=breakfix \
    --ignore-not-found --wait=true >/dev/null
fi

# Kustomize creates a content-addressed ConfigMap. Old hashes are unreferenced
# after the control plane stops and must not make a reset look like it still
# carries the previous internal-worker contract.
kubectl -n "$namespace" get configmap -o name | while IFS= read -r resource; do
  case "$resource" in
    configmap/breakfix-config-*)
      kubectl -n "$namespace" delete "$resource" --ignore-not-found --wait=true >/dev/null
      ;;
  esac
done

kubectl -n "$namespace" delete persistentvolumeclaim breakfix-server-data --ignore-not-found --wait=true
kubectl -n "$namespace" delete persistentvolumeclaim data-breakfix-postgresql-0 --ignore-not-found --wait=true

printf 'Removed Kind PostgreSQL and Server data state in %s. Reapply the runtime and sync the catalog.\n' "$namespace"
