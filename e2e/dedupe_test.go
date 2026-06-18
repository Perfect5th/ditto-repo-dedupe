package e2e

import (
	"testing"
)

// TestDedupAcrossRepos proves that two PPAs which share many identical packages
// can be mirrored into separate destinations while sharing a single
// content-addressable store, and that doing so consumes less space than giving
// each mirror its own store.
//
// This exercises the cross-mirror sharing the store is designed for: the store
// lives above the mirror roots, so two independent --destination trees can
// point at one --store. We mirror each repo into its own store to measure their
// independent sizes, then mirror both into a shared store and assert the shared
// store is strictly smaller than the sum. The difference is exactly the bytes
// of content the two repos have in common.
func TestDedupAcrossRepos(t *testing.T) {
	requireE2E(t)

	betaStore := t.TempDir()
	dailyStore := t.TempDir()
	sharedStore := t.TempDir()

	// Independent mirrors, each with its own store.
	runMirror(t, betaRepo, testDist, testArch, t.TempDir(), betaStore)
	runMirror(t, dailyRepo, testDist, testArch, t.TempDir(), dailyStore)

	// Two separate mirror destinations sharing a single store.
	runMirror(t, betaRepo, testDist, testArch, t.TempDir(), sharedStore)
	runMirror(t, dailyRepo, testDist, testArch, t.TempDir(), sharedStore)

	betaEntries := inventoryCA(t, betaStore)
	dailyEntries := inventoryCA(t, dailyStore)
	sharedEntries := inventoryCA(t, sharedStore)

	for name, entries := range map[string]map[string]caEntry{
		"beta-only": betaEntries, "daily-only": dailyEntries, "shared": sharedEntries,
	} {
		if len(entries) == 0 {
			t.Fatalf("content-addressable store for %s is empty", name)
		}
		// Each store must hold exactly one object per unique content hash.
		for hash, group := range hashesByContent(entries) {
			if len(group) > 1 {
				t.Errorf("[%s] content hash %s stored %d times; deduplication failed", name, hash, len(group))
			}
		}
	}

	betaSize := totalSize(betaEntries)
	dailySize := totalSize(dailyEntries)
	sharedSize := totalSize(sharedEntries)

	// The shared store equals the union of the two repos' unique content, so it
	// must be smaller than the sum of the independent stores whenever the repos
	// share any content at all.
	if sharedSize >= betaSize+dailySize {
		t.Errorf("expected shared store (%d bytes) to be smaller than beta (%d) + daily (%d) = %d; the two repos appear to share no content",
			sharedSize, betaSize, dailySize, betaSize+dailySize)
	}

	// Sanity: the shared store must still be at least as large as either repo
	// alone, since it contains a superset of each repo's content.
	if sharedSize < betaSize || sharedSize < dailySize {
		t.Errorf("shared store (%d bytes) is smaller than an individual repo store (beta=%d, daily=%d); content is missing",
			sharedSize, betaSize, dailySize)
	}

	saved := (betaSize + dailySize) - sharedSize
	t.Logf("dedup saved %d bytes across the two repos (beta=%d, daily=%d, shared=%d)", saved, betaSize, dailySize, sharedSize)
}

// TestIdempotentRerun mirrors the same repo twice into one destination and
// asserts the content-addressable store does not grow on the second run:
// re-mirroring identical content must not create new CA entries.
func TestIdempotentRerun(t *testing.T) {
	requireE2E(t)

	dest := t.TempDir()
	store := t.TempDir()

	runMirror(t, betaRepo, testDist, testArch, dest, store)
	first := inventoryCA(t, store)

	runMirror(t, betaRepo, testDist, testArch, dest, store)
	second := inventoryCA(t, store)

	firstHashes := hashesByContent(first)
	secondHashes := hashesByContent(second)

	if len(secondHashes) != len(firstHashes) {
		t.Errorf("CA store changed across identical re-runs: %d unique hashes after first run, %d after second", len(firstHashes), len(secondHashes))
	}
	for hash := range firstHashes {
		if _, ok := secondHashes[hash]; !ok {
			t.Errorf("content hash %s present after first run but missing after re-run", hash)
		}
	}
}
