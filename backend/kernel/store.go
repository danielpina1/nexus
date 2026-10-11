package kernel

import "database/sql"

// Store gives modules access to the hub's operational SQLite database.
// Each module owns its own tables and runs its own migrations through Migrate.
type Store interface {
	DB() *sql.DB
	// Migrate applies the given ordered statements for a module once each,
	// tracked in a shared schema_migrations table keyed by (module, index).
	Migrate(module string, statements []string) error
}
