#!/bin/sh
set -eu

namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}

for command in base64 jq kubectl openssl tr; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

kubectl -n "$namespace" get secret "$runtime_secret" >/dev/null

read_runtime_key() {
  kubectl -n "$namespace" get secret "$runtime_secret" -o json |
    jq -r --arg key "$1" '.data[$key] // "" | @base64d'
}

encode_secret() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

new_key() {
  openssl rand -base64 48 | tr -d '\n'
}

agent_key=$(read_runtime_key agent_worker_api_key)
builder_key=$(read_runtime_key builder_worker_api_key)
publisher_key=$(read_runtime_key publisher_worker_api_key)
verifier_key=$(read_runtime_key verifier_worker_api_key)

# A partial or shared previous identity is unsafe. Rotate all four together so
# the Server and every Worker see one consistent, role-specific key set.
if [ -z "$agent_key" ] || [ -z "$builder_key" ] || [ -z "$publisher_key" ] || [ -z "$verifier_key" ] || \
  [ "$agent_key" = "$builder_key" ] || [ "$agent_key" = "$publisher_key" ] || [ "$agent_key" = "$verifier_key" ] || \
  [ "$builder_key" = "$publisher_key" ] || [ "$builder_key" = "$verifier_key" ] || [ "$publisher_key" = "$verifier_key" ]; then
  agent_key=$(new_key)
  builder_key=$(new_key)
  publisher_key=$(new_key)
  verifier_key=$(new_key)

  patch=$(jq -cn \
    --arg agent "$(encode_secret "$agent_key")" \
    --arg builder "$(encode_secret "$builder_key")" \
    --arg publisher "$(encode_secret "$publisher_key")" \
    --arg verifier "$(encode_secret "$verifier_key")" \
    '{data: {
      agent_worker_api_key: $agent,
      builder_worker_api_key: $builder,
      publisher_worker_api_key: $publisher,
      verifier_worker_api_key: $verifier
    }}')
  kubectl -n "$namespace" patch secret "$runtime_secret" --type merge --patch "$patch" >/dev/null
fi

apply_identity() {
  role=$1
  key=$2
  kubectl -n "$namespace" create secret generic "breakfix-$role-identity" \
    --from-literal=worker_api_key="$key" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  actual=$(kubectl -n "$namespace" get secret "breakfix-$role-identity" -o json |
    jq -r '.data.worker_api_key // "" | @base64d')
  [ "$actual" = "$key" ] || {
    printf 'Worker identity Secret for %s does not match %s\n' "$role" "$runtime_secret" >&2
    exit 1
  }
}

apply_identity agent-worker "$agent_key"
apply_identity builder "$builder_key"
apply_identity publisher "$publisher_key"
apply_identity verifier "$verifier_key"

# The previous runtime exposed one shared internal API key. The fixed Worker
# protocol has no reader for it, so remove it after all role identities are
# proven to match the Server's key set.
if kubectl -n "$namespace" get secret "$runtime_secret" -o json |
  jq -e '.data.internal_api_key != null' >/dev/null; then
  kubectl -n "$namespace" patch secret "$runtime_secret" --type json \
    --patch '[{"op":"remove","path":"/data/internal_api_key"}]' >/dev/null
fi

printf 'Verified four role-specific Worker identity Secrets in %s.\n' "$namespace"
