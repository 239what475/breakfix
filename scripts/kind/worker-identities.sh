#!/bin/sh
set -eu

namespace=${BREAKFIX_NAMESPACE:-breakfix-system}

for command in base64 jq kubectl openssl tr; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

encode_secret() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

new_key() {
  openssl rand -base64 48 | tr -d '\n'
}

read_identity() {
  kubectl -n "$namespace" get secret "breakfix-$1-identity" -o json 2>/dev/null |
    jq -r '.data.worker_api_key // "" | @base64d'
}

runtime_worker_api_key=$(read_identity runtime-worker || true)

# The Server validates the fixed Runtime Worker identity before exposing its
# internal workflow API.
if [ -z "$runtime_worker_api_key" ]; then
  runtime_worker_api_key=$(new_key)
fi

apply_identity() {
  role=$1
  key=$2
  kubectl -n "$namespace" create secret generic "breakfix-$role-identity" \
    --from-literal=worker_api_key="$key" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  actual=$(read_identity "$role")
  [ "$actual" = "$key" ] || {
    printf 'Worker identity Secret for %s is inconsistent\n' "$role" >&2
    exit 1
  }
}

apply_identity runtime-worker "$runtime_worker_api_key"

printf 'Verified Runtime Worker identity Secret in %s.\n' "$namespace"
