package store

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/google/uuid"
)

// Tx buffers write operations until Commit persists them atomically.
// The zero value is not usable; call [Store.Begin] to create a transaction.
//
// A Tx holds an exclusive WAL lock for its lifetime. Callers must call either
// [Tx.Commit] or [Tx.Rollback] to release the lock. Failing to do so blocks
// other writers and eventually readers (when WAL recovery is needed).
//
// If Commit fails after the WAL is written, the next Open or read operation
// replays the WAL to restore consistency (idempotent replay).
type Tx struct {
	store  *Store
	lock   *fs.Lock
	ops    map[uuid.UUID]walOp // keyed by ID, last op wins
	closed bool
}

// Begin starts a write transaction by acquiring an exclusive WAL lock.
// It recovers any pending WAL state before returning to ensure a clean slate.
//
// The caller must call [Tx.Commit] or [Tx.Rollback] to release the lock.
// Begin uses the store's configured lock timeout (default 10s).
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	if ctx == nil {
		return nil, errors.New("begin: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return nil, errors.New("begin: store is not open")
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	lock, err := s.locker.LockWithTimeout(lockCtx, s.lockPath)
	if err != nil {
		return nil, fmt.Errorf("begin: lock wal: %w", err)
	}

	err = s.recoverWalLocked(ctx)
	if err != nil {
		_ = lock.Close()

		return nil, fmt.Errorf("begin: %w", err)
	}

	return &Tx{
		store:  s,
		lock:   lock,
		ops:    make(map[uuid.UUID]walOp),
		closed: false,
	}, nil
}

// Put buffers a ticket write operation. The ticket is validated before buffering.
//
// Put does not write to disk; changes are applied atomically on [Tx.Commit].
// Multiple Puts for the same ID within a transaction are allowed; the last
// one wins.
//
// The ticket must have a valid UUIDv7 ID, and Path/ShortID must match the ID.
// Use [NewTicket] to create tickets with generated IDs and derived fields.
func (tx *Tx) Put(t *Ticket) (*Ticket, error) {
	if tx == nil {
		return nil, errors.New("put: tx is nil")
	}

	if tx.closed {
		return nil, errors.New("put: transaction closed")
	}

	if t == nil {
		return nil, errors.New("put: ticket is nil")
	}

	ticket := *t

	err := ticket.validate()
	if err != nil {
		return nil, fmt.Errorf("put: invalid ticket: %w", err)
	}

	tx.ops[ticket.ID] = walOp{
		Op:     walOpPut,
		ID:     ticket.ID,
		Path:   ticket.Path,
		Ticket: &ticket,
	}

	return &ticket, nil
}

// Delete buffers a ticket delete operation. The ticket file is removed on Commit.
//
// Delete validates the ID is a valid UUIDv7 but does not check if the file exists.
// Deleting a non-existent file succeeds silently (idempotent).
func (tx *Tx) Delete(id uuid.UUID) error {
	if tx == nil {
		return errors.New("delete: tx is nil")
	}

	if tx.closed {
		return errors.New("delete: transaction closed")
	}

	if id.Version() != 7 {
		return fmt.Errorf("delete: id %q is not UUIDv7", id)
	}

	relPath, err := pathFromID(id)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	tx.ops[id] = walOp{
		Op:   walOpDelete,
		ID:   id,
		Path: relPath,
	}

	return nil
}

// Commit persists all buffered operations atomically:
//  1. Write WAL with fsync (commit point)
//  2. Apply file writes/deletes
//  3. Update SQLite index
//  4. Truncate WAL
//
// If Commit fails after writing the WAL, the next Open/read replays it.
// Commit releases the lock and closes the transaction.
func (tx *Tx) Commit(ctx context.Context) error {
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

	ops := make([]walOp, 0, len(tx.ops))
	for _, op := range tx.ops {
		ops = append(ops, op)
	}

	err := tx.writeWAL(ops)
	if err != nil {
		return err
	}

	err = tx.store.replayWalOpsToFS(ctx, ops)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	err = tx.store.updateSqliteIndexFromOps(ctx, ops)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Ignore truncate errors - commit already succeeded, replay is idempotent.
	_ = truncateWal(tx.store.wal)

	return nil
}

// Rollback discards all buffered operations and releases the exclusive lock.
// Safe to call multiple times; subsequent calls are no-ops.
func (tx *Tx) Rollback() error {
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

func (tx *Tx) writeWAL(ops []walOp) error {
	content, err := encodeWalContent(ops)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
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
