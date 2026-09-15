# Breakfix Kubernetes documentation mirror

This directory contains the reproducible build wrapper for the pinned
Kubernetes website snapshot. The upstream repository is fetched into
`.local/docs/upstream` and is never vendored into the Breakfix binary or Git
tree.

The initial snapshot is described by [`manifest.yaml`](manifest.yaml). The
wrapper uses the upstream Hugo configuration and dependencies. It renders the
fixed `en` source tree without rewriting generated HTML, CSS, JavaScript, or
links, then adds Breakfix build metadata at `build-info.json`. The metadata
binds the rendered page scope to its fixed upstream source identity.

From the repository root (Docker or Podman is the only Hugo/Node build
dependency):

```bash
make docs-sync
make docs-image
make docs-check
make docs-smoke
```

`docs-build` builds the upstream Dockerfile with Hugo `0.144.2` and runs the
production Hugo command inside that image. The container's `/tmp/public` is
bound directly to `docs-site/public`, so no generated site is copied through
the host and the host does not need Hugo, Node.js, npm, or upstream
`node_modules`. Set `DOCS_CONTAINER_ENGINE=podman` to use Podman, or
`DOCS_CONTAINER_IMAGE` to use an already-built compatible image.
Node.js and npm are used only inside the upstream Dockerfile image and are not
installed or checked on the host.
The base URL and embedded parent origin are fixed build settings in
`manifest.yaml`. They affect emitted links and embedding, but are not inputs to
`DocumentContext` identity. The documentation entry point is
`<docs-origin>/docs/`; generated build metadata is available at
`<docs-origin>/build-info.json`.
`docs-smoke` opens the rendered tree and matching pinned source checkout using
the production Reader contract, then checks the fixed Pod lifecycle page,
`pod-lifetime` heading, source evidence, include evidence, and build metadata.

After `docs-build`, build the small runtime image that serves
`docs-site/public` and deploy that image to the local machine or Kind.
The default image tag is `breakfix/kubernetes-docs:snapshot-ce98a43`; run it
with `docker run --rm -p 1313:8080 breakfix/kubernetes-docs:snapshot-ce98a43`.

Kubernetes documentation is redistributed under its applicable CC BY 4.0
terms. The mirror must retain upstream attribution and must not copy
third-party assets whose licensing has not been checked separately.
