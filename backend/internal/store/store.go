// Package store provides the hub's operational SQLite database as the
// kernel.Store service ("store" module).
//
// Every hub module that persists state resolves kernel.SvcStore and creates
// its own tables with Migrate; this package only owns the shared
// schema_migrations bookkeeping table.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/internal/sqlitex"
)

const migrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	module     TEXT    NOT NULL,
	idx        INTEGER NOT NULL,
	applied_at INTEGER NOT NULL,
	PRIMARY KEY (module, idx)
)`

// Store implements kernel.Store over a *sql.DB.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serialises Migrate calls
}

// Open opens (creating if needed) the database file at path.
func Open(path string) (*Store, error) {
	db, err := sqlitex.Open(path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s, err := New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory opens a private in-memory store. Useful in other modules' tests.
// Note it is limited to a single connection: never hold a *sql.Rows open while
// issuing another query.
func OpenMemory() (*Store, error) {
	db, err := sqlitex.OpenMemory()
	if err != nil {
		return nil, err
	}
	s, err := New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// New wraps an already-open database and ensures the migrations table exists.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(migrationsDDL); err != nil {
		return nil, fmt.Errorf("store: create schema_migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// DB returns the shared database handle.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Migrate applies statements[i] for module exactly once, in order. Each
// statement runs in its own transaction together with its bookkeeping row, so
// a failure leaves earlier statements applied and the failing one rolled back.
//
// Statements must never be edited or reordered once released: append new ones.
func (s *Store) Migrate(module string, statements []string) error {
	if module == "" {
		return errors.New("store: migrate: empty module name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	for i, stmt := range statements {
		if err := s.apply(ctx, module, i, stmt); err != nil {
			return fmt.Errorf("store: migrate %s[%d]: %w", module, i, err)
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, module string, idx int, stmt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations WHERE module = ? AND idx = ?`, module, idx).Scan(&one)
	switch {
	case err == nil:
		return nil // already applied
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (module, idx, applied_at) VALUES (?, ?, ?)`,
		module, idx, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Applied returns how many migrations are recorded for module.
func (s *Store) Applied(module string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE module = ?`, module).Scan(&n)
	return n, err
}
