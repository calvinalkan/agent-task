package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// openSqlite opens the derived index database and applies the configured pragmas.
func openSqlite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("open sqlite: path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Ensure per-connection PRAGMAs apply consistently.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	err = db.PingContext(ctx)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	err = applyPragmas(ctx, db)
	if err != nil {
		_ = db.Close()

		return nil, err
	}

	return db, nil
}

// sqliteBusyTimeout is the time SQLite waits when the database is locked.
// After this, operations return SQLITE_BUSY.
const sqliteBusyTimeout = 10000 // milliseconds

// applyPragmas configures the SQLite connection using a single batch statement.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		PRAGMA busy_timeout = %d;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA mmap_size = 268435456;
		PRAGMA cache_size = -20000;
		PRAGMA temp_store = MEMORY;
	`, sqliteBusyTimeout))
	if err != nil {
		return fmt.Errorf("apply pragmas: %w", err)
	}

	return nil
}

// storedSchemaVersion reads the current SQLite PRAGMA user_version.
func storedSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}

	return version, nil
}
