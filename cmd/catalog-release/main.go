// catalog-release packages a portable Catalog Release source as an OCI
// artifact and can publish it to an operator-provided Registry.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	bootstrapcatalog "github.com/breakfix/breakfix/internal/bootstrap/catalogrelease"
)

func main() {
	source := flag.String("source", "catalog", "portable CatalogRelease source directory")
	output := flag.String("output", "", "destination OCI archive path")
	reference := flag.String("reference", "", "optional mutable OCI reference to publish, for example registry.example/catalog/foundation:2026.08.01")
	trustBundle := flag.String("trust-bundle-file", "", "optional PEM bundle trusted for the Registry")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	digest, err := bootstrapcatalog.Run(ctx, bootstrapcatalog.Options{
		Source: *source, Output: *output, Reference: *reference, TrustBundleFile: *trustBundle,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog release:", err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
