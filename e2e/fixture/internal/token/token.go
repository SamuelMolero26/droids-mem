// Package token parses request tokens for the auth middleware.
package token

// Parse returns the token unchanged when it looks valid.
func Parse(raw string) string {
	if raw == "" {
		return ""
	}
	return raw
}
