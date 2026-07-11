// Package fanout is a fixture for measuring CHA interface fan-out: one
// interface (Store) with three implementations and two call sites that
// dispatch through it. MockStore implements Store but is never constructed,
// so every edge to MockStore.Save is unreachable at runtime.
package fanout

// Store is the fixture interface.
type Store interface{ Save() }

// SQLStore is constructed and dispatched through Store.
type SQLStore struct{}

func (SQLStore) Save() {}

// MemStore is constructed and dispatched through Store.
type MemStore struct{}

func (MemStore) Save() {}

// MockStore implements Store but is never constructed anywhere in this package.
type MockStore struct{}

func (MockStore) Save() {}

// Broadcast dispatches through the interface — CHA resolves Save() to every
// Store implementation, one edge per implementation.
func Broadcast(s Store) { s.Save() }

// Echo is a second interface-dispatch call site.
func Echo(s Store) { s.Save() }

// Run constructs only SQLStore and MemStore; MockStore never flows through Store.
func Run() {
	Broadcast(SQLStore{})
	Echo(MemStore{})
}
