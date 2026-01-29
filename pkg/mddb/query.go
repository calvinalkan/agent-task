package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound indicates the requested document does not exist.
var ErrNotFound = errors.New("not found")

// Query executes fn with a read lock held for custom SQL queries.
//
// Acquires shared lock, replays pending WAL if needed, then calls fn with
// the SQLite *sql.DB. Multiple Query calls run concurrently.
//
// Returns [ErrClosed] if store is closed. Also returns lock timeout,
// WAL replay failures, or errors from fn.
func Query[T Document, R any](ctx context.Context, s *MDDB[T], fn func(db *sql.DB) (R, error)) (R, error) {
	var zero R

	if ctx == nil {
		return zero, errors.New("query: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return zero, fmt.Errorf("query: %w", ErrClosed)
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	readLock, err := s.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return zero, fmt.Errorf("query: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	return fn(s.sql)
}

// GetByPrefix finds documents by short_id or ID prefix.
//
// Returns up to 50 [BaseMeta] matches ordered by ID. Use [MDDB.Get] for full
// documents. Empty slice means no match; multiple results means ambiguous prefix.
//
// Returns [ErrClosed] if store is closed.
func (mddb *MDDB[T]) GetByPrefix(ctx context.Context, prefix string) ([]BaseMeta, error) {
	if ctx == nil {
		return nil, errors.New("get by prefix: context is nil")
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return nil, fmt.Errorf("get by prefix: %w", ErrClosed)
	}

	if prefix == "" {
		return nil, errors.New("get by prefix: prefix is empty")
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	readLock, err := mddb.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return nil, fmt.Errorf("get by prefix: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	query := "SELECT id, short_id, path, mtime_ns, title FROM " + mddb.tableName +
		" WHERE short_id LIKE ? ESCAPE '\\' OR id LIKE ? ESCAPE '\\' ORDER BY id LIMIT 50"

	pattern := escapeLike(prefix) + "%"

	rows, err := mddb.sql.QueryContext(ctx, query, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("get by prefix: query: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var results []BaseMeta

	for rows.Next() {
		var (
			idStr   string
			shortID string
			path    string
			mtimeNS int64
			title   string
		)

		scanErr := rows.Scan(&idStr, &shortID, &path, &mtimeNS, &title)
		if scanErr != nil {
			return nil, fmt.Errorf("get by prefix: scan: %w", scanErr)
		}

		results = append(results, BaseMeta{
			ID:      idStr,
			ShortID: shortID,
			Path:    path,
			MtimeNS: mtimeNS,
			Title:   title,
		})
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("get by prefix: rows: %w", err)
	}

	return results, nil
}

// Get retrieves a document by full ID.
//
// Looks up path in SQLite, reads file, parses via [Config.Parse].
// For prefix lookup, use [MDDB.GetByPrefix] first.
//
// Returns [ErrNotFound] if document doesn't exist or file is missing.
// Returns [ErrClosed] if store is closed.
func (mddb *MDDB[T]) Get(ctx context.Context, id string) (*T, error) {
	if ctx == nil {
		return nil, errors.New("get: context is nil")
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return nil, fmt.Errorf("get: %w", ErrClosed)
	}

	if id == "" {
		return nil, errors.New("get: id is empty")
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	readLock, err := mddb.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	// We need to find the path. Query SQLite for it.
	var path string

	query := "SELECT path FROM " + mddb.tableName + " WHERE id = ?"
	row := mddb.sql.QueryRowContext(ctx, query, id)

	err = row.Scan(&path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get %s: %w", id, ErrNotFound)
		}

		return nil, fmt.Errorf("get %s: scan: %w", id, err)
	}

	err = mddb.validateRelPath(path)
	if err != nil {
		return nil, fmt.Errorf("get %s: invalid path %q: %w", id, path, err)
	}

	return mddb.readDocumentFile(ctx, id, path)
}

// readDocumentFile reads and parses a document from its path.
func (mddb *MDDB[T]) readDocumentFile(_ context.Context, expectedID string, relPath string) (*T, error) {
	absPath := filepath.Join(mddb.dataDir, relPath)

	info, err := mddb.fs.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("get %s: %w", expectedID, ErrNotFound)
		}

		return nil, fmt.Errorf("get: stat %s: %w", relPath, err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("get %s: not a regular file: %w", expectedID, ErrNotFound)
	}

	data, err := mddb.fs.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("get: read %s: %w", relPath, err)
	}

	mtimeNS := info.ModTime().UnixNano()

	doc, err := mddb.parseDocumentContent(relPath, data, mtimeNS, expectedID)
	if err != nil {
		return nil, fmt.Errorf("get: parse: %w", err)
	}

	return doc, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(value string) string {
	return likeEscaper.Replace(value)
}
