#!/bin/bash
set -u

if kubectl get deployment web >/dev/null 2>&1; then
  deployment_observed=false
  deployment_details="web Deployment already exists"
else
  deployment_observed=true
  deployment_details="web Deployment is absent"
fi
if kubectl get service web >/dev/null 2>&1; then
  service_observed=false
  service_details="web Service already exists"
else
  service_observed=true
  service_details="web Service is absent"
fi
printf '{"evidence":[{"id":"deployment-absent","observed":%s,"summary":"Deployment absent","details":"%s"},{"id":"service-absent","observed":%s,"summary":"Service absent","details":"%s"}]}' \
  "$deployment_observed" "$deployment_details" "$service_observed" "$service_details"
