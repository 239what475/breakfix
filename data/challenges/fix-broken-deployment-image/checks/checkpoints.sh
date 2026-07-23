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

if kubectl get deployment web -n default >/dev/null 2>&1 && kubectl get service web -n default >/dev/null 2>&1; then
  add_check web-resources-present true "Deployment 和 Service 均存在" "default/web 资源可读取"
else
  add_check web-resources-present false "目标资源不完整" "需要保留 default 命名空间中的 web Deployment 和 Service"
fi

desired="$(kubectl get deployment web -n default -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
available="$(kubectl get deployment web -n default -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)"
if [[ -n "$desired" && "$desired" == "$available" ]]; then
  add_check web-deployment-available true "Deployment 已可用" "可用副本: ${available}/${desired}"
else
  add_check web-deployment-available false "Deployment 尚未可用" "可用副本: ${available:-0}/${desired:-未知}"
fi

selector="$(kubectl get service web -n default -o jsonpath='{.spec.selector.app}' 2>/dev/null || true)"
port="$(kubectl get service web -n default -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || true)"
if [[ "$selector" == "web" && -n "$port" ]]; then
  add_check web-service-preserved true "Service 配置完整" "selector app=web，端口 ${port}"
else
  add_check web-service-preserved false "Service 配置不完整" "需要保留 selector app=web 和服务端口"
fi

printf '{"checks":['
IFS=,
printf '%s' "${checks[*]}"
printf ']}'
