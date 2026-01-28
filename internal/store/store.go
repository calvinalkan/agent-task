package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store holds the derived SQLite index for query operations.
// File and WAL access come later; for now we keep the index handle centralized.
type Store struct {
	dir string
	sql *sql.DB
}

// Open initializes the SQLite index for a ticket directory.
// If the schema version is missing or mismatched, it rebuilds to avoid stale reads.
func Open(ctx context.Context, dir string) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("open store: context is nil")
	}

	if dir == "" {
		return nil, errors.New("open store: directory is empty")
	}

	ticketDir := filepath.Clean(dir)
	tkDir := filepath.Join(ticketDir, ".tk")

	err := os.MkdirAll(tkDir, 0o750)
	if err != nil {
		return nil, fmt.Errorf("open store: create .tk directory: %w", err)
	}

	db, err := openSQLite(ctx, filepath.Join(tkDir, "index.sqlite"))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	version, err := userVersion(ctx, db)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	if version != schemaVersion {
		entries, scanErr := scanTicketFiles(ctx, ticketDir)
		if scanErr != nil {
			_ = db.Close()

			return nil, fmt.Errorf("open store: %w", scanErr)
		}

		_, err = rebuildIndexInTxn(ctx, db, entries)
		if err != nil {
			_ = db.Close()

			return nil, fmt.Errorf("open store: %w", err)
		}
	}

	return &Store{dir: ticketDir, sql: db}, nil
}

// Close releases the SQLite handle opened by Open.
func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}

	err := s.sql.Close()
	if err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}

	return nil
}
