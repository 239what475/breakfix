#!/bin/sh
set -eu

if [ -x /usr/local/bin/cleanup.sh ]; then
  passed=true
  summary="清理脚本已就绪"
else
  passed=false
  summary="清理脚本尚未就绪"
fi
printf '{"checks":[{"id":"cleanup-script-ready","passed":%s,"summary":"%s"}]}' "$passed" "$summary"
