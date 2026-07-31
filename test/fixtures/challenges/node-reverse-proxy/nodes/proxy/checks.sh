#!/bin/bash
set -u

if systemctl is-active --quiet breakfix-proxy.service && grep -q 'TCP:app:8080' /etc/systemd/system/breakfix-proxy.service; then
  passed=true
  details="breakfix-proxy.service is active with app:8080 as its upstream"
else
  passed=false
  details="breakfix-proxy.service is inactive or still uses the wrong upstream"
fi
printf '{"checks":[{"id":"proxy-service-ready","passed":%s,"summary":"Proxy service","details":"%s"}]}' "$passed" "$details"
