#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  printf 'Usage: %s SOURCE_IMAGE DESTINATION_IMAGE\n' "$0" >&2
  exit 2
fi

source_image=$1
destination=$2
namespace=${BREAKFIX_NAMESPACE:-breakfix-system}
pod_name=${BREAKFIX_REGISTRY_PUSH_POD:-breakfix-registry-push}
push_image=${BREAKFIX_REGISTRY_PUSH_IMAGE:-quay.io/skopeo/stable:v1.18.0}
runtime_secret=${BREAKFIX_RUNTIME_SECRET:-breakfix-runtime}

case "$destination" in
  *[!a-zA-Z0-9._:/@-]* | */ | /* | *//*)
    printf 'Invalid destination image reference: %s\n' "$destination" >&2
    exit 2
    ;;
esac

for command in docker kubectl; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

docker image inspect "$source_image" >/dev/null
kubectl -n "$namespace" get secret "$runtime_secret" >/dev/null

tmpdir=$(mktemp -d)
cleanup() {
  kubectl -n "$namespace" delete pod "$pod_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  find "$tmpdir" -depth -delete
}
trap cleanup EXIT HUP INT TERM

docker save "$source_image" -o "$tmpdir/image.tar"
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
    - name: push
      image: $push_image
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-ec", "sleep 3600"]
      env:
        - name: DESTINATION
          value: "$destination"
        - name: REGISTRY_USERNAME
          valueFrom:
            secretKeyRef:
              name: $runtime_secret
              key: registry_username
        - name: REGISTRY_PASSWORD
          valueFrom:
            secretKeyRef:
              name: $runtime_secret
              key: registry_password
        - name: REGISTRY_TRUST_BUNDLE_FILE
          valueFrom:
            secretKeyRef:
              name: $runtime_secret
              key: registry_trust_bundle_file
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: work
          mountPath: /tmp
        - name: registry-ca
          mountPath: /var/run/config/breakfix-registry-ca
          readOnly: true
  volumes:
    - name: work
      emptyDir: {}
    - name: registry-ca
      configMap:
        name: breakfix-registry-ca
        optional: true
EOF

if ! kubectl -n "$namespace" wait --for=condition=Ready pod/"$pod_name" --timeout=2m >/dev/null; then
  kubectl -n "$namespace" describe pod "$pod_name" >&2 || true
  exit 1
fi

kubectl -n "$namespace" exec -i "$pod_name" -- \
  /bin/sh -ec 'cat > /tmp/image.tar' <"$tmpdir/image.tar"
kubectl -n "$namespace" exec "$pod_name" -- /bin/sh -ec '
  if [ -n "$REGISTRY_TRUST_BUNDLE_FILE" ]; then
    test -r /var/run/config/breakfix-registry-ca/ca.crt
    cert_args="--dest-cert-dir /var/run/config/breakfix-registry-ca"
  else
    cert_args=""
  fi
  if [ -n "$REGISTRY_USERNAME" ]; then
    skopeo copy $cert_args --dest-creds "$REGISTRY_USERNAME:$REGISTRY_PASSWORD" docker-archive:/tmp/image.tar "docker://$DESTINATION"
  else
    skopeo copy $cert_args docker-archive:/tmp/image.tar "docker://$DESTINATION"
  fi
' >&2
digest=$(kubectl -n "$namespace" exec "$pod_name" -- /bin/sh -ec '
  if [ -n "$REGISTRY_TRUST_BUNDLE_FILE" ]; then
    test -r /var/run/config/breakfix-registry-ca/ca.crt
    cert_args="--cert-dir /var/run/config/breakfix-registry-ca"
  else
    cert_args=""
  fi
  if [ -n "$REGISTRY_USERNAME" ]; then
    skopeo inspect $cert_args --creds "$REGISTRY_USERNAME:$REGISTRY_PASSWORD" --format "{{.Digest}}" "docker://$DESTINATION"
  else
    skopeo inspect $cert_args --format "{{.Digest}}" "docker://$DESTINATION"
  fi
')

case "$digest" in
  sha256:[0-9a-f][0-9a-f]*)
    ;;
  *)
    printf 'Registry returned an invalid digest: %s\n' "$digest" >&2
    exit 1
    ;;
esac

repository=${destination%@*}
repository=${repository%:*}
printf '%s@%s\n' "$repository" "$digest"
