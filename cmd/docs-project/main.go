// docs-project creates the offline, deterministic documentation library.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/docsproject"
)

func main() {
	config := docsproject.DefaultConfig()
	pages := flag.String("pages", "", "comma-separated site paths to project")
	flag.StringVar(&config.Root, "root", config.Root, "rendered site root")
	flag.StringVar(&config.Out, "out", config.Out, "document library output root")
	flag.IntVar(&config.Workers, "workers", config.Workers, "parallel page extraction workers")
	flag.StringVar(&config.Version, "version", "", "required generator version")
	flag.BoolVar(&config.Resume, "resume", false, "reuse matching page outputs")
	flag.StringVar(&config.SiteOrigin, "site-origin", "", "canonical site origin for links outside /docs/ (for example https://kubernetes.io)")
	flag.Parse()
	config.Pages = splitPages(*pages)

	if err := docsproject.Run(config); err != nil {
		fmt.Fprintln(os.Stderr, "docs-project:", err)
		os.Exit(docsproject.ExitCode(err))
	}
}

func splitPages(value string) []string {
	var pages []string
	for _, page := range strings.Split(value, ",") {
		if page = strings.TrimSpace(page); page != "" {
			pages = append(pages, page)
		}
	}
	return pages
}
