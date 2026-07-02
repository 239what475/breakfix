#!/bin/bash
set -euo pipefail

sentinel="${BREAKFIX_INIT_SENTINEL:-/var/lib/breakfix/.initialized}"
generate_script="${BREAKFIX_GENERATE_SCRIPT:-/breakfix/generate.sh}"

mkdir -p /var/lib/breakfix /breakfix

if [ ! -f "$sentinel" ] && [ -x "$generate_script" ]; then
  "$generate_script"
  touch "$sentinel"
fi

exec "$@"
