package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Source repositories used by the e2e tests. These two PPAs intentionally
// share many identical packages, which is what lets the dedup tests prove
// that identical content is stored only once.
const (
	betaRepo  = "https://ppa.launchpadcontent.net/landscape/self-hosted-beta/ubuntu/"
	dailyRepo = "https://ppa.launchpadcontent.net/landscape/self-hosted-daily/ubuntu/"

	// Distribution / architecture both PPAs serve and which the tests target.
	testDist = "jammy"
	testArch = "amd64"
)

// binPath is the absolute path to the compiled POC binary. It is populated by
// TestMain before any test runs.
var binPath string

// TestMain builds the POC command once for the whole package and exposes the
// resulting binary via binPath. The e2e tests hit the live PPAs over the
// network, so the entire suite is opt-in: it is skipped unless DITTO_E2E=1 and
// is also skipped under `go test -short`.
//
// Building still happens unconditionally so that compilation problems surface
// even when the network-dependent assertions are skipped.
func TestMain(m *testing.M) {
	// testing.Short() and other test flags are only valid after parsing.
	flag.Parse()

	if !e2eEnabled() {
		// Nothing to build or run; report success so the suite stays green
		// when network e2e is not requested. Individual tests also guard with
		// requireE2E so `go test -run` of a single test skips cleanly.
		os.Exit(m.Run())
	}

	bin, cleanup, err := buildBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: failed to build POC binary: %v\n", err)
		os.Exit(1)
	}
	binPath = bin

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// e2eEnabled reports whether the network-dependent e2e tests should run.
func e2eEnabled() bool {
	if testing.Short() {
		return false
	}
	return os.Getenv("DITTO_E2E") == "1"
}

// requireE2E skips the calling test unless the e2e suite is enabled.
func requireE2E(t *testing.T) {
	t.Helper()
	if !e2eEnabled() {
		t.Skip("set DITTO_E2E=1 (and do not pass -short) to run network e2e tests")
	}
	if binPath == "" {
		t.Fatal("binPath is empty: POC binary was not built")
	}
}

// buildBinary compiles ./cmd into a temporary directory and returns the path to
// the resulting executable along with a cleanup function.
func buildBinary() (bin string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "ditto-dedupe-bin-*")
	if err != nil {
		return "", nil, fmt.Errorf("cannot create temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	bin = filepath.Join(dir, "ditto-dedupe")

	// Resolve the module's cmd package relative to this test file.
	cmd := exec.Command("go", "build", "-o", bin, "../cmd")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cannot build ../cmd: %w", err)
	}

	return bin, cleanup, nil
}
