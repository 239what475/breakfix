#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/state" 2>/dev/null || true)" == "service=enabled" ]]; then
  satisfied=true
  details="derived state is enabled"
else
  satisfied=false
  details="derived state is missing or stale"
fi
printf '{"assertions":[{"id":"derived-state-ready","satisfied":%s,"summary":"Derived state","details":"%s"}]}' "$satisfied" "$details"
