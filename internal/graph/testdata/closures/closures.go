// Package closures is a fixture for the closure-callee edge projection fix
// (issue #69): Target's own deferred closure (Target$1) is func()-shaped, so
// CHA's funcsBySig resolves Noise's dynamic defer cancel() call to it, and
// resolve()'s Parent() collapse would attribute that edge to Target unless the
// raw closure-callee edge is dropped. Caller is the only real call site.
package closures

// Target has a non-func() signature so it is only poisoned via its own
// deferred closure, never directly by funcsBySig (cf. Store.Save's non-func()
// signature in the real repo).
func Target() int {
	defer func() {}()
	return 1
}

// Noise is the funcsBySig noise source: a dynamic func()-typed call that CHA
// over-approximates to every func()-shaped function in the program, including
// Target$1. Pre-fix it shows up as a ghost caller of Target.
func Noise() {
	var cancel func()
	defer cancel()
}

// Caller is the single real call site of Target.
func Caller() int { return Target() }
