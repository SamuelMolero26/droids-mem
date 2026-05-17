package store

// scrubPII redacts sensitive substrings (emails, phone numbers, API keys,
// JWTs, etc.) from free-text fields before persistence.
//
// V1 ships as a pass-through hook point. All save-path text fields route
// through this function in validate() so a future PR can add regex patterns
// without touching call sites. See Future.md for the planned pattern set.
func scrubPII(s string) string {
	return s
}
