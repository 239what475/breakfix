#!/usr/bin/env bash
set -u

checks=()

add_check() {
  local id="$1"
  local passed="$2"
  local summary="$3"
  local details="$4"
  checks+=("{\"id\":\"${id}\",\"passed\":${passed},\"summary\":\"${summary}\",\"details\":\"${details}\"}")
}

script=/usr/local/bin/cleanup.sh
if [[ -x "$script" ]]; then
  add_check cleanup-script-ready true "清理脚本已就绪" "${script} 存在且可执行"
else
  add_check cleanup-script-ready false "清理脚本尚未就绪" "需要创建并赋予 ${script} 执行权限"
fi

eligible_ok=true
eligible_details="所有目标日志均已归档"
while IFS= read -r file; do
  [[ -z "$file" ]] && continue
  base="${file%.log}"
  archives=(/backup/"${base}"*.tar.gz)
  if [[ ! -e "${archives[0]}" ]] || ! tar -tzf "${archives[0]}" | grep -Fxq "$file"; then
    eligible_ok=false
    eligible_details="缺少包含 ${file} 的有效归档"
    break
  fi
done </var/lib/breakfix/cleanup-logs/eligible.txt
if [[ "$eligible_ok" == true ]]; then
  add_check eligible-logs-archived true "目标日志已归档" "$eligible_details"
else
  add_check eligible-logs-archived false "目标日志尚未全部归档" "$eligible_details"
fi

protected_ok=true
protected_details="新日志和小日志均未归档"
while IFS= read -r file; do
  [[ -z "$file" ]] && continue
  base="${file%.log}"
  archives=(/backup/"${base}"*.tar.gz)
  if [[ ! -f "/var/log/${file}" ]]; then
    protected_ok=false
    protected_details="不应处理的日志已丢失: ${file}"
    break
  fi
  if [[ -e "${archives[0]}" ]]; then
    protected_ok=false
    protected_details="不应归档的日志已有归档: ${file}"
    break
  fi
done </var/lib/breakfix/cleanup-logs/protected.txt
if [[ "$protected_ok" == true ]]; then
  add_check protected-logs-preserved true "保护范围正确" "$protected_details"
else
  add_check protected-logs-preserved false "发现错误归档" "$protected_details"
fi

printf '{"checks":['
IFS=,
printf '%s' "${checks[*]}"
printf ']}'
