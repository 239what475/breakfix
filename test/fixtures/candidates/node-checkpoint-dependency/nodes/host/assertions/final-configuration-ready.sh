#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/config" 2>/dev/null || true)" == "enabled=true" ]]; then
  satisfied=true
  details="configuration enables the service"
else
  satisfied=false
  details="configuration is missing or disabled"
fi
printf '{"assertions":[{"id":"configuration-ready","satisfied":%s,"summary":"Configuration","details":"%s"}]}' "$satisfied" "$details"
