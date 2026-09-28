// Package model holds data shapes with no callers: the symbol view must
// render the no-callers note for the type, const and var alike.
package model

// User is a directory entry.
type User struct {
	Name string
}

// MaxRetries caps login attempts.
const MaxRetries = 3

// DefaultTimeout is the request budget in seconds.
var DefaultTimeout = 30
