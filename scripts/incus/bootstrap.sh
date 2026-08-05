#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
remote=${BREAKFIX_INCUS_REMOTE:-incus-cluster}
build_project=${BREAKFIX_INCUS_BUILD_PROJECT:-breakfix-build}
image_project=${BREAKFIX_INCUS_IMAGE_PROJECT:-breakfix-images}
storage_pool=${BREAKFIX_INCUS_STORAGE_POOL:-local}
base_alias=${BREAKFIX_INCUS_BASE_ALIAS:-node-systemd-base-v1}
tls_dir=${BREAKFIX_INCUS_TLS_DIR:-$repo_root/.local/incus}
incus_config_dir=${INCUS_CONF:-$HOME/.config/incus}
network=bf-bootstrap
network_cidr=${BREAKFIX_INCUS_BUILD_NETWORK_CIDR:-10.248.25.1/24}
profile=breakfix-bootstrap
instance=breakfix-bootstrap-node-systemd-base
upstream_alias=ubuntu/24.04
base_packages="bash ca-certificates curl dnsutils gnupg iproute2 iputils-ping jq less lsof nano netcat-openbsd openssh-client openssh-server procps psmisc rsync socat strace tcpdump tmux traceroute vim-tiny wget"

runtime_init="$repo_root/build/images/node-systemd-base/runtime-init.sh"
runtime_unit="$repo_root/build/images/node-systemd-base/breakfix-runtime-init.service"

require_file() {
  [ -f "$1" ] || {
    printf 'required file does not exist: %s\n' "$1" >&2
    exit 1
  }
}

certificate_fingerprint() {
  openssl x509 -in "$1" -outform DER | sha256sum | awk '{ print $1 }'
}

trust_certificate() {
  role=$1
  projects=$2
  role_dir=$tls_dir/$role
  certificate=$role_dir/client.crt
  key=$role_dir/client.key
  server_certificate=$incus_config_dir/servercerts/$remote.crt
  trust_name=breakfix-$role

  require_file "$server_certificate"
  mkdir -p "$role_dir"
  chmod 0700 "$role_dir"
  if [ ! -f "$certificate" ] || [ ! -f "$key" ]; then
    [ ! -e "$certificate" ] && [ ! -e "$key" ] || {
      printf 'incomplete Incus TLS identity in %s; remove both files before regenerating\n' "$role_dir" >&2
      exit 1
    }
    openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 3650 \
      -subj "/CN=$trust_name" -keyout "$key" -out "$certificate" >/dev/null 2>&1
    # Incus validates notBefore at second precision. Avoid racing the server in
    # the exact second a freshly generated certificate becomes valid.
    sleep 2
  fi
  if [ ! -f "$role_dir/server.crt" ] || ! cmp -s "$server_certificate" "$role_dir/server.crt"; then
    chmod 0600 "$role_dir/server.crt" 2>/dev/null || true
    cp "$server_certificate" "$role_dir/server.crt"
  fi
  chmod 0400 "$key"
  chmod 0444 "$certificate" "$role_dir/server.crt"

  fingerprint=$(certificate_fingerprint "$certificate")
  if incus config trust show "$remote:$fingerprint" >/dev/null 2>&1; then
    trust=$(incus config trust show "$remote:$fingerprint")
    actual_name=$(printf '%s\n' "$trust" | awk '/^name:/ { print $2; exit }')
    actual_restricted=$(printf '%s\n' "$trust" | awk '/^restricted:/ { print $2; exit }')
    if [ -n "$projects" ]; then
      expected_restricted=true
      actual_projects=$(printf '%s\n' "$trust" | awk '
        /^projects:/ { in_projects=1; next }
        in_projects && /^  - / { print $2; next }
        in_projects { exit }
      ' | sort | paste -sd, -)
      expected_projects=$(printf '%s\n' "$projects" | tr ',' '\n' | sort | paste -sd, -)
    else
      expected_restricted=false
      actual_projects=$(printf '%s\n' "$trust" | awk '/^projects:/ { print $2; exit }')
      expected_projects='[]'
    fi
    if [ "$actual_name" = "$trust_name" ] && \
      [ "$actual_restricted" = "$expected_restricted" ] && \
      [ "$actual_projects" = "$expected_projects" ]; then
      return
    fi
    # This bootstrap owns these role identities. Recreate only its own trust
    # entry when its declared access scope changes so development bootstrap is
    # convergent rather than requiring manual Incus state repair.
    incus config trust remove "$remote:$fingerprint"
  fi

  if [ -n "$projects" ]; then
    incus config trust add-certificate "$remote:" "$certificate" \
      --name "$trust_name" --description "Breakfix $role runtime identity" \
      --restricted --projects "$projects"
  else
    incus config trust add-certificate "$remote:" "$certificate" \
      --name "$trust_name" --description "Breakfix $role runtime identity"
  fi
}

project_exists() {
  incus project show "$remote:$1" >/dev/null 2>&1
}

ensure_project() {
  project=$1
  if ! project_exists "$project"; then
    incus project create "$remote:$project" --description "Breakfix managed platform project" \
      -c features.images=true \
      -c features.networks=false \
      -c features.profiles=true \
      -c features.storage.volumes=true
  fi
  incus project set "$remote:$project" \
    features.images=true \
    features.networks=false \
    features.profiles=true \
    features.storage.volumes=true
}

image_fingerprint() {
  image=$1
  project=${2:-}
  if [ -n "$project" ]; then
    incus image info "$image" --project "$project" | awk '/^Fingerprint:/ { print $2; exit }'
  else
    incus image info "$image" | awk '/^Fingerprint:/ { print $2; exit }'
  fi
}

image_property() {
	image=$1
	property=$2
	project=$3
	incus image get-property "$image" "$property" --project "$project" 2>/dev/null || true
}

image_never_expires() {
	image=$1
	project=$2
	expires_at=$(incus image show "$image" --project "$project" | awk '/^expires_at:/ { print $2; exit }')
	case "$expires_at" in
		''|0001-01-01T00:00:00*) return 0 ;;
		*) return 1 ;;
	esac
}

clear_instance_metadata_expiry() {
	instance_name=$1
	metadata=$(mktemp)
	incus config metadata show "$remote:$instance_name" --project "$build_project" >"$metadata"
	sed -i '/^[[:space:]]*expiry_date:/d' "$metadata"
	incus config metadata edit "$remote:$instance_name" --project "$build_project" <"$metadata"
	rm -f "$metadata"
}

require_file "$runtime_init"
require_file "$runtime_unit"
command -v incus >/dev/null 2>&1 || {
  printf 'incus CLI is required for bootstrap\n' >&2
  exit 1
}
command -v openssl >/dev/null 2>&1 || {
  printf 'openssl is required for role-specific Incus identities\n' >&2
  exit 1
}

server_version=$(incus version | awk '/Server version:/ { print $3; exit }')
[ "$server_version" = "7.0.1" ] || {
  printf 'Incus server version %s is unsupported; require 7.0.1\n' "$server_version" >&2
  exit 1
}

ensure_project "$build_project"
ensure_project "$image_project"

trust_certificate server ""
trust_certificate controller ""
# Runtime Worker builds and verifies provider-side artifacts, including work in
# short-lived NodeEnvironment projects created by Controller. Those projects do
# not exist when this identity is provisioned, so a static project allowlist
# cannot express the required verification access.
trust_certificate runtime ""

if ! incus network show "$remote:$network" --project default >/dev/null 2>&1; then
  incus network create "$remote:$network" --project default --type bridge \
    ipv4.address="$network_cidr" ipv4.nat=true ipv6.address=none
fi
actual_network_cidr=$(incus network get "$remote:$network" ipv4.address --project default)
actual_network_nat=$(incus network get "$remote:$network" ipv4.nat --project default)
actual_network_ipv6=$(incus network get "$remote:$network" ipv6.address --project default)
[ "$actual_network_cidr" = "$network_cidr" ] && \
  [ "$actual_network_nat" = true ] && \
  [ "$actual_network_ipv6" = none ] || {
  printf 'Incus bootstrap network %s must use ipv4.address=%s, ipv4.nat=true, ipv6.address=none\n' \
    "$network" "$network_cidr" >&2
  exit 1
}
incus project set "$remote:$build_project" \
  restricted=true \
  restricted.cluster.target=block \
  restricted.containers.lowlevel=block \
  restricted.containers.nesting=block \
  restricted.containers.privilege=isolated \
  restricted.devices.disk=managed \
  restricted.devices.nic=managed \
  restricted.networks.access="$network" \
  restricted.storage-pools.access="$storage_pool"
incus project set "$remote:$image_project" \
  restricted=true \
  restricted.cluster.target=block \
  restricted.containers.lowlevel=block \
  restricted.containers.nesting=block \
  restricted.containers.privilege=isolated \
  restricted.devices.disk=managed \
  restricted.devices.nic=managed \
  restricted.storage-pools.access="$storage_pool"

if ! incus profile show "$remote:$profile" --project "$build_project" >/dev/null 2>&1; then
  incus profile create "$remote:$profile" --project "$build_project"
fi
incus profile set "$remote:$profile" --project "$build_project" \
  security.privileged=false \
  security.idmap.isolated=true \
  limits.cpu=2 \
  limits.memory=2GiB \
  limits.processes=1024
if incus profile device show "$remote:$profile" --project "$build_project" | awk '$1 == "root:" { found=1 } END { exit !found }'; then
  incus profile device set "$remote:$profile" root pool="$storage_pool" path=/ size=8GiB --project "$build_project"
else
  incus profile device add "$remote:$profile" root disk pool="$storage_pool" path=/ size=8GiB --project "$build_project"
fi
if incus profile device show "$remote:$profile" --project "$build_project" | awk '$1 == "eth0:" { found=1 } END { exit !found }'; then
  incus profile device set "$remote:$profile" eth0 network="$network" name=eth0 --project "$build_project"
else
  incus profile device add "$remote:$profile" eth0 nic network="$network" name=eth0 --project "$build_project"
fi

upstream_fingerprint=$(image_fingerprint "images:$upstream_alias")
[ ${#upstream_fingerprint} -eq 64 ] || {
  printf 'failed to resolve full upstream fingerprint for images:%s\n' "$upstream_alias" >&2
  exit 1
}
upstream_local_alias="breakfix-upstream-${upstream_fingerprint}"
if ! incus image info "$remote:$upstream_fingerprint" --project "$build_project" >/dev/null 2>&1; then
  incus image copy "images:$upstream_fingerprint" "$remote:" \
    --target-project "$build_project" --alias "$upstream_local_alias"
fi

bootstrap_revision=$(
  {
    printf '%s\n' "$upstream_fingerprint"
    printf '%s\n' "$base_packages"
    sha256sum "$runtime_init" "$runtime_unit"
  } | sha256sum | awk '{ print $1 }'
)
old_build_fingerprint=$(image_fingerprint "$remote:$base_alias" "$build_project" 2>/dev/null || true)
base_fingerprint=
if [ -n "$old_build_fingerprint" ] && \
  [ "$(image_property "$remote:$base_alias" user.breakfix.role "$build_project")" = node-systemd-base ] && \
  [ "$(image_property "$remote:$base_alias" user.breakfix.upstream_fingerprint "$build_project")" = "$upstream_fingerprint" ] && \
  [ "$(image_property "$remote:$base_alias" user.breakfix.bootstrap_revision "$build_project")" = "$bootstrap_revision" ] && \
  image_never_expires "$remote:$base_alias" "$build_project"; then
  base_fingerprint=$old_build_fingerprint
fi

if [ -z "$base_fingerprint" ]; then
  if incus info "$remote:$instance" --project "$build_project" >/dev/null 2>&1; then
    incus delete "$remote:$instance" --project "$build_project" --force
  fi
  cleanup() {
    if incus info "$remote:$instance" --project "$build_project" >/dev/null 2>&1; then
      incus delete "$remote:$instance" --project "$build_project" --force >/dev/null 2>&1 || true
    fi
  }
  trap cleanup EXIT HUP INT TERM

  incus launch "$remote:$upstream_fingerprint" "$remote:$instance" \
    --project "$build_project" --profile "$profile"
  incus exec "$remote:$instance" --project "$build_project" \
    --env "BREAKFIX_BASE_PACKAGES=$base_packages" -- sh -ec '
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  # The package list is a trusted bootstrap input and is intentionally word-split.
  apt-get install -y --no-install-recommends $BREAKFIX_BASE_PACKAGES
  apt-get clean
  rm -rf /var/lib/apt/lists/*
  install -d -m 0755 /usr/local/libexec /opt/breakfix/challenge /var/lib/breakfix/runtime-init
'
  incus file push "$runtime_init" "$remote:$instance/usr/local/libexec/breakfix-runtime-init" \
    --project "$build_project" --uid 0 --gid 0 --mode 0755
  incus file push "$runtime_unit" "$remote:$instance/etc/systemd/system/breakfix-runtime-init.service" \
    --project "$build_project" --uid 0 --gid 0 --mode 0644
  incus exec "$remote:$instance" --project "$build_project" -- sh -ec '
  systemctl daemon-reload
  systemctl enable breakfix-runtime-init.service
  test "$(cat /proc/1/comm)" = systemd
  test -f /sys/fs/cgroup/cgroup.controllers
  tmux -V
	'
	incus stop "$remote:$instance" --project "$build_project" --timeout 60
	clear_instance_metadata_expiry "$instance"
	incus publish "$remote:$instance" "$remote:" --project "$build_project" \
    --alias "$base_alias" --reuse \
    user.breakfix.role=node-systemd-base \
    user.breakfix.upstream_fingerprint="$upstream_fingerprint" \
    user.breakfix.bootstrap_revision="$bootstrap_revision"

  base_fingerprint=$(image_fingerprint "$remote:$base_alias" "$build_project")
  cleanup
  trap - EXIT HUP INT TERM
fi
[ ${#base_fingerprint} -eq 64 ] || {
	printf 'failed to resolve published base fingerprint\n' >&2
	exit 1
}
image_never_expires "$remote:$base_fingerprint" "$build_project" || {
	printf 'published base image must not expire\n' >&2
	exit 1
}
if ! incus image info "$remote:$base_fingerprint" --project "$image_project" >/dev/null 2>&1; then
	incus image copy "$remote:$base_fingerprint" "$remote:" \
		--project "$build_project" --target-project "$image_project"
fi
image_never_expires "$remote:$base_fingerprint" "$image_project" || {
	printf 'copied base image must not expire\n' >&2
	exit 1
}
old_image_fingerprint=$(image_fingerprint "$remote:$base_alias" "$image_project" 2>/dev/null || true)
if [ "$old_image_fingerprint" != "$base_fingerprint" ]; then
  if [ -n "$old_image_fingerprint" ]; then
    incus image alias delete "$remote:$base_alias" --project "$image_project"
  fi
  incus image alias create "$remote:$base_alias" "$base_fingerprint" --project "$image_project"
fi
if [ -n "$old_build_fingerprint" ] && [ "$old_build_fingerprint" != "$base_fingerprint" ]; then
  if incus image info "$remote:$old_build_fingerprint" --project "$build_project" >/dev/null 2>&1; then
    incus image delete "$remote:$old_build_fingerprint" --project "$build_project"
  fi
fi
if [ -n "$old_image_fingerprint" ] && [ "$old_image_fingerprint" != "$base_fingerprint" ]; then
  if [ "$(image_property "$remote:$old_image_fingerprint" user.breakfix.role "$image_project")" = node-systemd-base ]; then
    incus image delete "$remote:$old_image_fingerprint" --project "$image_project"
  fi
fi

printf 'Incus bootstrap completed.\n'
printf 'upstream_fingerprint=%s\n' "$upstream_fingerprint"
printf 'base_image_alias=%s\n' "$base_alias"
printf 'base_image_fingerprint=%s\n' "$base_fingerprint"
printf 'tls_directory=%s\n' "$tls_dir"
