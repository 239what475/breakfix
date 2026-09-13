#!/bin/bash
set -euo pipefail

if kubectl get configmap breakfix-runtime-fixture >/dev/null 2>&1; then
  observed=false
  summary="验收配置已经存在"
else
  observed=true
  summary="验收配置尚未创建"
fi
printf '{"evidence":[{"id":"runtime-config-absent","observed":%s,"summary":"%s"}]}' "$observed" "$summary"
