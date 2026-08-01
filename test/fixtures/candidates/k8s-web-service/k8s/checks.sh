#!/bin/bash
set -u

replicas="$(kubectl get deployment web -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
ready_replicas="$(kubectl get deployment web -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
service_type="$(kubectl get service web -o jsonpath='{.spec.type}' 2>/dev/null || true)"
selector="$(kubectl get service web -o jsonpath='{.spec.selector.app}' 2>/dev/null || true)"
port="$(kubectl get service web -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || true)"
target_port="$(kubectl get service web -o jsonpath='{.spec.ports[0].targetPort}' 2>/dev/null || true)"

if [[ "$replicas" == "2" && "$ready_replicas" == "2" ]]; then
  deployment_passed=true
  deployment_details="web has 2 desired and ready replicas"
else
  deployment_passed=false
  deployment_details="expected 2 desired and ready replicas, got desired=${replicas:-none} ready=${ready_replicas:-none}"
fi

if [[ "$service_type" == "ClusterIP" && "$selector" == "web" && "$port" == "80" && "$target_port" == "80" ]]; then
  service_passed=true
  service_details="web Service selects app=web on port 80"
else
  service_passed=false
  service_details="expected ClusterIP app=web port=80 targetPort=80"
fi

printf '{"checks":[{"id":"deployment-ready","passed":%s,"summary":"Deployment readiness","details":"%s"},{"id":"service-configured","passed":%s,"summary":"Service configuration","details":"%s"}]}' \
  "$deployment_passed" "$deployment_details" "$service_passed" "$service_details"
