package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SamuelMolero26/droids-mem/internal/db"
	"github.com/SamuelMolero26/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

// fileBackedStore opens a real file-backed SQLite DB so concurrent writes
// actually contend (in-memory ":memory:" DBs share no concurrency reality).
func fileBackedStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")
	conn, err := sql.Open("sqlite", db.BuildDSN(dbPath))
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	conn.SetMaxOpenConns(8)
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn), dbPath
}

// TestSave_ConcurrentDedupe verifies the BEGIN IMMEDIATE wrap + UPSERT path
// collapses concurrent identical saves to exactly one stored row with no
// UNIQUE constraint error surfaced to the caller.
func TestSave_ConcurrentDedupe(t *testing.T) {
	s, _ := fileBackedStore(t)

	const goroutines = 10
	req := store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "HubSpot phone field mapping",
		What:     "Upload failed because target field was phone_number",
		Learned:  "Map Phone Number to phone",
		Tags:     "hubspot phone",
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	saved := 0
	skipped := 0
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.Save(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch resp.Status {
			case "saved":
				saved++
			case "skipped":
				skipped++
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent save returned error: %v", err)
	}

	if saved != 1 {
		t.Errorf("expected exactly 1 saved, got %d", saved)
	}
	if skipped != goroutines-1 {
		t.Errorf("expected %d skipped, got %d", goroutines-1, skipped)
	}
}

func TestDoctor_HealthyDB(t *testing.T) {
	s, dbPath := fileBackedStore(t)
	s.Save(context.Background(), store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "phone mapping",
		What:     "x",
		Learned:  "y",
	})

	rep, err := s.Doctor(dbPath)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !rep.IntegrityOK {
		t.Errorf("expected integrity_ok on healthy DB, got false (err=%q)", rep.IntegrityErr)
	}
	if rep.Rebuilt {
		t.Error("expected no rebuild on healthy DB")
	}
	if !rep.Optimized {
		t.Error("expected optimize to run")
	}
	if !rep.Vacuumed {
		t.Error("expected vacuum to run")
	}
	if rep.BytesBefore <= 0 || rep.BytesAfter <= 0 {
		t.Errorf("expected non-zero byte counts, got before=%d after=%d", rep.BytesBefore, rep.BytesAfter)
	}
}

// TestSearch_TotalReflectsFullMatchCount verifies the count(*) pre-limit
// query returns true match count, not just the returned page size.
func TestSearch_TotalReflectsFullMatchCount(t *testing.T) {
	s := newTestStore(t)
	// Save 7 distinct memories all mentioning "phone" but with enough divergent
	// content that Layer-2 Jaccard does not collapse them as near-duplicates.
	contexts := []struct{ title, what, learned string }{
		{"HubSpot phone mapping bug", "API rejected E164 number on contact create", "Strip leading + before sending"},
		{"Salesforce phone parsing", "CSV import dropped trailing extensions", "Quote phone column in CSV export"},
		{"Twilio phone webhook signature", "Verification failed under proxy", "Use raw body, not parsed JSON"},
		{"phone field NULL in Pipedrive", "Bulk update wiped phones", "Set ignore_empty=true on patch"},
		{"phone normalization for Zendesk", "Duplicate contacts created", "Strip whitespace before lookup"},
		{"international phone code prefix", "Belgian numbers truncated", "Preserve leading 0 in storage"},
		{"phone vs mobile column", "Excel imports conflated columns", "Map by header name not index"},
	}
	for _, c := range contexts {
		s.Save(context.Background(), store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "error_resolution",
			Title:    c.title,
			What:     c.what,
			Learned:  c.learned,
		})
	}

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "phone", Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Errorf("expected 3 results in page, got %d", len(resp.Results))
	}
	if resp.Total != 7 {
		t.Errorf("expected Total=7 (full match count), got %d", resp.Total)
	}
}

// TestSearch_Unicode61KeepsIdentifierAtomic confirms the v1.0 tokenizer
// (`unicode61 tokenchars=_-`) treats snake_case / kebab-case identifiers as
// single tokens — the property we traded substring matching for. Decision
// #17 in the v1.0 plan re-baselined this test off the previous trigram
// behavior.
func TestSearch_Unicode61KeepsIdentifierAtomic(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "field-mapping bug",
		What:     "phone_number column rejected the value",
		Learned:  "Strip leading plus before send",
		Tags:     "hubspot field-mapping",
	})

	respFull, err := s.Search(context.Background(), store.SearchRequest{Query: "phone_number"})
	if err != nil {
		t.Fatalf("Search full identifier: %v", err)
	}
	if respFull.Total == 0 {
		t.Error("unicode61 tokenizer should index 'phone_number' as one token")
	}

	respHyphen, err := s.Search(context.Background(), store.SearchRequest{Query: "field-mapping"})
	if err != nil {
		t.Fatalf("Search kebab identifier: %v", err)
	}
	if respHyphen.Total == 0 {
		t.Error("unicode61 tokenchars=_- should keep 'field-mapping' atomic")
	}
}
