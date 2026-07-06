#!/bin/bash
set -euo pipefail

kubectl set image deployment/web web=nginx:1.25.5 -n default
kubectl rollout status deployment/web -n default --timeout=120s
