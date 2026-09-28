// Package fanout is the truncation stress case: 60 distinct callers of one
// sink push the neighbor list past the 50-row cap, so the symbol view must
// show the "Showing 50 of 60" note and the flow view the truncated hint.
package fanout

// Target is called by every CallerNN below.
func Target() int {
	return 1
}

func Caller01() int { return Target() }
func Caller02() int { return Target() }
func Caller03() int { return Target() }
func Caller04() int { return Target() }
func Caller05() int { return Target() }
func Caller06() int { return Target() }
func Caller07() int { return Target() }
func Caller08() int { return Target() }
func Caller09() int { return Target() }
func Caller10() int { return Target() }
func Caller11() int { return Target() }
func Caller12() int { return Target() }
func Caller13() int { return Target() }
func Caller14() int { return Target() }
func Caller15() int { return Target() }
func Caller16() int { return Target() }
func Caller17() int { return Target() }
func Caller18() int { return Target() }
func Caller19() int { return Target() }
func Caller20() int { return Target() }
func Caller21() int { return Target() }
func Caller22() int { return Target() }
func Caller23() int { return Target() }
func Caller24() int { return Target() }
func Caller25() int { return Target() }
func Caller26() int { return Target() }
func Caller27() int { return Target() }
func Caller28() int { return Target() }
func Caller29() int { return Target() }
func Caller30() int { return Target() }
func Caller31() int { return Target() }
func Caller32() int { return Target() }
func Caller33() int { return Target() }
func Caller34() int { return Target() }
func Caller35() int { return Target() }
func Caller36() int { return Target() }
func Caller37() int { return Target() }
func Caller38() int { return Target() }
func Caller39() int { return Target() }
func Caller40() int { return Target() }
func Caller41() int { return Target() }
func Caller42() int { return Target() }
func Caller43() int { return Target() }
func Caller44() int { return Target() }
func Caller45() int { return Target() }
func Caller46() int { return Target() }
func Caller47() int { return Target() }
func Caller48() int { return Target() }
func Caller49() int { return Target() }
func Caller50() int { return Target() }
func Caller51() int { return Target() }
func Caller52() int { return Target() }
func Caller53() int { return Target() }
func Caller54() int { return Target() }
func Caller55() int { return Target() }
func Caller56() int { return Target() }
func Caller57() int { return Target() }
func Caller58() int { return Target() }
func Caller59() int { return Target() }
func Caller60() int { return Target() }
