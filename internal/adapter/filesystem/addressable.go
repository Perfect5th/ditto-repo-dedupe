// Package filesystem provides a content-addressable implementation of
// ditto-repo's repo.FileSystem interface.
//
// ditto-repo writes every file by creating a temporary "<dest>.tmp" file and
// then atomically renaming it into place. This adapter hooks that workflow: on
// Rename it hashes the staged bytes, moves them into a content-addressable
// store keyed by their SHA-256, and replaces the destination with a symlink
// into that store. Files with identical content therefore share a single
// backing object, deduplicating the mirror.
package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/canonical/ditto-repo/repo"
)

// AddressableFileSystem is a repo.FileSystem that stores file contents in a
// content-addressable store and exposes them through symlinks.
type AddressableFileSystem struct {
	// storeDir is the path of the content-addressable store. It lives outside of
	// any single mirror's directory so that multiple mirrors can share it.
	storeDir string

	// mu guards the check-and-move of objects into the store so that
	// concurrent download workers writing identical content cannot race.
	mu sync.Mutex
}

// Ensure the adapter satisfies the interface it is implementing.
var _ repo.FileSystem = (*AddressableFileSystem)(nil)

// New returns a content-addressable FileSystem backed by the store at storeDir.
// The store is independent of the mirror destination, so several mirrors can be
// pointed at the same storeDir to deduplicate content across all of them.
func New(storeDir string) *AddressableFileSystem {
	return &AddressableFileSystem{
		storeDir: storeDir,
	}
}

// Downloader returns a repo.Downloader that deduplicates downloads against the
// store. ditto-repo knows each package's SHA-256 from the Packages index and
// passes it as expectedSHA256; since the store is keyed by that same hash, a
// requested object that already exists can be linked into place instead of
// being downloaded again. Anything not already present (or whose hash is
// unknown, like metadata indices) is delegated to fallback.
func (a *AddressableFileSystem) Downloader(fallback repo.Downloader) repo.Downloader {
	return &addressableDownloader{fs: a, fallback: fallback}
}

// addressableDownloader is the repo.Downloader returned by
// AddressableFileSystem.Downloader.
type addressableDownloader struct {
	fs       *AddressableFileSystem
	fallback repo.Downloader
}

var _ repo.Downloader = (*addressableDownloader)(nil)

// DownloadFile links destPath to an existing store object when one matching
// expectedSHA256 is already present, avoiding the network entirely. Otherwise
// it delegates to the fallback downloader, which writes through the
// content-addressable FileSystem and so interns the freshly downloaded bytes.
func (d *addressableDownloader) DownloadFile(urlStr, destPath, expectedSHA256 string) (string, error) {
	if expectedSHA256 != "" {
		storePath := d.fs.objectPath(expectedSHA256)
		if _, err := os.Stat(storePath); err == nil {
			if err := d.fs.linkInto(storePath, destPath); err != nil {
				return "", err
			}
			return expectedSHA256, nil
		}
	}
	return d.fallback.DownloadFile(urlStr, destPath, expectedSHA256)
}

// ReadFile reads the entire file at path, following symlinks into the store.
func (a *AddressableFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// Stat returns file info for path, following symlinks so that callers see the
// size and mode of the backing object in the store.
func (a *AddressableFileSystem) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

// Open opens path for reading, following symlinks into the store.
func (a *AddressableFileSystem) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// Create creates or truncates a file for writing. ditto-repo only ever creates
// temporary staging files (and by-hash copies) directly; the deduplication
// happens later in Rename, so Create just writes a real file to the given path.
func (a *AddressableFileSystem) Create(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

// MkdirAll creates a directory and all necessary parents.
func (a *AddressableFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// Remove deletes the file or empty directory at path. For a symlink this
// removes only the link, leaving its backing object in the store untouched
// (reclaiming unreferenced objects is left to a future garbage-collection pass).
func (a *AddressableFileSystem) Remove(path string) error {
	return os.Remove(path)
}

// Rename finalizes a download. ditto-repo calls it to move "<dest>.tmp" into
// place; this adapter instead hashes the staged file, stores its bytes in the
// content-addressable store (once per unique content), and leaves newPath as a
// symlink pointing into the store.
//
// If oldPath is not a regular file (e.g. a directory), it falls back to a plain
// rename so the adapter stays correct for any non-download use.
func (a *AddressableFileSystem) Rename(oldPath, newPath string) error {
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return os.Rename(oldPath, newPath)
	}

	hash, err := hashFile(oldPath)
	if err != nil {
		return err
	}

	storePath := a.objectPath(hash)

	if err := a.intern(oldPath, storePath); err != nil {
		return err
	}

	return a.linkInto(storePath, newPath)
}

// Link creates a reference at newPath to the content of oldPath. ditto-repo uses
// it for by-hash aliases. When oldPath is already a symlink into the store,
// newPath is created as a symlink to the same backing object so the alias keeps
// pointing at deduplicated content; otherwise a hard link is created.
func (a *AddressableFileSystem) Link(oldPath, newPath string) error {
	if target, ok := a.resolveStoreTarget(oldPath); ok {
		return a.linkInto(target, newPath)
	}
	return os.Link(oldPath, newPath)
}

// WalkDir traverses the directory tree rooted at root.
func (a *AddressableFileSystem) WalkDir(root string, walkFn func(path string, d fs.DirEntry, err error) error) error {
	return filepath.WalkDir(root, walkFn)
}

// intern moves the staged file at stagePath into the store at storePath. If an
// object already exists for this content the staged file is discarded, which is
// what achieves deduplication. The check-and-move is serialized to avoid races
// between concurrent download workers.
func (a *AddressableFileSystem) intern(stagePath, storePath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, err := os.Stat(storePath); err == nil {
		// Identical content already stored: drop the duplicate staged copy.
		return os.Remove(stagePath)
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(stagePath, storePath); err != nil {
		return fmt.Errorf("cannot intern object: %w", err)
	}
	return nil
}

// linkInto replaces linkPath with a relative symlink pointing at storePath.
func (a *AddressableFileSystem) linkInto(storePath, linkPath string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	// Remove any pre-existing file or stale link at the destination.
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	target, err := filepath.Rel(filepath.Dir(linkPath), storePath)
	if err != nil {
		// Fall back to an absolute target if a relative one cannot be computed.
		target = storePath
	}
	return os.Symlink(target, linkPath)
}

// resolveStoreTarget returns the absolute path of the store object referenced by
// path when path is a symlink pointing into the store, reporting false
// otherwise.
func (a *AddressableFileSystem) resolveStoreTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)

	rel, err := filepath.Rel(a.storeDir, target)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) >= 2 && rel[:2] == "..") {
		return "", false
	}
	return target, true
}

// objectPath returns the in-store path for content with the given hex hash. The
// store is sharded by the first two byte-pairs of the hash to keep directories
// small. The object is named purely by its content hash so that byte-identical
// files are stored exactly once regardless of the names under which they appear
// in the mirror.
func (a *AddressableFileSystem) objectPath(hash string) string {
	return filepath.Join(a.storeDir, hash[0:2], hash[2:4], hash[4:])
}

// hashFile returns the hex-encoded SHA-256 of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
