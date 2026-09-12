#!/bin/bash
set -u

root=/var/lib/breakfix/dependency-fixture
if [[ "$(cat "$root/config" 2>/dev/null || true)" != "enabled=true" ]]; then
  config_observed=true
  config_details="configuration is missing or disabled"
else
  config_observed=false
  config_details="configuration already enables the service"
fi
if [[ "$(cat "$root/state" 2>/dev/null || true)" != "service=enabled" ]]; then
  state_observed=true
  state_details="derived state is missing or stale"
else
  state_observed=false
  state_details="derived state is already enabled"
fi
printf '{"evidence":[{"id":"configuration-missing","observed":%s,"summary":"Configuration missing","details":"%s"},{"id":"derived-state-missing","observed":%s,"summary":"Derived state missing","details":"%s"}]}' \
  "$config_observed" "$config_details" "$state_observed" "$state_details"
