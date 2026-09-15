#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/config" 2>/dev/null || true)" != "enabled=true" ]]; then
  satisfied=true
  details="configuration is missing or disabled"
else
  satisfied=false
  details="configuration already enables the service"
fi
printf '{"assertions":[{"id":"configuration-missing","satisfied":%s,"summary":"Configuration missing","details":"%s"}]}' "$satisfied" "$details"
