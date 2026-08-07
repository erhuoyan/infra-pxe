// Package db provides SQLite-backed storage for the PXE engine.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with WAL mode and application-level write serialization.
type DB struct {
	conn *sql.DB
	wmu  sync.Mutex // serialize writes (SQLite single-writer)
}

// Open creates or opens the SQLite database at path.
// Enables WAL mode and foreign keys.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single connection for writes, multiple for reads
	conn.SetMaxOpenConns(4)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	slog.Info("database opened", "path", path, "mode", "WAL")
	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the underlying *sql.DB for custom queries.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// WithWrite serializes write operations.
func (db *DB) WithWrite(fn func() error) error {
	db.wmu.Lock()
	defer db.wmu.Unlock()
	return fn()
}
