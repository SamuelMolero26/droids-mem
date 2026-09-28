// Package store is the callee side of the interface edge: auth holds a
// Store and calls Get through it. DB.Get fans out to cache.Fetch.
package store

import "e2e-fixture/internal/cache"

// Store abstracts the user directory.
type Store interface {
	Get(key string) string
}

// DB is the production Store.
type DB struct{}

// Get returns the cached value for key, fetching on miss.
func (d DB) Get(key string) string {
	return cache.Fetch(key)
}

// Put stores value and evicts the stale read cache.
func (d DB) Put(key, value string) string {
	cache.Evict(key)
	return value
}
