# Breakfix Kubernetes documentation mirror

This directory is the reproducible build wrapper for the pinned Kubernetes
website snapshot — the **input side of the offline document library**. The
upstream repository is fetched into
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
make docs-build
make docs-check
make docs-project
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
`docs-project` consumes the rendered tree and writes the offline parsed
library to `documents/` (per-page markdown with page and anchor digests). The
Server reads only that library; nothing here is deployed or served at runtime.
The former nginx runtime image and the iframe context-injection script were
retired when the reader switched to parsed pages. The base URL is a fixed
build setting in `manifest.yaml`; it affects emitted links but is not an input
to `DocumentContext` identity.
`docs-smoke` opens the rendered tree and the generated library using the
production Reader contract, then checks the fixed Pod lifecycle page, its
`pod-lifetime` anchor slice, page and anchor digests, tree, and assets.

Kubernetes documentation is redistributed under its applicable CC BY 4.0
terms. The mirror must retain upstream attribution and must not copy
third-party assets whose licensing has not been checked separately.
