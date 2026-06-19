package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// countingProxy is a reverse proxy in front of a real upstream repository that
// counts the number of bytes served for package (.deb) requests. It lets the
// download-efficiency test observe how much package data actually crosses the
// network without giving up real-network behaviour: bodies are still fetched
// from the upstream PPA.
type countingProxy struct {
	server   *httptest.Server
	upstream string       // upstream base URL, ending in "/"
	debBytes atomic.Int64 // bytes served for .deb requests
}

func newCountingProxy(t *testing.T, upstream string) *countingProxy {
	t.Helper()

	p := &countingProxy{upstream: upstream}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)
	return p
}

// URL returns the proxy base URL to pass as --source.
func (p *countingProxy) handle(w http.ResponseWriter, r *http.Request) {
	target := p.upstream + strings.TrimPrefix(r.URL.Path, "/")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	resp, err := http.Get(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	if resp.StatusCode == http.StatusOK && strings.HasSuffix(r.URL.Path, ".deb") {
		p.debBytes.Add(n)
	}
}

// resetDebBytes zeroes the package byte counter and returns the previous value.
func (p *countingProxy) resetDebBytes() int64 {
	return p.debBytes.Swap(0)
}

// TestDownloadEfficiency verifies the README's "Download efficiency" claim: when
// content with a given hash is already in the store, the tool links to it
// instead of downloading it again.
//
// The same repository is mirrored twice into two different destinations that
// share one store, both via a counting proxy. The first mirror must download
// package data; the second must download none, because every package's content
// is already present in the shared store and is linked into the new
// destination's pool without touching the network.
func TestDownloadEfficiency(t *testing.T) {
	requireE2E(t)

	proxy := newCountingProxy(t, betaRepo)
	store := t.TempDir()

	// First mirror: a cold store, so package data must be fetched.
	runMirror(t, proxy.server.URL, testDist, testArch, t.TempDir(), store)
	firstBytes := proxy.resetDebBytes()
	if firstBytes == 0 {
		t.Fatal("expected the first mirror to download package data, but none was served")
	}

	// Second mirror of the same repo into a fresh destination sharing the store:
	// every package is already in the store, so no package bytes should be
	// downloaded.
	runMirror(t, proxy.server.URL, testDist, testArch, t.TempDir(), store)
	secondBytes := proxy.resetDebBytes()

	t.Logf("package bytes downloaded: first=%d, second=%d", firstBytes, secondBytes)
	if secondBytes != 0 {
		t.Errorf("expected no package downloads on the second mirror (content already in store), but %d bytes were served", secondBytes)
	}
}
