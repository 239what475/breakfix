# Kind Runtime Overlay

This development-only overlay installs the bundled Registry as a fixed
`NodePort` on TCP `30443`. It is not part of the production deployment.

Run `make deploy-kind` after creating the runtime and Registry authentication
Secrets. The command applies the root package first, then this additive overlay.
It chooses the Kind control-plane Docker-network IP as the one Registry
authority, issues a local development leaf certificate with that IP as its only
SAN, updates the runtime and pull Secrets, and installs only the CA in each
Kind node's system trust store. Immutable image references and in-cluster
control-plane clients all use `<kind-node-ip>:30443/breakfix`. No custom DNS
record, CoreDNS rule, or `/etc/hosts` entry is required.

The generated CA and key live under `.local/kind-registry/`, which is ignored by
Git and belongs only to the disposable local Kind cluster.

Production uses the root deployment package and an operator-provided HTTPS OCI
Registry instead of this overlay.
