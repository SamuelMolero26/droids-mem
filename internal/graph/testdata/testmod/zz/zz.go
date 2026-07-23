// Package zz is a fixture for neighbor ordering (issue #49): Hub is called by a
// same-package caller (Near) and a cross-package one (main). Its qname prefix
// "zz" sorts AFTER "testmod", so plain-alphabetical order would list the
// cross-package caller first — same-package-first ordering must override that.
package zz

// Hub is the queried target.
func Hub() {}

// Near calls Hub from the same package.
func Near() { Hub() }
