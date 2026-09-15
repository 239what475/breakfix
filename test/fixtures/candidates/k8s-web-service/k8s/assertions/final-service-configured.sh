#!/bin/bash
set -u

service_type="$(kubectl get service web -o jsonpath='{.spec.type}' 2>/dev/null || true)"
selector="$(kubectl get service web -o jsonpath='{.spec.selector.app}' 2>/dev/null || true)"
port="$(kubectl get service web -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || true)"
target_port="$(kubectl get service web -o jsonpath='{.spec.ports[0].targetPort}' 2>/dev/null || true)"
if [[ "$service_type" == "ClusterIP" && "$selector" == "web" && "$port" == "80" && "$target_port" == "80" ]]; then
  satisfied=true
  details="web Service selects app=web on port 80"
else
  satisfied=false
  details="expected ClusterIP app=web port=80 targetPort=80"
fi
printf '{"assertions":[{"id":"service-configured","satisfied":%s,"summary":"Service configuration","details":"%s"}]}' "$satisfied" "$details"
