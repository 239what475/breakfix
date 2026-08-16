#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_worker_replicas=${BREAKFIX_KIND_RUNTIME_WORKER_REPLICAS:-1}
root_manifest=${BREAKFIX_KIND_ROOT_MANIFEST:-$repo_root}
kind_overlay=${BREAKFIX_KIND_OVERLAY_MANIFEST:-$repo_root/deploy/overlays/kind}
registry_node_port=30443

for command in docker jq kind kubectl; do
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
    printf 'scripts/kind/runtime.sh requires a Kind context, current context is %s\n' "$context" >&2
    exit 2
    ;;
esac

# The development manifests intentionally use the mutable `:dev` image tags.
# Load the freshly built local images into this Kind cluster before applying
# them so `IfNotPresent` cannot silently reuse an older node cache.
kind_cluster=${context#kind-}
runtime_images=$(kubectl kustomize "$root_manifest" | awk '
  /^[[:space:]]*image: ghcr.io\/breakfix\/breakfix-/ { print $2 }
')
[ -n "$runtime_images" ] || {
  printf 'could not find Breakfix runtime images in %s\n' "$root_manifest" >&2
  exit 1
}
for image in $runtime_images; do
  docker image inspect "$image" >/dev/null 2>&1 || {
    printf 'local runtime image is required before Kind deployment: %s\n' "$image" >&2
    exit 1
  }
  kind load docker-image --name "$kind_cluster" "$image" >/dev/null
done

kubectl apply -f "$repo_root/deploy/manifests/namespace.yaml" >/dev/null

kubectl -n "$namespace" get secret breakfix-runtime >/dev/null 2>&1 || {
  printf 'runtime Secret breakfix-runtime is required before applying the runtime\n' >&2
  exit 1
}

kubectl apply -k "$root_manifest"

"$repo_root/scripts/kind/registry.sh"
kubectl apply -k "$kind_overlay"
kubectl -n "$namespace" wait --for=condition=Available deployment/breakfix-registry --timeout=2m >/dev/null

secret_value() {
  kubectl -n "$namespace" get secret breakfix-runtime -o json |
    jq -r --arg key "$1" 'if .data[$key] == null then "" else .data[$key] | @base64d end'
}

registry_repository=$(secret_value registry_repository)
registry_pull_secret=$(secret_value registry_pull_secret)
registry_trust_bundle_file=$(secret_value registry_trust_bundle_file)
[ -n "$registry_repository" ] || {
  printf 'runtime Secret breakfix-runtime must provide registry_repository\n' >&2
  exit 1
}
registry_authority=${registry_repository%%/*}
registry_port=${registry_authority##*:}
if [ "$registry_port" = "$registry_authority" ]; then
  registry_port=443
fi
case "$registry_port" in
  '' | *[!0-9]*)
    printf 'registry_repository must contain a numeric port when one is specified\n' >&2
    exit 1
    ;;
esac
[ "$registry_port" = "$registry_node_port" ] || {
  printf 'Kind runtime registry_repository must use NodePort %s, got %s\n' "$registry_node_port" "$registry_repository" >&2
  exit 1
}
if [ -n "$registry_pull_secret" ]; then
  pull_secret_type=$(kubectl -n "$namespace" get secret "$registry_pull_secret" -o jsonpath='{.type}' 2>/dev/null || true)
  [ "$pull_secret_type" = 'kubernetes.io/dockerconfigjson' ] || {
    printf 'Docker pull Secret %s must exist in %s with type kubernetes.io/dockerconfigjson\n' "$registry_pull_secret" "$namespace" >&2
    exit 1
  }
  pull_secret_hosts=$(kubectl -n "$namespace" get secret "$registry_pull_secret" -o json |
    jq -r '.data[".dockerconfigjson"] | @base64d | fromjson | .auths | keys[]')
  printf '%s\n' "$pull_secret_hosts" | grep -Fqx "$registry_authority" || {
    printf 'Docker pull Secret %s must contain credentials for %s\n' "$registry_pull_secret" "$registry_authority" >&2
    exit 1
  }
fi

"$repo_root/scripts/kind/worker-identities.sh"

kubectl -n "$namespace" get secret breakfix-registry-tls >/dev/null 2>&1 || {
  printf 'Kind Registry preparation did not create TLS Secret breakfix-registry-tls\n' >&2
  exit 1
}
kubectl -n "$namespace" get secret breakfix-registry-auth >/dev/null 2>&1 || {
  printf 'Kind Registry requires authentication Secret breakfix-registry-auth\n' >&2
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

case "$runtime_worker_replicas" in
  '' | *[!0-9]*)
    printf 'BREAKFIX_KIND_RUNTIME_WORKER_REPLICAS must be a non-negative integer\n' >&2
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

base_image_digest=$(secret_value k8s_base_image_digest)
case "$base_image_digest" in
  "$registry_repository/k8s-base@"*)
    ;;
  *)
    docker image inspect breakfix-k8s-base:latest >/dev/null 2>&1 || {
      printf 'local image breakfix-k8s-base:latest is required after the Kind Registry endpoint changed\n' >&2
      printf 'build it with: make images\n' >&2
      exit 1
    }
    "$repo_root/scripts/kind/push-k8s-base.sh"
    ;;
esac

actual_registry_node_port=$(kubectl -n "$namespace" get service breakfix-registry \
  -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')
[ "$actual_registry_node_port" = "$registry_node_port" ] || {
  printf 'Kind Registry Service must expose NodePort %s, got %s\n' "$registry_node_port" "$actual_registry_node_port" >&2
  exit 1
}
kubectl -n "$namespace" get networkpolicy breakfix-runtime-worker -o json |
  jq --argjson incus_port "$incus_port" --argjson registry_node_port "$registry_node_port" '
    .spec.egress |= map(
      if any(.to[]?; has("ipBlock")) then
        .ports = (((.ports // []) + [
          {protocol: "TCP", port: $incus_port},
          {protocol: "TCP", port: $registry_node_port}
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

kubectl -n "$namespace" get networkpolicy breakfix-runtime-worker -o json |
  jq -e '
    any(.spec.egress[]?;
      any(.to[]?; .podSelector.matchLabels["app.kubernetes.io/name"] == "breakfix-registry") and
      any(.ports[]?; .protocol == "TCP" and .port == 5000)
    )
  ' >/dev/null || {
    printf 'Runtime Worker NetworkPolicy must allow Registry Pods on TCP 5000\n' >&2
    exit 1
  }

kubectl -n "$namespace" scale deployment/breakfix-runtime-worker \
  --replicas="$runtime_worker_replicas" >/dev/null

# Secret-backed environment variables are read only when a Pod starts. This
# development entry point applies an administrator-owned Secret and must make
# every local control-plane process observe its current values.
kubectl -n "$namespace" rollout restart deployment/breakfix-registry >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-registry --timeout=2m >/dev/null
for deployment in server controller runtime-worker; do
  kubectl -n "$namespace" rollout restart deployment/"breakfix-$deployment" >/dev/null
done
for deployment in server controller runtime-worker; do
  if [ "$deployment" = runtime-worker ] && [ "$runtime_worker_replicas" -eq 0 ]; then
    continue
  fi
  kubectl -n "$namespace" rollout status deployment/"breakfix-$deployment" --timeout=3m >/dev/null
done

printf 'Applied Kind runtime with %s Runtime Worker replica(s), Incus egress port %s, and Registry NodePort %s at %s.\n' \
	"$runtime_worker_replicas" "$incus_port" "$registry_node_port" "$registry_repository"
printf 'Kind nodes trust the Registry CA through their system trust store; no custom DNS or /etc/hosts entry is required.\n'
