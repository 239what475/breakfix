#!/bin/bash
set -u

marker=/var/lib/breakfix/runtime-init-fixture/ready
if [[ "$(cat "$marker" 2>/dev/null || true)" == "ready" ]]; then
  passed=true
  details="runtime marker is ready"
else
  passed=false
  details="runtime marker is missing or invalid"
fi
printf '{"checks":[{"id":"runtime-marker-ready","passed":%s,"summary":"Runtime marker","details":"%s"}]}' "$passed" "$details"
