#!/bin/bash
set -euo pipefail

kubectl get deployment web -n default >/dev/null 2>&1 || {
  echo "FAIL: deployment/web not found"
  exit 1
}

kubectl get service web -n default >/dev/null 2>&1 || {
  echo "FAIL: service/web not found"
  exit 1
}

kubectl rollout status deployment/web -n default --timeout=120s >/tmp/verify-rollout.log 2>&1 || {
  cat /tmp/verify-rollout.log
  echo "FAIL: deployment/web did not become available"
  exit 1
}

READY="$(kubectl get deployment web -n default -o jsonpath='{.status.readyReplicas}')"
DESIRED="$(kubectl get deployment web -n default -o jsonpath='{.spec.replicas}')"
IMAGE="$(kubectl get deployment web -n default -o jsonpath='{.spec.template.spec.containers[0].image}')"

if [ -z "${READY}" ] || [ -z "${DESIRED}" ]; then
  echo "FAIL: deployment status is incomplete"
  exit 1
fi

if [ "${READY}" != "${DESIRED}" ]; then
  echo "FAIL: ready replicas ${READY} do not match desired replicas ${DESIRED}"
  exit 1
fi

echo "Deployment image: ${IMAGE}"
echo "Ready replicas: ${READY}/${DESIRED}"
echo "PASS"
