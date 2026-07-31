#!/bin/bash
set -u

response="$(curl --fail --silent --max-time 5 http://proxy:8080 2>/dev/null || true)"
if [[ "$response" == "breakfix-multi-node" ]]; then
  passed=true
  details="client reached the application through proxy:8080"
else
  passed=false
  details="client did not receive the expected response through proxy:8080"
fi
printf '{"checks":[{"id":"application-reachable","passed":%s,"summary":"Application reachability","details":"%s"}]}' "$passed" "$details"
