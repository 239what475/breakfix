# Breakfix Kubernetes documentation mirror

This directory contains the reproducible build wrapper for the pinned
Kubernetes website snapshot. The upstream repository is fetched into
`.local/docs/upstream` and is never vendored into the Breakfix binary or Git
tree.

The initial snapshot is described by [`manifest.yaml`](manifest.yaml). The
wrapper uses the upstream Hugo configuration, dependencies, and official
`make production-build` target. It copies the complete upstream `public/`
tree without rewriting generated HTML, CSS, JavaScript, or links, then adds
Breakfix build metadata at `build-info.json`.

From the repository root:

```bash
make docs-sync
DOCS_BASE_URL=http://localhost:1313/ make docs-build
make docs-check
make docs-serve
```

`docs-build` requires the extended Hugo `0.144.2` binary and Node.js `20.17.0`
with npm for the upstream asset pipeline. The script checks both tool
versions, installs the locked npm dependencies when needed, invokes the
upstream production build, and serves the resulting site from its original
root layout. `DOCS_BASE_URL` must end in `/` and should be the docs origin used
for deployment. The documentation entry point is `<docs-origin>/docs/`;
generated build metadata is available at `<docs-origin>/build-info.json`.

Kubernetes documentation is redistributed under its applicable CC BY 4.0
terms. The mirror must retain upstream attribution and must not copy
third-party assets whose licensing has not been checked separately.
