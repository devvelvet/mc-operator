// Package download implements an on-disk cache for URL-sourced plugin jars.
// The cache is keyed by URL basename plus (when provided) a SHA-256 integrity
// check. Entries are reused across reconcile cycles so a manifest that
// references the same plugin URL many times never downloads twice.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
)

// Cache stores downloaded artifacts in Dir.
type Cache struct {
	Dir    string
	Client *http.Client
}

// New ensures dir exists and returns a Cache.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache: %w", err)
	}
	return &Cache{Dir: dir, Client: http.DefaultClient}, nil
}

// Fetch downloads rawURL into the cache (if not already present with a matching
// hash) and returns the on-disk path. When expectedSHA256 is non-empty it is
// verified; a mismatch causes the cached file to be discarded and re-downloaded.
func (c *Cache) Fetch(ctx context.Context, rawURL, expectedSHA256 string) (string, error) {
	// Use net/url + path.Base (forward-slash semantics) so URL parsing is
	// consistent across platforms — filepath.Base is OS-aware and would
	// behave differently on Windows.
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	name := path.Base(u.Path)
	if name == "" || name == "/" || name == "." {
		return "", fmt.Errorf("cannot derive filename from url %q", rawURL)
	}
	dst := filepath.Join(c.Dir, name)

	if _, err := os.Stat(dst); err == nil {
		if expectedSHA256 == "" {
			return dst, nil
		}
		ok, _ := verifyHash(dst, expectedSHA256)
		if ok {
			return dst, nil
		}
		_ = os.Remove(dst)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", rawURL, resp.Status)
	}

	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	if expectedSHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != expectedSHA256 {
			_ = os.Remove(dst)
			return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", rawURL, got, expectedSHA256)
		}
	}
	return dst, nil
}

func verifyHash(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}
