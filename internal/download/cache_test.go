package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// helper: create a Cache rooted in a temp dir.
func newTempCache(t *testing.T) *Cache {
	t.Helper()
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// helper: a one-shot test server that counts how many times it's been hit.
func newCountingServer(t *testing.T, payload []byte) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/java-archive")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchSucceedsAndCachesOnDisk(t *testing.T) {
	cache := newTempCache(t)
	payload := []byte("fake plugin contents")
	srv, hits := newCountingServer(t, payload)

	dst, err := cache.Fetch(context.Background(), srv.URL+"/luckperms.jar", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if filepath.Base(dst) != "luckperms.jar" {
		t.Errorf("expected basename luckperms.jar, got %s", filepath.Base(dst))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("cached content mismatch")
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("expected 1 server hit, got %d", *hits)
	}
}

func TestFetchHitsCacheOnSecondCall(t *testing.T) {
	cache := newTempCache(t)
	payload := []byte("plugin v1")
	srv, hits := newCountingServer(t, payload)
	url := srv.URL + "/plugin.jar"

	for i := 0; i < 3; i++ {
		if _, err := cache.Fetch(context.Background(), url, ""); err != nil {
			t.Fatalf("Fetch %d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("expected exactly 1 upstream hit across 3 fetches, got %d", got)
	}
}

func TestFetchVerifiesSHA256(t *testing.T) {
	cache := newTempCache(t)
	payload := []byte("integrity protected plugin")
	expected := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(expected[:])

	srv, _ := newCountingServer(t, payload)
	dst, err := cache.Fetch(context.Background(), srv.URL+"/strict.jar", expectedHex)
	if err != nil {
		t.Fatalf("Fetch with valid hash: %v", err)
	}
	if dst == "" {
		t.Errorf("expected dst path, got empty")
	}
}

func TestFetchRejectsBadSHA256AndDoesNotCache(t *testing.T) {
	cache := newTempCache(t)
	payload := []byte("plugin contents")
	srv, _ := newCountingServer(t, payload)
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	_, err := cache.Fetch(context.Background(), srv.URL+"/bad.jar", wrongHash)
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}

	// File should NOT remain in cache after a hash mismatch.
	entries, _ := os.ReadDir(cache.Dir)
	for _, e := range entries {
		if e.Name() == "bad.jar" {
			t.Errorf("cache must not retain a file that failed integrity check")
		}
	}
}

func TestFetchInvalidatesCacheOnHashMismatch(t *testing.T) {
	cache := newTempCache(t)
	payloadA := []byte("plugin v1")
	hashA := sha256.Sum256(payloadA)
	hashAHex := hex.EncodeToString(hashA[:])

	// Pre-populate cache with payload A.
	srv, hits := newCountingServer(t, payloadA)
	if _, err := cache.Fetch(context.Background(), srv.URL+"/p.jar", hashAHex); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("expected 1 hit after first fetch, got %d", *hits)
	}

	// Now request the same URL with a DIFFERENT expected hash → cache should
	// be invalidated and a re-fetch attempted (which will then fail because
	// the upstream still serves payload A).
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := cache.Fetch(context.Background(), srv.URL+"/p.jar", wrongHash)
	if err == nil {
		t.Fatal("expected error on hash mismatch")
	}
	if atomic.LoadInt32(hits) != 2 {
		t.Errorf("expected 2 upstream hits (invalidated cache forces refetch), got %d", *hits)
	}
}

func TestFetchHTTPErrorsAreReported(t *testing.T) {
	cache := newTempCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := cache.Fetch(context.Background(), srv.URL+"/missing.jar", "")
	if err == nil {
		t.Fatal("expected 404 error")
	}
	if !contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// Sanity: bare-URL filename derivation.
func TestFetchRejectsURLWithoutBasename(t *testing.T) {
	cache := newTempCache(t)
	_, err := cache.Fetch(context.Background(), "http://example.com/", "")
	if err == nil {
		t.Fatal("expected error for URL without basename")
	}
}

// Sanity: cache directory is created on demand.
func TestNewCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "subdir", "cache")
	c, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(c.Dir); err != nil {
		t.Errorf("cache dir not created: %v", err)
	}
}

// Sanity: just verifies fmt is imported correctly (the helpers above use fmt indirectly).
var _ = fmt.Sprintf
