#!/usr/bin/env bash
set -euo pipefail

# Run one Breakfix component locally while retaining its in-cluster identity,
# Service DNS, and mounted role credentials.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="${BREAKFIX_TELEPRESENCE_STATE_DIR:-$ROOT_DIR/.local/telepresence}"
CONFIG_SOURCE="$ROOT_DIR/config/app/in-cluster.yaml"

TP_NAMESPACE="${BREAKFIX_TELEPRESENCE_NAMESPACE:-breakfix-system}"
TP_MANAGER_NAMESPACE="${BREAKFIX_TELEPRESENCE_MANAGER_NAMESPACE:-ambassador}"
TP_RUNTIME_SECRET="${BREAKFIX_TELEPRESENCE_RUNTIME_SECRET:-breakfix-runtime}"
TP_DEBUG_SECRET="${BREAKFIX_TELEPRESENCE_DEBUG_SECRET:-breakfix-debug}"
TP_SERVER_PORT="${BREAKFIX_TELEPRESENCE_SERVER_PORT:-19091}"
TP_CONTROLLER_HEALTH_PORT="${BREAKFIX_TELEPRESENCE_CONTROLLER_HEALTH_PORT:-18081}"
TP_WORKSPACE_IMAGE="${BREAKFIX_TELEPRESENCE_WORKSPACE_IMAGE:-}"

usage() {
  cat <<'EOF'
Usage: scripts/dev/telepresence.sh <command>

Commands:
  connect                  Install/connect the Traffic Manager for the current cluster.
  server                   Replace Server with a local foreground process.
  controller               Replace Controller with a local foreground process.
  runtime-worker          Replace the Runtime Worker pool locally.
  down [name]              Restore one component or all components.
  status                   Show Telepresence and Breakfix runtime status.
  disconnect               Restore all components and stop local Telepresence daemons.

Replacement commands remain in the foreground so their logs are visible during
E2E runs. Worker replacement requires
BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1. The script records and restores
the original Deployment replica count. Set
BREAKFIX_TELEPRESENCE_WORKSPACE_IMAGE when the local OpenSandbox provider cannot
pull the configured workspace image.
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
      fail "Telepresence is connected to another Kubernetes context"
    printf '%s\n' "$status" | grep -Fq "$TP_NAMESPACE" || \
      fail "Telepresence is not mapped to namespace $TP_NAMESPACE"
    return
  fi

  telepresence helm install --namespace "$TP_MANAGER_NAMESPACE"
  telepresence connect --manager-namespace "$TP_MANAGER_NAMESPACE" --namespace "$TP_NAMESPACE"
}

all_components() {
  printf '%s\n' server controller runtime-worker
}

is_worker() {
  case "$1" in
    runtime-worker) return 0 ;;
    *) return 1 ;;
  esac
}

workload_for() {
  case "$1" in
    server) printf '%s\n' breakfix-server ;;
    controller) printf '%s\n' breakfix-controller ;;
    runtime-worker) printf '%s\n' breakfix-runtime-worker ;;
    *) fail "unknown component: $1" ;;
  esac
}

container_for() {
  case "$1" in
    server|controller) printf '%s\n' "$1" ;;
    runtime-worker) printf '%s\n' "$1" ;;
    *) fail "unknown component: $1" ;;
  esac
}

binary_for() {
  printf '%s/bin/breakfix-%s\n' "$ROOT_DIR" "$1"
}

health_port_for() {
  case "$1" in
    controller) printf '%s\n' "$TP_CONTROLLER_HEALTH_PORT" ;;
    runtime-worker) printf '%s\n' 18082 ;;
    server) printf '%s\n' 18086 ;;
    *) fail "unknown component: $1" ;;
  esac
}

build_component() {
  case "$1" in
    server|controller|runtime-worker) (cd "$ROOT_DIR" && make --no-print-directory build) ;;
    *) fail "unknown component: $1" ;;
  esac
}

secret_value() {
  local key="$1"
  kubectl -n "$TP_NAMESPACE" get secret "$TP_RUNTIME_SECRET" \
    -o "go-template={{with index .data \"$key\"}}{{.}}{{end}}" | base64 --decode
}

optional_secret_value() {
  local secret="$1"
  local key="$2"
  kubectl -n "$TP_NAMESPACE" get secret "$secret" \
    -o "go-template={{with index .data \"$key\"}}{{.}}{{end}}" 2>/dev/null | base64 --decode 2>/dev/null || true
}

worker_identity_secret() {
  case "$1" in
    runtime-worker) printf '%s\n' breakfix-runtime-worker-identity ;;
    *) fail "unknown worker identity: $1" ;;
  esac
}

worker_identity_key() {
  local secret
  secret="$(worker_identity_secret "$1")"
  kubectl -n "$TP_NAMESPACE" get secret "$secret" \
    -o 'go-template={{index .data "worker_api_key"}}' | base64 --decode
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
  test -n "$source_context" && test -n "$source_cluster" && test -n "$source_user" || \
    fail "current kubeconfig is incomplete"

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
  local kubeconfig="${2:-}"
  local mount_root="${3:-}"
  local output="$STATE_DIR/$component.yaml"
  local health_port data_dir

  health_port="$(health_port_for "$component")"
  data_dir="$STATE_DIR/$component-data"
  if test "$component" = server; then
    data_dir="$mount_root/var/lib/breakfix"
  fi

  sed \
    -e "s|^port:.*|port: $TP_SERVER_PORT|" \
    -e "s|^health_port:.*|health_port: $health_port|" \
    -e "s|^data_dir:.*|data_dir: $data_dir|" \
    -e "s|^kubeconfig:.*|kubeconfig: $kubeconfig|" \
    -e "s|^    server_certificate_file:.*|    server_certificate_file: $mount_root/var/run/secrets/breakfix-incus/server.crt|" \
    -e "s|^    client_certificate_file:.*|    client_certificate_file: $mount_root/var/run/secrets/breakfix-incus/client.crt|" \
    -e "s|^    client_key_file:.*|    client_key_file: $mount_root/var/run/secrets/breakfix-incus/client.key|" \
    "$CONFIG_SOURCE" > "$output"

  if test -n "$TP_WORKSPACE_IMAGE"; then
    sed -i "s|^  workspace_image:.*|  workspace_image: $TP_WORKSPACE_IMAGE|" "$output"
  fi

  if test "$component" = controller; then
    local vcluster_binary
    vcluster_binary="$(command -v vcluster || true)"
    test -n "$vcluster_binary" || fail "controller replacement requires vcluster on PATH"
    sed -i "s|^vcluster_binary:.*|vcluster_binary: $vcluster_binary|" "$output"
  fi

  printf '%s\n' "$output"
}

ensure_server_fuse() {
  test -r /etc/fuse.conf || fail "Server replacement requires /etc/fuse.conf"
  grep -Eq '^[[:space:]]*user_allow_other[[:space:]]*$' /etc/fuse.conf || \
    fail "enable user_allow_other in /etc/fuse.conf before replacing Server"
}

replicas_file() {
  printf '%s/%s.replicas\n' "$STATE_DIR" "$1"
}

prepare_worker_replacement() {
  local component="$1"
  local workload file replicas
  workload="$(workload_for "$component")"
  file="$(replicas_file "$component")"
  test -f "$file" && return
  test "${BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE:-}" = 1 || \
    fail "set BREAKFIX_TELEPRESENCE_ALLOW_WORKER_REPLACE=1 to replace $component"

  replicas="$(kubectl -n "$TP_NAMESPACE" get deployment "$workload" -o jsonpath='{.spec.replicas}')"
  case "$replicas" in
    ''|*[!0-9]*) fail "invalid $component replica count: $replicas" ;;
  esac
  printf '%s\n' "$replicas" > "$file"
  kubectl -n "$TP_NAMESPACE" scale deployment "$workload" --replicas=1
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$workload" --timeout=90s
}

restore_worker_replicas() {
  local component="$1"
  local workload file replicas
  workload="$(workload_for "$component")"
  file="$(replicas_file "$component")"
  test -f "$file" || return
  replicas="$(<"$file")"
  case "$replicas" in
    ''|*[!0-9]*) fail "invalid recorded $component replica count: $replicas" ;;
  esac
  kubectl -n "$TP_NAMESPACE" scale deployment "$workload" --replicas="$replicas"
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$workload" --timeout=90s
  find "$file" -maxdepth 0 -delete
}

cleanup_local_files() {
  local component="$1"
  find "$STATE_DIR" -maxdepth 1 -type f -name "$component.*" -delete 2>/dev/null || true
  find "$STATE_DIR/mount-$component" -depth -delete 2>/dev/null || true
  find "$STATE_DIR/$component-data" -depth -delete 2>/dev/null || true
}

detach_component() {
  local component="$1"
  local workload
  workload="$(workload_for "$component")"
  telepresence detach "$workload" --namespace "$TP_NAMESPACE" >/dev/null 2>&1 || true
  telepresence uninstall "$workload" >/dev/null 2>&1 || true
  kubectl -n "$TP_NAMESPACE" rollout status "deployment/$workload" --timeout=90s >/dev/null || true
  if is_worker "$component"; then
    restore_worker_replicas "$component" || true
  fi
  cleanup_local_files "$component"
}

replace_command() {
  local component="$1"
  local config="$2"
  local mount_root="${3:-}"
  local workload container
  workload="$(workload_for "$component")"
  container="$(container_for "$component")"
  shift 3

  if test -n "$mount_root"; then
    telepresence replace --namespace "$TP_NAMESPACE" --container "$container" \
      --mount="$mount_root" "$workload" -- "$@" "$(binary_for "$component")" -config "$config"
  else
    telepresence replace --namespace "$TP_NAMESPACE" --container "$container" \
      --mount=false "$workload" -- "$@" "$(binary_for "$component")" -config "$config"
  fi
}

registry_trust_bundle_for_mount() {
  local component="$1"
  local mount_root="$2"
  local configured
  configured="$(secret_value registry_trust_bundle_file)"
  if test -n "$configured"; then
    # Telepresence mounts PVCs and some projected volumes, but does not expose
    # every ConfigMap projection to the local process. Keep the same CA bytes
    # in the disposable local debug state instead of weakening TLS verification.
    local output="$STATE_DIR/$component.registry-ca.crt"
    kubectl -n "$TP_NAMESPACE" get configmap breakfix-registry-ca \
      -o 'jsonpath={.data.ca\.crt}' > "$output"
    test -s "$output" || fail "Registry CA ConfigMap is empty or unavailable"
    chmod 600 "$output"
    printf '%s\n' "$output"
    return
  fi
  printf '%s\n' ''
}

run_server() {
  local kubeconfig config mount_root base_url sandbox_namespace registry_trust_bundle_file catalog_release_reference debug_credential
  ensure_server_fuse
  mount_root="$STATE_DIR/mount-server"
  kubeconfig="$(create_service_account_kubeconfig server)"
  config="$(prepare_config server "$kubeconfig" "$mount_root")"
  base_url="$(deployment_env_value breakfix-server server BREAKFIX_OPENSANDBOX_BASE_URL)"
  sandbox_namespace="$(deployment_env_value breakfix-server server BREAKFIX_OPENSANDBOX_NAMESPACE)"
  test -n "$base_url" && test -n "$sandbox_namespace" || \
    fail "Server Deployment is missing OpenSandbox environment values"
  registry_trust_bundle_file="$(registry_trust_bundle_for_mount server "$mount_root")"
  if test -n "$registry_trust_bundle_file"; then
    sed -i "s|^  trust_bundle_file:.*|  trust_bundle_file: $registry_trust_bundle_file|" "$config"
  fi
  catalog_release_reference="$(secret_value catalog_release_reference)"
  debug_credential="$(optional_secret_value "$TP_DEBUG_SECRET" credential)"

  printf 'Replacing Server locally at http://127.0.0.1:%s.\n' "$TP_SERVER_PORT"
  env \
    BREAKFIX_DATABASE_URL="$(secret_value database_url)" \
    BREAKFIX_JWT_SECRET="$(secret_value jwt_secret)" \
    BREAKFIX_DEBUG_CREDENTIAL="$debug_credential" \
    BREAKFIX_RUNTIME_WORKER_API_KEY="$(worker_identity_key runtime-worker)" \
    BREAKFIX_REGISTRY_REPOSITORY="$(secret_value registry_repository)" \
    BREAKFIX_REGISTRY_USERNAME="$(secret_value registry_username)" \
    BREAKFIX_REGISTRY_PASSWORD="$(secret_value registry_password)" \
    BREAKFIX_REGISTRY_TRUST_BUNDLE_FILE="$registry_trust_bundle_file" \
    BREAKFIX_CATALOG_RELEASE_REFERENCE="$catalog_release_reference" \
    BREAKFIX_INCUS_ENDPOINT="$(secret_value incus_endpoint)" \
    BREAKFIX_INCUS_BASE_IMAGE_FINGERPRINT="$(secret_value incus_base_image_fingerprint)" \
    BREAKFIX_INCUS_BUILD_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_build_project)" \
    BREAKFIX_INCUS_IMAGE_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_image_project)" \
    BREAKFIX_INCUS_NAME_PREFIX="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_name_prefix)" \
    BREAKFIX_K8S_BASE_IMAGE_DIGEST="$(secret_value k8s_base_image_digest)" \
    OPEN_SANDBOX_API_KEY="$(secret_value opensandbox_api_key)" \
    BREAKFIX_OPENSANDBOX_BASE_URL="$base_url" \
    BREAKFIX_OPENSANDBOX_NAMESPACE="$sandbox_namespace" \
    telepresence replace --namespace "$TP_NAMESPACE" --container server \
      --mount="$mount_root" --port "$TP_SERVER_PORT:9090" breakfix-server -- \
      "$(binary_for server)" -config "$config"
}

run_controller() {
  local kubeconfig config mount_root
  mount_root="$STATE_DIR/mount-controller"
  kubeconfig="$(create_service_account_kubeconfig controller)"
  config="$(prepare_config controller "$kubeconfig" "$mount_root")"
  printf 'Replacing Controller locally; readiness is at http://127.0.0.1:%s/readyz.\n' "$TP_CONTROLLER_HEALTH_PORT"
  export BREAKFIX_INCUS_ENDPOINT="$(secret_value incus_endpoint)"
  export BREAKFIX_INCUS_BASE_IMAGE_FINGERPRINT="$(secret_value incus_base_image_fingerprint)"
  export BREAKFIX_INCUS_BUILD_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_build_project)"
  export BREAKFIX_INCUS_IMAGE_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_image_project)"
  export BREAKFIX_INCUS_NAME_PREFIX="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_name_prefix)"
  export BREAKFIX_K8S_BASE_IMAGE_DIGEST="$(secret_value k8s_base_image_digest)"
  export BREAKFIX_REGISTRY_PULL_SECRET="$(secret_value registry_pull_secret)"
  replace_command controller "$config" "$mount_root" env HOME="$STATE_DIR/controller-data"
}

run_runtime_worker() {
  local kubeconfig config mount_root registry_trust_bundle_file
  prepare_worker_replacement runtime-worker
  mount_root="$STATE_DIR/mount-runtime-worker"
  kubeconfig="$(create_service_account_kubeconfig runtime-worker)"
  config="$(prepare_config runtime-worker "$kubeconfig" "$mount_root")"
  registry_trust_bundle_file="$(registry_trust_bundle_for_mount runtime-worker "$mount_root")"
  if test -n "$registry_trust_bundle_file"; then
    sed -i "s|^  trust_bundle_file:.*|  trust_bundle_file: $registry_trust_bundle_file|" "$config"
  fi
  printf 'Replacing Runtime Worker locally.\n'
  export BREAKFIX_WORKER_API_KEY="$(worker_identity_key runtime-worker)"
  export BREAKFIX_REGISTRY_REPOSITORY="$(secret_value registry_repository)"
  export BREAKFIX_REGISTRY_USERNAME="$(secret_value registry_username)"
  export BREAKFIX_REGISTRY_PASSWORD="$(secret_value registry_password)"
  export BREAKFIX_REGISTRY_TRUST_BUNDLE_FILE="$registry_trust_bundle_file"
  export BREAKFIX_INCUS_ENDPOINT="$(secret_value incus_endpoint)"
  export BREAKFIX_INCUS_BASE_IMAGE_FINGERPRINT="$(secret_value incus_base_image_fingerprint)"
  export BREAKFIX_INCUS_BUILD_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_build_project)"
  export BREAKFIX_INCUS_IMAGE_PROJECT="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_image_project)"
  export BREAKFIX_INCUS_NAME_PREFIX="$(optional_secret_value "$TP_RUNTIME_SECRET" incus_name_prefix)"
  replace_command runtime-worker "$config" "$mount_root" env POD_NAME=telepresence-runtime-worker
}

run_component() {
  local component="$1"
  local status
  ensure_prerequisites
  ensure_connected
  mkdir -p "$STATE_DIR"
  build_component "$component"
  trap "detach_component '$component'" EXIT
  set +e
  "run_${component//-/_}"
  status=$?
  set -e
  exit "$status"
}

show_status() {
  local workloads=()
  local component
  telepresence status || true
  while IFS= read -r component; do
    workloads+=("$(workload_for "$component")")
  done < <(all_components)
  kubectl -n "$TP_NAMESPACE" get deployment "${workloads[@]}" \
    -o custom-columns=NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas,AVAILABLE:.status.availableReplicas
  if telepresence status 2>/dev/null | grep -q 'Traffic Manager: Connected'; then
    telepresence list --namespace "$TP_NAMESPACE"
  fi
}

restore_all() {
  local component
  while IFS= read -r component; do
    detach_component "$component"
  done < <(all_components)
}

main() {
  local command="${1:-}"
  case "$command" in
    connect)
      ensure_prerequisites
      ensure_connected
      ;;
    server|controller|runtime-worker)
      run_component "$command"
      ;;
    down)
      if test "${2:-all}" = all; then
        restore_all
      else
        case "$2" in
          server|controller|runtime-worker) detach_component "$2" ;;
          *) fail "unknown component: $2" ;;
        esac
      fi
      ;;
    status) show_status ;;
    disconnect)
      restore_all
      telepresence quit --stop-daemons
      ;;
    -h|--help|help|'') usage ;;
    *) fail "unknown command: $command" ;;
  esac
}

main "$@"
