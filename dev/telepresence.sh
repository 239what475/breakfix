#!/usr/bin/env bash
set -euo pipefail

# Run one Breakfix runtime component locally while it keeps its in-cluster
# identity, Service DNS, and (for Server) persistent data volume.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${BREAKFIX_TELEPRESENCE_STATE_DIR:-$ROOT_DIR/.local/telepresence}"
CONFIG_SOURCE="$ROOT_DIR/config/breakfix.in-cluster.yaml"

TP_NAMESPACE="${BREAKFIX_TELEPRESENCE_NAMESPACE:-breakfix-system}"
TP_MANAGER_NAMESPACE="${BREAKFIX_TELEPRESENCE_MANAGER_NAMESPACE:-ambassador}"
TP_RUNTIME_SECRET="${BREAKFIX_TELEPRESENCE_RUNTIME_SECRET:-breakfix-runtime}"
TP_SERVER_PORT="${BREAKFIX_TELEPRESENCE_SERVER_PORT:-19091}"
TP_CONTROLLER_HEALTH_PORT="${BREAKFIX_TELEPRESENCE_CONTROLLER_HEALTH_PORT:-18081}"

SERVER_WORKLOAD="breakfix-server"
CONTROLLER_WORKLOAD="breakfix-controller"
WORKER_WORKLOAD="breakfix-agent-worker"

usage() {
  cat <<'EOF'
Usage: dev/telepresence.sh <command>

Commands:
  connect       Install/connect the Traffic Manager for the current cluster.
  server        Replace the in-cluster Server with a local foreground process.
  controller    Replace the in-cluster Controller with a local foreground process.
  worker        Replace the Agent Worker locally after explicitly authorizing its scale-down.
  down [name]   Restore one component (server|controller|worker) or all components.
  status        Show Telepresence and Breakfix runtime status.
  disconnect    Restore all components and stop local Telepresence daemons.

The server, controller, and worker commands remain in the foreground so their
logs are directly visible while Playwright or another E2E command runs.

Worker replacement requires BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1. It
records the current replica count, scales the remote Deployment to one, and
restores the recorded count when the local replacement exits or `down` runs.
EOF
}

fail() {
  printf 'telepresence: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

ensure_prerequisites() {
  require_command kubectl
  require_command telepresence
  require_command go
  require_command make
  require_command base64
  test -f "$CONFIG_SOURCE" || fail "missing in-cluster config: $CONFIG_SOURCE"
}

ensure_connected() {
  local status current_context
  status="$(telepresence status 2>/dev/null || true)"
  current_context="$(kubectl config current-context)"
  if printf '%s\n' "$status" | grep -q 'Traffic Manager: Connected'; then
    printf '%s\n' "$status" | grep -Fq "Kubernetes context: $current_context" || \
      fail "Telepresence is connected to another Kubernetes context; run make telepresence-disconnect first"
    printf '%s\n' "$status" | grep -Fq "$TP_NAMESPACE" || \
      fail "Telepresence is not mapped to namespace $TP_NAMESPACE; run make telepresence-disconnect first"
    return
  fi

  telepresence helm install --namespace "$TP_MANAGER_NAMESPACE"
  telepresence connect --manager-namespace "$TP_MANAGER_NAMESPACE" --namespace "$TP_NAMESPACE"
}

workload_for() {
  case "$1" in
    server) printf '%s\n' "$SERVER_WORKLOAD" ;;
    controller) printf '%s\n' "$CONTROLLER_WORKLOAD" ;;
    worker) printf '%s\n' "$WORKER_WORKLOAD" ;;
    *) fail "unknown component: $1" ;;
  esac
}

binary_for() {
  case "$1" in
    server) printf '%s/bin/breakfix-server\n' "$ROOT_DIR" ;;
    controller) printf '%s/bin/breakfix-controller\n' "$ROOT_DIR" ;;
    worker) printf '%s/bin/breakfix-agent-worker\n' "$ROOT_DIR" ;;
    *) fail "unknown component: $1" ;;
  esac
}

build_component() {
  local component="$1"
  case "$component" in
    server) (cd "$ROOT_DIR" && make --no-print-directory dev-build-server) ;;
    controller) (cd "$ROOT_DIR" && go build -o "$ROOT_DIR/bin/breakfix-controller" ./cmd/controller) ;;
    worker) (cd "$ROOT_DIR" && go build -o "$ROOT_DIR/bin/breakfix-agent-worker" ./cmd/agent-worker) ;;
    *) fail "unknown component: $component" ;;
  esac
}

secret_value() {
  local key="$1"
  kubectl -n "$TP_NAMESPACE" get secret "$TP_RUNTIME_SECRET" \
    -o "go-template={{index .data \"$key\"}}" | base64 --decode
}

deployment_env_value() {
  local workload="$1"
  local container="$2"
  local name="$3"
  kubectl -n "$TP_NAMESPACE" get deployment "$workload" \
    -o "go-template={{range .spec.template.spec.containers}}{{if eq .name \"$container\"}}{{range .env}}{{if eq .name \"$name\"}}{{.value}}{{end}}{{end}}{{end}}{{end}}"
}

create_service_account_kubeconfig() {
  local component="$1"
  local service_account="breakfix-$component"
  local output="$STATE_DIR/$component.kubeconfig"
  local source_context source_cluster source_user token local_identity

  source_context="$(kubectl config current-context)"
  source_cluster="$(kubectl config view --minify -o jsonpath='{.contexts[0].context.cluster}')"
  source_user="$(kubectl config view --minify -o jsonpath='{.contexts[0].context.user}')"
  test -n "$source_context" && test -n "$source_cluster" && test -n "$source_user" || fail "current kubeconfig is incomplete"

  token="$(kubectl -n "$TP_NAMESPACE" create token "$service_account" --duration=1h)"
  local_identity="telepresence-$service_account"
  kubectl config view --raw --minify --flatten > "$output"
  KUBECONFIG="$output" kubectl config set-credentials "$local_identity" --token="$token" >/dev/null
  KUBECONFIG="$output" kubectl config set-context "$local_identity" \
    --cluster="$source_cluster" --user="$local_identity" --namespace="$TP_NAMESPACE" >/dev/null
  KUBECONFIG="$output" kubectl config use-context "$local_identity" >/dev/null
  KUBECONFIG="$output" kubectl config delete-context "$source_context" >/dev/null 2>&1 || true
  KUBECONFIG="$output" kubectl config delete-user "$source_user" >/dev/null 2>&1 || true
  chmod 600 "$output"
  printf '%s\n' "$output"
}

prepare_config() {
  local component="$1"
  local kubeconfig="$2"
  local output="$STATE_DIR/$component.yaml"
  local data_dir="$STATE_DIR/mount-server/var/lib/breakfix"
  local vcluster_binary

  case "$component" in
    server)
      sed \
        -e "s|^port:.*|port: $TP_SERVER_PORT|" \
        -e "s|^data_dir:.*|data_dir: $data_dir|" \
        -e "s|^kubeconfig:.*|kubeconfig: $kubeconfig|" \
        -e 's|^registry_insecure:.*|registry_insecure: false|' \
        "$CONFIG_SOURCE" > "$output"
      ;;
    controller)
      vcluster_binary="$(command -v vcluster || true)"
      test -n "$vcluster_binary" || fail "controller replacement requires vcluster on PATH"
      sed \
        -e "s|^health_port:.*|health_port: $TP_CONTROLLER_HEALTH_PORT|" \
        -e "s|^data_dir:.*|data_dir: $STATE_DIR/controller-data|" \
        -e "s|^kubeconfig:.*|kubeconfig: $kubeconfig|" \
        -e "s|^vcluster_binary:.*|vcluster_binary: $vcluster_binary|" \
        -e 's|^registry_insecure:.*|registry_insecure: false|' \
        "$CONFIG_SOURCE" > "$output"
      ;;
    worker)
      sed \
        -e "s|^data_dir:.*|data_dir: $STATE_DIR/worker-data|" \
        -e 's|^registry_insecure:.*|registry_insecure: false|' \
        "$CONFIG_SOURCE" > "$output"
      ;;
    *) fail "unknown component: $component" ;;
  esac

  printf '%s\n' "$output"
}

ensure_server_fuse() {
  test -r /etc/fuse.conf || fail "Server replacement requires /etc/fuse.conf with user_allow_other enabled"
  grep -Eq '^[[:space:]]*user_allow_other[[:space:]]*$' /etc/fuse.conf || \
    fail "enable user_allow_other in /etc/fuse.conf before replacing Server"
}

worker_replicas_file() {
  printf '%s/worker.replicas\n' "$STATE_DIR"
}

prepare_worker_replacement() {
  local replicas file
  file="$(worker_replicas_file)"
  if test -f "$file"; then
    return
  fi
  test "${BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE:-}" = '1' || \
    fail 'set BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 to replace the Agent Worker'

  replicas="$(kubectl -n "$TP_NAMESPACE" get deployment "$WORKER_WORKLOAD" -o jsonpath='{.spec.replicas}')"
  case "$replicas" in
    ''|*[!0-9]*) fail "invalid Agent Worker replica count: $replicas" ;;
  esac

  printf '%s\n' "$replicas" > "$file"
  kubectl -n "$TP_NAMESPACE" scale deployment "$WORKER_WORKLOAD" --replicas=1
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$WORKER_WORKLOAD" --timeout=90s
}

restore_worker_replicas() {
  local file replicas
  file="$(worker_replicas_file)"
  test -f "$file" || return
  replicas="$(<"$file")"
  case "$replicas" in
    ''|*[!0-9]*) fail "invalid recorded Agent Worker replica count: $replicas" ;;
  esac

  kubectl -n "$TP_NAMESPACE" scale deployment "$WORKER_WORKLOAD" --replicas="$replicas"
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$WORKER_WORKLOAD" --timeout=90s
  find "$file" -maxdepth 0 -delete
}

cleanup_local_files() {
  local component="$1"
  find "$STATE_DIR" -maxdepth 1 -type f -name "$component.*" -delete 2>/dev/null || true
  case "$component" in
    server)
      rmdir "$STATE_DIR/mount-server" 2>/dev/null || true
      ;;
    controller)
      find "$STATE_DIR/mount-controller" -depth -delete 2>/dev/null || true
      find "$STATE_DIR/controller-data" -depth -delete 2>/dev/null || true
      find "$STATE_DIR/controller-home" -depth -delete 2>/dev/null || true
      ;;
    worker)
      find "$STATE_DIR/worker-data" -depth -delete 2>/dev/null || true
      ;;
  esac
}

detach_component() {
  local component="$1"
  local workload
  workload="$(workload_for "$component")"

  telepresence detach "$workload" --namespace "$TP_NAMESPACE" >/dev/null 2>&1 || true
  telepresence uninstall "$workload" >/dev/null 2>&1 || true
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$workload" --timeout=90s >/dev/null || true
  if test "$component" = 'worker'; then
    restore_worker_replicas || true
  fi
  cleanup_local_files "$component"
}

run_server() {
  local kubeconfig config base_url sandbox_namespace
  ensure_server_fuse
  kubeconfig="$(create_service_account_kubeconfig server)"
  config="$(prepare_config server "$kubeconfig")"
  base_url="$(deployment_env_value "$SERVER_WORKLOAD" server BREAKFIX_OPENSANDBOX_BASE_URL)"
  sandbox_namespace="$(deployment_env_value "$SERVER_WORKLOAD" server BREAKFIX_OPENSANDBOX_NAMESPACE)"
  test -n "$base_url" && test -n "$sandbox_namespace" || fail "Server Deployment is missing OpenSandbox environment values"

  printf 'Replacing Server locally. Logs remain in this terminal; HTTP is available on http://127.0.0.1:%s.\n' "$TP_SERVER_PORT"
  env \
    BREAKFIX_DATABASE_URL="$(secret_value database_url)" \
    BREAKFIX_AGENT_DATABASE_URL="$(secret_value agent_database_url)" \
    BREAKFIX_JWT_SECRET="$(secret_value jwt_secret)" \
    BREAKFIX_INTERNAL_API_KEY="$(secret_value internal_api_key)" \
    BREAKFIX_REGISTRY_ADDR="$(secret_value registry_addr)" \
    BREAKFIX_REGISTRY_INSECURE="$(secret_value registry_insecure)" \
    DEEPSEEK_API_KEY="$(secret_value deepseek_api_key)" \
    OPEN_SANDBOX_API_KEY="$(secret_value opensandbox_api_key)" \
    BREAKFIX_OPENSANDBOX_BASE_URL="$base_url" \
    BREAKFIX_OPENSANDBOX_NAMESPACE="$sandbox_namespace" \
    telepresence replace --namespace "$TP_NAMESPACE" --container server \
      --mount="$STATE_DIR/mount-server" --port "$TP_SERVER_PORT:9090" "$SERVER_WORKLOAD" -- \
      "$(binary_for server)" -config "$config"
}

run_controller() {
  local kubeconfig config
  kubeconfig="$(create_service_account_kubeconfig controller)"
  config="$(prepare_config controller "$kubeconfig")"

  printf 'Replacing Controller locally. Logs remain in this terminal; health is available on http://127.0.0.1:%s/readyz.\n' "$TP_CONTROLLER_HEALTH_PORT"
  env \
    BREAKFIX_INTERNAL_API_KEY="$(secret_value internal_api_key)" \
    BREAKFIX_REGISTRY_ADDR="$(secret_value registry_addr)" \
    BREAKFIX_REGISTRY_INSECURE="$(secret_value registry_insecure)" \
    telepresence replace --namespace "$TP_NAMESPACE" --container controller \
      --mount="$STATE_DIR/mount-controller" "$CONTROLLER_WORKLOAD" -- \
      env HOME="$STATE_DIR/controller-home" "$(binary_for controller)" -config "$config"
}

run_worker() {
  local config
  prepare_worker_replacement
  config="$(prepare_config worker '')"

  printf 'Replacing Agent Worker locally. Logs remain in this terminal.\n'
  env \
    BREAKFIX_AGENT_DATABASE_URL="$(secret_value agent_database_url)" \
    BREAKFIX_INTERNAL_API_KEY="$(secret_value internal_api_key)" \
    DEEPSEEK_API_KEY="$(secret_value deepseek_api_key)" \
    telepresence replace --namespace "$TP_NAMESPACE" --container agent-worker --mount=false "$WORKER_WORKLOAD" -- \
      env POD_NAME=telepresence-local-worker "$(binary_for worker)" -config "$config"
}

run_component() {
  local component="$1" status
  ensure_prerequisites
  ensure_connected
  mkdir -p "$STATE_DIR"
  build_component "$component"
  trap "detach_component '$component'" EXIT

  set +e
  "run_$component"
  status=$?
  set -e
  exit "$status"
}

show_status() {
  telepresence status || true
  kubectl -n "$TP_NAMESPACE" get deployment "$SERVER_WORKLOAD" "$CONTROLLER_WORKLOAD" "$WORKER_WORKLOAD" \
    -o custom-columns=NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas,AVAILABLE:.status.availableReplicas
  if telepresence status 2>/dev/null | grep -q 'Traffic Manager: Connected'; then
    telepresence list --namespace "$TP_NAMESPACE"
  fi
}

main() {
  local command="${1:-}"
  case "$command" in
    connect)
      ensure_prerequisites
      ensure_connected
      ;;
    server|controller|worker)
      run_component "$command"
      ;;
    down)
      case "${2:-all}" in
        all)
          detach_component server
          detach_component controller
          detach_component worker
          ;;
        server|controller|worker) detach_component "$2" ;;
        *) fail "unknown component: ${2:-}" ;;
      esac
      ;;
    status)
      show_status
      ;;
    disconnect)
      detach_component server
      detach_component controller
      detach_component worker
      telepresence quit --stop-daemons
      ;;
    -h|--help|help|'') usage ;;
    *) fail "unknown command: $command" ;;
  esac
}

main "$@"
