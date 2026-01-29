package mddb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/calvinalkan/fileproc"
)

// ErrIndexScan is the sentinel error wrapped by [IndexScanError].
// Use [errors.Is](err, ErrIndexScan) to detect scan failures.
var ErrIndexScan = errors.New("index scan")

// FileIssueError represents a single file that failed during index scanning.
// Contains the file path and the underlying error (parse failure, missing fields, etc).
type FileIssueError struct {
	Path string // Relative path to the problematic file
	ID   string // Document ID if parsed, empty otherwise
	Err  error  // Underlying error (parse, validation, etc)
}

func (e FileIssueError) Error() string {
	return e.Err.Error()
}

// IndexScanError aggregates all file issues encountered during [MDDB.Reindex].
// Iterate Issues for per-file details. Unwraps to [ErrIndexScan].
type IndexScanError struct {
	Total  int              // Count of files with issues
	Issues []FileIssueError // Details for each problematic file
}

func (e *IndexScanError) Error() string {
	return fmt.Sprintf("scan: %d invalid files", e.Total)
}

func (*IndexScanError) Unwrap() error {
	return ErrIndexScan
}

// Reindex rebuilds the SQLite index by scanning all document files.
//
// Called automatically by [Open] on schema version mismatch. Returns count
// of indexed documents. Holds exclusive lock for duration.
//
// Returns [ErrClosed] if store is closed. Returns [*IndexScanError] (wraps
// [ErrIndexScan]) if files fail validation; inspect Issues for details.
func (mddb *MDDB[T]) Reindex(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("reindex: context is nil")
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return 0, fmt.Errorf("reindex: %w", ErrClosed)
	}

	err := ctx.Err()
	if err != nil {
		return 0, fmt.Errorf("reindex: canceled: %w", context.Cause(ctx))
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	lock, err := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		return 0, fmt.Errorf("reindex: lock wal: %w", err)
	}

	defer func() { _ = lock.Close() }()

	return mddb.reindexLocked(ctx)
}

// reindexLocked rebuilds the index under the WAL write lock.
func (mddb *MDDB[T]) reindexLocked(ctx context.Context) (int, error) {
	err := mddb.recoverWalLocked(ctx)
	if err != nil {
		return 0, err
	}

	entries, scanErr := mddb.scanDocumentFiles(ctx)
	if scanErr != nil {
		return 0, scanErr
	}

	indexed, err := mddb.reindexSQLInTxn(ctx, entries)
	if err != nil {
		return 0, fmt.Errorf("rebuild index: %w", err)
	}

	return indexed, nil
}

// scanDocumentFiles finds all .md files and parses them.
func (mddb *MDDB[T]) scanDocumentFiles(ctx context.Context) ([]fileproc.Result[T], error) {
	opts := fileproc.Options{
		Recursive: true,
		Suffix:    ".md",
		OnError: func(err error, _, _ int) bool {
			return !errors.Is(err, errSkipInternalPath)
		},
	}

	results, errs := fileproc.ProcessStat(ctx, mddb.dataDir, func(path []byte, st fileproc.Stat, f fileproc.LazyFile) (*T, error) {
		if isInternalPathBytes(path) {
			return nil, errSkipInternalPath
		}

		pathStr := string(path)

		data, readErr := io.ReadAll(f)
		if readErr != nil {
			return nil, &FileIssueError{
				Path: pathStr,
				Err:  fmt.Errorf("read file: %w", readErr),
			}
		}

		doc, parseErr := mddb.parseDocumentFile(data, pathStr, st.ModTime)
		if parseErr != nil {
			return nil, &FileIssueError{
				Path: pathStr,
				Err:  parseErr,
			}
		}

		return doc, nil
	}, opts)

	if len(errs) > 0 {
		issues := make([]FileIssueError, 0, len(errs))

		for _, err := range errs {
			var ioErr *fileproc.IOError
			if errors.As(err, &ioErr) {
				return nil, errors.Join(errs...)
			}

			var procErr *fileproc.ProcessError
			if !errors.As(err, &procErr) {
				return nil, errors.Join(errs...)
			}

			var scanErr *FileIssueError
			if errors.As(procErr.Err, &scanErr) {
				issues = append(issues, *scanErr)

				continue
			}

			issues = append(issues, FileIssueError{
				Path: filepath.Join(mddb.dataDir, procErr.Path),
				Err:  procErr.Err,
			})
		}

		if len(issues) > 0 {
			return nil, &IndexScanError{
				Total:  len(issues),
				Issues: issues,
			}
		}
	}

	return results, nil
}

var internalDirBytes = []byte(".mddb")

func isInternalPathBytes(path []byte) bool {
	if bytes.Equal(path, internalDirBytes) {
		return true
	}

	if !bytes.HasPrefix(path, internalDirBytes) || len(path) <= len(internalDirBytes) {
		return false
	}

	sep := path[len(internalDirBytes)]

	return sep == '/' || sep == '\\'
}

// parseDocumentFile parses a document file.
func (mddb *MDDB[T]) parseDocumentFile(data []byte, relPath string, mtimeNS int64) (*T, error) {
	return mddb.parseDocumentContent(relPath, data, mtimeNS, "")
}

// reindexSQLInTxn rebuilds the index in a single SQLite transaction.
func (mddb *MDDB[T]) reindexSQLInTxn(ctx context.Context, entries []fileproc.Result[T]) (int, error) {
	tx, err := mddb.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin rebuild txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	err = mddb.cfg.RecreateIndex(ctx, tx, mddb.tableName)
	if err != nil {
		return 0, fmt.Errorf("create index: %w", err)
	}

	inserter, err := mddb.cfg.Prepare(ctx, tx, mddb.tableName)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}

	defer func() { _ = inserter.Close() }()

	indexed := 0

	for i := range entries {
		entry := entries[i].Value
		if entry == nil {
			continue
		}

		upsertErr := inserter.Upsert(ctx, entry)
		if upsertErr != nil {
			d, ok := any(*entry).(Document)
			if !ok {
				return 0, fmt.Errorf("index: type assertion failed: %w", upsertErr)
			}

			return 0, fmt.Errorf("index %s (%s): %w", d.ID(), d.RelPath(), upsertErr)
		}

		indexed++
	}

	expectedVersion := combinedSchemaVersion(mddb.cfg.SchemaVersion)

	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", expectedVersion))
	if err != nil {
		return 0, fmt.Errorf("set user_version: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf("commit rebuild txn: %w", err)
	}

	committed = true

	return indexed, nil
}

var errSkipInternalPath = errors.New("skip internal .mddb path")
