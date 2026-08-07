// Package closures is a fixture for the closure edge projection fix (issue
// #69). It pins BOTH halves, which pull in opposite directions:
//
//   - An edge INTO a closure is dropped. Target's own deferred closure
//     (Target$1) is func()-shaped, so CHA's funcsBySig resolves Noise's
//     dynamic defer cancel() to it; without the guard, resolve()'s Parent()
//     collapse turns that into a Noise → Target ghost.
//   - An edge FROM inside a closure is kept, attributed to the enclosing
//     declaration. ClosureCaller reaches Target only through a closure and
//     must still be reported as a caller.
//
// Caller and ClosureCaller are the only real call sites of Target.
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

// Caller is the direct real call site of Target.
func Caller() int { return Target() }

// ClosureCaller reaches Target ONLY from inside a closure. The caller-side
// Parent() collapse must attribute that edge to ClosureCaller itself, and the
// callee-side guard must not take it out — this is the shape newSaveCmd and
// saveHandler use to reach Store.Save in the real repo. Its signature is
// non-func() for the same reason Target's is: a func()-shaped ClosureCaller
// would itself be a funcsBySig target of Noise's defer cancel(), adding a real
// (CHA-over-approximate) Noise → ClosureCaller edge that has nothing to do
// with closures and would muddy the assertion.
func ClosureCaller() int {
	var n int
	func() { n = Target() }()
	return n
}
