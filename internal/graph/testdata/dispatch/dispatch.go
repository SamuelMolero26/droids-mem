// Package dispatch is a fixture for the caller-fidelity dispatch split
// (issue #48/decision 2, amended): every interface call site dispatches
// through Doer, and CHA resolves each site to EVERY implementation — so
// Dominant.Do and Minor.Do share the same 3 interface call sites, but differ
// in how many additional STATIC (direct, non-interface) callers each has.
// Dominant.Do: 3 interface + 1 static = 4 total, 75% interface (dominance
// hint must fire). Minor.Do: 3 interface + 6 static = 9 total, 33% interface
// (hint must not fire).
package dispatch

// Doer is dispatched through by every ISite function below.
type Doer interface{ Do() }

// Dominant is reached mostly via interface dispatch.
type Dominant struct{}

// Do is Dominant's method.
func (Dominant) Do() {}

// Minor is reached mostly via static (direct) calls.
type Minor struct{}

// Do is Minor's method.
func (Minor) Do() {}

// ISite1 calls through the interface — CHA resolves this to every Doer
// implementation, so it is a caller of both Dominant.Do and Minor.Do.
func ISite1(d Doer) { d.Do() }

// ISite2 is a second interface-dispatch call site.
func ISite2(d Doer) { d.Do() }

// ISite3 is a third interface-dispatch call site.
func ISite3(d Doer) { d.Do() }

// StaticDominant calls Dominant.Do directly (not through Doer).
func StaticDominant() { Dominant{}.Do() }

// StaticMinor1 calls Minor.Do directly.
func StaticMinor1() { Minor{}.Do() }

// StaticMinor2 calls Minor.Do directly.
func StaticMinor2() { Minor{}.Do() }

// StaticMinor3 calls Minor.Do directly.
func StaticMinor3() { Minor{}.Do() }

// StaticMinor4 calls Minor.Do directly.
func StaticMinor4() { Minor{}.Do() }

// StaticMinor5 calls Minor.Do directly.
func StaticMinor5() { Minor{}.Do() }

// StaticMinor6 calls Minor.Do directly.
func StaticMinor6() { Minor{}.Do() }
