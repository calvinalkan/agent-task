package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// ErrCommitAmbiguous indicates WAL was durable but apply/index failed.
var ErrCommitAmbiguous = errors.New("commit ambiguous")

// Tx buffers write operations until [Tx.Commit] persists them atomically.
//
// Create via [MDDB.Begin]. Holds exclusive WAL lock until Commit or Rollback.
// Multiple Put/Delete for same ID allowed; last wins.
//
// Commit writes WAL (crash-safe), then files, then index. Crash after WAL
// write is recovered on next [Open] or read.
type Tx[T Document] struct {
	store  *MDDB[T]
	lock   *fs.Lock
	ops    map[string]walOp[T] // keyed by ID, last op wins
	closed bool
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
		return nil, errors.New("begin: context is nil")
	}

	if mddb == nil || mddb.sql == nil || mddb.wal == nil {
		return nil, fmt.Errorf("begin: %w", ErrClosed)
	}

	lockCtx, cancel := context.WithTimeout(ctx, mddb.lockTimeout)
	defer cancel()

	lock, err := mddb.locker.LockWithTimeout(lockCtx, mddb.lockPath)
	if err != nil {
		return nil, fmt.Errorf("begin: lock wal: %w", err)
	}

	err = mddb.recoverWalLocked(ctx)
	if err != nil {
		_ = lock.Close()

		return nil, fmt.Errorf("begin: %w", err)
	}

	return &Tx[T]{
		store:  mddb,
		lock:   lock,
		ops:    make(map[string]walOp[T]),
		closed: false,
	}, nil
}

// Put buffers a document for writing on [Tx.Commit].
//
// Validates via [Document.Validate]. Returns error if ID is empty.
// No disk I/O until commit.
func (tx *Tx[T]) Put(doc *T) (*T, error) {
	if tx == nil {
		return nil, errors.New("put: tx is nil")
	}

	if tx.closed {
		return nil, errors.New("put: transaction closed")
	}

	if doc == nil {
		return nil, errors.New("put: document is nil")
	}

	// Type assert to get Document interface methods
	d, ok := any(*doc).(Document)
	if !ok {
		return nil, errors.New("put: type assertion to Document failed")
	}

	err := d.Validate()
	if err != nil {
		return nil, fmt.Errorf("put: invalid document: %w", err)
	}

	id := d.ID()
	if id == "" {
		return nil, errors.New("put: id is empty")
	}

	path := d.RelPath()
	if path == "" {
		return nil, errors.New("put: path is empty")
	}

	err = tx.store.validateRelPath(path)
	if err != nil {
		return nil, fmt.Errorf("put: %w", err)
	}

	tx.ops[id] = walOp[T]{
		Op:   walOpPut,
		ID:   id,
		Path: path,
		Doc:  doc,
	}

	return doc, nil
}

// Delete buffers a document for removal on [Tx.Commit].
//
// Requires both ID and path (WAL needs path for crash recovery).
// Deleting non-existent document succeeds (idempotent).
func (tx *Tx[T]) Delete(id string, path string) error {
	if tx == nil {
		return errors.New("delete: tx is nil")
	}

	if tx.closed {
		return errors.New("delete: transaction closed")
	}

	if id == "" {
		return errors.New("delete: id is empty")
	}

	if path == "" {
		return errors.New("delete: path is empty")
	}

	err := tx.store.validateRelPath(path)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	tx.ops[id] = walOp[T]{
		Op:   walOpDelete,
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
// Returns [ErrCommitAmbiguous] if WAL was durable but apply/index failed.
func (tx *Tx[T]) Commit(ctx context.Context) error {
	if tx == nil {
		return errors.New("commit: tx is nil")
	}

	if tx.closed {
		return errors.New("commit: transaction closed")
	}

	tx.closed = true

	defer func() {
		if tx.lock != nil {
			_ = tx.lock.Close()
			tx.lock = nil
		}
	}()

	if len(tx.ops) == 0 {
		return nil
	}

	ops := make([]walOp[T], 0, len(tx.ops))
	for _, op := range tx.ops {
		ops = append(ops, op)
	}

	// Snapshot markdown content before WAL write so recovery replays exact bytes.
	err := tx.materializeOps(ops)
	if err != nil {
		return err
	}

	err = tx.writeWAL(ops)
	if err != nil {
		return err
	}

	// WAL fsync is the durable commit point; finish apply even if ctx is canceled.
	applyCtx := context.WithoutCancel(ctx)

	err = tx.store.applyOpsToFS(applyCtx, ops)
	if err != nil {
		return fmt.Errorf("commit: %w: %w", ErrCommitAmbiguous, err)
	}

	err = tx.store.updateSqliteIndexFromOps(applyCtx, ops)
	if err != nil {
		return fmt.Errorf("commit: %w: %w", ErrCommitAmbiguous, err)
	}

	// Ignore truncate errors - commit already succeeded, replay is idempotent.
	_ = truncateWal(tx.store.wal)

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
			return fmt.Errorf("commit: missing document for %s", op.ID)
		}

		content, err := tx.store.marshalDocument(*op.Doc)
		if err != nil {
			return fmt.Errorf("commit: %w", err)
		}

		op.Content = string(content)
	}

	return nil
}

// DB returns the underlying SQLite handle for direct queries.
//
// Safe because [Tx] holds exclusive lock. Useful for:
//   - Auto-increment IDs (query sqlite_sequence)
//   - Validation queries before Put
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
//	tx.Put(&doc)
//	tx.Commit(ctx)
//
// # Footguns
//
//   - Direct writes bypass WAL crash recovery. If you INSERT/UPDATE/DELETE
//     directly and crash before Commit, those SQLite changes are lost while
//     file writes may persist, causing inconsistency.
//   - Use [Tx.Put] and [Tx.Delete] for document operations.
//   - Direct reads are safe.
func (tx *Tx[T]) DB() *sql.DB {
	return tx.store.sql
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

	if tx.lock != nil {
		err := tx.lock.Close()
		tx.lock = nil

		if err != nil {
			return fmt.Errorf("rollback: unlock: %w", err)
		}
	}

	return nil
}

func (tx *Tx[T]) writeWAL(ops []walOp[T]) error {
	content, err := encodeWalContent(ops)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	err = truncateWal(tx.store.wal)
	if err != nil {
		return fmt.Errorf("commit: truncate wal: %w", err)
	}

	_, err = tx.store.wal.Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("commit: seek wal: %w", err)
	}

	n, err := tx.store.wal.Write(content)
	if err != nil {
		truncErr := truncateWal(tx.store.wal)

		return errors.Join(fmt.Errorf("commit: write wal: %w", err), truncErr)
	}

	if n != len(content) {
		truncErr := truncateWal(tx.store.wal)

		return errors.Join(fmt.Errorf("commit: short write: %d/%d bytes", n, len(content)), truncErr)
	}

	err = tx.store.wal.Sync()
	if err != nil {
		// On fsync failures, don't try any further file ops.
		return fmt.Errorf("commit: fsync wal: %w", err)
	}

	return nil
}
