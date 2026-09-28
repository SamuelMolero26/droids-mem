// Package auth sits mid-chain: api.HandleLogin -> Service.Login ->
// store.Get. Login calls through the Store interface, and Middleware leans
// on the token package. helper stays unexported for the pkg-view marker.
package auth

import (
	"e2e-fixture/internal/store"
	"e2e-fixture/internal/token"
)

// Service serves logins backed by any Store implementation.
type Service struct {
	Store store.Store
}

// Login returns the display name for the session user.
func (s Service) Login() string {
	return "user:" + s.Store.Get("session")
}

// Middleware reports whether the request token parses.
func Middleware() bool {
	return token.Parse("req") != ""
}

func helper() bool {
	return true
}
