# Kind Runtime Overlay

This development-only overlay installs the bundled Registry as a fixed
`NodePort` on TCP `30443`. It is not part of the production deployment.

Run `dev/kind-registry.sh` (or `make dev-kind-registry`) before applying the
runtime. It chooses the Kind control-plane Docker-network IP as the image
authority, issues a local development leaf certificate with both that IP and
`breakfix-registry.breakfix-system.svc.cluster.local` as SANs, updates the
runtime and pull Secrets, and installs only the CA in each Kind node's system
trust store. Immutable image references use `<kind-node-ip>:30443/breakfix`;
in-cluster control-plane clients use the Registry Service DNS name. No custom
DNS record, CoreDNS rule, or `/etc/hosts` entry is required.

The generated CA and key live under `.local/kind-registry/`, which is ignored by
Git and belongs only to the disposable local Kind cluster.

Production uses the root deployment package and an operator-provided HTTPS OCI
Registry instead of this overlay.
