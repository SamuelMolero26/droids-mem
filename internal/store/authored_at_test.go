package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const day = int64(86400)

type sharedLine struct {
	Kind       string `json:"kind"`
	TaskType   string `json:"task_type"`
	Title      string `json:"title"`
	What       string `json:"what"`
	Learned    string `json:"learned"`
	Tags       string `json:"tags"`
	AuthoredAt int64  `json:"authored_at,omitempty"`
}

func jsonl(t *testing.T, lines ...sharedLine) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return b.String()
}

// readStamps reads authored_at/created_at/review_after by id — the stable key
// across a force-update, where title/learned are unchanged by construction
// (fingerprint match requires it).
func readStamps(t *testing.T, conn *sql.DB, id string) (authoredAt, createdAt int64, reviewAfter sql.NullInt64) {
	t.Helper()
	err := conn.QueryRow(
		`SELECT authored_at, created_at, review_after FROM memories WHERE id = ?`, id,
	).Scan(&authoredAt, &createdAt, &reviewAfter)
	if err != nil {
		t.Fatalf("read stamps for %q: %v", id, err)
	}
	return
}

// TestSave_AuthoredAtDefaultsToCreatedAt: a locally-authored memory is its own
// origin, so the two provenance stamps agree — there is no peer date to carry.
func TestSave_AuthoredAtDefaultsToCreatedAt(t *testing.T) {
	s, conn := newStoreWithDB(t)
	req := validReq()
	req.Title = "local lesson about batch sizing"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	authored, created, _ := readStamps(t, conn, resp.ID)
	if authored != created {
		t.Errorf("authored_at = %d, want created_at %d for a locally-authored memory", authored, created)
	}
}

// TestImport_PreservesAuthoredAt is the provenance core (D6/D7 of the design):
// import re-stamps created_at to now (so an old peer lesson never jumps the
// newest-first queue) while preserving authored_at (so the peer's real origin
// date is observable later). Before this, every pool pull erased the
// distinction between a lesson written today and one written three years ago.
func TestImport_PreservesAuthoredAt(t *testing.T) {
	s, conn := newStoreWithDB(t)
	now := time.Now().Unix()
	old := now - 200*day

	in := jsonl(t,
		sharedLine{
			Kind: "error_resolution", TaskType: "pool_tt",
			Title:   "ancient peer lesson about gateway timeouts",
			What:    "the upload kept timing out at the gateway boundary",
			Learned: "cap batch uploads at 200 rows to dodge the gateway timeout",
			Tags:    "gateway timeout", AuthoredAt: old,
		},
	)
	res, err := s.ImportShared(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ImportShared: %v", err)
	}
	if res.Imported != 1 {
		t.Fatalf("imported %d rows (failed=%d, skipped=%d), want 1", res.Imported, res.Failed, res.Skipped)
	}

	var id string
	if err := conn.QueryRow(
		`SELECT id FROM memories WHERE title = ?`, "ancient peer lesson about gateway timeouts",
	).Scan(&id); err != nil {
		t.Fatalf("find imported row: %v", err)
	}

	authored, created, _ := readStamps(t, conn, id)
	if authored != old {
		t.Errorf("authored_at = %d, want the peer's original %d — origin date must survive import", authored, old)
	}
	if created < now {
		t.Errorf("created_at = %d, want >= %d — import date is the recency key, not the peer's date", created, now)
	}
}

// TestImport_ClampsFutureAuthoredAt: authored_at crosses the pool trust
// boundary (D7). A skewed or hostile peer clock sending a future stamp is
// clamped to now, not rejected — losing a whole lesson to clock skew is worse
// than importing it with a conservative "authored now" stamp.
func TestImport_ClampsFutureAuthoredAt(t *testing.T) {
	s, conn := newStoreWithDB(t)
	now := time.Now().Unix()

	in := jsonl(t, sharedLine{
		Kind: "error_resolution", TaskType: "pool_tt",
		Title:   "peer lesson with a clock from the future",
		What:    "the exporting host had a badly skewed system clock",
		Learned: "never trust a timestamp that crossed a trust boundary unclamped",
		Tags:    "clock skew", AuthoredAt: now + 100*day,
	})
	res, err := s.ImportShared(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ImportShared: %v", err)
	}
	if res.Failed != 0 {
		t.Fatalf("failed = %d, want 0 — a future authored_at is clamped, never rejected", res.Failed)
	}
	if res.Imported != 1 {
		t.Fatalf("imported = %d, want 1", res.Imported)
	}

	var id string
	if err := conn.QueryRow(
		`SELECT id FROM memories WHERE title = ?`, "peer lesson with a clock from the future",
	).Scan(&id); err != nil {
		t.Fatalf("find imported row: %v", err)
	}
	authored, _, _ := readStamps(t, conn, id)
	if authored > now+5 {
		t.Errorf("authored_at = %d, want clamped to ~now (%d)", authored, now)
	}
}

// TestSave_NeverWritesReviewAfter pins the Option-2 invariant (spec: "review_after
// and needs_review stay inert"): no write path — plain save, force-save, or
// import — may ever populate review_after. There is no decay clock in this
// change; a regression here would silently reintroduce one.
func TestSave_NeverWritesReviewAfter(t *testing.T) {
	t.Run("plain save", func(t *testing.T) {
		s, conn := newStoreWithDB(t)
		resp, err := s.Save(context.Background(), validReq())
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		_, _, review := readStamps(t, conn, resp.ID)
		if review.Valid {
			t.Errorf("review_after = %d, want NULL", review.Int64)
		}
	})

	t.Run("force save", func(t *testing.T) {
		s, conn := newStoreWithDB(t)
		first, err := s.Save(context.Background(), validReq())
		if err != nil {
			t.Fatalf("initial Save: %v", err)
		}
		req := validReq()
		req.Force = true
		req.What = "HITL correction: field was phone_number but should have been phone"
		resp, err := s.Save(context.Background(), req)
		if err != nil {
			t.Fatalf("force Save: %v", err)
		}
		if resp.ID != first.ID {
			t.Fatalf("force save id = %q, want same row %q", resp.ID, first.ID)
		}
		_, _, review := readStamps(t, conn, resp.ID)
		if review.Valid {
			t.Errorf("review_after = %d, want NULL", review.Int64)
		}
	})

	t.Run("import", func(t *testing.T) {
		s, conn := newStoreWithDB(t)
		in := jsonl(t, sharedLine{
			Kind: "task_pattern", TaskType: "pool_tt",
			Title:   "imported pattern for retry backoff",
			What:    "retries without backoff hammered the gateway",
			Learned: "add exponential backoff to the retry loop",
			Tags:    "retry backoff",
		})
		res, err := s.ImportShared(context.Background(), strings.NewReader(in))
		if err != nil {
			t.Fatalf("ImportShared: %v", err)
		}
		if res.Imported != 1 {
			t.Fatalf("imported = %d, want 1", res.Imported)
		}
		var id string
		if err := conn.QueryRow(
			`SELECT id FROM memories WHERE title = ?`, "imported pattern for retry backoff",
		).Scan(&id); err != nil {
			t.Fatalf("find imported row: %v", err)
		}
		_, _, review := readStamps(t, conn, id)
		if review.Valid {
			t.Errorf("review_after = %d, want NULL", review.Int64)
		}
	})
}

// decodeShared reads an exported JSONL blob back into wire shapes.
func decodeShared(t *testing.T, out string) []sharedLine {
	t.Helper()
	var got []sharedLine
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var l sharedLine
		if err := dec.Decode(&l); err != nil {
			t.Fatalf("decode export line: %v", err)
		}
		got = append(got, l)
	}
	return got
}

// TestExport_CoarsensAuthoredAtToUTCDay pins the pool's anonymity budget. A
// locally-authored row's authored_at IS its created_at — the exact second the
// user ran save. Shipping that second-resolution stamp into an anonymous pool
// re-clusters one contributor's rows by timestamp adjacency (session-end
// rollups land several summaries inside a single second, which is why the
// retention tiebreak needs `id DESC` at all) and leaks working hours and
// timezone — undoing exactly the attribution removal Scrub exists to enforce.
// Export therefore truncates to the UTC day; the local row keeps full
// resolution, since it never left the machine.
func TestExport_CoarsensAuthoredAtToUTCDay(t *testing.T) {
	s, conn := newStoreWithDB(t)
	req := validReq()
	req.Scope = "shared"
	req.Title = "shared lesson that leaves this machine"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	authored, _, _ := readStamps(t, conn, resp.ID)

	var out strings.Builder
	if err := s.ExportShared(context.Background(), &out); err != nil {
		t.Fatalf("ExportShared: %v", err)
	}
	got := decodeShared(t, out.String())
	if len(got) != 1 {
		t.Fatalf("exported %d lines, want 1", len(got))
	}

	if want := authored - authored%day; got[0].AuthoredAt != want {
		t.Errorf("exported authored_at = %d, want UTC-day floor %d (raw %d) — a second-resolution stamp deanonymizes the contributor",
			got[0].AuthoredAt, want, authored)
	}
	if authored%day != 0 && got[0].AuthoredAt == authored {
		t.Errorf("exported authored_at = %d, the exact save second — the pool must not carry it", authored)
	}
	if stillLocal, _, _ := readStamps(t, conn, resp.ID); stillLocal != authored {
		t.Errorf("local authored_at = %d, want %d unchanged — coarsening is an export-time concern only", stillLocal, authored)
	}
}

// TestExport_RoundTripIsByteStable is the other half of the coarsening
// contract: truncation must be idempotent, or a pool would churn its git diff
// every time a peer re-exported what it just imported.
func TestExport_RoundTripIsByteStable(t *testing.T) {
	a, _ := newStoreWithDB(t)
	req := validReq()
	req.Scope = "shared"
	req.Title = "shared lesson that survives a pool round trip"
	if _, err := a.Save(context.Background(), req); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var first strings.Builder
	if err := a.ExportShared(context.Background(), &first); err != nil {
		t.Fatalf("ExportShared(a): %v", err)
	}

	b, _ := newStoreWithDB(t)
	res, err := b.ImportShared(context.Background(), strings.NewReader(first.String()))
	if err != nil {
		t.Fatalf("ImportShared(b): %v", err)
	}
	if res.Imported != 1 {
		t.Fatalf("imported = %d (failed=%d), want 1", res.Imported, res.Failed)
	}

	var second strings.Builder
	if err := b.ExportShared(context.Background(), &second); err != nil {
		t.Fatalf("ExportShared(b): %v", err)
	}
	if first.String() != second.String() {
		t.Errorf("re-export differs after a round trip:\n first  = %q\n second = %q", first.String(), second.String())
	}
}

// TestSave_ForcePreservesAuthoredAt: a force-save is a body correction, not a
// re-authoring. Every real force caller (CLI `--force`, MCP force:true) leaves
// AuthoredAt zero, so resolving it like a fresh insert would silently overwrite
// an imported row's peer origin date with "now" — and on a local row would push
// authored_at past created_at, which is the exact signal the TUI reads as
// "this row came from the pool".
func TestSave_ForcePreservesAuthoredAt(t *testing.T) {
	t.Run("zero keeps the existing stamp", func(t *testing.T) {
		s, conn := newStoreWithDB(t)
		old := time.Now().Unix() - 300*day

		req := validReq()
		req.Title = "peer lesson a later force-save corrects"
		req.AuthoredAt = old
		resp, err := s.Save(context.Background(), req)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}

		// Same title/learned/task_type/kind = same fingerprint, so this lands on
		// the layer-1 dupe path and force-updates the body in place.
		fix := req
		fix.What = "corrected body after the operator spotted a wrong detail"
		fix.AuthoredAt = 0
		fix.Force = true
		fixResp, err := s.Save(context.Background(), fix)
		if err != nil {
			t.Fatalf("force Save: %v", err)
		}
		if fixResp.Status != "updated" {
			t.Fatalf("status = %q, want \"updated\" — the force path was not exercised", fixResp.Status)
		}

		authored, created, _ := readStamps(t, conn, resp.ID)
		if authored != old {
			t.Errorf("authored_at = %d, want the original %d — a body correction must not re-author the row", authored, old)
		}
		if authored > created {
			t.Errorf("authored_at %d > created_at %d — that divergence is the TUI's \"imported\" signal", authored, created)
		}
	})

	t.Run("explicit stamp still wins", func(t *testing.T) {
		s, conn := newStoreWithDB(t)
		now := time.Now().Unix()

		req := validReq()
		req.Title = "lesson whose origin date is corrected explicitly"
		resp, err := s.Save(context.Background(), req)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}

		want := now - 40*day
		fix := req
		fix.What = "corrected body carrying an explicit origin date"
		fix.AuthoredAt = want
		fix.Force = true
		if _, err := s.Save(context.Background(), fix); err != nil {
			t.Fatalf("force Save: %v", err)
		}

		if authored, _, _ := readStamps(t, conn, resp.ID); authored != want {
			t.Errorf("authored_at = %d, want the supplied %d", authored, want)
		}
	})
}
