package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

const (
	defaultWalLockTimeout = 10 * time.Second
	defaultTableName      = "documents"
	// sqliteBusyTimeout is the time SQLite waits when the database is locked.
	// After this, operations return SQLITE_BUSY.
	sqliteBusyTimeout = 10000 // milliseconds
)

var (
	// frontmatterKeyID is the "id" frontmatter key.
	// Do not modify; reuse to avoid per-call allocations in hot paths.
	frontmatterKeyID = []byte("id")
	// frontmatterKeySchemaVersion is the "schema_version" frontmatter key.
	// Do not modify; reuse to avoid per-call allocations in hot paths.
	frontmatterKeySchemaVersion = []byte("schema_version")
	// frontmatterKeyTitle is the "title" frontmatter key.
	// Do not modify; reuse to avoid per-call allocations in hot paths.
	frontmatterKeyTitle = []byte("title")
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
// Safe for concurrent use. Uses [sync.RWMutex] for in-process coordination and
// flock for cross-process coordination:
//   - Readers ([MDDB.Get], [MDDB.GetByPrefix], [Query]) hold shared lock
//   - Writers ([MDDB.Begin], [MDDB.Reindex]) hold exclusive lock
//   - [MDDB.Begin] holds lock until [Tx.Commit] or [Tx.Rollback]
//   - [MDDB.Close] waits for in-flight operations
type MDDB[T Document] struct {
	cfg         Config[T]
	dataDir     string
	schema      *SQLSchema
	sql         *sql.DB
	fs          fs.FS
	locker      *fs.Locker
	atomic      *fs.AtomicWriter
	wal         fs.File
	lockPath    string
	lockTimeout time.Duration
	closed      atomic.Bool

	// mu guards in-process concurrent access to the MDDB.
	//
	// File locking (flock) coordinates across processes but not within a process -
	// multiple goroutines in the same process share the same flock. This RWMutex
	// provides in-process coordination:
	//
	//   - Writers (Begin, Reindex, Close) acquire exclusive lock via mu.Lock()
	//   - Readers (Get, GetByPrefix, Query) acquire shared lock via mu.RLock()
	//   - Begin holds the lock until Commit/Rollback releases it
	//
	// Lock ordering: mu is always acquired BEFORE flock to avoid deadlocks and
	// ensure goroutines block early (mutex) rather than all hitting the kernel (flock).
	mu sync.RWMutex
}

// Open initializes a document store for the configured data directory.
//
// Creates the data directory and .mddb subdirectory if needed. On open:
//   - Replays pending WAL if previous transaction crashed
//   - Rebuilds index if schema fingerprint changed (columns, types, indexes)
//
// Required [Config] fields: BaseDir, DocumentFrom.
//
// Returns errors for config validation, directory/SQLite init failures,
// WAL recovery, reindex failures, or lock timeout.
func Open[T Document](ctx context.Context, cfg Config[T]) (*MDDB[T], error) {
	if ctx == nil {
		return nil, withContext(errors.New("context is nil"), "", "")
	}

	if cfg.BaseDir == "" {
		return nil, withContext(errors.New("Config.BaseDir is required"), "", "")
	}

	if cfg.DocumentFrom == nil {
		return nil, withContext(errors.New("Config.DocumentFrom is required"), "", "")
	}

	// Default path layout: flat (id.md)
	if cfg.RelPathFromID == nil {
		cfg.RelPathFromID = func(id string) string { return id + ".md" }
	}

	// Default short ID: full ID
	if cfg.ShortIDFromID == nil {
		cfg.ShortIDFromID = func(id string) string { return id }
	}

	// Default schema if not provided
	schema := cfg.SQLSchema
	if schema == nil {
		schema = NewBaseSQLSchema(defaultTableName)
	}

	// Validate schema
	if err := schema.validate(); err != nil {
		return nil, withContext(err, "", "")
	}

	// If schema has user columns, SQLColumnValues is required
	userColCount := schema.userColumnCount()
	if userColCount > 0 && cfg.SQLColumnValues == nil {
		return nil, withContext(errors.New("Config.SQLColumnValues is required when SQLSchema has user columns"), "", "")
	}

	// Default parse options: no line limit, require opening delimiter
	if cfg.ParseOptions == nil {
		cfg.ParseOptions = []frontmatter.ParseOption{
			frontmatter.WithLineLimit(0),
			frontmatter.WithRequireDelimiter(true),
			frontmatter.WithTrimLeadingBlankTail(true),
		}
	}

	lockTimeout := cfg.LockTimeout
	if lockTimeout == 0 {
		lockTimeout = defaultWalLockTimeout
	}

	dataDir := filepath.Clean(cfg.BaseDir)
	mddbDir := filepath.Join(dataDir, ".mddb")
	fsReal := fs.NewReal()
	locker := fs.NewLocker(fsReal)
	atomicWriter := fs.NewAtomicWriter(fsReal)

	err := fsReal.MkdirAll(mddbDir, 0o750)
	if err != nil {
		return nil, withContext(fmt.Errorf("fs: mkdir %s: %w", mddbDir, err), "", "")
	}

	walPath := filepath.Join(mddbDir, "wal")

	walFile, err := fsReal.OpenFile(walPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, withContext(fmt.Errorf("fs: open wal %s: %w", walPath, err), "", "")
	}

	sqlite, err := openSqlite(ctx, filepath.Join(mddbDir, "index.sqlite"))
	if err != nil {
		_ = walFile.Close()

		return nil, withContext(err, "", "")
	}

	mddb := &MDDB[T]{
		cfg:         cfg,
		dataDir:     dataDir,
		schema:      schema,
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

		return nil, withContext(err, "", "")
	}

	expectedVersion := schemaVersion(schema.fingerprint())
	versionMismatch := int64(storedVersion) != expectedVersion

	walSize, err := mddb.walSize()
	if err != nil {
		_ = mddb.Close()

		return nil, withContext(err, "", "")
	}

	if !versionMismatch && walSize == 0 {
		return mddb, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	lock, err := locker.LockWithTimeout(lockCtx, walPath)
	if err != nil {
		_ = mddb.Close()

		return nil, withContext(fmt.Errorf("lock: wal: %w", err), "", "")
	}

	if versionMismatch {
		_, err = mddb.reindexLocked(ctx)
	} else {
		err = mddb.recoverWalLocked(ctx)
	}

	closeErr := lock.Close()

	if err != nil || closeErr != nil {
		_ = mddb.Close()

		if closeErr != nil {
			closeErr = fmt.Errorf("lock: unlock wal: %w", closeErr)
		}

		return nil, withContext(errors.Join(err, closeErr), "", "")
	}

	return mddb, nil
}

// Close releases SQLite and WAL file handles. Safe on nil, idempotent.
// Waits for in-flight operations to complete before closing.
func (mddb *MDDB[T]) Close() error {
	if mddb == nil {
		return nil
	}

	// Wait for all in-flight operations (readers and writers) to complete.
	mddb.mu.Lock()
	defer mddb.mu.Unlock()

	if mddb.closed.Load() {
		return nil
	}

	mddb.closed.Store(true)

	var errs []error

	if mddb.sql != nil {
		err := mddb.sql.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("sqlite: close: %w", err))
		}

		mddb.sql = nil
	}

	if mddb.wal != nil {
		err := mddb.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("wal: close: %w", err))
		}

		mddb.wal = nil
	}

	return withContext(errors.Join(errs...), "", "")
}

func (mddb *MDDB[T]) walSize() (int64, error) {
	info, err := mddb.wal.Stat()
	if err != nil {
		return 0, fmt.Errorf("wal: stat: %w", err)
	}

	return info.Size(), nil
}

// acquireReadLock acquires both the in-process read lock (mu.RLock) and the
// cross-process file lock, replaying any pending WAL first. Returns a release
// function that must be called to unlock both.
func (mddb *MDDB[T]) acquireReadLock(ctx context.Context) (func(), error) {
	mddb.mu.RLock()

	if mddb.closed.Load() || mddb.sql == nil || mddb.wal == nil {
		mddb.mu.RUnlock()

		return nil, ErrClosed
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	flock, err := mddb.locker.RLockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		mddb.mu.RUnlock()

		return nil, fmt.Errorf("lock: read: %w", err)
	}

	// Check for pending WAL and replay if needed.
	for {
		walSize, statErr := mddb.walSize()
		if statErr != nil {
			_ = flock.Close()

			mddb.mu.RUnlock()

			return nil, statErr
		}

		if walSize == 0 {
			return func() {
				_ = flock.Close()

				mddb.mu.RUnlock()
			}, nil
		}

		// WAL not empty - upgrade to write lock, replay, then re-acquire read lock.
		_ = flock.Close()

		writeLock, lockErr := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
		if lockErr != nil {
			mddb.mu.RUnlock()

			return nil, fmt.Errorf("lock: write: %w", lockErr)
		}

		err = mddb.recoverWalLocked(ctx)
		if err != nil {
			_ = writeLock.Close()

			mddb.mu.RUnlock()

			return nil, err
		}

		_ = writeLock.Close()

		flock, err = mddb.locker.RLockWithTimeout(lockCtx, mddb.lockPath)
		if err != nil {
			mddb.mu.RUnlock()

			return nil, fmt.Errorf("lock: read: %w", err)
		}
	}
}

// acquireWriteLock acquires both the in-process write lock (mu.Lock) and the
// cross-process file lock, replaying any pending WAL first. Returns a release
// function that must be called to unlock both.
func (mddb *MDDB[T]) acquireWriteLock(ctx context.Context) (func(), error) {
	mddb.mu.Lock()

	if mddb.closed.Load() || mddb.sql == nil || mddb.wal == nil {
		mddb.mu.Unlock()

		return nil, ErrClosed
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	flock, err := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		mddb.mu.Unlock()

		return nil, fmt.Errorf("lock: write: %w", err)
	}

	err = mddb.recoverWalLocked(ctx)
	if err != nil {
		_ = flock.Close()

		mddb.mu.Unlock()

		return nil, err
	}

	return func() {
		_ = flock.Close()

		mddb.mu.Unlock()
	}, nil
}

// openSqlite opens the derived index database and applies the configured pragmas.
func openSqlite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("sqlite: path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	// Ensure per-connection PRAGMAs apply consistently.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	err = db.PingContext(ctx)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		PRAGMA busy_timeout = %d;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA mmap_size = 268435456;
		PRAGMA cache_size = -20000;
		PRAGMA temp_store = MEMORY;
	`, sqliteBusyTimeout))
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("sqlite: apply pragmas: %w", err)
	}

	return db, nil
}

// storedSchemaVersion reads the current SQLite PRAGMA user_version.
func storedSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("sqlite: user_version: %w", err)
	}

	return version, nil
}
