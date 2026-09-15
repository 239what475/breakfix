#!/bin/bash
set -u

marker=/var/lib/breakfix/runtime-init-fixture/ready
if [[ "$(cat "$marker" 2>/dev/null || true)" == "ready" ]]; then
  satisfied=true
  details="runtime marker is ready"
else
  satisfied=false
  details="runtime marker is missing or invalid"
fi
printf '{"assertions":[{"id":"runtime-marker-ready","satisfied":%s,"summary":"Runtime marker","details":"%s"}]}' "$satisfied" "$details"
