#!/bin/bash
set -u

marker=/var/lib/breakfix/runtime-init-fixture/ready
if [[ "$(cat "$marker" 2>/dev/null || true)" != "ready" ]]; then
  observed=true
  details="runtime marker is missing or invalid"
else
  observed=false
  details="runtime marker is already ready"
fi
printf '{"evidence":[{"id":"runtime-marker-missing","observed":%s,"summary":"Runtime marker missing","details":"%s"}]}' "$observed" "$details"
