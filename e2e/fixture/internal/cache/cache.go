// Package cache is the bottom of the chain: store.DB.Get -> Fetch.
package cache

// Fetch returns the cached value for key.
func Fetch(key string) string {
	return key
}

// Evict drops key from the cache.
func Evict(key string) {
	_ = key
}
