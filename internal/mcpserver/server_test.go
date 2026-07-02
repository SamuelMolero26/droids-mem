package mcpserver

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is the downstream handler under the middleware — a 200 sentinel so
// tests can tell "middleware let the request through" from "middleware blocked".
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

func TestBearerAuth(t *testing.T) {
	const token = "secrettoken0" // 12 bytes; same-length wrong value below exercises the ConstantTimeCompare branch, not the length guard
	const endpoint = "/mcp"

	cases := []struct {
		name       string
		path       string
		authHeader string
		wantStatus int
	}{
		{"correct token", endpoint, "Bearer " + token, http.StatusOK},
		{"wrong token same length", endpoint, "Bearer secrettoken1", http.StatusUnauthorized},
		{"wrong token diff length", endpoint, "Bearer nope", http.StatusUnauthorized},
		{"missing header", endpoint, "", http.StatusUnauthorized},
		{"missing Bearer prefix", endpoint, token, http.StatusUnauthorized},
		{"lowercase bearer prefix", endpoint, "bearer " + token, http.StatusUnauthorized},
		{"leading space", endpoint, " Bearer " + token, http.StatusUnauthorized},
		{"non-protected path bypasses auth", "/healthz", "", http.StatusOK},
		{"non-protected path bypasses even with bad auth", "/healthz", "Bearer garbage", http.StatusOK},
	}

	h := bearerAuth(token, endpoint, okHandler)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Errorf("401 response missing WWW-Authenticate challenge header")
				}
			}
		})
	}
}

func TestIdentityProof(t *testing.T) {
	const token = "tok-abc"

	// Independently recomputed HMAC — locks the construction so client and
	// server (ensure-server) can never drift on it.
	want := func(nonce string) string {
		mac := hmac.New(sha256.New, []byte(token))
		mac.Write([]byte(nonce))
		return hex.EncodeToString(mac.Sum(nil))
	}

	if got := IdentityProof(token, "nonce-1"); got != want("nonce-1") {
		t.Fatalf("proof = %q, want %q", got, want("nonce-1"))
	}
	// A fresh nonce must yield a fresh proof (replay of old proofs is useless).
	if IdentityProof(token, "nonce-1") == IdentityProof(token, "nonce-2") {
		t.Fatal("different nonces produced identical proofs")
	}
	// A different token must yield a different proof for the same nonce.
	if IdentityProof(token, "nonce-1") == IdentityProof("other-tok", "nonce-1") {
		t.Fatal("different tokens produced identical proofs")
	}
}

func TestIdentityHandler(t *testing.T) {
	const token = "tok-xyz"
	h := identityHandler(token)

	t.Run("empty nonce is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/identity", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("over-long nonce is rejected", func(t *testing.T) {
		long := strings.Repeat("a", maxIdentityNonceLen+1)
		req := httptest.NewRequest(http.MethodGet, "/identity?nonce="+long, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("valid nonce returns the proof", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/identity?nonce=n1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, IdentityProof(token, "n1")) {
			t.Errorf("body %q missing expected proof", body)
		}
		if !strings.Contains(body, ServerName) {
			t.Errorf("body %q missing server name", body)
		}
	})
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.0.0.2", true}, // whole 127/8 loopback block
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"10.0.0.1", false},
		{"example.com", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestLimitBody(t *testing.T) {
	// Downstream reads the body; MaxBytesReader surfaces the cap as a read error.
	reader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := limitBody(reader)

	t.Run("body under cap passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(make([]byte, 1024)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("body over cap is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(make([]byte, maxRequestBody+1)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})
}
