#!/bin/bash
set -euo pipefail

sentinel="${BREAKFIX_INIT_SENTINEL:-/var/lib/breakfix/.initialized}"
generate_script="${BREAKFIX_GENERATE_SCRIPT:-/breakfix/generate.sh}"

if [ -f /opt/breakfix/scenario/.breakfix-defer-initialization ]; then
  touch "$sentinel"
  exec sleep infinity
fi

mkdir -p /var/lib/breakfix /breakfix

if [ -n "${KUBECONFIG:-}" ]; then
  for _ in $(seq 1 120); do
    if kubectl get ns >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

if [ ! -f "$sentinel" ]; then
  if [ ! -f "$generate_script" ]; then
    printf 'missing Kubernetes generator %s\n' "$generate_script" >&2
    exit 1
  fi
  /bin/bash "$generate_script"
  touch "$sentinel"
fi

exec "$@"
