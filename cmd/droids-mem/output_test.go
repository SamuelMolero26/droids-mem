package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// Every command's entire contract is "JSON on stdout, JSON on stderr". A
// marshal failure that emits nothing would read to any consumer as a
// successful empty result, so encoding must always produce a valid envelope.
func TestErrorEnvelope_FallsBackWhenInputCannotMarshal(t *testing.T) {
	// Input is the only caller-supplied field, so it is the only one that can
	// carry something unmarshalable. A channel is never valid JSON.
	e := &errResponse{
		Status:    "error",
		Code:      "usage_error",
		Message:   "bad flag",
		Retryable: false,
		Input:     make(chan int),
	}

	b := errorEnvelope(e)

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\nraw: %s", err, b)
	}
	if got["code"] != "usage_error" {
		t.Errorf("code = %v, want usage_error", got["code"])
	}
	if got["message"] != "bad flag" {
		t.Errorf("message = %v, want 'bad flag'", got["message"])
	}
	if _, present := got["input"]; present {
		t.Errorf("unmarshalable input was retained: %s", b)
	}
}

// A NaN float is the realistic in-repo failure mode — every score field is a
// division — so the fallback must survive one appearing anywhere in Input.
func TestErrorEnvelope_FallsBackOnNaNInput(t *testing.T) {
	e := &errResponse{
		Status:  "error",
		Code:    "field_too_large",
		Message: "over cap",
		Input:   map[string]any{"score": math.NaN()},
	}

	b := errorEnvelope(e)

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\nraw: %s", err, b)
	}
	if got["code"] != "field_too_large" {
		t.Errorf("code = %v, want field_too_large", got["code"])
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
