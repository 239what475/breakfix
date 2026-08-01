#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}

for command in base64 jq kubectl tr; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

registry_repository=$(kubectl -n "$namespace" get secret "$runtime_secret" -o json |
	jq -r '.data.registry_repository // "" | @base64d')
[ -n "$registry_repository" ] || {
	printf '%s must provide registry_repository\n' "$runtime_secret" >&2
  exit 1
}
kubectl -n "$namespace" get secret "$runtime_secret" >/dev/null

reference=$("$repo_root/scripts/kind/push-image.sh" \
  breakfix-k8s-base:latest "$registry_repository/k8s-base:latest")
encoded=$(printf '%s' "$reference" | base64 | tr -d '\n')
patch=$(jq -cn --arg digest "$encoded" '{data: {k8s_base_image_digest: $digest}}')
kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch "$patch" >/dev/null
printf 'Published Kind K8s base image: %s\n' "$reference"
