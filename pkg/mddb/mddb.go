package mddb

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

const (
	defaultLockTimeout = 10 * time.Second
	defaultTableName   = "documents"
)

// ErrClosed indicates an operation was attempted on a closed MDDB.
var ErrClosed = errors.New("mddb closed")

// MDDB provides document storage with SQLite indexing and WAL-based crash recovery.
//
// Stores [Document] implementations as markdown files with YAML frontmatter.
// SQLite provides fast querying; a write-ahead log ensures atomicity and crash
// recovery. Markdown files are the source of truth; the index is ephemeral and
// rebuildable via [MDDB.Reindex].
//
// # Concurrency
//
// Uses file locking for multi-process coordination. Readers hold shared locks;
// writers hold exclusive locks. Safe for concurrent reads within a process.
// Only one write transaction active at a time; others block until lock available.
//
// # Operations
//
//   - [Open]: Create/open store, recover pending WAL
//   - [MDDB.Get], [MDDB.GetByPrefix]: Read by ID
//   - [Query]: Custom SQL queries with read lock
//   - [MDDB.Begin]: Start write transaction
//   - [MDDB.Reindex]: Rebuild index from files
//   - [MDDB.Close]: Release resources
type MDDB[T Document] struct {
	cfg         Config[T]
	dataDir     string
	tableName   string
	sql         *sql.DB
	fs          fs.FS
	locker      *fs.Locker
	atomic      *fs.AtomicWriter
	wal         fs.File
	lockPath    string
	lockTimeout time.Duration
}

// Open initializes a document store for the configured data directory.
//
// Creates the data directory and .mddb subdirectory if needed. On open:
//   - Replays pending WAL if previous transaction crashed
//   - Rebuilds index if [Config.SchemaVersion] changed
//
// Required [Config] fields: Dir, Parse, RecreateIndex, Prepare.
//
// Returns errors for config validation, directory/SQLite init failures,
// WAL recovery, reindex failures, or lock timeout.
func Open[T Document](ctx context.Context, cfg Config[T]) (*MDDB[T], error) {
	if ctx == nil {
		return nil, errors.New("open: context is nil")
	}

	if cfg.Dir == "" {
		return nil, errors.New("open: Config.Dir is required")
	}

	if cfg.Parse == nil {
		return nil, errors.New("open: Config.Parse is required")
	}

	if cfg.RecreateIndex == nil {
		return nil, errors.New("open: Config.RecreateIndex is required")
	}

	if cfg.Prepare == nil {
		return nil, errors.New("open: Config.Prepare is required")
	}

	tableName := cfg.TableName
	if tableName == "" {
		tableName = defaultTableName
	}

	if !isValidIdentifier(tableName) {
		return nil, fmt.Errorf("open: invalid table name %q: must be lowercase a-z and underscore", tableName)
	}

	lockTimeout := cfg.LockTimeout
	if lockTimeout == 0 {
		lockTimeout = defaultLockTimeout
	}

	dataDir := filepath.Clean(cfg.Dir)
	mddbDir := filepath.Join(dataDir, ".mddb")
	fsReal := fs.NewReal()
	locker := fs.NewLocker(fsReal)
	atomicWriter := fs.NewAtomicWriter(fsReal)

	err := fsReal.MkdirAll(mddbDir, 0o750)
	if err != nil {
		return nil, fmt.Errorf("open: create .mddb directory: %w", err)
	}

	walPath := filepath.Join(mddbDir, "wal")

	walFile, err := fsReal.OpenFile(walPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open: wal: %w", err)
	}

	sqlite, err := openSqlite(ctx, filepath.Join(mddbDir, "index.sqlite"))
	if err != nil {
		_ = walFile.Close()

		return nil, fmt.Errorf("open: %w", err)
	}

	mddb := &MDDB[T]{
		cfg:         cfg,
		dataDir:     dataDir,
		tableName:   tableName,
		sql:         sqlite,
		fs:          fsReal,
		locker:      locker,
		atomic:      atomicWriter,
		wal:         walFile,
		lockPath:    walPath, // for now, wal is the lock (we do not remove the wal, only truncate it)
		lockTimeout: lockTimeout,
	}

	storedVersion, err := storedSchemaVersion(ctx, sqlite)
	if err != nil {
		_ = mddb.Close()

		return nil, fmt.Errorf("open: %w", err)
	}

	expectedVersion := combinedSchemaVersion(cfg.SchemaVersion)
	versionMismatch := storedVersion != expectedVersion

	walSize, err := mddb.walSize()
	if err != nil {
		_ = mddb.Close()

		return nil, fmt.Errorf("open: wal stat: %w", err)
	}

	if !versionMismatch && walSize == 0 {
		return mddb, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	lock, err := locker.LockWithTimeout(lockCtx, walPath)
	if err != nil {
		_ = mddb.Close()

		return nil, fmt.Errorf("open: lock wal: %w", err)
	}

	if versionMismatch {
		_, err = mddb.reindexLocked(ctx)
	} else {
		err = mddb.recoverWalLocked(ctx)
	}

	closeErr := lock.Close()

	if err != nil || closeErr != nil {
		_ = mddb.Close()

		return nil, errors.Join(err, closeErr)
	}

	return mddb, nil
}

// Close releases SQLite and WAL file handles. Safe on nil, idempotent.
// Does not wait for active transactions; caller must commit/rollback first.
func (mddb *MDDB[T]) Close() error {
	if mddb == nil {
		return nil
	}

	var errs []error

	if mddb.sql != nil {
		err := mddb.sql.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close sqlite: %w", err))
		}

		mddb.sql = nil
	}

	if mddb.wal != nil {
		err := mddb.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("close wal: %w", err))
		}

		mddb.wal = nil
	}

	return errors.Join(errs...)
}

func (mddb *MDDB[T]) walSize() (int64, error) {
	info, err := mddb.wal.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}

	return info.Size(), nil
}

func (mddb *MDDB[T]) acquireReadLock(ctx, lockCtx context.Context) (*fs.Lock, error) {
	readLock, err := mddb.locker.RLockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock wal: %w", err)
	}

	for {
		walSize, statErr := mddb.walSize()
		if statErr != nil {
			_ = readLock.Close()

			return nil, fmt.Errorf("wal stat: %w", statErr)
		}

		if walSize == 0 {
			return readLock, nil
		}

		err = readLock.Close()
		if err != nil {
			return nil, fmt.Errorf("unlock wal: %w", err)
		}

		writeLock, lockErr := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
		if lockErr != nil {
			return nil, fmt.Errorf("lock wal: %w", lockErr)
		}

		err = mddb.recoverWalLocked(ctx)
		if err != nil {
			_ = writeLock.Close()

			return nil, err
		}

		err = writeLock.Close()
		if err != nil {
			return nil, fmt.Errorf("unlock wal: %w", err)
		}

		readLock, err = mddb.locker.RLockWithTimeout(lockCtx, mddb.lockPath)
		if err != nil {
			return nil, fmt.Errorf("lock wal: %w", err)
		}
	}
}

// isValidIdentifier checks if s is a valid SQL identifier (a-z, underscore only).
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}

	return true
}
