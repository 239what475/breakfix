#!/bin/bash
set -euo pipefail

deadline=$((SECONDS + 30))
until curl --fail --silent --show-error --max-time 5 http://proxy:8080 >/var/lib/breakfix/client-observation; do
  if (( SECONDS >= deadline )); then
    exit 1
  fi
  sleep 1
done
