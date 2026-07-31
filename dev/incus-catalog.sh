#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
remote=${BREAKFIX_INCUS_REMOTE:-incus-cluster}
build_project=${BREAKFIX_INCUS_BUILD_PROJECT:-breakfix-build}
image_project=${BREAKFIX_INCUS_IMAGE_PROJECT:-breakfix-images}
storage_pool=${BREAKFIX_INCUS_STORAGE_POOL:-local}
base_alias=${BREAKFIX_INCUS_BASE_ALIAS:-node-systemd-base-v1}
node_network_pool=${BREAKFIX_INCUS_NODE_NETWORK_POOL:-10.240.0.0/16}
node_network_prefix=${BREAKFIX_INCUS_NODE_NETWORK_PREFIX:-24}
tls_dir=${BREAKFIX_INCUS_TLS_DIR:-$repo_root/.local/incus}
challenge_dir=${BREAKFIX_CATALOG_CHALLENGE_DIR:-$repo_root/data/challenges/cleanup-logs}
incus_config_dir=${INCUS_CONF:-$HOME/.config/incus}

[ -d "$challenge_dir" ] || {
  printf 'Catalog challenge does not exist: %s\n' "$challenge_dir" >&2
  exit 1
}

for command in incus jq; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

endpoint=$(incus remote list --format json | jq -r --arg remote "$remote" '.[$remote].LastWorkingAddr // .[$remote].Addrs[0] // empty')
[ -n "$endpoint" ] || {
  printf 'Incus remote does not have a usable endpoint: %s\n' "$remote" >&2
  exit 1
}
server_certificate=$incus_config_dir/servercerts/$remote.crt
[ -f "$server_certificate" ] || {
  printf 'Incus server certificate does not exist: %s\n' "$server_certificate" >&2
  exit 1
}
base_fingerprint=$(incus image list "$remote:" "$base_alias" --project "$image_project" --format csv,noheader --columns F)
case "$base_fingerprint" in
  ????????* )
    [ ${#base_fingerprint} -eq 64 ] || {
      printf 'Incus base image fingerprint is invalid: %s\n' "$base_fingerprint" >&2
      exit 1
    }
    ;;
  *)
    printf 'Unable to resolve Incus base image alias: %s\n' "$base_alias" >&2
    exit 1
    ;;
esac

exec go run "$repo_root/cmd/catalog-seed" \
  -endpoint "$endpoint" \
  -server-certificate "$server_certificate" \
  -storage-pool "$storage_pool" \
  -build-project "$build_project" \
  -image-project "$image_project" \
  -base-image-alias "$base_alias" \
  -base-image-fingerprint "$base_fingerprint" \
  -node-network-pool "$node_network_pool" \
  -node-network-prefix "$node_network_prefix" \
  -tls-dir "$tls_dir" \
  -challenge-dir "$challenge_dir" \
  -replace-existing-image
