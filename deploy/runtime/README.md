# Breakfix Runtime Deployment

This base Kustomize package installs PostgreSQL, Server, Controller, and four fixed Worker Deployments: Agent, Builder, Publisher, and Verifier. The repository-root package additionally installs the managed OCI Registry; `deploy/overlays/external-registry` omits it for Harbor, cloud Registries, or another operator-provided OCI Registry. The workflow is persisted in PostgreSQL as `WorkItem` records and is executed as:

```text
CandidateRevision -> Build -> ArtifactPublish -> Verify -> author review -> ChallengePublish
```

The Controller only reconciles `NodeEnvironment` and `VK8sEnvironment`; it never owns CandidateRevision state or Worker leases.

OpenSandbox is an explicit prerequisite. Install its tested native Kubernetes workload provider in namespace `opensandbox` before applying this package. Breakfix Server creates and removes its own BYO workspace PVCs; OpenSandbox only mounts them and must not receive PVC lifecycle ownership.

## Build and publish runtime images

Build release binaries outside Docker, then package the six runtime images. Docker receives only the matching binary under `bin/release/<os>-<arch>/<component>/`; it does not receive source code, Go modules, Node dependencies, or host Go network configuration.

```bash
make runtime-push TARGETOS=linux TARGETARCH=amd64 \
  RUNTIME_IMAGE_REPOSITORY=ghcr.io/breakfix RUNTIME_IMAGE_TAG=dev
```

For a release, `make release-manifest` resolves all six image digests and writes `dist/breakfix-<tag>.yaml`. Apply that artifact rather than manually changing the base manifests.

The separate K8s management-terminal base image is built and pushed by `make k8s-base-image` to the configured OCI Registry. Its resolved digest is written to the local `breakfix-runtime` Secret before the runtime manifest is applied. The Node system-container base image is not an OCI image: `make dev-incus` bootstraps it in Incus and prints the full fingerprint required by configuration.

## Secrets

Create the namespace and runtime Secret before applying the package. Keep the copied environment file outside this repository.

```bash
cp deploy/runtime/runtime-secret.example.env /secure/path/breakfix-runtime.env
# Edit the copied file with real values.
kubectl apply -f deploy/runtime/namespace.yaml
kubectl -n breakfix-system create secret generic breakfix-runtime \
  --from-env-file=/secure/path/breakfix-runtime.env
```

Create four independent Worker identity Secrets from different random values. Server receives all four to authenticate its internal API; each Worker Deployment receives only its own key.

```bash
for role in agent-worker builder publisher verifier; do
  kubectl -n breakfix-system create secret generic "breakfix-${role}-identity" \
    --from-env-file=/secure/path/"${role}"-identity.env
done
```

The repository-root package is the managed internal-Registry mode. Its `breakfix-registry` Service is a generic `LoadBalancer`; add the private-LB annotation required by the target cloud or on-premises implementation in an operator overlay. Choose a stable private DNS name such as `registry.breakfix.internal`, point it at that LoadBalancer, and make it resolvable by every Kubernetes node.

The Registry TLS Secret is supplied by the cluster administrator. Create a leaf certificate for that private DNS name using the administrator-owned internal CA; Breakfix never creates, renews, or stores the CA private key. Install the CA root in every Kubernetes node's image-runtime trust store before any image pull. Server and Publisher are distroless images, so when using an internal CA also expose its public root as `breakfix-registry-ca` and set `registry_trust_bundle_file=/var/run/config/breakfix-registry-ca/ca.crt` in `breakfix-runtime`:

```bash
kubectl -n breakfix-system create secret tls breakfix-registry-tls \
  --cert=/secure/path/registry.crt \
  --key=/secure/path/registry.key
kubectl -n breakfix-system create configmap breakfix-registry-ca \
  --from-file=ca.crt=/secure/path/registry-ca.crt
```

The managed Registry also needs an authentication Secret. `breakfix-registry-auth` is mounted only by the bundled Registry. The Publisher and Server obtain Registry credentials through `breakfix-runtime`; Builder never gets Registry credentials. Use the Registry hostname (including a non-default port when applicable), without a repository prefix, in the Docker config Secret:

```bash
REGISTRY_HOST=registry.breakfix.internal
REGISTRY_USER=breakfix
REGISTRY_PASSWORD=replace-with-a-long-random-password

htpasswd -Bbn "$REGISTRY_USER" "$REGISTRY_PASSWORD" >/secure/path/breakfix-registry.htpasswd
kubectl -n breakfix-system create secret generic breakfix-registry-auth \
  --from-file=htpasswd=/secure/path/breakfix-registry.htpasswd
kubectl -n breakfix-system create secret docker-registry breakfix-registry-pull \
  --docker-server="$REGISTRY_HOST" --docker-username="$REGISTRY_USER" --docker-password="$REGISTRY_PASSWORD"
```

For an external OCI Registry, apply `kubectl apply -k deploy/overlays/external-registry` instead of `kubectl apply -k .`. Set `registry_addr`, optional `registry_pull_secret`, optional `registry_trust_bundle_file`, and optional Publisher credentials in `breakfix-runtime`; use the same CA/node trust procedure for a private external Registry. The external endpoint must support HTTPS OCI push, immutable digest pulls, and manifest DELETE. A publicly trusted external Registry leaves `registry_trust_bundle_file` empty.

When `registry_pull_secret` is nonempty, Controller copies that Docker config Secret into each transient VK8s namespace and binds it to a `breakfix-runtime` ServiceAccount. When it is empty, Controller creates the same tokenless ServiceAccount without pull credentials. The terminal Pod is created with that ServiceAccount and `automountServiceAccountToken: false`; Kubernetes may materialize the ServiceAccount image-pull reference into the stored Pod spec, but the Secret is never mounted into the challenge container. The bundled Distribution Registry uses basic authentication. Deployments requiring repository-scoped read and write permissions should use an OCI Registry authorization backend that provides them, such as Harbor robot accounts.

## Incus provider

Node environments require an Incus 7.0.1 cluster reachable at the configured private mTLS endpoint. Bootstrap fixed projects, the `node-systemd-base` image, and role-specific certificates with:

```bash
make dev-incus
make dev-incus-secrets
```

The second command creates `breakfix-incus-server`, `breakfix-incus-controller`, `breakfix-incus-builder`, `breakfix-incus-publisher`, and `breakfix-incus-verifier`. Builder is restricted to the build Project; Publisher is restricted to build/image Projects; Server, Controller, and Verifier have distinct revocable platform identities because they must access dynamic Environment Projects. Do not mount a developer CLI certificate or `~/.config/incus` into workloads.

The bundled Incus bootstrap is an explicit, idempotent operational action. It does not run during application startup. It clears the upstream image expiry before publishing `node-systemd-base`, so platform and challenge images remain permanent immutable artifacts. It uses Incus CLI only to establish the provider baseline; all runtime operations use the Go SDK.

The Builder bootstrap bridge is separate from NodeEnvironment bridges. It defaults to `10.248.25.1/24` and can be changed with `BREAKFIX_INCUS_BUILD_NETWORK_CIDR`; select a dedicated CIDR outside `incus.node_network_pool`. Bootstrap validates an existing bridge instead of accepting a differently configured network or asking Incus to auto-select one.

## Apply and inspect

The repository root is the canonical Kustomize package. It adds generated CRDs and the in-cluster ConfigMap to this runtime package.

```bash
make verify-crd-generated
make verify-api-generated
kubectl kustomize .
kubectl apply -k .
kubectl -n breakfix-system get deployments,pods
```

This is a destructive development migration: it intentionally has no importer for the previous SQLite/PostgreSQL schema or Server data layout. To recreate a disposable Kind baseline before applying the current package, run `make dev-kind-reset-state`, then `make dev-kind-runtime` and `make dev-kind-catalog`. The reset command refuses a non-Kind context unless `BREAKFIX_ALLOW_NON_KIND_RESET=1` is explicitly set.
`make dev-kind-runtime` validates the runtime Secret, optional Docker pull Secret, managed Registry TLS/auth Secrets, and optional CA ConfigMap before applying, then restarts the development Deployments so Secret-backed environment variables are current. `make dev-incus-secrets` similarly records the bootstrap's formal Node base fingerprint in `breakfix-runtime`. Use `BREAKFIX_KIND_RUNTIME_MANIFEST=$PWD/deploy/overlays/external-registry make dev-kind-runtime` for an external Registry. The script never adds an insecure Registry, a node CA, node DNS/hosts entries, containerd configuration, or an image preload fallback; node DNS and CA bootstrap remain explicit operator work.

Server owns the RWO `breakfix-server-data` PVC and therefore uses `Recreate`. PostgreSQL and the managed Registry each own separate RWO PVCs. The managed Registry remains a single replica until its storage backend is replaced with shared object storage.

NetworkPolicy rules permit each Worker to reach Server plus its explicit provider endpoints only. A production CNI must enforce NetworkPolicy. The managed Registry uses an operator-configured private LoadBalancer; its configured endpoint must be reachable through node DNS. Kubernetes CoreDNS is a Pod DNS service, not the node DNS used by kubelet/containerd.

## Operational model

`/readyz` reports only each process's core serving state. Dynamic dependencies are capability checks rather than Pod readiness gates: Server and Node-capable Workers expose `/capabilities/node-provider`; Publisher also exposes `/capabilities/registry`; Verifier exposes `/capabilities/kubernetes-api`. An unavailable Incus provider therefore does not remove Server, Controller, Builder, Publisher, Verifier, or VK8s paths from service. Node operations report a retryable provider failure and reconnect on later calls. Fixed Workers emit durable WorkItem IDs and attempts, so inspect their Deployment logs directly.
