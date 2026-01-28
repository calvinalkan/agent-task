package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/calvinalkan/fileproc"
)



// Limit frontmatter scan length to avoid unbounded reads when a delimiter is missing.
const rebuildFrontmatterLineLimit = 100

// ErrIndexScan is returned (via errors.Is) when scanning hits per-file validation issues.
// Use errors.Is(err, ErrIndexScan) to detect scan failures.
var ErrIndexScan = errors.New("index scan")

// FileIssueError captures a single file scan problem.
type FileIssueError struct {
	// Path is the absolute path of the problematic file.
	Path string
	// ID is the parsed ticket ID when available.
	ID string
	// Err is the underlying validation or parse error.
	Err error
}

func (e FileIssueError) Error() string {
	return e.Err.Error()
}

// IndexScanError aggregates per-file scan issues.
// It unwraps to [ErrIndexScan] for errors.Is checks.
type IndexScanError struct {
	// Total is the number of invalid files encountered.
	Total int
	// Issues contains per-file errors for reporting.
	Issues []FileIssueError
}

func (e *IndexScanError) Error() string {
	return fmt.Sprintf("scan: %d invalid files", e.Total)
}

func (*IndexScanError) Unwrap() error {
	return ErrIndexScan
}

// Reindex scans ticket files and rebuilds the SQLite index from scratch.
//
// The index is treated as disposable: rebuild is intentionally strict about ticket validity.
// Reindex returns the number of indexed tickets and an error that matches [ErrIndexScan]
// when files cannot be indexed. Use errors.Is(err, ErrIndexScan) to detect scan failures.
//
// If any scan errors are encountered, rebuild returns them without touching SQLite
// to avoid publishing a partial or stale index. Fix the files and rerun rebuild.
//
// Reindex acquires the WAL lock before rebuilding. It may return [ErrWALCorrupt]
// or [ErrWALReplay] if recovery fails.
func (s *Store) Reindex(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("reindex: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return 0, errors.New("reindex: store is not open")
	}

	err := ctx.Err()
	if err != nil {
		return 0, fmt.Errorf("reindex: canceled: %w", context.Cause(ctx))
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	lock, err := s.locker.LockWithTimeout(lockCtx, s.lockPath)
	if err != nil {
		return 0, fmt.Errorf("reindex: lock wal: %w", err)
	}

	defer func() { _ = lock.Close() }()

	return s.reindexLocked(ctx)
}

// reindexLocked rebuilds the index under the WAL write lock (must be held by caller).
// It recovers any pending WAL, scans ticket files, and rebuilds SQLite.
func (s *Store) reindexLocked(ctx context.Context) (int, error) {
	err := s.recoverWalLocked(ctx)
	if err != nil {
		return 0, err
	}

	entries, scanErr := scanTicketFiles(ctx, s.dir)
	if scanErr != nil {
		return 0, scanErr
	}

	indexed, err := reindexSQLInTxn(ctx, s.sql, entries)
	if err != nil {
		return 0, fmt.Errorf("rebuild index: %w", err)
	}

	return indexed, nil
}

// scanTicketFiles finds all the md files in the data directory and parses them to a Ticket.
func scanTicketFiles(ctx context.Context, root string) ([]fileproc.Result[Ticket], error) {
	opts := fileproc.Options{
		Recursive: true,
		Suffix:    ".md",
		OnError: func(err error, _, _ int) bool {
			// Don't collect internalSkip errors in errs result in ProcessStat().
			return !errors.Is(err, errSkipInternalPath)
		},
	}

	results, errs := fileproc.ProcessStat(ctx, root, func(path []byte, st fileproc.Stat, f fileproc.LazyFile) (*Ticket, error) {
		if bytes.HasPrefix(path, []byte(".tk/")) {
			return nil, errSkipInternalPath
		}

		relPath := string(path)

		data, readErr := io.ReadAll(f)
		if readErr != nil {
			return nil, &FileIssueError{
				Path: relPath,
				Err:  fmt.Errorf("read file: %w", readErr),
			}
		}

		ticket, parseErr := parseTicketFile(data, relPath, st.ModTime)
		if parseErr != nil {
			return nil, &FileIssueError{
				Path: relPath,
				Err:  parseErr,
			}
		}

		return ticket, nil
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
				Path: filepath.Join(root, procErr.Path),
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

// reindexSQLInTxn rebuilds the derived index in a single SQLite transaction.
func reindexSQLInTxn(ctx context.Context, db *sql.DB, entries []fileproc.Result[Ticket]) (int, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, fmt.Errorf("begin rebuild txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	err = dropAndRecreateSchema(ctx, tx)
	if err != nil {
		return 0, err
	}

	inserter, err := prepareTicketInserter(ctx, tx)
	if err != nil {
		return 0, err
	}

	defer inserter.Close()

	indexed := 0

	for i := range entries {
		entry := entries[i].Value
		if entry == nil {
			continue
		}

		err = inserter.Insert(ctx, tx, entry)
		if err != nil {
			return 0, fmt.Errorf("index %s (%s): %w", entry.ID, entry.Path, err)
		}

		indexed++
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion))
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

var errSkipInternalPath = errors.New("skip internal .tk path")
