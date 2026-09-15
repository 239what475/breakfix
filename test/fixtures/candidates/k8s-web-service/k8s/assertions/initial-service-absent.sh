#!/bin/bash
set -u

if kubectl get service web >/dev/null 2>&1; then
  satisfied=false
  details="web Service already exists"
else
  satisfied=true
  details="web Service is absent"
fi
printf '{"assertions":[{"id":"service-absent","satisfied":%s,"summary":"Service absent","details":"%s"}]}' "$satisfied" "$details"
