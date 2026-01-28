package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/calvinalkan/fileproc"
	"github.com/google/uuid"
)

const (
	statusOpen       = "open"
	statusInProgress = "in_progress"
	statusClosed     = "closed"
)

const (
	minPriority = 1
	maxPriority = 4
)

// Limit frontmatter scan length to avoid unbounded reads when a delimiter is missing.
const rebuildFrontmatterLineLimit = 100

// ErrIndexScan is returned (via errors.Is) when scanning hits per-file validation issues.
// Use errors.Is(err, ErrIndexScan) to detect scan failures.
var ErrIndexScan = errors.New("index scan")

// FileIssueError captures a single file scan problem.
type FileIssueError struct {
	Path string // Path is the absolute path of the problematic file.
	ID   string // ID is the parsed ticket ID when available.
	Err  error  // Err is the underlying validation or parse error.
}

func (e FileIssueError) Error() string {
	return e.Err.Error()
}

// IndexScanError aggregates per-file scan issues.
// It unwraps to [ErrIndexScan] for errors.Is checks.
type IndexScanError struct {
	Total  int              // Total is the number of invalid files encountered.
	Issues []FileIssueError // Issues contains per-file errors for reporting.
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

	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
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

		fm, tail, parseErr := ParseFrontmatterReader(f, WithLineLimit(rebuildFrontmatterLineLimit))
		if parseErr != nil {
			return nil, &FileIssueError{
				Path: filepath.Join(root, string(path)),
				Err:  parseErr,
			}
		}

		relPath := string(path)
		contextLabel := filepath.Join(root, relPath)

		entry, id, entryErr := ticketFromFrontmatter(relPath, contextLabel, root, st, fm, tail)
		if entryErr != nil {
			return nil, &FileIssueError{
				Path: contextLabel,
				ID:   id,
				Err:  entryErr,
			}
		}

		return &entry, nil
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

func ticketFromFrontmatter(relPath, contextLabel, root string, st fileproc.Stat, fm TicketFrontmatter, tail io.Reader) (Ticket, string, error) {
	idRaw, err := requireScalarString(fm, "id", contextLabel)
	if err != nil {
		return Ticket{}, "", err
	}

	err = validateIDString(idRaw)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("validate id at %s: %w", contextLabel, err)
	}

	id, err := uuid.Parse(idRaw)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("parse id at %s: %w", contextLabel, err)
	}

	err = validateUUIDv7(id)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("validate id at %s: %w", contextLabel, err)
	}

	expectedRel, err := PathFromID(id)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("derive path for %s: %w", idRaw, err)
	}

	if filepath.Clean(relPath) != filepath.Clean(expectedRel) {
		expectedPath := filepath.Join(root, expectedRel)

		return Ticket{}, idRaw, fmt.Errorf("validate path: id %s stored at %s (expected %s)", idRaw, contextLabel, expectedPath)
	}

	contextLabel = fmt.Sprintf("%s (id %s)", contextLabel, idRaw)

	schema, err := requireScalarInt(fm, "schema_version", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	if schema < 1 || schema != int64(currentSchemaVersion) {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: schema_version %d is unsupported (expected %d)", contextLabel, schema, currentSchemaVersion)
	}

	status, err := requireScalarString(fm, "status", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	if !isValidStatus(status) {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: status %q is invalid (expected open, in_progress, closed)", contextLabel, status)
	}

	kind, err := requireScalarString(fm, "type", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	if !isValidType(kind) {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: type %q is invalid (expected bug, feature, task, epic, chore)", contextLabel, kind)
	}

	priority, err := requireScalarInt(fm, "priority", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	if !isValidPriority(priority) {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: priority %d is invalid (expected %d-%d)", contextLabel, priority, minPriority, maxPriority)
	}

	createdRaw, err := requireScalarString(fm, "created", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	createdAt, err := parseRFC3339(createdRaw)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: created timestamp %q: %w", contextLabel, createdRaw, err)
	}

	closedAt, err := optionalRFC3339(fm, "closed", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	if status == statusClosed && !closedAt.Valid {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: closed timestamp required when status is %q", contextLabel, statusClosed)
	}

	if status != statusClosed && closedAt.Valid {
		return Ticket{}, idRaw, fmt.Errorf("parse frontmatter at %s: closed timestamp present when status is %q", contextLabel, status)
	}

	assignee, err := optionalScalarString(fm, "assignee", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	parent, err := optionalScalarString(fm, "parent", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	externalRef, err := optionalScalarString(fm, "external-ref", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	blockedBy, err := optionalList(fm, "blocked-by", contextLabel)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	title, err := extractTitle(tail)
	if err != nil {
		return Ticket{}, idRaw, fmt.Errorf("parse body at %s: %w", contextLabel, err)
	}

	shortID, err := ShortIDFromUUID(id)
	if err != nil {
		return Ticket{}, idRaw, err
	}

	return Ticket{
		ID:          id.String(),
		ShortID:     shortID,
		Path:        expectedRel,
		MtimeNS:     st.ModTime,
		Status:      status,
		Type:        kind,
		Priority:    priority,
		Assignee:    nullStringValue(assignee),
		Parent:      nullStringValue(parent),
		CreatedAt:   createdAt.UTC(),
		ClosedAt:    nullTimePtr(closedAt),
		ExternalRef: nullStringValue(externalRef),
		Title:       title,
		BlockedBy:   blockedBy,
	}, idRaw, nil
}

func extractTitle(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "# "); ok {
			title := strings.TrimSpace(after)
			if title == "" {
				return "", errors.New("title is empty (expected '# <title>')")
			}

			return title, nil
		}
	}

	err := scanner.Err()
	if err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}

	return "", errors.New("missing title (expected '# <title>')")
}

func requireScalarString(fm TicketFrontmatter, key, contextLabel string) (string, error) {
	value, ok := fm[key]
	if !ok {
		return "", fmt.Errorf("parse frontmatter at %s: missing required field %q", contextLabel, key)
	}

	if value.Kind != ValueScalar || value.Scalar.Kind != ScalarString {
		return "", fmt.Errorf("parse frontmatter at %s: field %q must be a string", contextLabel, key)
	}

	if value.Scalar.String == "" {
		return "", fmt.Errorf("parse frontmatter at %s: field %q cannot be empty", contextLabel, key)
	}

	return value.Scalar.String, nil
}

func requireScalarInt(fm TicketFrontmatter, key, contextLabel string) (int64, error) {
	value, ok := fm[key]
	if !ok {
		return 0, fmt.Errorf("parse frontmatter at %s: missing required field %q", contextLabel, key)
	}

	if value.Kind != ValueScalar || value.Scalar.Kind != ScalarInt {
		return 0, fmt.Errorf("parse frontmatter at %s: field %q must be an integer", contextLabel, key)
	}

	return value.Scalar.Int, nil
}

func optionalScalarString(fm TicketFrontmatter, key, contextLabel string) (sql.NullString, error) {
	value, ok := fm[key]
	if !ok {
		return sql.NullString{}, nil
	}

	if value.Kind != ValueScalar || value.Scalar.Kind != ScalarString {
		return sql.NullString{}, fmt.Errorf("parse frontmatter at %s: field %q must be a string", contextLabel, key)
	}

	if value.Scalar.String == "" {
		return sql.NullString{}, fmt.Errorf("parse frontmatter at %s: field %q cannot be empty", contextLabel, key)
	}

	return sql.NullString{String: value.Scalar.String, Valid: true}, nil
}

func optionalList(fm TicketFrontmatter, key, contextLabel string) ([]string, error) {
	value, ok := fm[key]
	if !ok {
		return nil, nil
	}

	if value.Kind != ValueList {
		return nil, fmt.Errorf("parse frontmatter at %s: field %q must be a list of strings", contextLabel, key)
	}

	if len(value.List) == 0 {
		return []string{}, nil
	}

	seen := make(map[string]struct{}, len(value.List))
	out := make([]string, 0, len(value.List))

	for _, item := range value.List {
		if item == "" {
			return nil, fmt.Errorf("parse frontmatter at %s: field %q contains an empty list item", contextLabel, key)
		}

		if _, exists := seen[item]; exists {
			continue
		}

		seen[item] = struct{}{}
		out = append(out, item)
	}

	return out, nil
}

func optionalRFC3339(fm TicketFrontmatter, key, contextLabel string) (sql.NullInt64, error) {
	value, ok := fm[key]
	if !ok {
		return sql.NullInt64{}, nil
	}

	if value.Kind != ValueScalar || value.Scalar.Kind != ScalarString {
		return sql.NullInt64{}, fmt.Errorf("parse frontmatter at %s: field %q must be a string timestamp", contextLabel, key)
	}

	if value.Scalar.String == "" {
		return sql.NullInt64{}, fmt.Errorf("parse frontmatter at %s: field %q cannot be empty", contextLabel, key)
	}

	parsed, err := parseRFC3339(value.Scalar.String)
	if err != nil {
		return sql.NullInt64{}, fmt.Errorf("parse frontmatter at %s: %s timestamp %q: %w", contextLabel, key, value.Scalar.String, err)
	}

	return sql.NullInt64{Int64: parsed.Unix(), Valid: true}, nil
}

func parseRFC3339(value string) (time.Time, error) {
	timestamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w (expected RFC3339 UTC)", err)
	}

	return timestamp.UTC(), nil
}

func validateIDString(value string) error {
	if value == "" {
		return errors.New("value is empty")
	}

	if strings.ContainsAny(value, "/\\") {
		return errors.New("contains a path separator")
	}

	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("contains a NUL byte")
	}

	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return errors.New("contains whitespace")
	}

	return nil
}

func isValidStatus(status string) bool {
	switch status {
	case statusOpen, statusInProgress, statusClosed:
		return true
	default:
		return false
	}
}

func isValidType(ticketType string) bool {
	switch ticketType {
	case "bug", "feature", "task", "epic", "chore":
		return true
	default:
		return false
	}
}

func isValidPriority(priority int64) bool {
	return priority >= minPriority && priority <= maxPriority
}

var errSkipInternalPath = errors.New("skip internal .tk path")
