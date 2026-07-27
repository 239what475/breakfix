# Breakfix Runtime Deployment

This package installs the independent PostgreSQL, Server, Controller, Agent Worker, and OCI Registry workloads described in
`EINO-RESEARCH.md`. OpenSandbox is an explicit prerequisite: install its tested native Kubernetes workload provider
in the `opensandbox` namespace before applying this package. Breakfix creates and deletes its own BYO workspace PVCs;
OpenSandbox only mounts those PVCs and must not be granted their lifecycle ownership.

Build the release binaries outside Docker, then package and publish the four runtime images. Each Docker build
receives only its binary under `bin/release/<os>-<arch>/<component>/`; it does not receive source code, Node
dependencies, Go modules, or host Go network configuration. Replace the development tags in `kustomization.yaml`
(or an environment overlay):

```bash
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/breakfix RUNTIME_IMAGE_TAG=dev \
  VERIFIER_IMAGE_REPOSITORY=registry.breakfix.internal/breakfix
```

`VERIFIER_IMAGE_REPOSITORY` must exactly equal the configured `registry_addr`: the Controller derives every verifier
Job image as `<registry_addr>/breakfix-verifier:latest`. The Server, Controller, and Agent Worker images may use a
separate registry such as GHCR.

The Controller image and the configured vcluster chart are pinned to `v0.35.1`. The Controller ClusterRole includes
the corresponding rendered chart RBAC contract, because Kubernetes otherwise refuses Helm's role bindings as a
privilege escalation. The bundled Registry is a single-replica
`registry:2.8.3` Deployment with its own RWO PVC. It exposes HTTPS on a `LoadBalancer` Service. Before applying this
package, an installation overlay must add the cloud/provider-specific annotation that makes this LoadBalancer internal;
the base manifest deliberately does not guess a provider annotation. The Registry hostname must resolve to that internal
endpoint from every Kubernetes node, and its TLS issuer must be trusted by every node runtime.

The bundled Registry has Docker Registry V2 manifest deletion enabled. Failed VerifyTasks delete their temporary
verification manifest before cleanup can finish. This does not reclaim unreferenced blobs immediately: run Distribution
garbage collection only in a scheduled maintenance window while the Registry is offline.

Breakfix creates OpenSandbox Sandboxes with `ManualCleanup=true`. The Server explicitly deletes the Sandbox and
its BYO PVC when the owning Generator Run becomes terminal, so the provider must permit manual lifecycle deletion
and must not impose an independent workspace timeout.

Create the control-plane namespace and runtime Secret before applying this package. Keep the copied environment file outside the repository:

```bash
cp deploy/runtime/runtime-secret.example.env /secure/path/breakfix-runtime.env
# Edit /secure/path/breakfix-runtime.env with real values.
kubectl apply -f deploy/runtime/namespace.yaml
kubectl -n breakfix-system create secret generic breakfix-runtime \
  --from-env-file=/secure/path/breakfix-runtime.env
```

Create the Registry-specific Secrets in the same namespace. `breakfix-registry-auth` is mounted only by the Registry;
`breakfix-registry-write` is injected only into verifier Jobs; the Controller copies only the Docker config data from
`breakfix-registry-pull` into each short-lived challenge namespace and never mounts it into a challenge container.
Use the registry host only, without the `/breakfix` repository prefix, in the Docker config Secret:

```bash
REGISTRY_HOST=registry.breakfix.internal
REGISTRY_USER=breakfix
REGISTRY_PASSWORD=replace-with-a-long-random-password

htpasswd -Bbn "$REGISTRY_USER" "$REGISTRY_PASSWORD" >/secure/path/breakfix-registry.htpasswd
kubectl -n breakfix-system create secret generic breakfix-registry-auth \
  --from-file=htpasswd=/secure/path/breakfix-registry.htpasswd
kubectl -n breakfix-system create secret tls breakfix-registry-tls \
  --cert=/secure/path/registry.crt --key=/secure/path/registry.key
kubectl -n breakfix-system create secret docker-registry breakfix-registry-pull \
  --docker-server="$REGISTRY_HOST" --docker-username="$REGISTRY_USER" --docker-password="$REGISTRY_PASSWORD"
kubectl -n breakfix-system create secret generic breakfix-registry-write \
  --from-literal=REGISTRY_USERNAME="$REGISTRY_USER" --from-literal=REGISTRY_PASSWORD="$REGISTRY_PASSWORD"
```

Set the matching `registry_addr`, `registry_username`, and `registry_password` in `breakfix-runtime.env`. Bootstrap
`breakfix-base`, `breakfix-k8s-base`, and `breakfix-verifier` in `registry_addr` before creating the first VerifyTask;
the verifier Job and BuildKit both pull from this Registry.

The repository root is the canonical Kustomize package. It keeps the in-cluster config in `config/` while using
Kustomize's default file-loading restrictions. Apply it after the Secret exists:

```bash
kubectl apply -k .
```

The Server owns the RWO `breakfix-server-data` PVC and therefore uses `Recreate`. PostgreSQL and Registry each own a
separate RWO PVC. The Registry is intentionally a single replica until its filesystem backend is replaced with object
storage. The Server alone receives the OpenSandbox lifecycle key and has PVC permission for the `opensandbox` namespace.
The Agent Worker has no Kubernetes RBAC, no provider key, and disables ServiceAccount token mounting.
