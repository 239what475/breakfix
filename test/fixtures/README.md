# Test Fixtures

`catalog-release/` is a complete portable Catalog Release source containing a
Node runtime smoke fixture. Browser E2E installs its immutable OCI bundle
through the administrator API when the target platform has no catalog.

`candidates/` contains standalone candidate directories used by content
validation, build and Incus integration tests. They deliberately have no
platform publication fields and are not copied into Server data directories.
