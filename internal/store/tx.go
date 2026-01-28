package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// Tx buffers write operations until Commit persists them atomically.
// The zero value is not usable; call [Store.Begin] to create a transaction.
//
// A Tx holds an exclusive WAL lock for its lifetime. Callers must call either
// [Tx.Commit] or [Tx.Rollback] to release the lock. Failing to do so blocks
// other writers and eventually readers (when WAL recovery is needed).
//
// Operations within a Tx are buffered in memory. The commit sequence:
//  1. Encode buffered ops to JSONL + footer
//  2. Write WAL body + footer, fsync (commit point)
//  3. Apply file writes/deletes via atomic helpers
//  4. Update SQLite index in a single transaction
//  5. Truncate WAL
//
// If Commit fails after the WAL is written, the next Open or read operation
// replays the WAL to restore consistency (idempotent replay).
type Tx struct {
	store  *Store
	lock   *fs.Lock
	ops    map[string]walOp // keyed by ID, last op wins
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

	// Recover any pending WAL before starting a new transaction.
	// This ensures we don't overwrite uncommitted or partially-committed state.
	err = s.recoverWalLocked(ctx)
	if err != nil {
		_ = lock.Close()

		return nil, fmt.Errorf("begin: %w", err)
	}

	return &Tx{
		store:  s,
		lock:   lock,
		ops:    make(map[string]walOp),
		closed: false,
	}, nil
}

// Put buffers a ticket write operation. The ticket is validated and its path
// is derived from the ID. If the ID is empty, a new UUIDv7 is generated.
//
// Put does not write to disk; changes are applied atomically on [Tx.Commit].
// Multiple Puts for the same ID within a transaction are allowed; the last
// one wins.
//
// Required fields: Status, Type, Priority, CreatedAt, Title.
// Optional: Assignee, Parent, ExternalRef, BlockedBy, ClosedAt.
func (tx *Tx) Put(ctx context.Context, t *Ticket) (*Ticket, error) {
	if ctx == nil {
		return nil, errors.New("put: context is nil")
	}

	if tx == nil {
		return nil, errors.New("put: tx is nil")
	}

	if tx.closed {
		return nil, errors.New("put: transaction closed")
	}

	ticket := *t // copy to avoid mutating caller's struct

	// Generate or validate the ID.
	var parsed uuid.UUID

	if ticket.ID == "" {
		id, err := NewUUIDv7()
		if err != nil {
			return nil, fmt.Errorf("put: generate id: %w", err)
		}

		parsed = id
		ticket.ID = id.String()
	} else {
		id, err := uuid.Parse(ticket.ID)
		if err != nil {
			return nil, fmt.Errorf("put: invalid id %q: %w", ticket.ID, err)
		}

		err = validateUUIDv7(id)
		if err != nil {
			return nil, fmt.Errorf("put: %w", err)
		}

		parsed = id
	}

	// Derive path and short_id from ID.
	relPath, err := PathFromID(parsed)
	if err != nil {
		return nil, fmt.Errorf("put: %w", err)
	}

	shortID, err := ShortIDFromUUID(parsed)
	if err != nil {
		return nil, fmt.Errorf("put: %w", err)
	}

	ticket.Path = relPath
	ticket.ShortID = shortID

	// Validate required fields.
	if ticket.Status == "" {
		return nil, errors.New("put: status is required")
	}

	if ticket.Type == "" {
		return nil, errors.New("put: type is required")
	}

	if ticket.Priority < 1 {
		return nil, errors.New("put: priority must be >= 1")
	}

	if ticket.CreatedAt.IsZero() {
		return nil, errors.New("put: created_at is required")
	}

	if ticket.Title == "" {
		return nil, errors.New("put: title is required")
	}

	// Build frontmatter from ticket.
	fm := buildFrontmatter(&ticket)

	// Build content (markdown body).
	content := buildContent(&ticket)

	tx.ops[ticket.ID] = walOp{
		Op:          walOpPut,
		ID:          ticket.ID,
		Path:        relPath,
		Frontmatter: fm,
		Content:     content,
	}

	return &ticket, nil
}

// Delete buffers a ticket delete operation. The ticket file is removed on Commit.
//
// Delete validates the ID format but does not check if the file exists.
// Deleting a non-existent file succeeds silently (idempotent).
func (tx *Tx) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("delete: context is nil")
	}

	if tx == nil {
		return errors.New("delete: tx is nil")
	}

	if tx.closed {
		return errors.New("delete: transaction closed")
	}

	if id == "" {
		return errors.New("delete: id is empty")
	}

	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("delete: invalid id %q: %w", id, err)
	}

	err = validateUUIDv7(parsed)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	relPath, err := PathFromID(parsed)
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

// Commit persists all buffered operations atomically. The sequence:
//  1. Encode ops to JSONL, append CRC footer, write to WAL, fsync (commit point)
//  2. Apply file writes/deletes using atomic helpers
//  3. Update SQLite index in a single transaction
//  4. Truncate WAL
//
// If Commit fails after writing the WAL but before truncating it, the next
// Open or read operation replays the WAL (idempotent recovery).
//
// Commit releases the exclusive lock and marks the transaction as closed.
// Further operations on this Tx return an error.
func (tx *Tx) Commit(ctx context.Context) error {
	if ctx == nil {
		return errors.New("commit: context is nil")
	}

	if tx == nil {
		return errors.New("commit: tx is nil")
	}

	if tx.closed {
		return errors.New("commit: transaction closed")
	}

	// Mark closed early to prevent double-commit. Lock released in defer.
	tx.closed = true

	defer func() {
		if tx.lock != nil {
			_ = tx.lock.Close()
			tx.lock = nil
		}
	}()

	// Empty transaction: nothing to do.
	if len(tx.ops) == 0 {
		return nil
	}

	// Convert map to slice. The map already contains only the last op per ID.
	ops := make([]walOp, 0, len(tx.ops))
	for _, op := range tx.ops {
		ops = append(ops, op)
	}

	// Step 1: Write WAL body + footer (commit point).
	err := tx.writeWAL(ops)
	if err != nil {
		return err
	}

	// Step 2: Apply file operations.
	err = tx.store.replayWalOpsToFS(ctx, ops)
	if err != nil {
		// WAL is committed, so next Open/read will replay. Return error but
		// don't truncate WAL.
		return fmt.Errorf("commit: %w", err)
	}

	// Step 3: Update SQLite index.
	err = tx.store.updateSqliteIndexFromOps(ctx, ops)
	if err != nil {
		// WAL is committed, so next Open/read will replay.
		return fmt.Errorf("commit: %w", err)
	}

	// Step 4: Truncate WAL.
	err = truncateWal(tx.store.wal)
	if err != nil {
		// Files and index are updated. WAL truncation failure is non-fatal;
		// next Open will see a committed WAL and replay (idempotent).
		return fmt.Errorf("commit: truncate wal: %w", err)
	}

	return nil
}

// Rollback discards all buffered operations and releases the exclusive lock.
//
// Rollback is safe to call multiple times; subsequent calls are no-ops.
// After Rollback, further operations on this Tx return an error.
func (tx *Tx) Rollback() error {
	if tx == nil {
		return nil
	}

	if tx.closed {
		return nil // already closed, no-op
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

// writeWAL encodes ops to JSONL, appends the CRC footer, and fsyncs.
// The WAL file is written in place (not temp+rename) because it's our
// durable commit point. The footer signals a committed transaction.
func (tx *Tx) writeWAL(ops []walOp) error {
	// Encode JSONL body.
	var body strings.Builder

	enc := json.NewEncoder(&body)

	for _, op := range ops {
		err := enc.Encode(op)
		if err != nil {
			return fmt.Errorf("commit: encode wal op: %w", err)
		}
	}

	bodyBytes := []byte(body.String())

	// Build footer.
	footer := encodeFooter(bodyBytes)

	// Write body + footer to WAL file.
	// Use Seek + Write + Truncate to overwrite in place.
	_, err := tx.store.wal.Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("commit: seek wal: %w", err)
	}

	_, err = tx.store.wal.Write(bodyBytes)
	if err != nil {
		return fmt.Errorf("commit: write wal body: %w", err)
	}

	_, err = tx.store.wal.Write(footer)
	if err != nil {
		return fmt.Errorf("commit: write wal footer: %w", err)
	}

	// Truncate to exact size in case previous WAL was larger.
	totalSize := int64(len(bodyBytes) + len(footer))

	err = truncateToSize(tx.store.wal, totalSize)
	if err != nil {
		return fmt.Errorf("commit: truncate wal to size: %w", err)
	}

	// Fsync to make the commit durable.
	err = tx.store.wal.Sync()
	if err != nil {
		return fmt.Errorf("commit: fsync wal: %w", err)
	}

	return nil
}

// encodeFooter builds the 32-byte WAL footer with magic, lengths, and CRC.
func encodeFooter(body []byte) []byte {
	footer := make([]byte, walFooterSize)
	copy(footer[:8], walMagic)

	bodyLen := uint64(len(body))
	binary.LittleEndian.PutUint64(footer[8:16], bodyLen)
	binary.LittleEndian.PutUint64(footer[16:24], ^bodyLen)

	crc := crc32.Checksum(body, walCRC32C)
	binary.LittleEndian.PutUint32(footer[24:28], crc)
	binary.LittleEndian.PutUint32(footer[28:32], ^crc)

	return footer
}

// truncateToSize truncates the file to the given size.
func truncateToSize(file fs.File, size int64) error {
	osFile, ok := file.(*os.File)
	if !ok {
		return errors.New("truncate: not an *os.File")
	}

	err := osFile.Truncate(size)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}

	return nil
}

// buildFrontmatter constructs a TicketFrontmatter from a Ticket.
func buildFrontmatter(t *Ticket) TicketFrontmatter {
	fm := TicketFrontmatter{
		"id":             StringValue(t.ID),
		"schema_version": IntValue(1),
		"created":        StringValue(t.CreatedAt.UTC().Format(time.RFC3339)),
		"priority":       IntValue(t.Priority),
		"status":         StringValue(t.Status),
		"type":           StringValue(t.Type),
	}

	if t.Assignee != "" {
		fm["assignee"] = StringValue(t.Assignee)
	}

	if len(t.BlockedBy) > 0 {
		fm["blocked-by"] = ListValue(append([]string(nil), t.BlockedBy...))
	}

	if t.ClosedAt != nil {
		fm["closed"] = StringValue(t.ClosedAt.UTC().Format(time.RFC3339))
	}

	if t.ExternalRef != "" {
		fm["external-ref"] = StringValue(t.ExternalRef)
	}

	if t.Parent != "" {
		fm["parent"] = StringValue(t.Parent)
	}

	return fm
}

// buildContent constructs the markdown body from a Ticket.
// The body consists of a title heading followed by the description.
func buildContent(t *Ticket) string {
	var builder strings.Builder

	builder.WriteString("# ")
	builder.WriteString(t.Title)
	builder.WriteString("\n")

	// For now, we don't store the body in Ticket (only Title).
	// Future: add Body field to Ticket if needed.

	return builder.String()
}
