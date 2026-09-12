#!/bin/sh
set -eu

if [ ! -x /usr/local/bin/breakfix-runtime-fixture ]; then
  observed=true
  summary="运行时标记尚未创建"
else
  observed=false
  summary="运行时标记已经存在"
fi
printf '{"evidence":[{"id":"runtime-marker-absent","observed":%s,"summary":"%s"}]}' "$observed" "$summary"
