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
