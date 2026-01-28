package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// Store wires the derived SQLite index together with WAL and lock coordination.
// Keeping these handles centralized ensures recovery uses consistent fs primitives.
type Store struct {
	dir string
	sql *sql.DB
	fs  fs.FS

	locker  *fs.Locker
	atomic  *fs.AtomicWriter
	wal     fs.File
	walPath string
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

	version, err := userVersion(ctx, db)
	if err != nil {
		_ = walFile.Close()
		_ = db.Close()

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

	rebuildIndex := version != schemaVersion

	walHasEntries, err := walHasData(walFile)
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	if rebuildIndex || walHasEntries {
		lock, err := locker.Lock(walPath)
		if err != nil {
			_ = store.Close()

			return nil, fmt.Errorf("open store: lock wal: %w", err)
		}

		recoverErr := store.recoverWal(ctx, rebuildIndex)

		closeErr := lock.Close()
		if closeErr != nil && recoverErr == nil {
			recoverErr = fmt.Errorf("open store: unlock wal: %w", closeErr)
		}

		if recoverErr != nil {
			_ = store.Close()

			return nil, recoverErr
		}
	}

	return store, nil
}

// Close releases the SQLite handle opened by Open.
func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}

	errs := []error{}

	err := s.sql.Close()
	if err != nil {
		errs = append(errs, fmt.Errorf("close sqlite: %w", err))
	}

	if s.wal != nil {
		err := s.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close wal: %w", err))
		}
	}

	return errors.Join(errs...)
}
