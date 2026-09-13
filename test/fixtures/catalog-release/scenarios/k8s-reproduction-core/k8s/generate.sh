#!/bin/bash
set -euo pipefail

kubectl delete configmap breakfix-runtime-fixture --ignore-not-found
