package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// On-disk layout produced by the POC, as described in the README.
const (
	distsDir = "dists"
	poolDir  = "pool"

	// mirrorTimeout bounds a single CLI invocation. Mirroring real PPAs over
	// the network can take a while, so this is generous.
	mirrorTimeout = 15 * time.Minute
)

// runMirror executes the POC binary to mirror a single source repository into
// dest, deduplicating file contents into the shared store at storeDir. It
// streams the command's output to the test log and fails the test if the
// command does not exit successfully.
func runMirror(t *testing.T, source, dist, arch, dest, storeDir string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), mirrorTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath,
		"--source", source,
		"--dist", dist,
		"--arch", arch,
		"--destination", dest,
		"--store", storeDir,
	)

	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Logf("mirror output (source=%s):\n%s", source, out)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("mirror timed out after %s (source=%s)", mirrorTimeout, source)
	}
	if err != nil {
		t.Fatalf("mirror failed (source=%s): %v", source, err)
	}
}

// caEntry describes one file in the content-addressable store.
type caEntry struct {
	path string // absolute path of the CA file
	size int64
	hash string // sha256 of the file contents
}

// inventoryCA walks the content-addressable store at storeDir and returns every
// file it finds, keyed by absolute path. It fails the test if the store is
// missing entirely, since a working mirror must populate it.
func inventoryCA(t *testing.T, storeDir string) map[string]caEntry {
	t.Helper()

	info, err := os.Stat(storeDir)
	if err != nil {
		t.Fatalf("content-addressable store not found at %s: %v", storeDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("content-addressable path %s is not a directory", storeDir)
	}

	entries := make(map[string]caEntry)
	err = filepath.WalkDir(storeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		entries[path] = caEntry{
			path: path,
			size: fi.Size(),
			hash: hashFile(t, path),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking content-addressable store: %v", err)
	}
	return entries
}

// hashesByContent groups CA entries by their content hash. A correctly
// deduplicating store has exactly one entry per hash.
func hashesByContent(entries map[string]caEntry) map[string][]caEntry {
	byHash := make(map[string][]caEntry)
	for _, e := range entries {
		byHash[e.hash] = append(byHash[e.hash], e)
	}
	return byHash
}

// totalSize returns the combined byte size of every object in a CA inventory.
func totalSize(entries map[string]caEntry) int64 {
	var total int64
	for _, e := range entries {
		total += e.size
	}
	return total
}

// listFilesWithSuffix returns the absolute paths of every regular file (or
// symlink) under root whose name ends with suffix. Symlinks are reported by
// their link path, not their target.
func listFilesWithSuffix(t *testing.T, root, suffix string) []string {
	t.Helper()

	var matches []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), suffix) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return matches
}

// resolveToCA verifies that path (a file under dists/ or pool/) is backed by a
// file inside the content-addressable store at storeDir and returns the
// absolute path of that backing CA file. It accepts either dedup mechanism:
//
//   - symlink: the link target, resolved to an absolute path, lies under
//     storeDir.
//   - hardlink: the file shares an inode with exactly one CA entry.
//
// The test fails if path is a standalone copy (its bytes live outside the CA
// store), since that would defeat deduplication.
func resolveToCA(t *testing.T, storeDir, path string, caEntries map[string]caEntry) string {
	t.Helper()

	caRoot := storeDir

	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}

	// Case 1: symlink. Resolve the target and ensure it points into the store.
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatalf("readlink %s: %v", path, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		target = filepath.Clean(target)

		rel, err := filepath.Rel(caRoot, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("symlink %s resolves to %s, which is outside the content-addressable store %s", path, target, caRoot)
		}
		if _, ok := caEntries[target]; !ok {
			t.Fatalf("symlink %s targets %s, which is not a known CA entry", path, target)
		}
		return target
	}

	// Case 2: hardlink (or, undesirably, a standalone regular file). Match by
	// inode against the CA inventory.
	ino, ok := inodeOf(fi)
	if !ok {
		t.Fatalf("cannot determine inode for %s on this platform", path)
	}
	for _, e := range caEntries {
		efi, err := os.Stat(e.path)
		if err != nil {
			t.Fatalf("stat CA entry %s: %v", e.path, err)
		}
		eino, ok := inodeOf(efi)
		if ok && eino == ino {
			return e.path
		}
	}

	t.Fatalf("file %s is not deduplicated: its bytes do not live in the content-addressable store (not a symlink into %s and no CA entry shares its inode)", path, caRoot)
	return ""
}

// hashFile returns the hex-encoded sha256 of the file's contents, following
// symlinks.
func hashFile(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hashing %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
