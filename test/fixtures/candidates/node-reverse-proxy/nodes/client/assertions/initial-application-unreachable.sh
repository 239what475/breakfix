#!/bin/bash
set -u

response="$(curl --fail --silent --max-time 5 http://proxy:8080 2>/dev/null || true)"
if [[ "$response" != "breakfix-multi-node" ]]; then
  observed=true
  details="client cannot receive the expected response through proxy:8080"
else
  observed=false
  details="client can already reach the application through the proxy"
fi
printf '{"assertions":[{"id":"application-unreachable","satisfied":%s,"summary":"Application is unreachable","details":"%s"}]}' "$observed" "$details"
