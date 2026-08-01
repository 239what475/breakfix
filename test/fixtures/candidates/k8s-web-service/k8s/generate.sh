#!/bin/bash
set -euo pipefail

kubectl delete deployment web --ignore-not-found
kubectl delete service web --ignore-not-found
