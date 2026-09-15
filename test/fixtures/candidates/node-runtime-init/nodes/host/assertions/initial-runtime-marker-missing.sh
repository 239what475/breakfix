#!/bin/bash
set -u

marker=/var/lib/breakfix/runtime-init-fixture/ready
if [[ "$(cat "$marker" 2>/dev/null || true)" != "ready" ]]; then
  satisfied=true
  details="runtime marker is missing or invalid"
else
  satisfied=false
  details="runtime marker is already ready"
fi
printf '{"assertions":[{"id":"runtime-marker-missing","satisfied":%s,"summary":"Runtime marker missing","details":"%s"}]}' "$satisfied" "$details"
