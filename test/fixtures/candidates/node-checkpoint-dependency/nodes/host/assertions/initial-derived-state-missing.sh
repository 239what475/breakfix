#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/state" 2>/dev/null || true)" != "service=enabled" ]]; then
  satisfied=true
  details="derived state is missing or stale"
else
  satisfied=false
  details="derived state is already enabled"
fi
printf '{"assertions":[{"id":"derived-state-missing","satisfied":%s,"summary":"Derived state missing","details":"%s"}]}' "$satisfied" "$details"
