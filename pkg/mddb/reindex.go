package mddb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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

// IncrementalIndexResult summarizes changes applied by [MDDB.ReindexIncremental].
type IncrementalIndexResult struct {
	Inserted int
	Updated  int
	Deleted  int
	Skipped  int
	Total    int
}

func (e *IndexScanError) Error() string {
	return fmt.Sprintf("scan: %d issues", len(e.Issues))
}

// ReindexIncremental updates only changed documents by comparing mtime/size.
//
// Strategy (why this shape):
//   - We load all index metadata (path -> id, mtime_ns, size_bytes) once.
//     This avoids per-file SQLite lookups, which would require a path index
//     and slow down inserts (our bottleneck).
//   - During scan we call Stat() only. If mtime+size match, we skip reading
//     the file entirely (fast path, no inserts).
//   - We track seen paths and then delete missing rows by ID (PK), so we
//     don't need a path index for deletes.
//
// Tradeoffs:
//   - Uses memory proportional to number of docs (path map).
//   - In exchange, it minimizes SQLite writes: only changed/new rows are
//     inserted/updated, deletes are batched by ID.
//
// Uses the existing SQLite index as the baseline, then scans files and:
//   - Skips unchanged files (mtime_ns + size_bytes match)
//   - Inserts new files
//   - Updates changed files
//   - Deletes missing files
//
// Returns counts for each category plus the resulting total row count.
func (mddb *MDDB[T]) ReindexIncremental(ctx context.Context) (IncrementalIndexResult, error) {
	var zero IncrementalIndexResult

	if ctx == nil {
		return zero, errors.New("context is nil")
	}

	if mddb == nil || mddb.closed.Load() {
		return zero, ErrClosed
	}

	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("canceled: %w", context.Cause(ctx))
	}

	release, err := mddb.acquireWriteLockWithWalRecover(ctx)
	if err != nil {
		return zero, fmt.Errorf("acquiring write lock: %w", err)
	}

	defer func() { _ = release() }()

	result, err := mddb.reindexIncrementalLocked(ctx)
	if err != nil {
		return zero, err
	}

	return result, nil
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
		return 0, errors.New("context is nil")
	}

	if mddb == nil || mddb.closed.Load() {
		return 0, ErrClosed
	}

	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("canceled: %w", context.Cause(ctx))
	}

	release, err := mddb.acquireWriteLockWithWalRecover(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquiring write lock: %w", err)
	}

	defer func() { _ = release() }()

	indexed, err := mddb.reindexLocked(ctx)
	if err != nil {
		return 0, err
	}

	return indexed, nil
}

// reindexLocked rebuilds the index. Caller must hold exclusive WAL lock.
// Assumes pending WAL has already been recovered.
func (mddb *MDDB[T]) reindexLocked(ctx context.Context) (int, error) {
	entries, scanErr := mddb.scanDocumentFiles(ctx)
	if scanErr != nil {
		return 0, fmt.Errorf("scan documents: %w", scanErr)
	}

	indexed, err := mddb.rebuildIndexTemp(ctx, entries)
	if err != nil {
		return 0, fmt.Errorf("rebuild index: %w", err)
	}

	return indexed, nil
}

type indexMeta struct {
	id        string
	mtimeNS   int64
	sizeBytes int64
}

// reindexIncrementalLocked updates only changed files. Caller must hold exclusive WAL lock.
func (mddb *MDDB[T]) reindexIncrementalLocked(ctx context.Context) (IncrementalIndexResult, error) {
	result := IncrementalIndexResult{}

	existing, err := mddb.loadIndexMeta(ctx)
	if err != nil {
		return result, fmt.Errorf("load index metadata: %w", err)
	}

	var (
		seenMu   sync.Mutex
		seen     = make(map[string]struct{}, len(existing))
		inserted int
		updated  int
		skipped  int
	)

	opts := []fileproc.Option{
		fileproc.WithRecursive(),
		fileproc.WithSuffix(".md"),
	}

	root := []byte(mddb.dataDir)

	// TODO: remove locking, instead, return values with a stat per res.
	results, errs := fileproc.Process(ctx, mddb.dataDir, func(f *fileproc.File, w *fileproc.FileWorker) (*IndexableDocument, error) {
		absPath := f.AbsPathBorrowed()

		relBorrowed := relPathFromAbs(absPath, root)

		if isInternalPathBytes(relBorrowed) {
			return nil, fileproc.ErrSkip
		}

		pathStr := string(relBorrowed)

		stat, statErr := f.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("fs: %w", statErr)
		}

		seenMu.Lock()

		seen[pathStr] = struct{}{}

		seenMu.Unlock()

		meta, ok := existing[pathStr]
		if ok && meta.mtimeNS == stat.ModTime && meta.sizeBytes == stat.Size {
			seenMu.Lock()

			skipped++

			seenMu.Unlock()

			return nil, fileproc.ErrSkip
		}

		data, readErr := f.Bytes()
		if readErr != nil {
			return nil, fmt.Errorf("fs: %w", readErr)
		}

		// Retain relPath and data so parsed slices survive beyond callback.
		relPath := w.RetainBytes(relBorrowed)
		data = w.RetainBytes(data)

		parsed, parseErr := mddb.parseIndexable(relPath, data, stat.ModTime, stat.Size, "")
		if parseErr != nil {
			return nil, fmt.Errorf("parsing document: %w", parseErr)
		}

		seenMu.Lock()

		if ok {
			updated++
		} else {
			inserted++
		}

		seenMu.Unlock()

		return &parsed, nil
	}, opts...)

	if ctx.Err() != nil {
		return result, fmt.Errorf("canceled: %w", context.Cause(ctx))
	}

	scanErr := buildIndexScanError(errs)
	if scanErr != nil {
		return result, scanErr
	}

	deleteIDs := make([]string, 0)

	for path, meta := range existing {
		if _, ok := seen[path]; ok {
			continue
		}

		deleteIDs = append(deleteIDs, meta.id)
	}

	result.Inserted = inserted
	result.Updated = updated
	result.Deleted = len(deleteIDs)
	result.Skipped = skipped
	result.Total = len(existing) - result.Deleted + result.Inserted

	if len(deleteIDs) == 0 && len(results) == 0 {
		return result, nil
	}

	tx, err := mddb.sql.BeginTx(ctx, nil)
	if err != nil {
		return IncrementalIndexResult{}, fmt.Errorf("sqlite: begin txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if len(deleteIDs) > 0 {
		deleteStmt, prepErr := mddb.prepareDeleteByIDStmt(ctx, tx, indexDeleteBatchSize)
		if prepErr != nil {
			return IncrementalIndexResult{}, fmt.Errorf("prepare delete: %w", prepErr)
		}

		defer func() { _ = deleteStmt.Close() }()

		args := make([]any, indexDeleteBatchSize)

		for i := 0; i < len(deleteIDs); i += indexDeleteBatchSize {
			end := min(i+indexDeleteBatchSize, len(deleteIDs))
			batch := deleteIDs[i:end]

			stmt := deleteStmt
			if len(batch) < indexDeleteBatchSize {
				stmt, prepErr = mddb.prepareDeleteByIDStmt(ctx, tx, len(batch))
				if prepErr != nil {
					return IncrementalIndexResult{}, fmt.Errorf("prepare delete: %w", prepErr)
				}
			}

			for j, id := range batch {
				args[j] = id
			}

			_, execErr := stmt.ExecContext(ctx, args[:len(batch)]...)
			if len(batch) < indexDeleteBatchSize {
				_ = stmt.Close()
			}

			if execErr != nil {
				return IncrementalIndexResult{}, fmt.Errorf("sqlite: %w", execErr)
			}

			if mddb.cfg.AfterDelete != nil {
				for _, id := range batch {
					callbackErr := mddb.cfg.AfterDelete(ctx, tx, id)
					if callbackErr != nil {
						return IncrementalIndexResult{}, fmt.Errorf("AfterDelete: %w (doc_id=%s)", callbackErr, id)
					}
				}
			}

			if mddb.cfg.AfterIncrementalIndex != nil {
				callbackErr := mddb.cfg.AfterIncrementalIndex(ctx, tx, []IndexableDocument{}, batch)
				if callbackErr != nil {
					return IncrementalIndexResult{}, fmt.Errorf("AfterIncrementalIndex: %w", callbackErr)
				}
			}
		}
	}

	if len(results) > 0 {
		err = mddb.bulkInsertDocs(ctx, tx, results, true, func(batch []IndexableDocument) error {
			if mddb.cfg.AfterIncrementalIndex == nil {
				return nil
			}

			callbackErr := mddb.cfg.AfterIncrementalIndex(ctx, tx, batch, []string{})
			if callbackErr != nil {
				return fmt.Errorf("AfterIncrementalIndex: %w", callbackErr)
			}

			return nil
		})
		if err != nil {
			return IncrementalIndexResult{}, fmt.Errorf("bulk insert: %w", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return IncrementalIndexResult{}, fmt.Errorf("sqlite: commit txn: %w", err)
	}

	committed = true

	return result, nil
}

func buildIndexScanError(errs []error) error {
	if len(errs) == 0 {
		return nil
	}

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

	return &IndexScanError{Issues: issues}
}

// scanDocumentFiles walks the data directory and parses all .md files.
// Returns parsed documents and any errors encountered. Errors are collected
// rather than failing fast, so users see all issues at once.
func (mddb *MDDB[T]) scanDocumentFiles(ctx context.Context) ([]*IndexableDocument, error) {
	opts := []fileproc.Option{
		fileproc.WithRecursive(),
		fileproc.WithSuffix(".md"),
	}

	root := []byte(mddb.dataDir)

	results, errs := fileproc.Process(ctx, mddb.dataDir, func(f *fileproc.File, w *fileproc.FileWorker) (*IndexableDocument, error) {
		absPath := f.AbsPathBorrowed()

		relPathEphemeral := relPathFromAbs(absPath, root)

		if isInternalPathBytes(relPathEphemeral) {
			return nil, fileproc.ErrSkip
		}

		// Stat before read so fileproc can size the read buffer appropriately.
		stat, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("fs: %w", err)
		}

		data, err := f.Bytes()
		if err != nil {
			return nil, fmt.Errorf("fs: %w", err)
		}

		// Retain relPath and data so parsed slices survive beyond callback.
		relPath := w.RetainBytes(relPathEphemeral)
		data = w.RetainBytes(data)

		parsed, err := mddb.parseIndexable(relPath, data, stat.ModTime, stat.Size, "")
		if err != nil {
			return nil, fmt.Errorf("parsing document: %w", err)
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

	return nil, buildIndexScanError(errs)
}

// loadIndexMeta returns path-indexed metadata used by incremental reindex.
//
// We keep this in-memory to avoid per-file SQLite lookups (which would require
// a path index and add write overhead). Deletions are done by ID (PK), so
// we store id alongside path for fast delete batching.
func (mddb *MDDB[T]) loadIndexMeta(ctx context.Context) (map[string]indexMeta, error) {
	rows, err := mddb.sql.QueryContext(ctx, mddb.schema.selectIndexMetaSQL())
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	defer func() { _ = rows.Close() }()

	meta := make(map[string]indexMeta, 1024)

	for rows.Next() {
		var (
			id   string
			path string
			mt   int64
			size int64
		)

		scanErr := rows.Scan(&id, &path, &mt, &size)
		if scanErr != nil {
			return nil, fmt.Errorf("sqlite: %w", scanErr)
		}

		meta[path] = indexMeta{id: id, mtimeNS: mt, sizeBytes: size}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	return meta, nil
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
		return 0, fmt.Errorf("remove temp index: %w", err)
	}

	if err := mddb.removeFileIfExists(tmpPath + "-wal"); err != nil {
		return 0, fmt.Errorf("remove temp wal: %w", err)
	}

	if err := mddb.removeFileIfExists(tmpPath + "-shm"); err != nil {
		return 0, fmt.Errorf("remove temp shm: %w", err)
	}

	// Build into a fresh temp DB with unsafe pragmas (fast, disposable).
	tmpDB, err := openSqliteUnsafe(ctx, tmpPath)
	if err != nil {
		return 0, fmt.Errorf("open temp index: %w", err)
	}

	var removeErr error

	defer func() {
		removeErr = errors.Join(removeErr, mddb.fs.Remove(tmpPath))
	}()

	indexed, rebuildErr := mddb.rebuildIndexOnDB(ctx, tmpDB, entries, false)
	closeErr := tmpDB.Close()

	if rebuildErr != nil {
		return 0, errors.Join(rebuildErr, closeErr)
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

		return 0, fmt.Errorf("swap index: fs: %w", renameErr)
	}

	// Reopen the swapped DB with safe runtime pragmas.
	newDB, err := openSqlite(ctx, indexPath)
	if err != nil {
		return 0, fmt.Errorf("open index: %w", err)
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
		return 0, fmt.Errorf("recreate schema: %w", err)
	}

	// Let user create related tables (e.g., FTS, lookup tables) in same transaction.
	if mddb.cfg.AfterRecreateSchema != nil {
		err = mddb.cfg.AfterRecreateSchema(ctx, tx)
		if err != nil {
			return 0, fmt.Errorf("AfterRecreateSchema: %w", err)
		}
	}

	if len(entries) > 0 {
		err = mddb.bulkInsertDocs(ctx, tx, entries, replace, func(batch []IndexableDocument) error {
			if mddb.cfg.AfterBulkIndex == nil {
				return nil
			}

			callbackErr := mddb.cfg.AfterBulkIndex(ctx, tx, batch)
			if callbackErr != nil {
				return fmt.Errorf("AfterBulkIndex: %w", callbackErr)
			}

			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("bulk insert: %w", err)
		}
	}

	// Store schema fingerprint so Open() can detect mismatches.
	version := mddb.schema.fingerprint()

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
		return nil, errors.New("path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	// Single connection ensures pragma consistency and avoids lock churn.
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

	_, err = db.ExecContext(ctx, `
		PRAGMA journal_mode = OFF;
		PRAGMA synchronous = OFF;
		PRAGMA locking_mode = EXCLUSIVE;
		PRAGMA temp_store = MEMORY;
		PRAGMA foreign_keys = OFF;
		PRAGMA cache_size = 100000;
	`)
	if err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("sqlite: close: %w", closeErr)
		}

		return nil, errors.Join(fmt.Errorf("sqlite: apply unsafe pragmas: %w", err), closeErr)
	}

	return db, nil
}

// 50-100 seems to be the optimum for SQLite with CGO.
const indexInsertBatchSize = 50
const indexDeleteBatchSize = 50

// bulkInsertDocs inserts documents in batches for efficiency.
// Uses prepared statements to reduce parse overhead.
func (mddb *MDDB[T]) bulkInsertDocs(ctx context.Context, tx *sql.Tx, entries []*IndexableDocument, withReplace bool, afterBatch func([]IndexableDocument) error) error {
	colCount := len(mddb.schema.columnNames())

	// Pre-compile statement for full batches (the common case).
	batchStmt, err := mddb.prepareUpsertStmt(ctx, tx, indexInsertBatchSize, withReplace)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
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
				return fmt.Errorf("prepare upsert: %w", err)
			}
		}

		sqlArgs := args[:len(batch)*colCount]

		err = mddb.fillBatchUpsertSQLArgs(batch, colCount, sqlArgs)
		if err != nil {
			if isRemainderStmt {
				_ = stmt.Close()
			}

			return fmt.Errorf("build upsert args: %w", err)
		}

		_, err = stmt.ExecContext(ctx, sqlArgs...)
		if isRemainderStmt {
			_ = stmt.Close()
		}

		if err != nil {
			return fmt.Errorf("sqlite: %w", err)
		}

		if afterBatch != nil {
			err := afterBatch(batch)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (mddb *MDDB[T]) removeFileIfExists(path string) error {
	err := mddb.fs.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs: %w", err)
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

// relPathFromAbs strips the root prefix from an absolute path.
// Assumes absPath starts with root (guaranteed by fileproc walking from root).
func relPathFromAbs(absPath []byte, root []byte) []byte {
	rel := absPath[len(root):]
	if len(rel) > 0 && rel[0] == os.PathSeparator {
		rel = rel[1:]
	}

	return rel
}
