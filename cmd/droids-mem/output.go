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
	b, _ := json.Marshal(v)
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
	b, _ := json.Marshal(e)
	fmt.Fprintln(os.Stderr, string(b))
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
