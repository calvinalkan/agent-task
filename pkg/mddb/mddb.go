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
		return nil, errors.New("context is nil")
	}

	if cfg.BaseDir == "" {
		return nil, errors.New("Config.BaseDir is required")
	}

	if cfg.DocumentFrom == nil {
		return nil, errors.New("Config.DocumentFrom is required")
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

	if err := schema.validate(); err != nil {
		return nil, fmt.Errorf("validating schema: %w", err)
	}

	// If schema has user columns, SQLColumnValues is required
	userColCount := schema.userColumnCount()
	if userColCount > 0 && cfg.SQLColumnValues == nil {
		return nil, errors.New("Config.SQLColumnValues is required when SQLSchema has user columns")
	}

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
		return nil, fmt.Errorf("creating internal mddb dir: fs: %w", err)
	}

	walPath := filepath.Join(mddbDir, "wal")

	walFile, err := fsReal.OpenFile(walPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening wal: fs: %w", err)
	}

	sqlite, err := openSqlite(ctx, filepath.Join(mddbDir, "index.sqlite"))
	if err != nil {
		closeErr := walFile.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("fs: close wal: %w", closeErr)
		}

		return nil, errors.Join(fmt.Errorf("open: %w", err), closeErr)
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

	storedVersion, err := queryUserVersion(ctx, sqlite)
	if err != nil {
		closeErr := mddb.Close()

		return nil, errors.Join(fmt.Errorf("querying schema version: %w", err), closeErr)
	}

	expectedVersion := schema.fingerprint()
	versionMismatch := int64(storedVersion) != expectedVersion

	walSize, err := mddb.walSize()
	if err != nil {
		closeErr := mddb.Close()

		return nil, errors.Join(fmt.Errorf("checking wal size: %w", err), closeErr)
	}

	if !versionMismatch && walSize == 0 {
		// No Wal to replay, and same version => return early.
		return mddb, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	release, err := mddb.acquireWriteLockWithWalRecover(lockCtx)
	if err != nil {
		closeErr := mddb.Close()

		return nil, errors.Join(fmt.Errorf("acquiring write lock: %w", err), closeErr)
	}
	// At this point, a leftover wal is already replayed (inside acquireWriteLock).
	defer func() { _ = release() }()

	if versionMismatch {
		_, err = mddb.reindexLocked(ctx)
		if err != nil {
			// !! must release writer lock before close, because close locks mddb.mu (deadlock)
			// release is idempotent.
			_ = release()

			closeErr := mddb.Close()

			return nil, errors.Join(fmt.Errorf("reindexing: %w", err), closeErr)
		}
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
			errs = append(errs, fmt.Errorf("sqlite: %w", err))
		}

		mddb.sql = nil
	}

	if mddb.wal != nil {
		err := mddb.wal.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("fs: close wal: %w", err))
		}

		mddb.wal = nil
	}

	return errors.Join(errs...)
}

const (
	defaultWalLockTimeout = 10 * time.Second
	defaultTableName      = "documents"
	// sqliteBusyTimeoutMs is the time SQLite waits when the database is locked.
	// After this, operations return SQLITE_BUSY.
	sqliteBusyTimeoutMs = 10000 // milliseconds
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

// walSize() reads the size of the underling (opened) WAL fd.
// This can be used as a quick check before replaying the wal, but,
// it's only 100% accurate if holding the [MDDB.aqcuireWriteLock] lock.
func (mddb *MDDB[T]) walSize() (int64, error) {
	info, err := mddb.wal.Stat()
	if err != nil {
		return 0, fmt.Errorf("wal: %w", err)
	}

	return info.Size(), nil
}

// acquireReadLock acquires both the in-process read lock (mu.RLock) and the
// cross-process file lock, replaying any pending WAL first. Returns an
// idempotent release function that must be called to unlock both.
func (mddb *MDDB[T]) acquireReadLock(ctx context.Context) (func() error, error) {
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

		return nil, fmt.Errorf("lock: %w", err)
	}

	// Check for pending WAL and replay if needed.
	for {
		walSize, statErr := mddb.walSize()
		if statErr != nil {
			closeErr := flock.Close()
			if closeErr != nil {
				closeErr = fmt.Errorf("releasing lock: %w", closeErr)
			}

			mddb.mu.RUnlock()

			return nil, errors.Join(statErr, closeErr)
		}

		if walSize == 0 {
			// Idempotent release: prevents panic from double mu.RUnlock()
			// if caller accidentally invokes release multiple times.
			var (
				once     sync.Once
				closeErr error
			)

			return func() error {
				once.Do(func() {
					closeErr = flock.Close()

					mddb.mu.RUnlock()
				})

				if closeErr != nil {
					return fmt.Errorf("lock: %w", closeErr)
				}

				return nil
			}, nil
		}

		// WAL not empty - upgrade to write lock, replay, then re-acquire read lock.
		_ = flock.Close()

		writeLock, lockErr := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
		if lockErr != nil {
			mddb.mu.RUnlock()

			return nil, fmt.Errorf("lock: %w", lockErr)
		}

		err = mddb.recoverWalLocked(ctx)
		if err != nil {
			closeErr := writeLock.Close()
			if closeErr != nil {
				closeErr = fmt.Errorf("releasing lock: %w", closeErr)
			}

			mddb.mu.RUnlock()

			return nil, errors.Join(fmt.Errorf("recovering wal: %w", err), closeErr)
		}

		_ = writeLock.Close()

		flock, err = mddb.locker.RLockWithTimeout(lockCtx, mddb.lockPath)
		if err != nil {
			mddb.mu.RUnlock()

			return nil, fmt.Errorf("lock: %w", err)
		}
	}
}

// acquireWriteLockWithWalRecover acquires both the in-process write lock (mu.Lock) and the
// cross-process file lock, replaying any pending WAL first. Returns an
// idempotent release function that must be called to unlock both.
func (mddb *MDDB[T]) acquireWriteLockWithWalRecover(ctx context.Context) (func() error, error) {
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

		return nil, fmt.Errorf("lock: %w", err)
	}

	err = mddb.recoverWalLocked(ctx)
	if err != nil {
		closeErr := flock.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("releasing lock: %w", closeErr)
		}

		mddb.mu.Unlock()

		return nil, errors.Join(fmt.Errorf("recovering wal: %w", err), closeErr)
	}

	// Idempotent release: prevents panic from double mu.Unlock()
	// if caller accidentally invokes release multiple times.
	var (
		once     sync.Once
		closeErr error
	)

	return func() error {
		once.Do(func() {
			closeErr = flock.Close()

			mddb.mu.Unlock()
		})

		if closeErr != nil {
			return fmt.Errorf("lock: %w", closeErr)
		}

		return nil
	}, nil
}

// openSqlite opens the derived index database and applies the configured pragmas.
func openSqlite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	// Ensure per-connection PRAGMAs apply consistently.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	err = db.PingContext(ctx)
	if err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("sqlite: close: %w", closeErr)
		}

		return nil, errors.Join(fmt.Errorf("sqlite: ping: %w", err), closeErr)
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		PRAGMA busy_timeout = %d;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA mmap_size = 268435456;
		PRAGMA cache_size = -20000;
		PRAGMA temp_store = MEMORY;
	`, sqliteBusyTimeoutMs))
	if err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("sqlite: close: %w", closeErr)
		}

		return nil, errors.Join(fmt.Errorf("sqlite: apply pragmas: %w", err), closeErr)
	}

	return db, nil
}

// queryUserVersion reads the current SQLite PRAGMA user_version.
func queryUserVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("sqlite: %w", err)
	}

	return version, nil
}
