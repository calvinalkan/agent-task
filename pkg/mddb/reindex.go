package mddb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/calvinalkan/fileproc"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// IndexableDocument holds parsed document data for indexing and document creation.
//
// Used by [Config.DocumentFrom] and [Config.SQLColumnValues].
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

// IndexRow holds owned data ready for SQLite indexing and hook consumption.
//
// Used by [Config.AfterIndexBatch].
// All fields are owned and safe to retain beyond the callback. The batch slice
// passed to hooks is reused between calls; copy it if you need to keep it.
// CustomRowValues contains custom columns in the same order as defined in [SQLSchema].
//
// Note: CustomRowValues elements must be safe to retain. mddb normalizes []byte
// values when building rows: TEXT columns are converted to string; BLOB columns
// are copied. Other reference types are passed through as-is and must be owned.
type IndexRow struct {
	ID        string
	ShortID   string
	RelPath   string
	MtimeNS   int64
	SizeBytes int64
	Title     string
	// Values from [Config.SQLColumnValues] for custom columns,
	// in order of definition per row.
	CustomRowValues []any
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

	metaIndex, err := mddb.loadIndexMeta(ctx)
	if err != nil {
		return zero, fmt.Errorf("load index metadata: %w", err)
	}

	result, err := mddb.runReindex(ctx, mddb.sql, metaIndex)
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

	mddbDir := filepath.Dir(mddb.lockPath)
	indexPath := filepath.Join(mddbDir, "index.sqlite")
	tmpPath := indexPath + ".tmp"

	// Clean up any stale temp DB from a previous crash before rebuilding.
	if rmErr := mddb.removeFileIfExists(tmpPath); rmErr != nil {
		return 0, fmt.Errorf("remove temp index: %w", rmErr)
	}

	if rmErr := mddb.removeFileIfExists(tmpPath + "-wal"); rmErr != nil {
		return 0, fmt.Errorf("remove temp wal: %w", rmErr)
	}

	if rmErr := mddb.removeFileIfExists(tmpPath + "-shm"); rmErr != nil {
		return 0, fmt.Errorf("remove temp shm: %w", rmErr)
	}

	// Build into a fresh temp DB with unsafe pragmas (fast, disposable).
	tmpDB, err := openSqliteUnsafe(ctx, tmpPath)
	if err != nil {
		return 0, fmt.Errorf("open temp index: %w", err)
	}

	defer func() { _ = mddb.fs.Remove(tmpPath) }()

	result, rebuildErr := mddb.runReindex(ctx, tmpDB, nil)

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

	return result.Total, nil
}

// reindex runs a full or incremental reindex within a single transaction.
// If metaIndex is nil, it rebuilds schema and skips delete batching.
func (mddb *MDDB[T]) runReindex(
	ctx context.Context,
	db *sql.DB,
	metaIndex *indexMetaIndex,
) (IncrementalIndexResult, error) {
	// ==================== 1. Setup + Transaction ====================
	result := IncrementalIndexResult{}
	incremental := metaIndex != nil

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("sqlite: begin txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// ==================== 2. Schema (full reindex only) ====================
	if !incremental {
		err = mddb.schema.recreate(ctx, tx)
		if err != nil {
			return result, fmt.Errorf("recreate schema: %w", err)
		}

		if mddb.cfg.AfterRecreateSchema != nil {
			err = mddb.cfg.AfterRecreateSchema(ctx, tx)
			if err != nil {
				return result, fmt.Errorf("AfterRecreateSchema: %w", err)
			}
		}
	}

	// ==================== 3. Scan + Write Pipeline ====================
	// seen tracks which indexed rows still exist on disk; atomics avoid locks in callbacks.
	var seen []atomic.Bool
	if incremental {
		seen = make([]atomic.Bool, len(metaIndex.list))
	}

	var (
		inserted atomic.Int64
		updated  atomic.Int64
		skipped  atomic.Int64
	)

	// Cancel the scan when the writer fails (and vice versa) so goroutines exit promptly.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Bounded channel provides backpressure so parsing doesn't outrun SQLite.
	sqliteInsertChan := make(chan IndexRow, indexInsertBatchSize*indexInsertQueueBatches)
	sqliteResultChan := make(chan struct {
		count int
		err   error
	}, 1)

	// Single writer goroutine owns SQLite writes + hook callbacks within the txn.
	go func() {
		count, writeErr := mddb.streamInserts(ctx, tx, sqliteInsertChan, incremental)
		if writeErr != nil {
			cancel(writeErr)
		}

		sqliteResultChan <- struct {
			count int
			err   error
		}{count: count, err: writeErr}
	}()

	// Scan files in parallel and stream changed rows into the writer.
	scanErr := mddb.scanFiles(ctx, metaIndex, seen, sqliteInsertChan, &inserted, &updated, &skipped)
	close(sqliteInsertChan)

	sqliteRes := <-sqliteResultChan

	if scanErr != nil {
		return result, fmt.Errorf("scan documents: %w", scanErr)
	}

	if sqliteRes.err != nil {
		if errors.Is(sqliteRes.err, context.Canceled) || errors.Is(sqliteRes.err, context.DeadlineExceeded) {
			return result, fmt.Errorf("canceled: %w", context.Cause(ctx))
		}

		return IncrementalIndexResult{}, fmt.Errorf("bulk insert: %w", sqliteRes.err)
	}

	if ctx.Err() != nil {
		return IncrementalIndexResult{}, fmt.Errorf("canceled: %w", context.Cause(ctx))
	}

	// ==================== 4. Deletes (incremental only) ====================
	// Delete after upserts to avoid buffering all changed docs; safe because
	// path/ID mismatches are rejected during parsing.
	if incremental {
		deleteIDs := make([]string, 0)

		for _, meta := range metaIndex.list {
			if seen[meta.idx].Load() {
				continue
			}

			deleteIDs = append(deleteIDs, meta.id)
		}

		result.Inserted = int(inserted.Load())
		result.Updated = int(updated.Load())
		result.Deleted = len(deleteIDs)
		result.Skipped = int(skipped.Load())
		result.Total = len(metaIndex.byPath) - result.Deleted + result.Inserted
		// ==================== 5. Apply Deletes + Hooks (incremental only) ====================
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

				if mddb.cfg.AfterIndexBatch != nil {
					callbackErr := mddb.cfg.AfterIndexBatch(ctx, tx, []IndexRow{}, batch)
					if callbackErr != nil {
						return IncrementalIndexResult{}, fmt.Errorf("AfterIndexBatch: %w", callbackErr)
					}
				}
			}
		}
	} else {
		result.Inserted = sqliteRes.count
		result.Total = sqliteRes.count
	}

	// ==================== 6. Schema Fingerprint (full reindex only) ====================
	if !incremental {
		version := mddb.schema.fingerprint()

		_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
		if err != nil {
			return result, fmt.Errorf("sqlite: set user_version: %w", err)
		}
	}

	// ==================== 7. Commit ====================
	err = tx.Commit()
	if err != nil {
		return result, fmt.Errorf("sqlite: commit txn: %w", err)
	}

	// Important: if not set, defer() at the top will rollback the txn.
	committed = true

	return result, nil
}

// scanFiles walks the data directory and streams rows.
// If metaIndex is nil, it behaves like a full scan.
func (mddb *MDDB[T]) scanFiles(
	ctx context.Context,
	metaIndex *indexMetaIndex,
	seen []atomic.Bool,
	sqliteInsertChan chan<- IndexRow,
	inserted *atomic.Int64,
	updated *atomic.Int64,
	skipped *atomic.Int64,
) error {
	opts := []fileproc.Option{
		fileproc.WithRecursive(),
		fileproc.WithSuffix(".md"),
	}
	incremental := metaIndex != nil

	_, errs := fileproc.Process(ctx, mddb.dataDir, func(f *fileproc.File, _ *fileproc.FileWorker) (*IndexRow, error) {
		relPath := f.RelPath()
		if isInternalPath(relPath) {
			return nil, fileproc.ErrSkip
		}

		stat, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("fs: %w", err)
		}

		var (
			meta indexMeta
			ok   bool
		)

		if incremental {
			meta, ok = metaIndex.byPath[string(relPath)]
			if ok {
				// Mark seen even when unchanged so deletes only include truly missing files.
				seen[meta.idx].Store(true)

				if meta.mtimeNS == stat.ModTime && meta.sizeBytes == stat.Size {
					skipped.Add(1)

					return nil, fileproc.ErrSkip
				}
			}
		}

		data, err := f.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("fs: %w", err)
		}

		parsed, err := mddb.parseIndexable(relPath, data, stat.ModTime, stat.Size, "")
		if err != nil {
			return nil, fmt.Errorf("parsing document: %w", err)
		}

		row, err := mddb.buildIndexRow(&parsed)
		if err != nil {
			return nil, err
		}

		if ok {
			updated.Add(1)
		} else {
			inserted.Add(1)
		}

		// Backpressure here keeps memory bounded while the writer catches up.
		select {
		case sqliteInsertChan <- row:
		case <-ctx.Done():
			return nil, fileproc.ErrSkip
		}

		return nil, fileproc.ErrSkip
	}, opts...)

	return toIndexScanError(errs)
}

// buildIndexRow converts a borrowed IndexableDocument into an owned IndexRow.
// CustomRowValues are copied/normalized so they remain valid after the scan callback.
func (mddb *MDDB[T]) buildIndexRow(doc *IndexableDocument) (IndexRow, error) {
	row := IndexRow{
		ID:        string(doc.ID),
		ShortID:   string(doc.ShortID),
		RelPath:   string(doc.RelPath),
		MtimeNS:   doc.MtimeNS,
		SizeBytes: doc.SizeBytes,
		Title:     string(doc.Title),
	}

	customColCount := mddb.schema.customColumnCount()
	if customColCount == 0 {
		return row, nil
	}

	if mddb.cfg.SQLColumnValues == nil {
		return IndexRow{}, errors.New("internal error: cfg.SQLColumnValues is nil")
	}

	userVals := mddb.cfg.SQLColumnValues(*doc)
	if len(userVals) != customColCount {
		return IndexRow{}, fmt.Errorf("SQLColumnValues: expected %d values, got %d", customColCount, len(userVals))
	}

	row.CustomRowValues = make([]any, customColCount)

	for i, val := range userVals {
		if val == nil {
			row.CustomRowValues[i] = nil

			continue
		}

		col := mddb.schema.columns[baseColumnCount+i]

		switch v := val.(type) {
		case []byte:
			switch col.typ {
			case ColBlob:
				row.CustomRowValues[i] = append([]byte(nil), v...)
			case ColText:
				row.CustomRowValues[i] = string(v)
			default:
				row.CustomRowValues[i] = v
			}
		default:
			row.CustomRowValues[i] = val
		}
	}

	return row, nil
}

// streamInserts drains rowCh, batches rows, writes them to SQLite, and calls hooks.
// It owns the transaction and must be called from the single writer goroutine.
func (mddb *MDDB[T]) streamInserts(
	ctx context.Context,
	tx *sql.Tx,
	sqliteInsertChan <-chan IndexRow,
	withReplace bool,
) (int, error) {
	colCount := len(mddb.schema.columnNames())

	// Pre-compile statement for full batches (common case) to avoid repeated parses.
	batchStmt, err := mddb.prepareUpsertStmt(ctx, tx, indexInsertBatchSize, withReplace)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}

	defer func() { _ = batchStmt.Close() }()

	args := make([]any, indexInsertBatchSize*colCount)
	batch := make([]IndexRow, 0, indexInsertBatchSize)
	total := 0

	flush := func() error {
		stmt := batchStmt

		isRemainderStmt := len(batch) < indexInsertBatchSize
		if isRemainderStmt {
			// Remainder batch needs a smaller statement to match placeholder count.
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

		// Hook runs after successful insert; batch slice is reused after return.
		if mddb.cfg.AfterIndexBatch != nil {
			err = mddb.cfg.AfterIndexBatch(ctx, tx, batch, []string{})
			if err != nil {
				return fmt.Errorf("AfterIndexBatch: %w", err)
			}
		}

		total += len(batch)
		batch = batch[:0]

		return nil
	}

	for {
		select {
		case row, ok := <-sqliteInsertChan:
			if !ok {
				// Channel closed: flush remainder and exit.
				if len(batch) == 0 {
					return total, nil
				}

				if err := flush(); err != nil {
					return total, err
				}

				return total, nil
			}

			batch = append(batch, row)
			if len(batch) >= indexInsertBatchSize {
				if err := flush(); err != nil {
					return total, err
				}
			}
		case <-ctx.Done():
			return total, fmt.Errorf("canceled: %w", context.Cause(ctx))
		}
	}
}

// indexMeta is lightweight metadata loaded from the index for incremental scans.
// idx is the position in the list used for the seen bitmap.
type indexMeta struct {
	id        string
	mtimeNS   int64
	sizeBytes int64
	idx       int
}

type indexMetaIndex struct {
	byPath map[string]indexMeta
	list   []indexMeta
}

// loadIndexMeta returns path-indexed metadata used by incremental reindex.
//
// We keep this in-memory to avoid per-file SQLite lookups (which would require
// a path index and add write overhead). Deletions are done by ID (PK), so
// we store id alongside path for fast delete batching. The list includes
// stable idx values for the seen bitmap.
func (mddb *MDDB[T]) loadIndexMeta(ctx context.Context) (*indexMetaIndex, error) {
	rows, err := mddb.sql.QueryContext(ctx, mddb.schema.selectIndexMetaSQL())
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	defer func() { _ = rows.Close() }()

	meta := make(map[string]indexMeta, 1024)
	metas := make([]indexMeta, 0, 1024)

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

		entry := indexMeta{id: id, mtimeNS: mt, sizeBytes: size, idx: len(metas)}
		metas = append(metas, entry)
		meta[path] = entry
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	return &indexMetaIndex{byPath: meta, list: metas}, nil
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

// indexInsertQueueBatches controls channel depth in batches for scan→writer buffering.
const indexInsertQueueBatches = 4

func (mddb *MDDB[T]) removeFileIfExists(path string) error {
	err := mddb.fs.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs: %w", err)
	}

	return nil
}

var internalmddbDirInDataDir = []byte(".mddb")

// isInternalPath checks if path is inside the .mddb directory.
func isInternalPath(path []byte) bool {
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

func toIndexScanError(errs []error) error {
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
