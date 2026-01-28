package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// defaultLockTimeout is the maximum time to wait when acquiring WAL locks.
// Operations fail with a timeout error if the lock cannot be acquired.
const defaultLockTimeout = 10 * time.Second

// Option configures Store behavior.
type Option func(*storeOptions)

type storeOptions struct {
	lockTimeout time.Duration
}

// WithLockTimeout sets the max wait time for WAL lock acquisition.
// Default is 10 seconds. Use shorter values in tests to avoid slow blocking tests.
func WithLockTimeout(d time.Duration) Option {
	return func(o *storeOptions) {
		o.lockTimeout = d
	}
}

// Store wires the derived SQLite index together with WAL and lock coordination.
// Keeping these handles centralized ensures recovery uses consistent fs primitives.
type Store struct {
	dir         string
	sql         *sql.DB
	fs          fs.FS
	locker      *fs.Locker
	atomic      *fs.AtomicWriter
	wal         fs.File
	lockPath    string // the wal path, for now. But opaque to callers.
	lockTimeout time.Duration
}

// Open initializes the SQLite index for a ticket directory.
// If the schema version is missing or mismatched, it rebuilds to avoid stale reads.
//
// Open acquires the WAL lock before recovery. It may return [ErrWALCorrupt]
// or [ErrWALReplay] if recovery fails.
func Open(ctx context.Context, dir string, opts ...Option) (*Store, error) {
	cfg := storeOptions{lockTimeout: defaultLockTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}

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

	db, err := openSqlite(ctx, filepath.Join(tkDir, "index.sqlite"))
	if err != nil {
		_ = walFile.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	store := &Store{
		dir:         ticketDir,
		sql:         db,
		fs:          fsReal,
		locker:      locker,
		atomic:      atomicWriter,
		wal:         walFile,
		lockPath:    walPath,
		lockTimeout: cfg.lockTimeout,
	}

	storedSchemaVersion, err := storedSchemaVersion(ctx, db)
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: %w", err)
	}

	versionMismatch := storedSchemaVersion != currentSchemaVersion

	walSize, err := store.walSize()
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: wal stat: %w", err)
	}

	if !versionMismatch && walSize == 0 {
		return store, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, cfg.lockTimeout)
	defer cancel()

	lock, err := locker.LockWithTimeout(lockCtx, walPath)
	if err != nil {
		_ = store.Close()

		return nil, fmt.Errorf("open store: lock wal: %w", err)
	}

	if versionMismatch {
		_, err = store.reindexLocked(ctx)
	} else {
		err = store.recoverWalLocked(ctx)
	}

	closeErr := lock.Close()

	if err != nil || closeErr != nil {
		_ = store.Close()

		return nil, errors.Join(err, closeErr)
	}

	return store, nil
}

// Close releases the SQLite and WAL handles opened by [Open].
// It is safe to call Close on a nil Store. Close is idempotent.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}

	var errs []error

	if s.sql != nil {
		err := s.sql.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close sqlite: %w", err))
		}

		s.sql = nil
	}

	if s.wal != nil {
		err := s.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close wal: %w", err))
		}

		s.wal = nil
	}

	return errors.Join(errs...)
}

// walSize returns the current WAL file size without acquiring a lock.
// It reads directly from the file descriptor.
func (s *Store) walSize() (int64, error) {
	info, err := s.wal.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}

	return info.Size(), nil
}

// acquireReadLock takes a shared lock and recovers any pending WAL before returning.
// The caller must close the returned lock when done. If WAL recovery is needed,
// this temporarily upgrades to an exclusive lock, recovers, then downgrades.
func (s *Store) acquireReadLock(ctx, lockCtx context.Context) (*fs.Lock, error) {
	readLock, err := s.locker.RLockWithTimeout(lockCtx, s.lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock wal: %w", err)
	}

	for {
		walSize, statErr := s.walSize()
		if statErr != nil {
			_ = readLock.Close()

			return nil, fmt.Errorf("wal stat: %w", statErr)
		}

		if walSize == 0 {
			return readLock, nil
		}

		closeErr := readLock.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("unlock wal: %w", closeErr)
		}

		writeLock, lockErr := s.locker.LockWithTimeout(lockCtx, s.lockPath)
		if lockErr != nil {
			return nil, fmt.Errorf("lock wal: %w", lockErr)
		}

		recoverErr := s.recoverWalLocked(ctx)
		if recoverErr != nil {
			_ = writeLock.Close()

			return nil, recoverErr
		}

		closeErr = writeLock.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("unlock wal: %w", closeErr)
		}

		readLock, err = s.locker.RLockWithTimeout(lockCtx, s.lockPath)
		if err != nil {
			return nil, fmt.Errorf("lock wal: %w", err)
		}
	}
}
