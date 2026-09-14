package mcpserver

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	h := identityHandler(token, "v9.9.9")

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

	// The stop path signals the PID this answer reports, so every field it
	// decides on is asserted here rather than probed for a substring.
	t.Run("valid nonce answers with the full identity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/identity?nonce=n1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		type identity struct {
			Server   string `json:"server"`
			Proof    string `json:"proof"`
			Version  string `json:"version"`
			Pid      int    `json:"pid"`
			PidProof string `json:"pid_proof"`
		}
		var got identity
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode body %q: %v", rec.Body.String(), err)
		}
		want := identity{ServerName, IdentityProof(token, "n1"), "v9.9.9",
			os.Getpid(), IdentityPidProof(token, "n1", os.Getpid())}
		if got != want {
			t.Errorf("identity = %+v, want %+v", got, want)
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

func TestInstructions_TransportFork(t *testing.T) {
	httpVar, stdioVar := instructions(false), instructions(true)

	// Both variants share the core protocol, graph tools, and secrets tail.
	for _, s := range []string{httpVar, stdioVar} {
		for _, want := range []string{"AT THE START of a task", "mem_save", "Never put secrets", "graph_symbol", "Available tools:"} {
			if !strings.Contains(s, want) {
				t.Errorf("instructions missing %q", want)
			}
		}
	}

	// Graph tools appear before the summary policy in both variants.
	for _, s := range []string{httpVar, stdioVar} {
		graphPos := strings.Index(s, "graph_symbol")
		httpPolicyPos := strings.Index(s, "Do NOT save session summaries")
		stdioPolicyPos := strings.Index(s, "AT THE END of a run")
		if graphPos < 0 {
			t.Errorf("graph tools not found in instructions")
		}
		if httpPolicyPos >= 0 && graphPos > httpPolicyPos {
			t.Errorf("graph_symbol (%d) appears after HTTP summary policy (%d)", graphPos, httpPolicyPos)
		}
		if stdioPolicyPos >= 0 && graphPos > stdioPolicyPos {
			t.Errorf("graph_symbol (%d) appears after stdio summary policy (%d)", graphPos, stdioPolicyPos)
		}
	}

	// Only the session-summary sentence forks.
	if !strings.Contains(httpVar, "Do NOT save session summaries") {
		t.Errorf("HTTP variant lost the no-self-save policy")
	}
	if strings.Contains(httpVar, "AT THE END of a run") {
		t.Errorf("HTTP variant carries the stdio self-save policy")
	}
	if !strings.Contains(stdioVar, "AT THE END of a run") {
		t.Errorf("stdio variant missing the self-save policy")
	}
	if strings.Contains(stdioVar, "Do NOT save session summaries") {
		t.Errorf("stdio variant carries the HTTP no-self-save policy")
	}
}

func TestIdentityPidProof(t *testing.T) {
	const token = "tok-abc"

	// Independently recomputed HMAC — locks the construction so the stop path
	// and the server can never drift on it.
	want := func(nonce string, pid int) string {
		kmac := hmac.New(sha256.New, []byte(token))
		kmac.Write([]byte("droids-mem/pid_proof"))
		mac := hmac.New(sha256.New, kmac.Sum(nil))
		mac.Write([]byte(nonce + ":" + strconv.Itoa(pid)))
		return hex.EncodeToString(mac.Sum(nil))
	}

	if got := IdentityPidProof(token, "n1", 42); got != want("n1", 42) {
		t.Fatalf("pid proof = %q, want %q", got, want("n1", 42))
	}
	// Must not collide with the PID-less proof, or a caller could accept one
	// where it required the other.
	if IdentityPidProof(token, "n1", 42) == IdentityProof(token, "n1") {
		t.Fatal("pid proof collided with the plain proof")
	}
	// A relay can ask any server for the plain proof of a crafted nonce. If
	// that equals a pid proof, the relay forges a PID of its choosing.
	if IdentityPidProof(token, "n1", 42) == IdentityProof(token, "n1:42") {
		t.Fatal("pid proof is obtainable as the plain proof of nonce+\":\"+pid")
	}
}
