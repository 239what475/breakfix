# Breakfix Kubernetes documentation mirror

This directory contains the reproducible build wrapper for the pinned
Kubernetes website snapshot. The upstream repository is fetched into
`.local/docs/upstream` and is never vendored into the Breakfix binary or Git
tree.

The initial snapshot is described by [`manifest.yaml`](manifest.yaml). The
wrapper uses the upstream Hugo configuration and dependencies. It renders the
complete upstream site without rewriting generated HTML, CSS, JavaScript, or
links, then adds Breakfix build metadata at `build-info.json`.

From the repository root (Docker or Podman is the only Hugo/Node build
dependency):

```bash
make docs-sync
DOCS_BASE_URL=http://localhost:1313/ make docs-build
make docs-package
make docs-check
```

`docs-build` builds the upstream Dockerfile with Hugo `0.144.2` and runs the
production Hugo command inside that image. The container's `/tmp/public` is
bound directly to `.local/docs/public`, so no generated site is copied through
the host and the host does not need Hugo, Node.js, npm, or upstream
`node_modules`. Set `DOCS_CONTAINER_ENGINE=podman` to use Podman, or
`DOCS_CONTAINER_IMAGE` to use an already-built compatible image.
Node.js and npm are used only inside the upstream Dockerfile image and are not
installed or checked on the host.
`DOCS_BASE_URL` must end in `/` and should be the docs origin used for
deployment. The documentation entry point is `<docs-origin>/docs/`; generated
build metadata is available at `<docs-origin>/build-info.json`.

`docs-package` creates a reproducible tar archive in `.local/docs/packages/`.
Deploy that archive's contents to the independent documentation origin.

Kubernetes documentation is redistributed under its applicable CC BY 4.0
terms. The mirror must retain upstream attribution and must not copy
third-party assets whose licensing has not been checked separately.
