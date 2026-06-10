package store

import "database/sql"

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB. Exposed for tests and operator tooling
// (doctor, inspect) that need to issue ad-hoc queries outside the Store API.
// Production save/search/context paths must go through Store methods so the
// dedupe + transaction discipline stays in one place.
func (s *Store) DB() *sql.DB {
	return s.db
}
