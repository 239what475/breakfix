# Test Fixtures

`catalog-release/` is a complete portable Catalog Release source containing a
Node runtime smoke fixture. Browser E2E uses a Server that was started with
the fixture's immutable OCI digest in `catalog.release_reference`; `make
e2e-prepare` owns that installation and Playwright global setup only checks the
explicitly supplied Server connection.

`candidates/` contains standalone candidate directories used by content
validation, build and Incus integration tests. They deliberately have no
platform publication fields and are not copied into Server data directories.
