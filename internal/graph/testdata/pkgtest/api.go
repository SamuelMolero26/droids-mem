// Package pkgtest is a fixture for the package-surface test/exported split.
package pkgtest

// Real is the only exported production symbol in this fixture.
func Real() int { return helper() }

func helper() int { return 1 }
