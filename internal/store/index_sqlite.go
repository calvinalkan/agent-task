package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/calvinalkan/fileproc"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
)

const schemaVersion = 1

func openSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("open sqlite: path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

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

// applyPragmas matches the durability/speed tradeoffs described in the migration plan.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA mmap_size = 268435456",
		"PRAGMA cache_size = -20000",
		"PRAGMA temp_store = MEMORY",
	}

	for _, stmt := range statements {
		_, err := db.ExecContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("apply pragma %q: %w", stmt, err)
		}
	}

	return nil
}

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}

	return version, nil
}

func rebuildIndexInTxn(ctx context.Context, db *sql.DB, entries []fileproc.Result[indexTicket]) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin rebuild txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	err = createSchema(ctx, tx)
	if err != nil {
		return 0, err
	}

	insertTicket, err := tx.PrepareContext(ctx, `
		INSERT INTO tickets (
			id,
			short_id,
			path,
			mtime_ns,
			status,
			type,
			priority,
			assignee,
			parent,
			created_at,
			closed_at,
			external_ref,
			title
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}

	defer func() { _ = insertTicket.Close() }()

	insertBlocker, err := tx.PrepareContext(ctx, `
		INSERT INTO ticket_blockers (ticket_id, blocker_id) VALUES (?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare blocker insert: %w", err)
	}

	defer func() { _ = insertBlocker.Close() }()

	indexed := 0

	for i := range entries {
		entry := entries[i].Value
		if entry == nil {
			continue
		}

		_, err = insertTicket.ExecContext(
			ctx,
			entry.ID,
			entry.ShortID,
			entry.Path,
			entry.MtimeNS,
			entry.Status,
			entry.Type,
			entry.Priority,
			entry.Assignee,
			entry.Parent,
			entry.CreatedAt,
			entry.ClosedAt,
			entry.ExternalRef,
			entry.Title,
		)
		if err != nil {
			return 0, fmt.Errorf("insert index row for %s (%s): %w", entry.ID, entry.Path, err)
		}

		indexed++

		for _, blocker := range entry.BlockedBy {
			_, err = insertBlocker.ExecContext(ctx, entry.ID, blocker)
			if err != nil {
				return 0, fmt.Errorf("insert blocker for %s (%s): %w", entry.ID, entry.Path, err)
			}
		}
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	if err != nil {
		return 0, fmt.Errorf("set user_version: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf("commit rebuild txn: %w", err)
	}

	committed = true

	return indexed, nil
}

func createSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		"DROP TABLE IF EXISTS ticket_blockers",
		"DROP TABLE IF EXISTS tickets",
		`CREATE TABLE tickets (
			id TEXT PRIMARY KEY,
			short_id TEXT NOT NULL,
			path TEXT NOT NULL,
			mtime_ns INTEGER NOT NULL,
			status TEXT NOT NULL,
			type TEXT NOT NULL,
			priority INTEGER NOT NULL,
			assignee TEXT,
			parent TEXT,
			created_at INTEGER NOT NULL,
			closed_at INTEGER,
			external_ref TEXT,
			title TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE ticket_blockers (
			ticket_id TEXT NOT NULL,
			blocker_id TEXT NOT NULL,
			PRIMARY KEY (ticket_id, blocker_id)
		) WITHOUT ROWID`,
		"CREATE INDEX idx_status_priority ON tickets(status, priority)",
		"CREATE INDEX idx_status_type ON tickets(status, type)",
		"CREATE INDEX idx_parent ON tickets(parent)",
		"CREATE INDEX idx_short_id ON tickets(short_id)",
		"CREATE INDEX idx_blocker ON ticket_blockers(blocker_id)",
	}

	for _, stmt := range statements {
		_, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("apply schema statement %q: %w", stmt, err)
		}
	}

	return nil
}
