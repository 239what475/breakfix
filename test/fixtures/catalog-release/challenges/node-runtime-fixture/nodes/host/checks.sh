#!/bin/sh
set -eu

if [ -x /usr/local/bin/breakfix-runtime-fixture ]; then
  passed=true
  summary="运行时标记已就绪"
else
  passed=false
  summary="运行时标记尚未创建"
fi
printf '{"checks":[{"id":"runtime-fixture-ready","passed":%s,"summary":"%s"}]}' "$passed" "$summary"
