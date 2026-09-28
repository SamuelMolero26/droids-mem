package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// Every command's entire contract is "JSON on stdout, JSON on stderr". A
// marshal failure that emits nothing would read to any consumer as a
// successful empty result, so encoding must always produce a valid envelope.
func TestErrorEnvelope_FallsBackOnUnmarshalableInput(t *testing.T) {
	tests := []struct {
		name string
		// Input is the only caller-supplied field, so it is the only one that
		// can carry something unmarshalable.
		input any
		code  string
		msg   string
		// wantMessage is empty when the case does not assert on it.
		wantMessage string
		// dropInput asserts the unmarshalable input was stripped.
		dropInput bool
	}{
		{
			// A channel is never valid JSON.
			name:        "channel input cannot marshal",
			input:       make(chan int),
			code:        "usage_error",
			msg:         "bad flag",
			wantMessage: "bad flag",
			dropInput:   true,
		},
		{
			// A NaN float is the realistic in-repo failure mode — every score
			// field is a division — so the fallback must survive one
			// appearing anywhere in Input.
			name:  "NaN score in input",
			input: map[string]any{"score": math.NaN()},
			code:  "field_too_large",
			msg:   "over cap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &errResponse{
				Status:    "error",
				Code:      tt.code,
				Message:   tt.msg,
				Retryable: false,
				Input:     tt.input,
			}

			b := errorEnvelope(e)

			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("envelope is not valid JSON: %v\nraw: %s", err, b)
			}
			if got["code"] != tt.code {
				t.Errorf("code = %v, want %s", got["code"], tt.code)
			}
			if tt.wantMessage != "" && got["message"] != tt.wantMessage {
				t.Errorf("message = %v, want %q", got["message"], tt.wantMessage)
			}
			if tt.dropInput {
				if _, present := got["input"]; present {
					t.Errorf("unmarshalable input was retained: %s", b)
				}
			}
		})
	}
}

// The normal path must be untouched: a marshalable Input is preserved.
func TestErrorEnvelope_KeepsMarshalableInput(t *testing.T) {
	e := &errResponse{
		Status:  "error",
		Code:    "usage_error",
		Message: "bad flag",
		Input:   map[string]any{"flag": "--nope"},
	}

	b := errorEnvelope(e)
	if !strings.Contains(string(b), `"--nope"`) {
		t.Errorf("marshalable input was dropped: %s", b)
	}
}

// The store's own Code must win over the generic fallback whenever it set one.
func TestValidationErrorFields_CodePassthrough(t *testing.T) {
	ve := &store.ValidationError{Code: "invalid_threshold", Retryable: true}
	code, _, _ := validationErrorFields(ve, "fallback suggestion")
	if code != "invalid_threshold" {
		t.Errorf("code = %q, want invalid_threshold", code)
	}
}

// An unset Code must fall back to the generic "validation_failed" — the
// shape every command already produces today.
func TestValidationErrorFields_CodeFallback(t *testing.T) {
	ve := &store.ValidationError{Retryable: true}
	code, _, _ := validationErrorFields(ve, "fallback suggestion")
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
}

// The store's own Suggestion must win over the command's generic fallback
// whenever it set one.
func TestValidationErrorFields_SuggestionPassthrough(t *testing.T) {
	ve := &store.ValidationError{Suggestion: "pass --id, or at least one of --kind, --task-type, --older-than-days"}
	_, suggestion, _ := validationErrorFields(ve, "fallback suggestion")
	if suggestion != ve.Suggestion {
		t.Errorf("suggestion = %q, want %q", suggestion, ve.Suggestion)
	}
}

// An unset Suggestion must fall back to the command-supplied generic string.
func TestValidationErrorFields_SuggestionFallback(t *testing.T) {
	ve := &store.ValidationError{}
	_, suggestion, _ := validationErrorFields(ve, "fallback suggestion")
	if suggestion != "fallback suggestion" {
		t.Errorf("suggestion = %q, want fallback suggestion", suggestion)
	}
}

// Retryable has no fallback — it always mirrors the store's own value,
// whichever it is.
func TestValidationErrorFields_RetryableMirrorsStore(t *testing.T) {
	for _, want := range []bool{true, false} {
		ve := &store.ValidationError{Retryable: want}
		_, _, retryable := validationErrorFields(ve, "")
		if retryable != want {
			t.Errorf("retryable = %v, want %v", retryable, want)
		}
	}
}

// Field and message must always pass through verbatim, whether or not
// Code/Suggestion used a fallback.
func TestValidationErrorFields_FieldAndMessageUntouched(t *testing.T) {
	ve := &store.ValidationError{Field: "query", Message: "required"}
	code, suggestion, _ := validationErrorFields(ve, "fallback")
	if ve.Field != "query" || ve.Message != "required" {
		t.Fatalf("validationErrorFields must not mutate ve, got field=%q message=%q", ve.Field, ve.Message)
	}
	// code/suggestion resolution is exercised above; this test only asserts
	// the function's return values don't clobber the fields it never owns.
	_ = code
	_ = suggestion
}
