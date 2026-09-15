#!/bin/bash
set -u

replicas="$(kubectl get deployment web -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
ready_replicas="$(kubectl get deployment web -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
if [[ "$replicas" == "2" && "$ready_replicas" == "2" ]]; then
  satisfied=true
  details="web has 2 desired and ready replicas"
else
  satisfied=false
  details="expected 2 desired and ready replicas, got desired=${replicas:-none} ready=${ready_replicas:-none}"
fi
printf '{"assertions":[{"id":"deployment-ready","satisfied":%s,"summary":"Deployment readiness","details":"%s"}]}' "$satisfied" "$details"
