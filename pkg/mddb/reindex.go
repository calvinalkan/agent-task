package mddb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/calvinalkan/fileproc"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// IndexableDocument holds parsed document data for indexing and document creation.
//
// Used by [Config.DocumentFrom], [Config.SQLColumnValues], and [Config.AfterBulkIndex].
//
// All data is BORROWED from the file read buffer. Do not retain any fields
// after the callback returns.
//
// Borrowed fields:
//   - ID, ShortID, RelPath, Title, Body: byte slices pointing into file buffer
//   - Frontmatter: with borrowed keys and values (not copied)
//
// Non-borrowed fields:
//   - MtimeNS, SizeBytes: copied scalars
//
// Safe operations during callback:
//   - Pass to sql.Stmt.Exec() - SQLite driver copies the bytes
//   - Convert to string: string(doc.ID) - Go copies the data
//   - Copy explicitly: copied := append([]byte(nil), doc.ID...)
//
// Unsafe after callback returns:
//   - Storing slices in long-lived data structures
//   - Returning slices from the callback
type IndexableDocument struct {
	ID          []byte                  // Document ID (borrowed)
	ShortID     []byte                  // Short ID for prefix search (borrowed)
	RelPath     []byte                  // Relative file path (borrowed)
	MtimeNS     int64                   // File modification time in nanoseconds
	SizeBytes   int64                   // File size in bytes
	Title       []byte                  // Document title (borrowed)
	Body        []byte                  // Markdown body after frontmatter (borrowed)
	Frontmatter frontmatter.Frontmatter // All frontmatter fields (borrowed)
}

// IndexScanError aggregates all issues encountered during [MDDB.Reindex].
//
// Check with [errors.As]:
//
//	var scanErr *mddb.IndexScanError
//	if errors.As(err, &scanErr) {
//	    for _, issue := range scanErr.Issues {
//	        log.Printf("id=%s path=%s: %v", issue.ID, issue.Path, issue.Err)
//	    }
//	}
type IndexScanError struct {
	Issues []*Error
}

func (e *IndexScanError) Error() string {
	return fmt.Sprintf("scan: %d issues", len(e.Issues))
}

// Reindex rebuilds the SQLite index by scanning all document files.
//
// Called automatically by [Open] on schema version mismatch. Returns count
// of indexed documents. Holds exclusive lock for entire duration, blocking
// all reads ([MDDB.Get], [MDDB.GetByPrefix], [Query]) and writes ([MDDB.Begin]).
//
// Returns [ErrClosed] if store is closed. Returns [*IndexScanError] if files
// fail validation; use [errors.As] to inspect Issues for details.
func (mddb *MDDB[T]) Reindex(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, withContext(errors.New("context is nil"), "", "")
	}

	if mddb == nil || mddb.closed.Load() {
		return 0, withContext(ErrClosed, "", "")
	}

	if err := ctx.Err(); err != nil {
		return 0, withContext(fmt.Errorf("canceled: %w", context.Cause(ctx)), "", "")
	}

	// In-process lock first (fast), then cross-process flock (slower).
	mddb.mu.Lock()
	defer mddb.mu.Unlock()

	if mddb.closed.Load() || mddb.sql == nil || mddb.wal == nil {
		return 0, withContext(ErrClosed, "", "")
	}

	// Acquire exclusive WAL lock before modifying index. This prevents concurrent
	// writers from corrupting state during the rebuild.
	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	lock, err := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		return 0, withContext(fmt.Errorf("lock: wal: %w", err), "", "")
	}

	defer func() { _ = lock.Close() }()

	indexed, err := mddb.reindexLocked(ctx)
	if err != nil {
		return 0, withContext(err, "", "")
	}

	return indexed, nil
}

// reindexLocked rebuilds the index. Caller must hold exclusive WAL lock.
func (mddb *MDDB[T]) reindexLocked(ctx context.Context) (int, error) {
	// Replay any pending WAL entries first, so we don't lose writes that
	// happened between the last commit and this reindex.
	err := mddb.recoverWalLocked(ctx)
	if err != nil {
		return 0, err
	}

	entries, scanErr := mddb.scanDocumentFiles(ctx)
	if scanErr != nil {
		return 0, scanErr
	}

	indexed, err := mddb.rebuildIndexTemp(ctx, entries)
	if err != nil {
		return 0, err
	}

	return indexed, nil
}

// scanDocumentFiles walks the data directory and parses all .md files.
// Returns parsed documents and any errors encountered. Errors are collected
// rather than failing fast, so users see all issues at once.
func (mddb *MDDB[T]) scanDocumentFiles(ctx context.Context) ([]*IndexableDocument, error) {
	opts := []fileproc.Option{
		fileproc.WithRecursive(),
		fileproc.WithSuffix(".md"),
		fileproc.WithOnError(func(err error, _, _ int) bool {
			// Continue processing other files unless it's an internal path skip.
			return !errors.Is(err, errSkipInternalPath)
		}),
	}

	results, errs := fileproc.Process(ctx, mddb.dataDir, func(f *fileproc.File, _ *fileproc.Worker) (*IndexableDocument, error) {
		relPathEphemeral := f.RelPathBorrowed()
		if isInternalPathBytes(relPathEphemeral) {
			return nil, errSkipInternalPath
		}

		// Copy relPath - it's ephemeral and only valid during callback.
		relPath := append([]byte(nil), relPathEphemeral...)

		// Stat before read so fileproc can size the read buffer appropriately.
		stat, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("fs: stat file: %w", err)
		}

		data, err := f.Bytes()
		if err != nil {
			return nil, fmt.Errorf("fs: read file: %w", err)
		}

		parsed, err := mddb.parseIndexable(relPath, data, stat.ModTime, stat.Size, "")
		if err != nil {
			return nil, err
		}

		return &parsed, nil
	}, opts...)

	// fileproc doesn't add cancellation to error slice - check explicitly.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("canceled: %w", context.Cause(ctx))
	}

	if len(errs) == 0 {
		return results, nil
	}

	// Unwrap fileproc errors to extract path/ID for user-friendly messages.
	issues := make([]*Error, 0, len(errs))
	for _, err := range errs {
		issue := &Error{Err: err}

		// IOError: filesystem-level failure (permissions, missing file, etc.)
		var ioErr *fileproc.IOError
		if errors.As(err, &ioErr) {
			issue.Path = ioErr.Path
			issue.Err = fmt.Errorf("fs: %w", ioErr.Err)
		}

		// ProcessError: our callback returned an error.
		var procErr *fileproc.ProcessError
		if errors.As(err, &procErr) {
			issue.Path = procErr.Path
			issue.Err = procErr.Err

			// If parseIndexable returned an *Error, extract context.
			var mErr *Error
			if errors.As(procErr.Err, &mErr) {
				issue.ID = mErr.ID
				issue.Err = mErr.Err
			}
		}

		issues = append(issues, issue)
	}

	return nil, &IndexScanError{Issues: issues}
}

// rebuildIndexTemp rebuilds the index into a temp DB and atomically swaps it in.
//
// Why:
//   - Rebuilds are fully derived from markdown files (source of truth).
//   - We can use unsafe SQLite pragmas for speed without risking corruption.
//   - The temp DB is swapped in atomically, so readers see either old or new index.
func (mddb *MDDB[T]) rebuildIndexTemp(ctx context.Context, entries []*IndexableDocument) (int, error) {
	mddbDir := filepath.Dir(mddb.lockPath)
	indexPath := filepath.Join(mddbDir, "index.sqlite")
	tmpPath := indexPath + ".tmp"

	// Clean up any stale temp DB from a previous crash before rebuilding.
	if err := mddb.removeFileIfExists(tmpPath); err != nil {
		return 0, fmt.Errorf("fs: remove temp index: %w", err)
	}

	if err := mddb.removeFileIfExists(tmpPath + "-wal"); err != nil {
		return 0, fmt.Errorf("fs: remove temp wal: %w", err)
	}

	if err := mddb.removeFileIfExists(tmpPath + "-shm"); err != nil {
		return 0, fmt.Errorf("fs: remove temp shm: %w", err)
	}

	// Build into a fresh temp DB with unsafe pragmas (fast, disposable).
	tmpDB, err := openSqliteUnsafe(ctx, tmpPath)
	if err != nil {
		return 0, err
	}

	var removeErr error

	defer func() {
		removeErr = errors.Join(removeErr, mddb.fs.Remove(tmpPath))
	}()

	indexed, rebuildErr := mddb.rebuildIndexOnDB(ctx, tmpDB, entries, false)
	closeErr := tmpDB.Close()

	if rebuildErr != nil {
		return 0, rebuildErr
	}

	if closeErr != nil {
		return 0, fmt.Errorf("sqlite: close temp index: %w", closeErr)
	}

	// Close current DB before swap. Windows disallows renaming open files.
	oldDB := mddb.sql
	if closeOldErr := oldDB.Close(); closeOldErr != nil {
		return 0, fmt.Errorf("sqlite: close old index: %w", closeOldErr)
	}

	// Atomically replace old index with the rebuilt temp DB.
	if renameErr := mddb.fs.Rename(tmpPath, indexPath); renameErr != nil {
		// Best-effort reopen old DB so the store stays usable.
		reopen, reopenErr := openSqlite(ctx, indexPath)
		if reopenErr == nil {
			mddb.sql = reopen
		}

		return 0, fmt.Errorf("fs: swap index: %w", renameErr)
	}

	// Reopen the swapped DB with safe runtime pragmas.
	newDB, err := openSqlite(ctx, indexPath)
	if err != nil {
		return 0, err
	}

	mddb.sql = newDB

	return indexed, nil
}

// rebuildIndexOnDB drops and recreates the index table, then bulk inserts all documents.
// Runs in a single transaction so failures leave the target DB untouched.
func (mddb *MDDB[T]) rebuildIndexOnDB(ctx context.Context, db *sql.DB, entries []*IndexableDocument, replace bool) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin txn: %w", err)
	}

	// Track commit state for deferred rollback. Using a flag instead of
	// checking tx state because sql.Tx doesn't expose whether it's committed.
	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Drop and recreate tables. This is atomic within the transaction.
	err = mddb.schema.recreate(ctx, tx)
	if err != nil {
		return 0, err
	}

	// Let user create related tables (e.g., FTS, lookup tables) in same transaction.
	if mddb.cfg.AfterRecreateSchema != nil {
		err = mddb.cfg.AfterRecreateSchema(ctx, tx)
		if err != nil {
			return 0, fmt.Errorf("after recreate schema: %w", err)
		}
	}

	if len(entries) > 0 {
		err = mddb.bulkInsertDocs(ctx, tx, entries, replace)
		if err != nil {
			return 0, err
		}
	}

	// Store schema fingerprint so Open() can detect mismatches.
	version := schemaVersion(mddb.schema.fingerprint())

	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
	if err != nil {
		return 0, fmt.Errorf("sqlite: set user_version: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf("sqlite: commit txn: %w", err)
	}

	committed = true

	return len(entries), nil
}

// openSqliteUnsafe opens a temporary index DB for rebuilds using unsafe pragmas.
//
// Why:
//   - Reindex rebuilds are derived entirely from markdown files.
//   - Temp DB is disposable; crash means rerun rebuild.
//   - Unsafe pragmas (journal OFF, sync OFF) are much faster.
func openSqliteUnsafe(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("sqlite: path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	// Single connection ensures pragma consistency and avoids lock churn.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	err = db.PingContext(ctx)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		PRAGMA journal_mode = OFF;
		PRAGMA synchronous = OFF;
		PRAGMA locking_mode = EXCLUSIVE;
		PRAGMA temp_store = MEMORY;
		PRAGMA foreign_keys = OFF;
		PRAGMA cache_size = 100000;
	`)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("sqlite: apply unsafe pragmas: %w", err)
	}

	return db, nil
}

// 50-100 seems to be the optimum for SQLite with CGO.
const indexInsertBatchSize = 50

// bulkInsertDocs inserts documents in batches for efficiency.
// Uses prepared statements to reduce parse overhead.
func (mddb *MDDB[T]) bulkInsertDocs(ctx context.Context, tx *sql.Tx, entries []*IndexableDocument, withReplace bool) error {
	colCount := len(mddb.schema.columnNames())

	// Pre-compile statement for full batches (the common case).
	batchStmt, err := mddb.prepareUpsertStmt(ctx, tx, indexInsertBatchSize, withReplace)
	if err != nil {
		return err
	}

	defer func() { _ = batchStmt.Close() }()

	// Reuse slices across iterations to reduce allocations.
	args := make([]any, indexInsertBatchSize*colCount)
	batch := make([]IndexableDocument, 0, indexInsertBatchSize)

	for i := 0; i < len(entries); i += indexInsertBatchSize {
		// Build batch by dereferencing entry values.
		batch = batch[:0]

		end := min(i+indexInsertBatchSize, len(entries))
		for j := i; j < end; j++ {
			batch = append(batch, *entries[j])
		}

		// Use pre-compiled statement for full batches, prepare lazily for remainder.
		stmt := batchStmt

		isRemainderStmt := len(batch) < indexInsertBatchSize
		if isRemainderStmt {
			stmt, err = mddb.prepareUpsertStmt(ctx, tx, len(batch), withReplace)
			if err != nil {
				return err
			}
		}

		sqlArgs := args[:len(batch)*colCount]

		err = mddb.fillBatchUpsertSQLArgs(batch, colCount, sqlArgs)
		if err != nil {
			if isRemainderStmt {
				_ = stmt.Close()
			}

			return err
		}

		_, err = stmt.ExecContext(ctx, sqlArgs...)
		if isRemainderStmt {
			_ = stmt.Close()
		}

		if err != nil {
			return fmt.Errorf("sqlite: batch insert: %w", err)
		}

		// Let user populate related tables (e.g., FTS) after each batch.
		if mddb.cfg.AfterBulkIndex != nil {
			err := mddb.cfg.AfterBulkIndex(ctx, tx, batch)
			if err != nil {
				return fmt.Errorf("after bulk index: %w", err)
			}
		}
	}

	return nil
}

func (mddb *MDDB[T]) removeFileIfExists(path string) error {
	err := mddb.fs.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs: remove: %w", err)
	}

	return nil
}

var internalmddbDirInDataDir = []byte(".mddb")

// isInternalPathBytes checks if path is inside the .mddb directory.
func isInternalPathBytes(path []byte) bool {
	if bytes.Equal(path, internalmddbDirInDataDir) {
		return true
	}

	if !bytes.HasPrefix(path, internalmddbDirInDataDir) || len(path) <= len(internalmddbDirInDataDir) {
		return false
	}

	// Must be followed by path separator to avoid matching ".mddb-backup" etc.
	sep := path[len(internalmddbDirInDataDir)]

	return sep == '/' || sep == '\\'
}

var errSkipInternalPath = errors.New("skip internal .mddb path")
