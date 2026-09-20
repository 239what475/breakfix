#!/bin/sh
set -eu

# Bootstrap the generatable prerequisites of a disposable E2E target on an
# empty Kind cluster: the runtime Secret (random database, JWT, and Registry
# credentials; placeholder values for everything a later prepare owns) and
# the Registry htpasswd Secret. Everything the full profile cannot generate -
# the Incus endpoint, certificates, and projects - stays with the operator:
# run this script, then BREAKFIX_E2E_PROFILE=core make e2e-prepare.

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}
registry_auth_secret=breakfix-registry-auth

fail() {
	printf 'Breakfix E2E bootstrap: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

for tool in docker jq kubectl openssl; do require_command "$tool"; done

context=$(kubectl config current-context)
case "$context" in
	kind-*)
		;;
	*)
		fail "requires a Kind context, current context is ${context:-unset}"
		;;
esac

kubectl apply -f "$repo_root/deploy/manifests/namespace.yaml" >/dev/null

kubectl -n "$namespace" get secret "$runtime_secret" >/dev/null 2>&1 &&
	fail "runtime Secret $namespace/$runtime_secret already exists; refusing to regenerate the credentials of a live target"
kubectl -n "$namespace" get secret "$registry_auth_secret" >/dev/null 2>&1 &&
	fail "Secret $namespace/$registry_auth_secret already exists; refusing to regenerate the credentials of a live target"

# Hex secrets keep the database URL free of characters that would need
# percent-encoding.
postgres_password=$(openssl rand -hex 24)
jwt_secret=$(openssl rand -hex 32)
registry_username=breakfix
registry_password=$(openssl rand -hex 24)
registry_htpasswd=$(docker run --rm httpd:2-alpine htpasswd -bnB "$registry_username" "$registry_password" | tr -d '\r')

# registry_repository, registry_trust_bundle_file, k8s_base_image_digest,
# ui_origin, and catalog_release_reference are owned by the prepare chain and
# start empty or as placeholders. The documentation prepare replaces the model
# key with its fixture value, and OpenSandbox is never called under the core
# profile, but the Server configuration validation requires a non-empty
# lifecycle key.
kubectl -n "$namespace" create secret generic "$runtime_secret" \
	--from-literal=database_url="postgres://breakfix:${postgres_password}@breakfix-postgresql:5432/breakfix?sslmode=disable" \
	--from-literal=postgres_password="$postgres_password" \
	--from-literal=jwt_secret="$jwt_secret" \
	--from-literal=registry_username="$registry_username" \
	--from-literal=registry_password="$registry_password" \
	--from-literal=registry_pull_secret=breakfix-registry-pull \
	--from-literal=deepseek_api_key=unused-in-core-profile \
	--from-literal=opensandbox_api_key=unused-in-core-profile \
	--from-literal=ui_origin=http://127.0.0.1:9 \
	--from-literal=catalog_release_reference= \
	--from-literal=k8s_base_image_digest= \
	>/dev/null

kubectl -n "$namespace" create secret generic "$registry_auth_secret" \
	--from-literal=htpasswd="$registry_htpasswd" >/dev/null

printf 'Bootstrapped a disposable core target on %s.\n' "$context"
printf 'Continue with: BREAKFIX_E2E_PROFILE=core make e2e-prepare\n'
printf 'A full-profile target additionally needs the Incus fields in %s and the breakfix-incus-* Secrets.\n' "$runtime_secret"
