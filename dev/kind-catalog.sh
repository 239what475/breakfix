#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
catalog_dir=${BREAKFIX_CATALOG_DIR:-$repo_root/data}
challenges_dir=${BREAKFIX_CHALLENGES_DIR:-$catalog_dir/challenges}
taxonomy_dir=${BREAKFIX_TAXONOMY_DIR:-$catalog_dir/taxonomy}
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
pvc=breakfix-server-data
pod_name=${BREAKFIX_CATALOG_SYNC_POD:-breakfix-catalog-sync}
sync_image=${BREAKFIX_CATALOG_SYNC_IMAGE:-curlimages/curl:8.12.1}

for command in kubectl tar; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done
[ -d "$challenges_dir" ] || {
  printf 'Challenge source directory does not exist: %s\n' "$challenges_dir" >&2
  exit 1
}
[ -d "$taxonomy_dir" ] || {
  printf 'Taxonomy source directory does not exist: %s\n' "$taxonomy_dir" >&2
  exit 1
}
kubectl -n "$namespace" apply -f "$repo_root/deploy/runtime/server-data.yaml" >/dev/null
kubectl -n "$namespace" get persistentvolumeclaim "$pvc" >/dev/null

cleanup() {
  kubectl -n "$namespace" delete pod "$pod_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

kubectl -n "$namespace" delete pod "$pod_name" --ignore-not-found --wait=true >/dev/null
kubectl -n "$namespace" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $pod_name
  labels:
    app.kubernetes.io/name: $pod_name
    app.kubernetes.io/part-of: breakfix
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
  containers:
    - name: sync
      image: $sync_image
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec", "sleep 3600"]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: data
          mountPath: /var/lib/breakfix
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: $pvc
EOF

if ! kubectl -n "$namespace" wait --for=condition=Ready pod/"$pod_name" --timeout=2m >/dev/null; then
  kubectl -n "$namespace" describe pod "$pod_name" >&2 || true
  exit 1
fi

kubectl -n "$namespace" exec "$pod_name" -- /bin/sh -ec '
  rm -rf /var/lib/breakfix/challenges.next
  rm -rf /var/lib/breakfix/taxonomy.next
  mkdir -p /var/lib/breakfix/challenges.next
  mkdir -p /var/lib/breakfix/taxonomy.next
'
tar -C "$challenges_dir" -cf - . | kubectl -n "$namespace" exec -i "$pod_name" -- \
  tar -xf - -C /var/lib/breakfix/challenges.next
tar -C "$taxonomy_dir" -cf - . | kubectl -n "$namespace" exec -i "$pod_name" -- \
  tar -xf - -C /var/lib/breakfix/taxonomy.next
kubectl -n "$namespace" exec "$pod_name" -- /bin/sh -ec '
  rm -rf /var/lib/breakfix/challenges.previous
  rm -rf /var/lib/breakfix/taxonomy.previous
  if [ -d /var/lib/breakfix/challenges ]; then
    mv /var/lib/breakfix/challenges /var/lib/breakfix/challenges.previous
  fi
  if [ -d /var/lib/breakfix/taxonomy ]; then
    mv /var/lib/breakfix/taxonomy /var/lib/breakfix/taxonomy.previous
  fi
  mv /var/lib/breakfix/challenges.next /var/lib/breakfix/challenges
  mv /var/lib/breakfix/taxonomy.next /var/lib/breakfix/taxonomy
  rm -rf /var/lib/breakfix/challenges.previous
  rm -rf /var/lib/breakfix/taxonomy.previous
'

printf 'Synchronized catalog from %s to %s/%s.\n' "$catalog_dir" "$namespace" "$pvc"
