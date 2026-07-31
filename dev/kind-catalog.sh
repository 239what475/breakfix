#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_dir=${BREAKFIX_CHALLENGES_DIR:-$repo_root/data/challenges}
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
pvc=${BREAKFIX_SERVER_DATA_PVC:-breakfix-server-data}
pod_name=${BREAKFIX_CATALOG_SYNC_POD:-breakfix-catalog-sync}
sync_image=${BREAKFIX_CATALOG_SYNC_IMAGE:-curlimages/curl:8.12.1}

for command in kubectl tar; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done
[ -d "$source_dir" ] || {
  printf 'Challenge source directory does not exist: %s\n' "$source_dir" >&2
  exit 1
}
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
  mkdir -p /var/lib/breakfix/challenges.next
'
tar -C "$source_dir" -cf - . | kubectl -n "$namespace" exec -i "$pod_name" -- \
  tar -xf - -C /var/lib/breakfix/challenges.next
kubectl -n "$namespace" exec "$pod_name" -- /bin/sh -ec '
  rm -rf /var/lib/breakfix/challenges.previous
  if [ -d /var/lib/breakfix/challenges ]; then
    mv /var/lib/breakfix/challenges /var/lib/breakfix/challenges.previous
  fi
  mv /var/lib/breakfix/challenges.next /var/lib/breakfix/challenges
  rm -rf /var/lib/breakfix/challenges.previous
'

printf 'Synchronized %s to %s/%s.\n' "$source_dir" "$namespace" "$pvc"
