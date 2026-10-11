# ADR-0005: Embedded SQLite for operational state and the vault index

- Status: proposed
- Date: 2026-10-11

## Context
Single-user system, single Hub, desire for single-binary deployment. Need relational state, an
append-only event log, full-text search and vector search.

## Decision
Use SQLite in WAL mode: `nexus.db` (operational state + event log) and `index.db` (vault graph,
FTS5, sqlite-vec). Access through `store` interfaces so Postgres (+ pgvector) can be added later.
Prefer a cgo-free driver (e.g. `ncruces/go-sqlite3`, which can load sqlite-vec) to keep
cross-compilation simple — to be validated in Phase 0.

## Consequences
- Zero external services; backups are file copies (or Litestream for continuous replication).
- Single-writer model: writes go through a serialized writer goroutine; reads are concurrent.
- If multi-user/multi-hub ever happens, migrate behind the same interfaces.
