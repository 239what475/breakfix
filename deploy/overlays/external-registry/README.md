# External OCI Registry Overlay

Apply this overlay instead of the repository root when an operator provides
Harbor, a cloud Registry, or another HTTPS OCI Registry:

```bash
kubectl apply -k deploy/overlays/external-registry
```

If this replaces a previously applied managed Registry, explicitly remove its
stateful resources once the external endpoint is ready:

```bash
kubectl delete -k deploy/registry-managed
```

Set `registry_addr`, optional `registry_pull_secret`, optional
`registry_trust_bundle_file`, and Registry credentials in `breakfix-runtime`.
The external Registry endpoint must be reachable by every Kubernetes node. If
it uses an internal CA, install that CA on every node and create the optional
`breakfix-registry-ca` ConfigMap for Server and Publisher as documented in the
runtime deployment guide.
