package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrCommitIncomplete indicates WAL was durable but file write or index update failed.
// The transaction will be recovered on next [Open] or read operation.
//
// Retry semantics: On retry, [MDDB.Begin] replays the WAL before returning,
// completing the original transaction. A subsequent [Tx.Create] for the same ID
// returns [ErrAlreadyExists] (safe failure). [Tx.Update] is idempotent (rewrites
// same content). In most cases, simply ignore this error - recovery is automatic.
var ErrCommitIncomplete = errors.New("wal durable; apply incomplete")

// ErrAlreadyExists indicates [Tx.Create] was attempted for a document that
// already exists in the index or filesystem.
var ErrAlreadyExists = errors.New("already exists")

// Tx buffers write operations until [Tx.Commit] persists them atomically.
//
// Create via [MDDB.Begin]. Holds exclusive WAL lock until Commit or Rollback.
// Multiple operations for same ID allowed; last wins.
//
// Commit writes WAL (crash-safe), then files, then index. Crash after WAL
// write is recovered on next [Open] or read.
type Tx[T Document] struct {
	mddb    *MDDB[T]
	ctx     context.Context
	release func() error
	ops     map[string]walOp[T] // keyed by ID, last op wins
	closed  bool
}

// Begin starts a write transaction with exclusive WAL lock.
//
// Replays pending WAL before returning. Caller must call [Tx.Commit] or
// [Tx.Rollback] to release lock.
//
// Returns [ErrClosed] if store is closed. Also returns lock timeout or
// WAL replay failures.
func (mddb *MDDB[T]) Begin(ctx context.Context) (*Tx[T], error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}

	if mddb == nil || mddb.closed.Load() {
		return nil, ErrClosed
	}

	release, err := mddb.acquireWriteLockWithWalRecover(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring write lock: %w", err)
	}

	return &Tx[T]{
		mddb:    mddb,
		ctx:     ctx,
		release: release,
		ops:     make(map[string]walOp[T]),
		closed:  false,
	}, nil
}

// Create buffers a new document for writing on [Tx.Commit].
//
// Returns [ErrAlreadyExists] if the document exists in the index or filesystem.
// Validates document fields (id, title, path, short_id must be non-empty).
// No disk I/O until commit.
func (tx *Tx[T]) Create(doc *T) (*T, error) {
	id, path, err := tx.validateForWrite(doc)
	if err != nil {
		return nil, withContext(fmt.Errorf("validating: %w", err), id, path)
	}

	// Check index first (fast path)
	exists, err := tx.existsInIndex(id)
	if err != nil {
		return nil, withContext(fmt.Errorf("checking index: %w", err), id, path)
	}

	if exists {
		return nil, withContext(ErrAlreadyExists, id, path)
	}

	// Check filesystem (source of truth)
	exists, err = tx.fileExists(path)
	if err != nil {
		return nil, withContext(fmt.Errorf("checking file: %w", err), id, path)
	}

	if exists {
		return nil, withContext(ErrAlreadyExists, id, path)
	}

	tx.bufferPut(id, path, doc, walKindCreate)

	return doc, nil
}

// Update buffers an existing document for writing on [Tx.Commit].
//
// Returns [ErrNotFound] if the document does not exist in the index or filesystem.
// Validates document fields (id, title, path, short_id must be non-empty).
// No disk I/O until commit.
func (tx *Tx[T]) Update(doc *T) (*T, error) {
	id, path, err := tx.validateForWrite(doc)
	if err != nil {
		return nil, withContext(fmt.Errorf("validating: %w", err), id, path)
	}

	// Check index first (fast path)
	exists, err := tx.existsInIndex(id)
	if err != nil {
		return nil, withContext(fmt.Errorf("checking index: %w", err), id, path)
	}

	if !exists {
		return nil, withContext(ErrNotFound, id, path)
	}

	// Check filesystem (source of truth)
	exists, err = tx.fileExists(path)
	if err != nil {
		return nil, withContext(fmt.Errorf("checking file: %w", err), id, path)
	}

	if !exists {
		return nil, withContext(ErrNotFound, id, path)
	}

	tx.bufferPut(id, path, doc, walKindUpdate)

	return doc, nil
}

// validateForWrite performs common validation for Create and Update.
func (tx *Tx[T]) validateForWrite(doc *T) (string, string, error) {
	if tx == nil {
		return "", "", errors.New("tx is nil")
	}

	if tx.closed {
		return "", "", errors.New("transaction closed")
	}

	if doc == nil {
		return "", "", errors.New("document is nil")
	}

	// Type assert to get Document interface methods
	d, ok := any(*doc).(Document)
	if !ok {
		return "", "", errors.New("type assertion to Document failed")
	}

	id, path, err := tx.mddb.validateDocument(d)
	if err != nil {
		return id, "", err
	}

	if existing, ok := tx.ops[id]; ok && existing.Path != "" && existing.Path != path {
		return id, "", fmt.Errorf("path mismatch for existing buffered document %q != %q", existing.Path, path)
	}

	return id, path, nil
}

// existsInIndex checks if a document ID exists in the SQLite index.
func (tx *Tx[T]) existsInIndex(id string) (bool, error) {
	var exists bool

	query := fmt.Sprintf("SELECT 1 FROM %s WHERE id = ? LIMIT 1", tx.mddb.schema.tableName)
	row := tx.mddb.sql.QueryRowContext(tx.ctx, query, id)

	err := row.Scan(&exists)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	return false, fmt.Errorf("sqlite: %w", err)
}

// fileExists checks if a document file exists on disk.
func (tx *Tx[T]) fileExists(path string) (bool, error) {
	absPath := filepath.Join(tx.mddb.dataDir, path)

	info, err := tx.mddb.fs.Stat(absPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("fs: path %s is not a regular file", absPath)
		}

		return true, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return false, fmt.Errorf("fs: %w", err)
}

// bufferPut adds a put operation to the transaction buffer.
func (tx *Tx[T]) bufferPut(id, path string, doc *T, kind walKind) {
	tx.ops[id] = walOp[T]{
		Op:   walOpPut,
		Kind: kind,
		ID:   id,
		Path: path,
		Doc:  doc,
	}
}

// Delete buffers a document for removal on [Tx.Commit].
//
// Returns [ErrNotFound] if the document file does not exist.
func (tx *Tx[T]) Delete(id string) error {
	if tx == nil {
		return errors.New("tx is nil")
	}

	if tx.closed {
		return errors.New("transaction closed")
	}

	if id == "" {
		return errEmptyID
	}

	if tx.mddb.cfg.RelPathFromID == nil {
		return errors.New("RelPathFromID is nil")
	}

	path := tx.mddb.cfg.RelPathFromID(id)
	if path == "" {
		return withContext(errEmptyPath, id, "")
	}

	err := tx.mddb.validateRelPath(path)
	if err != nil {
		return withContext(fmt.Errorf("validating path: %w", err), id, path)
	}

	if existing, ok := tx.ops[id]; ok {
		switch existing.Op {
		case walOpPut:
			// Allow delete-after-put without touching disk; preserves atomic intent.
			if existing.Path != "" && existing.Path != path {
				return withContext(fmt.Errorf("path mismatch for existing buffered document %q != %q", existing.Path, path), id, path)
			}
		case walOpDelete:
			return nil
		}
	} else {
		absPath := filepath.Join(tx.mddb.dataDir, path)

		_, statErr := tx.mddb.fs.Stat(absPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return withContext(ErrNotFound, id, path)
			}

			return withContext(fmt.Errorf("fs: %w", statErr), id, path)
		}
	}

	tx.ops[id] = walOp[T]{
		Op:   walOpDelete,
		Kind: walKindDelete,
		ID:   id,
		Path: path,
	}

	return nil
}

// Commit persists all buffered operations atomically.
//
// Writes WAL (crash-safe commit point), then files, then SQLite index.
// Empty transaction is a no-op. Transaction is closed after Commit; do not reuse.
// Crash after WAL write is recovered on next [Open] or read.
// Returns [ErrCommitIncomplete] if WAL was durable but apply/index failed.
func (tx *Tx[T]) Commit(ctx context.Context) error {
	if tx == nil {
		return errors.New("tx is nil")
	}

	if tx.closed {
		return errors.New("transaction closed")
	}

	tx.closed = true

	defer func() {
		if tx.release != nil {
			_ = tx.release()
			tx.release = nil
		}
	}()

	if len(tx.ops) == 0 {
		return nil
	}

	ops := make([]walOp[T], 0, len(tx.ops))
	for _, txOp := range tx.ops {
		ops = append(ops, txOp)
	}

	// Snapshot markdown content before WAL write so recovery replays exact bytes.
	err := tx.materializeOps(ops)
	if err != nil {
		return fmt.Errorf("materializing ops: %w", err)
	}

	err = tx.writeWAL(ops)
	if err != nil {
		return fmt.Errorf("writing wal: %w", err)
	}

	// WAL fsync is the durable commit point; finish apply even if ctx is canceled.
	applyCtx := context.WithoutCancel(ctx)

	err = tx.mddb.applyOpsToFS(applyCtx, ops)
	if err != nil {
		return fmt.Errorf("%w: applying ops to fs: %w", ErrCommitIncomplete, err)
	}

	err = tx.mddb.updateSqliteIndexFromOps(applyCtx, ops)
	if err != nil {
		return fmt.Errorf("%w: updating index: %w", ErrCommitIncomplete, err)
	}

	// Ignore truncate errors - commit already succeeded, replay is idempotent.
	_ = truncateWal(tx.mddb.wal)

	return nil
}

func (tx *Tx[T]) materializeOps(ops []walOp[T]) error {
	for i := range ops {
		op := &ops[i]
		if op.Op != walOpPut {
			continue
		}

		if op.Content != "" {
			continue
		}

		if op.Doc == nil {
			return fmt.Errorf("missing document (doc_id=%s)", op.ID)
		}

		content, err := tx.mddb.marshalDocument(*op.Doc)
		if err != nil {
			return fmt.Errorf("marshaling document: %w (doc_id=%s)", err, op.ID)
		}

		op.Content = string(content)
	}

	return nil
}

// DB returns the underlying SQLite handle for direct queries.
//
// Safe because [Tx] holds exclusive lock. Useful for:
//   - Auto-increment IDs (query sqlite_sequence)
//   - Validation queries before Create/Update
//   - Any read that needs transaction isolation
//
// Example (auto-increment with INTEGER PRIMARY KEY AUTOINCREMENT):
//
//	tx, _ := db.Begin(ctx)
//	defer tx.Rollback()
//
//	var seq int64
//	tx.DB().QueryRow("SELECT seq FROM sqlite_sequence WHERE name = ?", "docs").Scan(&seq)
//	doc.id = seq + 1
//
//	tx.Create(&doc)
//	tx.Commit(ctx)
//
// # Footguns
//
//   - Direct writes bypass WAL crash recovery. If you INSERT/UPDATE/DELETE
//     directly and crash before Commit, those SQLite changes are lost while
//     file writes may persist, causing inconsistency.
//   - Use [Tx.Create], [Tx.Update], and [Tx.Delete] for document operations.
//   - Direct reads are safe.
func (tx *Tx[T]) DB() *sql.DB {
	return tx.mddb.sql
}

// Rollback discards buffered operations and releases the lock.
// Safe on nil, after Commit, or multiple times (no-op).
func (tx *Tx[T]) Rollback() error {
	if tx == nil {
		return nil
	}

	if tx.closed {
		return nil
	}

	tx.closed = true
	tx.ops = nil

	if tx.release != nil {
		_ = tx.release()
		tx.release = nil
	}

	return nil
}

func (tx *Tx[T]) writeWAL(ops []walOp[T]) error {
	content, err := encodeWalContent(ops)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}

	err = truncateWal(tx.mddb.wal)
	if err != nil {
		return fmt.Errorf("truncating existing wal: %w", err)
	}

	_, err = tx.mddb.wal.Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("fs: seek: %w", err)
	}

	n, err := tx.mddb.wal.Write(content)
	if err != nil {
		truncErr := truncateWal(tx.mddb.wal)
		if truncErr != nil {
			truncErr = fmt.Errorf("truncating wal on rollback: %w", truncErr)
		}

		return errors.Join(fmt.Errorf("fs: write: %w", err), truncErr)
	}

	if n != len(content) {
		truncErr := truncateWal(tx.mddb.wal)
		if truncErr != nil {
			truncErr = fmt.Errorf("truncating wal on rollback: %w", truncErr)
		}

		return errors.Join(fmt.Errorf("fs: short write %d/%d bytes", n, len(content)), truncErr)
	}

	err = tx.mddb.wal.Sync()
	if err != nil {
		// On fsync failures, don't try any further file ops.
		return fmt.Errorf("fs: sync: %w", err)
	}

	return nil
}
