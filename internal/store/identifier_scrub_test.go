package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// Identifier fields (task_type, session_id) are persisted unscrubbed, so any
// scrub-detector hit must strict-reject the save — mirror of the tags policy.
func TestSave_RejectsSecretInIdentifiers(t *testing.T) {
	s := newTestStore(t)

	secretTaskType := "sk-ant-" + strings.Repeat("a", 50)
	req := store.SaveRequest{
		TaskType: secretTaskType,
		Kind:     "task_pattern",
		Title:    "t",
		What:     "w",
		Learned:  "l",
	}
	_, err := s.Save(context.Background(), req)
	ve := requireValidationError(t, err)
	if ve.Code != "task_type_contains_secret" || ve.Field != "task_type" {
		t.Fatalf("got code=%q field=%q, want task_type_contains_secret/task_type", ve.Code, ve.Field)
	}
	if !ve.Retryable {
		t.Fatal("identifier secret rejection must be retryable")
	}

	req.TaskType = "crm_upload"
	req.SessionID = "sess_x7Kp9q2mNv8wLz4r" // benign — must save
	if _, err := s.Save(context.Background(), req); err != nil {
		t.Fatalf("benign session_id rejected: %v", err)
	}

	req.Title = "different title entirely for new fingerprint"
	req.Learned = "another lesson body to avoid dedupe"
	req.SessionID = "ghp_" + strings.Repeat("A", 36)
	_, err = s.Save(context.Background(), req)
	ve = requireValidationError(t, err)
	if ve.Code != "session_id_contains_secret" {
		t.Fatalf("got code=%q, want session_id_contains_secret", ve.Code)
	}
}

func requireValidationError(t *testing.T, err error) *store.ValidationError {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	return ve
}
