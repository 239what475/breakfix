#!/bin/sh
set -eu

state_dir=/var/lib/breakfix/runtime-init
credentials_dir=${CREDENTIALS_DIRECTORY:-/dev/.incus-systemd-credentials}
bundle=/opt/breakfix/challenge
mkdir -p "$state_dir"

fail() {
  code=$?
  printf 'state=failure\nexit_code=%s\n' "$code" >"$state_dir/result"
  exit "$code"
}
trap fail EXIT HUP INT TERM

read_credential() {
  name=$1
  path="$credentials_dir/$name"
  [ -f "$path" ] || {
    printf 'missing systemd credential %s\n' "$name" >&2
    return 1
  }
  cat "$path"
}

node=$(read_credential breakfix.node)
topology=$(read_credential breakfix.topology)
case "$node" in
  ''|*[!a-z0-9-]*)
    printf 'invalid logical node name %s\n' "$node" >&2
    exit 1
    ;;
esac

generate="$bundle/nodes/$node/generate.sh"
[ -f "$generate" ] || {
  printf 'missing node generator %s\n' "$generate" >&2
  exit 1
}

hosts_tmp=$(mktemp)
awk '
  $0 == "# BEGIN BREAKFIX MANAGED HOSTS" { managed = 1; next }
  $0 == "# END BREAKFIX MANAGED HOSTS" { managed = 0; next }
  !managed { print }
' /etc/hosts >"$hosts_tmp"
{
  cat "$hosts_tmp"
  printf '%s\n' '# BEGIN BREAKFIX MANAGED HOSTS'
  printf '%s\n' "$topology"
  printf '%s\n' '# END BREAKFIX MANAGED HOSTS'
} >/etc/hosts
rm -f "$hosts_tmp"

/bin/bash "$generate"
printf 'state=success\nexit_code=0\n' >"$state_dir/result"
touch "$state_dir/succeeded"
trap - EXIT HUP INT TERM
