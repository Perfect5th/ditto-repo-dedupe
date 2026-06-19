package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/canonical/ditto-repo/repo"

	"github.com/perfect5th/ditto-repo-dedupe/internal/adapter/filesystem"
)

func main() {
	source := flag.String("source", "", "URL of the source repository to mirror")
	dist := flag.String("dist", "", "distribution to mirror (e.g. jammy)")
	arch := flag.String("arch", "", "architecture to mirror (e.g. amd64)")
	destination := flag.String("destination", "", "directory where the mirrored repository will be stored")
	store := flag.String("store", "", "directory for the shared content-addressable store (defaults to a 'content-addressable' directory alongside the destination, so sibling mirrors share it)")
	flag.Parse()

	missing := []string{}
	if *source == "" {
		missing = append(missing, "--source")
	}
	if *dist == "" {
		missing = append(missing, "--dist")
	}
	if *arch == "" {
		missing = append(missing, "--arch")
	}
	if *destination == "" {
		missing = append(missing, "--destination")
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing required flag(s): %v\n", missing)
		flag.Usage()
		os.Exit(2)
	}

	// The content-addressable store lives outside the mirror destination so that
	// multiple mirrors can share it. By default it sits next to the destination,
	// one level above the mirror root.
	storeDir := *store
	if storeDir == "" {
		storeDir = filepath.Join(filepath.Dir(filepath.Clean(*destination)), "content-addressable")
	}

	// Drive ditto-repo with a content-addressable FileSystem so that file
	// contents are deduplicated into a single shared store and the dists/ and
	// pool/ trees reference them via symlinks. The matching Downloader skips
	// re-fetching content that is already present in the store.
	fsAdapter := filesystem.New(storeDir)
	ditto := repo.NewDittoRepo(repo.DittoConfig{
		RepoURLs:     []string{*source},
		Dists:        []string{*dist},
		Components:   []string{"main"},
		Archs:        []string{*arch},
		DownloadPath: *destination,
		FileSystem:   fsAdapter,
		Downloader:   fsAdapter.Downloader(repo.NewHTTPDownloader(fsAdapter)),
	})

	for update := range ditto.Mirror(context.Background()) {
		if update.CurrentFile != "" {
			fmt.Printf("[%d/%d] %s\n", update.PackagesDownloaded, update.TotalPackages, update.CurrentFile)
		}
	}
}
