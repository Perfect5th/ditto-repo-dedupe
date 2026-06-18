package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMirrorSingleRepo mirrors a single PPA and asserts the resulting tree has
// the structure documented in the README: a dists/ metadata tree, a populated
// pool/, and a content-addressable store that holds the actual file bytes while
// pool/ entries merely link into it.
func TestMirrorSingleRepo(t *testing.T) {
	requireE2E(t)

	dest := t.TempDir()
	// The content-addressable store lives outside the mirror destination so
	// that multiple mirrors can share it.
	store := t.TempDir()
	runMirror(t, betaRepo, testDist, testArch, dest, store)

	// dists/<dist> must contain the Release file and at least one Packages index.
	distRoot := filepath.Join(dest, distsDir, testDist)
	if _, err := os.Stat(filepath.Join(distRoot, "Release")); err != nil {
		t.Errorf("expected Release file under %s: %v", distRoot, err)
	}
	if pkgs := listFilesWithSuffix(t, distRoot, "Packages"); len(pkgs) == 0 {
		// Packages may be compressed (Packages.gz); accept either.
		if gz := listFilesWithSuffix(t, distRoot, "Packages.gz"); len(gz) == 0 {
			t.Errorf("expected at least one Packages index under %s", distRoot)
		}
	}

	// pool/ must contain at least one .deb package.
	poolRoot := filepath.Join(dest, poolDir)
	debs := listFilesWithSuffix(t, poolRoot, ".deb")
	if len(debs) == 0 {
		t.Fatalf("expected at least one .deb under %s", poolRoot)
	}

	// The mirror destination itself must NOT contain the store: the deduped
	// objects live above the mirror root so they can be shared.
	if _, err := os.Stat(filepath.Join(dest, "content-addressable")); err == nil {
		t.Errorf("content-addressable store must not live inside the mirror destination %s", dest)
	}

	// The content-addressable store must be populated.
	caEntries := inventoryCA(t, store)
	if len(caEntries) == 0 {
		t.Fatal("content-addressable store is empty after mirroring")
	}

	// Every pool .deb must be backed by a file inside the CA store: its bytes
	// must not exist as a standalone copy under pool/.
	for _, deb := range debs {
		backing := resolveToCA(t, store, deb, caEntries)
		t.Logf("%s -> %s", deb, backing)
	}

	// The CA store itself must not contain duplicate content: each content hash
	// appears exactly once even within a single repo.
	for hash, group := range hashesByContent(caEntries) {
		if len(group) > 1 {
			t.Errorf("content hash %s is stored %d times in the CA store; expected exactly one", hash, len(group))
		}
	}
}
