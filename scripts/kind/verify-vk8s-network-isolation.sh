#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cluster=${BREAKFIX_VK8S_NETWORK_CLUSTER:-breakfix-vk8s-network}
context="kind-$cluster"
environment_namespace=breakfix-vk8s-network
platform_namespace=breakfix-network-platform
vcluster_name=acceptance-vc
test_image=breakfix-test/vk8s-network:1
egress_network="breakfix-vk8s-egress-$cluster"
egress_server="breakfix-vk8s-egress-server-$cluster"
egress_address=198.18.0.2
port_forward_port=${BREAKFIX_VK8S_NETWORK_PORT_FORWARD_PORT:-18443}
keep_cluster=${BREAKFIX_KEEP_VK8S_NETWORK_CLUSTER:-0}
work_dir=$(mktemp -d)
port_forward_pid=

fail() {
  printf 'vk8s network acceptance: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

k() {
  kubectl --context "$context" "$@"
}

wait_for_labeled_pods() {
  namespace=$1
  selector=$2
  for attempt in $(seq 1 60); do
    pods=$(k -n "$namespace" get pods -l "$selector" -o name)
    if [ -n "$pods" ]; then
      k -n "$namespace" wait --for=condition=Ready $pods --timeout=3m >/dev/null
      return
    fi
    sleep 1
  done
  fail "no Pods matching $selector appeared in namespace $namespace"
}

cleanup() {
  result=$?
  if [ -n "$port_forward_pid" ]; then
    kill "$port_forward_pid" >/dev/null 2>&1 || true
  fi
  if [ "$keep_cluster" != 1 ]; then
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
    docker rm -f "$egress_server" >/dev/null 2>&1 || true
    docker network rm "$egress_network" >/dev/null 2>&1 || true
  fi
  rm -rf "$work_dir"
  exit "$result"
}

trap cleanup 0 HUP INT TERM

for command in base64 docker grep kind kubectl seq vcluster; do
  require_command "$command"
done

if kind get clusters | grep -Fxq "$cluster"; then
  fail "Kind cluster $cluster already exists; choose BREAKFIX_VK8S_NETWORK_CLUSTER or delete it"
fi

printf 'Creating Kind cluster %s with no default CNI...\n' "$cluster"
kind create cluster --name "$cluster" --config "$repo_root/test/kind/vk8s-network-policy.yaml" >/dev/null

docker build --quiet --tag "$test_image" --file "$repo_root/test/kind/vk8s-network.Dockerfile" "$repo_root/test/kind" >/dev/null
kind load docker-image --name "$cluster" "$test_image" >/dev/null

printf 'Installing Calico v3.31.3 so NetworkPolicy is enforced...\n'
k apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.31.3/manifests/calico.yaml >/dev/null
k -n kube-system rollout status daemonset/calico-node --timeout=5m >/dev/null
k -n kube-system rollout status deployment/calico-kube-controllers --timeout=5m >/dev/null
k wait --for=condition=Ready node --all --timeout=2m >/dev/null

k create namespace "$environment_namespace" >/dev/null
k apply -f "$repo_root/test/kind/vk8s-network-host-resources.yaml" >/dev/null
k -n "$platform_namespace" wait --for=condition=Ready pod/platform --timeout=2m >/dev/null
test "$(k -n "$platform_namespace" exec platform -- wget -qO- http://platform:8080)" = ok || \
  fail "platform Service is not reachable from an unrestricted Pod"

printf 'Creating vcluster %s with native NetworkPolicy values...\n' "$vcluster_name"
vcluster create "$vcluster_name" --namespace "$environment_namespace" \
  --connect=false --background-proxy=false \
  --chart-repo https://charts.loft.sh --chart-version 0.35.1 \
  --values "$repo_root/test/kind/vk8s-network-vcluster.yaml" >/dev/null

k -n "$environment_namespace" wait --for=condition=Ready "pod/$vcluster_name-0" --timeout=3m >/dev/null
wait_for_labeled_pods "$environment_namespace" "k8s-app=vcluster-kube-dns,vcluster.loft.sh/managed-by=$vcluster_name"
k -n "$environment_namespace" get networkpolicy \
  "vc-cp-$vcluster_name" "vc-work-$vcluster_name" breakfix-vk8s-terminal >/dev/null

k -n "$environment_namespace" wait --for=condition=Ready pod/terminal --timeout=2m >/dev/null
platform_ip=$(k -n "$platform_namespace" get service platform -o jsonpath='{.spec.clusterIP}')
api_ip=$(k -n default get service kubernetes -o jsonpath='{.spec.clusterIP}')
test -n "$platform_ip" && test -n "$api_ip" || fail "expected platform and Kubernetes Service addresses"

k -n "$environment_namespace" exec terminal -- env \
  CONTROL_PLANE_FQDN="$vcluster_name.$environment_namespace.svc.cluster.local" \
  PLATFORM_IP="$platform_ip" HOST_API_IP="$api_ip" \
  sh -ceu '
    nslookup "$CONTROL_PLANE_FQDN"
    nc -z -w 5 "$CONTROL_PLANE_FQDN" 443
    for target in "$PLATFORM_IP:8080" "$HOST_API_IP:443" 169.254.169.254:80; do
      host=${target%:*}
      port=${target##*:}
      if nc -z -w 2 "$host" "$port"; then
        printf "management terminal unexpectedly reached protected target: %s\\n" "$target" >&2
        exit 1
      fi
    done
  '

vcluster_kubeconfig="$work_dir/vcluster.kubeconfig"
k -n "$environment_namespace" get secret "vc-$vcluster_name" -o jsonpath='{.data.config}' | base64 -d > "$vcluster_kubeconfig"

if ! docker network inspect "$egress_network" >/dev/null 2>&1; then
  docker network create --subnet 198.18.0.0/24 "$egress_network" >/dev/null
fi
docker run -d --name "$egress_server" --network "$egress_network" --ip "$egress_address" \
  --entrypoint sh "$test_image" -c 'mkdir -p /www && printf ok > /www/index.html && exec httpd -f -p 8080 -h /www' >/dev/null
docker network connect "$egress_network" "$cluster-control-plane"

k -n "$environment_namespace" port-forward "service/$vcluster_name" "$port_forward_port:443" >"$work_dir/port-forward.log" 2>&1 &
port_forward_pid=$!
for attempt in $(seq 1 30); do
  if grep -q 'Forwarding from' "$work_dir/port-forward.log"; then
    break
  fi
  sleep 1
done
grep -q 'Forwarding from' "$work_dir/port-forward.log" || {
  sed -n '1,120p' "$work_dir/port-forward.log" >&2
  fail "could not establish vcluster API port-forward"
}
kubectl --kubeconfig="$vcluster_kubeconfig" config set-cluster kubernetes \
  --server="https://127.0.0.1:$port_forward_port" --insecure-skip-tls-verify=true >/dev/null
kubectl --kubeconfig="$vcluster_kubeconfig" apply -f "$repo_root/test/kind/vk8s-network-workloads.yaml" >/dev/null
kubectl --kubeconfig="$vcluster_kubeconfig" wait --for=condition=Ready pod/peer pod/learner --timeout=3m >/dev/null

kubectl --kubeconfig="$vcluster_kubeconfig" label pod/learner "release=$vcluster_name" --overwrite >/dev/null
sleep 2
host_learner=$(k -n "$environment_namespace" get pod \
  -l "app=learner,vcluster.loft.sh/managed-by=$vcluster_name" -o jsonpath='{.items[0].metadata.name}')
test -n "$host_learner" || fail "could not find the translated learner Pod"
host_release=$(k -n "$environment_namespace" get pod "$host_learner" -o jsonpath='{.metadata.labels.release}')
test -z "$host_release" || fail "learner release label was not translated by vcluster"

kubectl --kubeconfig="$vcluster_kubeconfig" exec learner -- env \
  PEER_FQDN=peer.default.svc.cluster.local PLATFORM_IP="$platform_ip" \
  HOST_API_IP="$api_ip" EGRESS_IP="$egress_address" \
  sh -ceu '
    nslookup "$PEER_FQDN"
    test "$(wget -qO- -T 5 http://$PEER_FQDN:8080)" = ok
    test "$(wget -qO- -T 5 http://$EGRESS_IP:8080)" = ok
    for target in "$PLATFORM_IP:8080" "$HOST_API_IP:443" 10.0.0.1:443 169.254.169.254:80; do
      host=${target%:*}
      port=${target##*:}
      if nc -z -w 2 "$host" "$port"; then
        printf "learner unexpectedly reached protected target: %s\\n" "$target" >&2
        exit 1
      fi
    done
  '

printf 'VK8s network isolation acceptance passed on Calico.\n'
