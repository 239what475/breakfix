#!/bin/bash
set -euo pipefail

if kubectl get configmap breakfix-runtime-fixture >/dev/null 2>&1; then
  satisfied=false
  summary="验收配置已经存在"
else
  satisfied=true
  summary="验收配置尚未创建"
fi
printf '{"assertions":[{"id":"runtime-config-absent","satisfied":%s,"summary":"%s"}]}' "$satisfied" "$summary"
