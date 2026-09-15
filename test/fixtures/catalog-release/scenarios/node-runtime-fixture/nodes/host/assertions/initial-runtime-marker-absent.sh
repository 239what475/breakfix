#!/bin/sh
set -eu

if [ ! -x /usr/local/bin/breakfix-runtime-fixture ]; then
  satisfied=true
  summary="运行时标记尚未创建"
else
  satisfied=false
  summary="运行时标记已经存在"
fi
printf '{"assertions":[{"id":"runtime-marker-absent","satisfied":%s,"summary":"%s"}]}' "$satisfied" "$summary"
