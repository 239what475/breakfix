#!/bin/bash
set -u

if grep -q 'TCP:app:9090' /etc/systemd/system/breakfix-proxy.service 2>/dev/null; then
  observed=true
  details="proxy service still targets app:9090"
else
  observed=false
  details="proxy service no longer targets the known bad upstream"
fi
printf '{"assertions":[{"id":"proxy-upstream-misconfigured","satisfied":%s,"summary":"Proxy upstream is wrong","details":"%s"}]}' "$observed" "$details"
