#!/bin/bash
set -u

if kubectl get deployment web >/dev/null 2>&1; then
  satisfied=false
  details="web Deployment already exists"
else
  satisfied=true
  details="web Deployment is absent"
fi
printf '{"assertions":[{"id":"deployment-absent","satisfied":%s,"summary":"Deployment absent","details":"%s"}]}' "$satisfied" "$details"
