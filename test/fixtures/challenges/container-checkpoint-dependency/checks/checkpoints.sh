#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/config" 2>/dev/null || true)" == "enabled=true" ]]; then
  config_passed=true
  config_details="configuration enables the service"
else
  config_passed=false
  config_details="configuration is missing or disabled"
fi
if [[ "$(cat "$root/state" 2>/dev/null || true)" == "service=enabled" ]]; then
  state_passed=true
  state_details="derived state is enabled"
else
  state_passed=false
  state_details="derived state is missing or stale"
fi
printf '{"checks":[{"id":"configuration-ready","passed":%s,"summary":"Configuration","details":"%s"},{"id":"derived-state-ready","passed":%s,"summary":"Derived state","details":"%s"}]}' "$config_passed" "$config_details" "$state_passed" "$state_details"
