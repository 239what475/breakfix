#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
registry_node_port=${BREAKFIX_KIND_REGISTRY_NODE_PORT:-30443}
tls_dir=${BREAKFIX_KIND_REGISTRY_TLS_DIR:-$repo_root/.local/kind-registry}

for command in awk base64 docker grep jq kind kubectl openssl sha256sum tr; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

context=$(kubectl config current-context)
case "$context" in
  kind-*)
    kind_cluster=${context#kind-}
    ;;
  *)
    printf 'dev/kind-registry.sh requires a Kind context, current context is %s\n' "$context" >&2
    exit 2
    ;;
esac

control_plane=$(kubectl get nodes \
  -l node-role.kubernetes.io/control-plane \
  -o jsonpath='{.items[0].metadata.name}')
[ -n "$control_plane" ] || {
  printf 'Kind cluster %s has no control-plane node\n' "$kind_cluster" >&2
  exit 1
}

# kubelet/containerd cannot use a cluster-internal Service name as an image
# authority. Use the control-plane address on the Kind Docker network for
# immutable image references, and the Service DNS name for control-plane HTTP
# clients inside the cluster.
registry_ip=$(docker inspect \
  --format '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}' \
  "$control_plane")
printf '%s\n' "$registry_ip" | awk -F. '
  NF == 4 { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1; next }
  { exit 1 }
' || {
  printf 'could not resolve a valid Kind control-plane Docker-network IPv4 address for %s\n' "$control_plane" >&2
  exit 1
}
registry_authority=$registry_ip:$registry_node_port
registry_address=$registry_authority/breakfix
registry_client_address=breakfix-registry.$namespace.svc.cluster.local

secret_value() {
  kubectl -n "$namespace" get secret breakfix-runtime -o json |
    jq -r --arg key "$1" 'if .data[$key] == null then "" else .data[$key] | @base64d end'
}

kubectl -n "$namespace" get secret breakfix-runtime >/dev/null || {
  printf 'runtime Secret breakfix-runtime is required before preparing the Kind Registry\n' >&2
  exit 1
}
registry_username=$(secret_value registry_username)
registry_password=$(secret_value registry_password)
registry_pull_secret=$(secret_value registry_pull_secret)
[ -n "$registry_username" ] && [ -n "$registry_password" ] || {
  printf 'Kind Registry requires registry_username and registry_password in breakfix-runtime\n' >&2
  exit 1
}
[ -n "$registry_pull_secret" ] || {
  printf 'Kind Registry requires registry_pull_secret in breakfix-runtime\n' >&2
  exit 1
}
kubectl -n "$namespace" get secret breakfix-registry-auth >/dev/null || {
  printf 'Kind Registry requires the administrator-provided Secret breakfix-registry-auth\n' >&2
  exit 1
}

umask 077
mkdir -p "$tls_dir"
ca_key=$tls_dir/ca.key
ca_certificate=$tls_dir/ca.crt
leaf_key=$tls_dir/registry.key
leaf_csr=$tls_dir/registry.csr
leaf_certificate=$tls_dir/registry.crt
leaf_extensions=$tls_dir/registry.ext
ca_serial=$tls_dir/ca.srl

if [ ! -f "$ca_key" ] || [ ! -f "$ca_certificate" ]; then
  [ ! -e "$ca_key" ] && [ ! -e "$ca_certificate" ] || {
    printf 'incomplete Kind Registry CA material in %s\n' "$tls_dir" >&2
    exit 1
  }
  openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 3650 \
    -subj '/CN=Breakfix Kind Registry Development CA' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -keyout "$ca_key" -out "$ca_certificate" >/dev/null 2>&1
fi

leaf_is_current=false
if [ -f "$leaf_key" ] && [ -f "$leaf_certificate" ]; then
  if openssl x509 -in "$leaf_certificate" -checkend 86400 -noout >/dev/null 2>&1 &&
    openssl x509 -in "$leaf_certificate" -noout -ext subjectAltName 2>/dev/null |
      grep -Fq "IP Address:$registry_ip" &&
    openssl x509 -in "$leaf_certificate" -noout -ext subjectAltName 2>/dev/null |
      grep -Fq "DNS:$registry_client_address"; then
    leaf_is_current=true
  fi
fi

if [ "$leaf_is_current" != true ]; then
  cat >"$leaf_extensions" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:$registry_ip,DNS:$registry_client_address
EOF
  openssl req -new -newkey rsa:2048 -nodes \
    -subj "/CN=$registry_ip" \
    -keyout "$leaf_key" -out "$leaf_csr" >/dev/null 2>&1
  openssl x509 -req -sha256 -days 825 \
    -in "$leaf_csr" -CA "$ca_certificate" -CAkey "$ca_key" \
    -CAserial "$ca_serial" -CAcreateserial \
    -extfile "$leaf_extensions" -out "$leaf_certificate" >/dev/null 2>&1
fi
chmod 600 "$ca_key" "$leaf_key"
chmod 644 "$ca_certificate" "$leaf_certificate"

kubectl -n "$namespace" create configmap breakfix-registry-ca \
  --from-file=ca.crt="$ca_certificate" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$namespace" create secret tls breakfix-registry-tls \
  --cert="$leaf_certificate" --key="$leaf_key" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# Kind's embedded containerd uses the node system trust store in this profile.
# Install only the CA; do not edit containerd configuration, CoreDNS, or
# /etc/hosts. Existing containerd processes reload the trust store on restart.
ca_hash=$(sha256sum "$ca_certificate" | awk '{print $1}')
node_ca_path=/usr/local/share/ca-certificates/breakfix-kind-registry.crt
for node in $(kind get nodes --name "$kind_cluster"); do
  docker exec "$node" sh -ec 'command -v update-ca-certificates >/dev/null && command -v systemctl >/dev/null' || {
    printf 'Kind node %s must provide update-ca-certificates and systemctl\n' "$node" >&2
    exit 1
  }
  node_ca_hash=$(docker exec "$node" sha256sum "$node_ca_path" 2>/dev/null | awk '{print $1}')
  if [ "$node_ca_hash" != "$ca_hash" ]; then
    docker cp "$ca_certificate" "$node:$node_ca_path" >/dev/null
    docker exec "$node" chmod 644 "$node_ca_path"
    docker exec "$node" update-ca-certificates >/dev/null
    docker exec "$node" systemctl restart containerd
    kubectl wait --for=condition=Ready "node/$node" --timeout=2m >/dev/null
  fi
done

kubectl -n "$namespace" create secret docker-registry "$registry_pull_secret" \
  --docker-server="$registry_authority" \
  --docker-username="$registry_username" \
  --docker-password="$registry_password" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

address_encoded=$(printf '%s' "$registry_address" | base64 | tr -d '\n')
client_address_encoded=$(printf '%s' "$registry_client_address" | base64 | tr -d '\n')
bundle_path_encoded=$(printf '%s' '/var/run/config/breakfix-registry-ca/ca.crt' | base64 | tr -d '\n')
patch=$(jq -cn --arg address "$address_encoded" --arg client_address "$client_address_encoded" --arg bundle "$bundle_path_encoded" \
  '{data: {registry_addr: $address, registry_client_addr: $client_address, registry_trust_bundle_file: $bundle}}')
kubectl -n "$namespace" patch secret breakfix-runtime --type merge --patch "$patch" >/dev/null

kubectl -n "$namespace" apply -f "$repo_root/deploy/overlays/kind/registry.yaml" >/dev/null
kubectl -n "$namespace" rollout restart deployment/breakfix-registry >/dev/null
kubectl -n "$namespace" rollout status deployment/breakfix-registry --timeout=2m >/dev/null

printf 'Prepared Kind Registry for image pulls at https://%s and in-cluster clients at https://%s (CA: %s).\n' \
  "$registry_address" "$registry_client_address" "$tls_dir"
