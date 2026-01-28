package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver

	"github.com/calvinalkan/agent-task/pkg/fs"
)

const schemaVersion = 1

// Store wires the derived SQLite index together with WAL and lock coordination.
// Keeping these handles centralized ensures recovery uses consistent fs primitives.
type Store struct {
	dir     string
	sql     *sql.DB
	fs      fs.FS
	locker  *fs.Locker
	atomic  *fs.AtomicWriter
	wal     fs.File
	walPath string
}

// Open initializes the SQLite index for a ticket directory.
// If the schema version is missing or mismatched, it rebuilds to avoid stale reads.
//
// Open acquires the WAL lock before recovery. It may return [ErrWALCorrupt],
// [ErrWALReplay], or [ErrIndexUpdate] if recovery or index updates fail.
func Open(ctx context.Context, dir string) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("open store: context is nil")
	}

	if dir == "" {
		return nil, errors.New("open store: directory is empty")
	}

	ticketDir := filepath.Clean(dir)
	tkDir := filepath.Join(ticketDir, ".tk")

	fsReal := fs.NewReal()
	locker := fs.NewLocker(fsReal)
	atomicWriter := fs.NewAtomicWriter(fsReal)

	err := fsReal.MkdirAll(tkDir, 0o750)
	if err != nil {
		return nil, fmt.Errorf("open store: create .tk directory: %w", err)
	}

	walPath := filepath.Join(tkDir, "wal")

	walFile, err := fsReal.OpenFile(walPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store: open wal: %w", err)
	}

	db, err := openSQLite(ctx, filepath.Join(tkDir, "index.sqlite"))
	if err != nil {
		_ = walFile.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	store := &Store{
		dir:     ticketDir,
		sql:     db,
		fs:      fsReal,
		locker:  locker,
		atomic:  atomicWriter,
		wal:     walFile,
		walPath: walPath,
	}

	version, err := userVersion(ctx, db)
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	hasSchema, err := schemaExists(ctx, db)
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	needsRebuild := version != schemaVersion || !hasSchema

	walInfo, err := walFile.Stat()
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: wal stat: %w", err)
	}

	if needsRebuild || walInfo.Size() > 0 {
		lock, err := locker.Lock(walPath)
		if err != nil {
			_ = store.Close()

			return nil, fmt.Errorf("open store: lock wal: %w", err)
		}

		recovery, recoverErr := store.recoverWalLocked(ctx)
		if recoverErr != nil {
			_ = lock.Close()
			_ = store.Close()

			return nil, recoverErr
		}

		version, err = userVersion(ctx, db)
		if err != nil {
			_ = lock.Close()
			_ = store.Close()

			return nil, fmt.Errorf("open store: %w", err)
		}

		hasSchema, err = schemaExists(ctx, db)
		if err != nil {
			_ = lock.Close()
			_ = store.Close()

			return nil, fmt.Errorf("open store: %w", err)
		}

		needsRebuild = version != schemaVersion || !hasSchema

		if needsRebuild {
			entries, scanErr := scanTicketFiles(ctx, store.dir)
			if scanErr != nil {
				_ = lock.Close()
				_ = store.Close()

				return nil, fmt.Errorf("open store: scan tickets: %w", scanErr)
			}

			_, err = reindexSqlInTxn(ctx, store.sql, entries)
			if err != nil {
				_ = lock.Close()
				_ = store.Close()

				return nil, fmt.Errorf("open store: rebuild index: %w", err)
			}
		} else if recovery.state == walCommitted {
			err = store.updateIndexFromOps(ctx, recovery.ops)
			if err != nil {
				_ = lock.Close()
				_ = store.Close()

				return nil, err
			}
		}

		if recovery.state == walCommitted {
			err = truncateWal(store.wal)
			if err != nil {
				_ = lock.Close()
				_ = store.Close()

				return nil, fmt.Errorf("open store: truncate wal: %w", err)
			}
		}

		closeErr := lock.Close()
		if closeErr != nil {
			_ = store.Close()

			return nil, fmt.Errorf("open store: unlock wal: %w", closeErr)
		}
	}

	return store, nil
}

// Close releases the SQLite and WAL handles opened by [Open].
// It is safe to call Close on a nil Store. Close is idempotent.
func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}

	errs := []error{}

	err := s.sql.Close()
	if err != nil {
		errs = append(errs, fmt.Errorf("close sqlite: %w", err))
	}

	s.sql = nil

	if s.wal != nil {
		err := s.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close wal: %w", err))
		}

		s.wal = nil
	}

	return errors.Join(errs...)
}

// openSQLite opens the derived index database and applies the configured pragmas.
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

// applyPragmas configures the SQLite connection using a single batch statement.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA mmap_size = 268435456;
		PRAGMA cache_size = -20000;
		PRAGMA temp_store = MEMORY;
	`)
	if err != nil {
		return fmt.Errorf("apply pragmas: %w", err)
	}

	return nil
}

// userVersion reads the current SQLite PRAGMA user_version.
func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}

	return version, nil
}

func schemaExists(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name IN ('tickets', 'ticket_blockers')`)
	if err != nil {
		return false, fmt.Errorf("read schema: %w", err)
	}

	defer func() { _ = rows.Close() }()

	found := map[string]struct{}{}

	for rows.Next() {
		var name string

		err = rows.Scan(&name)
		if err != nil {
			return false, fmt.Errorf("read schema: %w", err)
		}

		found[name] = struct{}{}
	}

	err = rows.Err()
	if err != nil {
		return false, fmt.Errorf("read schema: %w", err)
	}

	_, hasTickets := found["tickets"]
	_, hasBlockers := found["ticket_blockers"]

	return hasTickets && hasBlockers, nil
}

// dropAndRecreateSchema drops and recreates the derived index tables and indices.
func dropAndRecreateSchema(ctx context.Context, tx *sql.Tx) error {
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
