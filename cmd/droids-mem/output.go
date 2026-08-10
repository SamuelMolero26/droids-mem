package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotFound = 3
	ExitConflict = 5
	ExitDryRun   = 10
)

func writeJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// Every command's contract is JSON on stdout. Printing nothing and
		// exiting 0 would read to a consumer as a successful empty result,
		// which is the worst possible shape for a machine-facing CLI — so an
		// encoding failure has to be loud.
		writeError("encode_failed", "cannot encode response: "+err.Error(), false)
		exitWith(ExitError)
	}
	fmt.Fprintln(os.Stdout, string(b))
}

// writeString prints a pre-rendered payload (e.g. the graph surface's TOON) to
// stdout as-is.
func writeString(s string) {
	fmt.Fprintln(os.Stdout, s)
}

type errResponse struct {
	Status     string `json:"status"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Field      string `json:"field,omitempty"`
	Input      any    `json:"input,omitempty"`
	Retryable  bool   `json:"retryable"`
	Suggestion string `json:"suggestion,omitempty"`
}

func writeError(code, message string, retryable bool, opts ...func(*errResponse)) {
	e := &errResponse{
		Status:    "error",
		Code:      code,
		Message:   message,
		Retryable: retryable,
	}
	for _, o := range opts {
		o(e)
	}
	fmt.Fprintln(os.Stderr, string(errorEnvelope(e)))
}

// errorEnvelope renders e as JSON, always. Input is the only caller-supplied
// field, so it is the only one that can hold something unmarshalable (a NaN
// score, a channel); dropping it leaves a struct of strings and bools that
// cannot fail. The literal is a last resort that should be unreachable.
func errorEnvelope(e *errResponse) []byte {
	if b, err := json.Marshal(e); err == nil {
		return b
	}
	e.Input = nil
	if b, err := json.Marshal(e); err == nil {
		return b
	}
	return []byte(`{"status":"error","code":"encode_failed","message":"response could not be encoded","retryable":false}`)
}

func withField(field string) func(*errResponse) {
	return func(e *errResponse) { e.Field = field }
}

func withInput(input any) func(*errResponse) {
	return func(e *errResponse) { e.Input = input }
}

func withSuggestion(s string) func(*errResponse) {
	return func(e *errResponse) { e.Suggestion = s }
}
