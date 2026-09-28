// Package calc is the callee side of the e2e fixture graph.
package calc

// Add returns a+b. Flow view must list main as its caller.
func Add(a, b int) int {
	return a + b
}
