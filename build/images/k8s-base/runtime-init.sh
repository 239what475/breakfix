#!/bin/bash
set -euo pipefail

sentinel="${BREAKFIX_INIT_SENTINEL:-/var/lib/breakfix/.initialized}"

# Blank management terminals carry no runnable bundle, so the deployment
# requests initialization deferral through the environment instead.
if [ "${BREAKFIX_DEFER_INITIALIZATION:-0}" = "1" ] || [ -f /opt/breakfix/runnable/.breakfix-defer-initialization ]; then
  touch "$sentinel"
  exec sleep infinity
fi

printf 'missing public runnable initialization marker\n' >&2
exit 1
