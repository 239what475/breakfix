#!/bin/bash
set -euo pipefail

sentinel="${BREAKFIX_INIT_SENTINEL:-/var/lib/breakfix/.initialized}"

if [ -f /opt/breakfix/runnable/.breakfix-defer-initialization ]; then
  touch "$sentinel"
  exec sleep infinity
fi

printf 'missing public runnable initialization marker\n' >&2
exit 1
