// Package main is a fixture for graph indexing tests: one interface dispatch
// (Announce → Greeter.Greet via English), one direct call chain
// (main → Announce → pick).
package main

// Lang is a fixture const: a non-callable symbol, so a blast-radius query on it
// has no call edges (issue #47).
const Lang = "en"

// Greeter is the fixture interface.
type Greeter interface {
	Greet() string
}

// English is the sole Greeter implementation.
type English struct{}

// Greet returns a greeting.
func (English) Greet() string { return "hi" }

func pick() Greeter { return English{} }

// Announce greets through the interface — CHA must resolve this edge.
func Announce() string {
	return pick().Greet()
}

func main() {
	println(Announce())
}
