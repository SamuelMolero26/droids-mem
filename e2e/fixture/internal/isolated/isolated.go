// Package isolated imports nothing: the map must park it in the trailing
// column for unlinked packages.
package isolated

// Lone has no callers and no callees.
func Lone() int {
	return 1
}
