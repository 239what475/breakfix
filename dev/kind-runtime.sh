#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
worker_replicas=${BREAKFIX_KIND_WORKER_REPLICAS:-1}
manifest=${BREAKFIX_KIND_RUNTIME_MANIFEST:-$repo_root}

for command in jq kubectl; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

context=$(kubectl config current-context)
case "$context" in
  kind-*)
    ;;
  *)
    printf 'dev/kind-runtime.sh requires a Kind context, current context is %s\n' "$context" >&2
    exit 2
    ;;
esac

kubectl -n "$namespace" get secret breakfix-runtime >/dev/null 2>&1 || {
  printf 'runtime Secret breakfix-runtime is required before applying the runtime\n' >&2
  exit 1
}

secret_value() {
  kubectl -n "$namespace" get secret breakfix-runtime -o json |
    jq -r --arg key "$1" 'if .data[$key] == null then "" else .data[$key] | @base64d end'
}

registry_address=$(secret_value registry_addr)
registry_pull_secret=$(secret_value registry_pull_secret)
registry_trust_bundle_file=$(secret_value registry_trust_bundle_file)
managed_registry=$(kubectl kustomize "$manifest" | awk '
  $1 == "kind:" { kind = $2; in_metadata = 0; next }
  $1 == "metadata:" { in_metadata = 1; next }
  in_metadata && kind == "Deployment" && $1 == "name:" && $2 == "breakfix-registry" { found = 1 }
  END { print found ? "true" : "false" }
')
[ -n "$registry_address" ] || {
  printf 'runtime Secret breakfix-runtime must provide registry_addr\n' >&2
  exit 1
}
registry_authority=${registry_address%%/*}
registry_port=${registry_authority##*:}
if [ "$registry_port" = "$registry_authority" ]; then
  registry_port=443
fi
case "$registry_port" in
  '' | *[!0-9]*)
    printf 'registry_addr must contain a numeric port when one is specified\n' >&2
    exit 1
    ;;
esac
if [ -n "$registry_pull_secret" ]; then
  pull_secret_type=$(kubectl -n "$namespace" get secret "$registry_pull_secret" -o jsonpath='{.type}' 2>/dev/null || true)
  [ "$pull_secret_type" = 'kubernetes.io/dockerconfigjson' ] || {
    printf 'Docker pull Secret %s must exist in %s with type kubernetes.io/dockerconfigjson\n' "$registry_pull_secret" "$namespace" >&2
    exit 1
  }
fi

"$repo_root/dev/kind-worker-identities.sh"

case "$worker_replicas" in
  '' | *[!0-9]*)
    printf 'BREAKFIX_KIND_WORKER_REPLICAS must be a non-negative integer\n' >&2
    exit 2
    ;;
esac

endpoint=$(kubectl -n "$namespace" get secret breakfix-runtime -o json |
  jq -r '.data.incus_endpoint | @base64d')
incus_port=${endpoint##*:}
incus_port=${incus_port%%/*}
case "$incus_port" in
  '' | *[!0-9]*)
    printf 'Incus endpoint must contain an explicit numeric port\n' >&2
    exit 1
    ;;
esac

kubectl apply -k "$manifest"

registry_mode=external
if [ "$managed_registry" = true ]; then
  registry_mode=managed
  kubectl -n "$namespace" get secret breakfix-registry-tls >/dev/null 2>&1 || {
    printf 'managed Registry requires the operator-provided TLS Secret breakfix-registry-tls\n' >&2
    exit 1
  }
  kubectl -n "$namespace" get secret breakfix-registry-auth >/dev/null 2>&1 || {
    printf 'managed Registry requires the authentication Secret breakfix-registry-auth\n' >&2
    exit 1
  }
  if [ -n "$registry_trust_bundle_file" ]; then
    registry_ca=$(kubectl -n "$namespace" get configmap breakfix-registry-ca -o json 2>/dev/null |
      jq -r '.data["ca.crt"] // empty')
    [ -n "$registry_ca" ] || {
      printf 'registry_trust_bundle_file is set but ConfigMap breakfix-registry-ca has no ca.crt\n' >&2
      exit 1
    }
  fi
  kubectl -n "$namespace" wait --for=condition=Available deployment/breakfix-registry --timeout=2m >/dev/null
fi

for policy in breakfix-builder breakfix-publisher breakfix-verifier; do
  kubectl -n "$namespace" get networkpolicy "$policy" -o json |
    jq --argjson incus_port "$incus_port" --argjson registry_port "$registry_port" '
      .spec.egress |= map(
        if any(.to[]?; has("ipBlock")) then
          .ports = (((.ports // []) + [
            {protocol: "TCP", port: $incus_port},
            {protocol: "TCP", port: $registry_port}
          ])
            | unique_by([.protocol, .port]))
        else
          .
        end
      )
      | del(
          .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"],
          .metadata.creationTimestamp,
          .metadata.generation,
          .metadata.managedFields,
          .metadata.resourceVersion,
          .metadata.uid
        )
    ' | kubectl replace -f - >/dev/null
done

for deployment in agent-worker builder publisher verifier; do
  kubectl -n "$namespace" scale deployment/"breakfix-$deployment" \
    --replicas="$worker_replicas" >/dev/null
done

# Secret-backed environment variables are read only when a Pod starts. This
# development entry point applies an administrator-owned Secret and must make
# every local control-plane process observe its current values.
if [ "$managed_registry" = true ]; then
  kubectl -n "$namespace" rollout restart deployment/breakfix-registry >/dev/null
  kubectl -n "$namespace" rollout status deployment/breakfix-registry --timeout=2m >/dev/null
fi
for deployment in server controller agent-worker builder publisher verifier; do
  kubectl -n "$namespace" rollout restart deployment/"breakfix-$deployment" >/dev/null
done
for deployment in server controller agent-worker builder publisher verifier; do
  if [ "$deployment" = agent-worker ] || [ "$deployment" = builder ] || [ "$deployment" = publisher ] || [ "$deployment" = verifier ]; then
    [ "$worker_replicas" -gt 0 ] || continue
  fi
  kubectl -n "$namespace" rollout status deployment/"breakfix-$deployment" --timeout=3m >/dev/null
done

printf 'Applied Kind runtime with %s Worker replica(s), Incus egress port %s, and %s Registry %s.\n' \
  "$worker_replicas" "$incus_port" "$registry_mode" "$registry_address"
printf 'Verify that every Kind node resolves this Registry endpoint and trusts its TLS CA before VK8s image pulls.\n'
