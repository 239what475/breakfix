# Breakfix Runtime Deployment

This package installs the independent PostgreSQL, Server, Controller, and Agent Worker workloads described in
`EINO-RESEARCH.md`. OpenSandbox is an explicit prerequisite: install its tested native Kubernetes workload provider
in the `opensandbox` namespace before applying this package. Breakfix creates and deletes its own BYO workspace PVCs;
OpenSandbox only mounts those PVCs and must not be granted their lifecycle ownership.

Build and publish the three runtime images, then replace their development tags in `kustomization.yaml` (or an
environment overlay):

```bash
docker build -t ghcr.io/breakfix/breakfix-server:dev -f deploy/images/server/Dockerfile .
docker build -t ghcr.io/breakfix/breakfix-controller:dev -f deploy/images/controller/Dockerfile .
docker build -t ghcr.io/breakfix/breakfix-agent-worker:dev -f deploy/images/agent-worker/Dockerfile .
```

The Controller image pins the bundled vcluster CLI to `v0.35.1`. The runtime Secret also supplies `registry_addr`
and `registry_insecure`, so the tracked configuration never names a deployment registry.

Create the control-plane namespace and runtime Secret before applying this package. Keep the copied environment file outside the repository:

```bash
cp deploy/runtime/runtime-secret.example.env /secure/path/breakfix-runtime.env
# Edit /secure/path/breakfix-runtime.env with real values.
kubectl apply -f deploy/runtime/namespace.yaml
kubectl -n breakfix-system create secret generic breakfix-runtime \
  --from-env-file=/secure/path/breakfix-runtime.env
```

The repository root is the canonical Kustomize package. It keeps the in-cluster config in `config/` while using
Kustomize's default file-loading restrictions. Apply it after the Secret exists:

```bash
kubectl apply -k .
```

The Server owns the RWO `breakfix-server-data` PVC and therefore uses `Recreate`. PostgreSQL owns a separate PVC.
The Server alone receives the OpenSandbox lifecycle key and has PVC permission for the `opensandbox` namespace. The
Agent Worker has no Kubernetes RBAC, no provider key, and disables ServiceAccount token mounting.
