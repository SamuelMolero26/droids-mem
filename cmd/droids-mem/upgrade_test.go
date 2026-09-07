package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serve returns a test server answering every request with status/body.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchChecksum(t *testing.T) {
	// sha256sum sidecar format: "<hex>  <filename>\n".
	srv := serve(t, http.StatusOK, "deadbeef  droids-mem-v1.2.1-darwin-arm64\n")

	got, err := fetchChecksum(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchChecksum: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("fetchChecksum = %q, want deadbeef", got)
	}
}

func TestFetchChecksum_NonOKStatus(t *testing.T) {
	srv := serve(t, http.StatusNotFound, "nope")

	if _, err := fetchChecksum(context.Background(), srv.URL); err == nil {
		t.Fatal("fetchChecksum on 404 should error")
	}
}

func TestDownloadToTemp_WritesAndHashes(t *testing.T) {
	payload := "fake droids-mem binary bytes"
	srv := serve(t, http.StatusOK, payload)
	dir := t.TempDir()

	path, sum, err := downloadToTemp(context.Background(), srv.URL, dir, maxAssetBytes, nil)
	if err != nil {
		t.Fatalf("downloadToTemp: %v", err)
	}

	want := sha256.Sum256([]byte(payload))
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path returned by the function under test
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if string(b) != payload {
		t.Errorf("temp contents = %q, want %q", b, payload)
	}
	if !strings.HasPrefix(path, dir) {
		t.Errorf("temp %q not created inside %q (rename onto the binary must stay same-filesystem)", path, dir)
	}
}

func TestDownloadToTemp_RejectsOversizedAsset(t *testing.T) {
	srv := serve(t, http.StatusOK, strings.Repeat("x", 128))
	dir := t.TempDir()

	// The checksum only rejects bad bytes AFTER they land, so the cap is what
	// bounds disk use against a malfunctioning or hostile server.
	if _, _, err := downloadToTemp(context.Background(), srv.URL, dir, 32, nil); err == nil {
		t.Fatal("downloadToTemp should reject an asset over the cap")
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("oversized download left %d temp file(s) behind, want 0", len(left))
	}
}

func TestDownloadToTemp_NonOKStatus(t *testing.T) {
	srv := serve(t, http.StatusInternalServerError, "boom")
	dir := t.TempDir()

	if _, _, err := downloadToTemp(context.Background(), srv.URL, dir, maxAssetBytes, nil); err == nil {
		t.Fatal("downloadToTemp on 500 should error")
	}
}
