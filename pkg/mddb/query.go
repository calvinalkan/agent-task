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

// GetPrefixRow contains the base fields returned by [MDDB.GetByPrefix].
// These correspond to just the required SQLite columns that all documents must have.
// Use [MDDB.Get] to retrieve the full document with body and custom fields.
type GetPrefixRow struct {
	// ID is the document's unique identifier.
	ID string

	// ShortID is the short identifier for human-friendly references.
	ShortID string

	// Path is the relative file path (relative to data directory).
	Path string

	// MtimeNS is the file modification time in nanoseconds.
	MtimeNS int64

	// Title is the document title for display in listings.
	Title string
}

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
		return zero, wrap(errors.New("context is nil"))
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return zero, wrap(ErrClosed)
	}

	release, err := s.acquireReadLock(ctx)
	if err != nil {
		return zero, wrap(err)
	}

	defer release()

	result, err := fn(s.sql)

	return result, wrap(err)
}

// GetByPrefix finds documents by short_id or ID prefix.
//
// Returns up to 50 [GetPrefixRow] matches ordered by ID. Use [MDDB.Get] for full
// documents. Empty slice means no match; multiple results means ambiguous prefix.
//
// Returns [ErrClosed] if store is closed.
func (mddb *MDDB[T]) GetByPrefix(ctx context.Context, prefix string) ([]GetPrefixRow, error) {
	if ctx == nil {
		return nil, wrap(errors.New("context is nil"))
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return nil, wrap(ErrClosed)
	}

	if prefix == "" {
		return nil, wrap(errors.New("prefix is empty"))
	}

	release, err := mddb.acquireReadLock(ctx)
	if err != nil {
		return nil, wrap(err)
	}

	defer release()

	query := "SELECT id, short_id, path, mtime_ns, title FROM " + mddb.schema.tableName +
		" WHERE short_id LIKE ? ESCAPE '\\' OR id LIKE ? ESCAPE '\\' ORDER BY id LIMIT 50"

	pattern := escapeLike(prefix) + "%"

	rows, err := mddb.sql.QueryContext(ctx, query, pattern, pattern)
	if err != nil {
		return nil, wrap(fmt.Errorf("sqlite: %w", err))
	}

	defer func() { _ = rows.Close() }()

	var results []GetPrefixRow

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
			return nil, wrap(fmt.Errorf("sqlite: %w", scanErr))
		}

		results = append(results, GetPrefixRow{
			ID:      idStr,
			ShortID: shortID,
			Path:    path,
			MtimeNS: mtimeNS,
			Title:   title,
		})
	}

	err = rows.Err()
	if err != nil {
		return nil, wrap(fmt.Errorf("sqlite: %w", err))
	}

	return results, nil
}

// Get retrieves a document by full ID.
//
// Looks up path in SQLite, reads file, builds via [Config.DocumentFrom].
// For prefix lookup, use [MDDB.GetByPrefix] first.
//
// Returns [ErrNotFound] if document doesn't exist or file is missing.
// Returns [ErrClosed] if mddb is closed.
func (mddb *MDDB[T]) Get(ctx context.Context, id string) (*T, error) {
	if ctx == nil {
		return nil, wrap(errors.New("context is nil"), withID(id))
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return nil, wrap(ErrClosed, withID(id))
	}

	if id == "" {
		return nil, wrap(ErrEmptyID)
	}

	release, err := mddb.acquireReadLock(ctx)
	if err != nil {
		return nil, wrap(err, withID(id))
	}

	defer release()

	// We need to find the path. Query SQLite for it.
	var path string

	query := "SELECT path FROM " + mddb.schema.tableName + " WHERE id = ?"
	row := mddb.sql.QueryRowContext(ctx, query, id)

	err = row.Scan(&path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, wrap(ErrNotFound, withID(id))
		}

		return nil, wrap(fmt.Errorf("sqlite: %w", err), withID(id))
	}

	err = mddb.validateRelPath(path)
	if err != nil {
		return nil, wrap(fmt.Errorf("%w: path %q", err, path), withID(id))
	}

	doc, err := mddb.readDocumentFile(id, path)
	if err != nil {
		return nil, wrap(err, withID(id), withPath(path))
	}

	return doc, nil
}

// readDocumentFile reads and parses a document from its path.
func (mddb *MDDB[T]) readDocumentFile(expectedID string, relPath string) (*T, error) {
	absPath := filepath.Join(mddb.dataDir, relPath)

	info, err := mddb.fs.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}

		return nil, fmt.Errorf("fs: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}

	data, err := mddb.fs.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("fs: %w", err)
	}

	mtimeNS := info.ModTime().UnixNano()

	doc, err := mddb.parseDocument(relPath, data, mtimeNS, expectedID)
	if err != nil {
		return nil, err
	}

	return doc, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(value string) string {
	return likeEscaper.Replace(value)
}
