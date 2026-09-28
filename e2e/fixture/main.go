// Package main wires a two-node call graph for the viewer e2e:
// main -> calc.Add. The Map view shows both packages, Symbol shows Add,
// Flow shows main as the caller of calc.Add.
package main

import (
	"fmt"

	"e2e-fixture/calc"
)

func main() {
	fmt.Println(calc.Add(2, 3))
}
