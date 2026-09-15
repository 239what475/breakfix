#!/bin/sh
set -eu

if [ -x /usr/local/bin/breakfix-runtime-fixture ]; then
  satisfied=true
  summary="运行时标记已就绪"
else
  satisfied=false
  summary="运行时标记尚未创建"
fi
printf '{"assertions":[{"id":"runtime-fixture-ready","satisfied":%s,"summary":"%s"}]}' "$satisfied" "$summary"
