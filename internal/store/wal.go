package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/google/uuid"
)

const (
	walMagic      = "TKWAL001"
	walFooterSize = 32
)

const (
	walOpPut    = "put"
	walOpDelete = "delete"
)

var walCRC32C = crc32.MakeTable(crc32.Castagnoli)

// ErrWALCorrupt reports a committed WAL with a mismatched checksum.
// Callers should use errors.Is(err, ErrWALCorrupt).
var ErrWALCorrupt = errors.New("wal corrupt")

// ErrWALReplay reports WAL validation or replay failures.
// Callers should use errors.Is(err, ErrWALReplay).
var ErrWALReplay = errors.New("wal replay")

// walState describes the WAL state discovered during recovery.
type walState uint8

const (
	walEmpty       walState = iota // WAL has no data.
	walUncommitted                 // WAL has data but no valid footer.
	walCommitted                   // WAL has a valid footer and checksum.
)

type walOp struct {
	Op     string    `json:"op"`
	ID     uuid.UUID `json:"id"`
	Path   string    `json:"path"`
	Ticket *Ticket   `json:"ticket,omitempty"` // nil for delete
}

// recoverWalLocked recovers any pending WAL state to restore consistency.
// It must be called under the WAL write lock.
//
// Behavior by WAL state:
//   - Empty: returns nil immediately (no work needed).
//   - Uncommitted: truncates the incomplete WAL and returns nil.
//   - Committed: replays ops to filesystem, updates SQLite index, truncates WAL.
//
// On success the WAL is always empty. On error the WAL may be partially
// processed; callers should treat errors as fatal for the Store.
func (s *Store) recoverWalLocked(ctx context.Context) error {
	state, body, err := readWalState(s.wal)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	switch state {
	case walEmpty:
		return nil
	case walUncommitted:
		err = truncateWal(s.wal)
		if err != nil {
			return fmt.Errorf("truncate uncommitted wal: %w", err)
		}

		return nil
	case walCommitted:
		ops, err := decodeWalOps(body)
		if err != nil {
			return fmt.Errorf("decode wal: %w", err)
		}

		err = s.replayWalOpsToFS(ctx, ops)
		if err != nil {
			return fmt.Errorf("replay wal: %w", err)
		}

		err = s.updateSqliteIndexFromOps(ctx, ops)
		if err != nil {
			return fmt.Errorf("update index: %w", err)
		}

		err = truncateWal(s.wal)
		if err != nil {
			return fmt.Errorf("truncate wal: %w", err)
		}

		return nil
	default:
		return fmt.Errorf("unknown wal state %d", state)
	}
}

// replayWalOpsToFS applies WAL operations to the filesystem using atomic writes.
func (s *Store) replayWalOpsToFS(ctx context.Context, ops []walOp) error {
	dirsToSync := make(map[string]struct{})

	for _, op := range ops {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("replay canceled: %w", context.Cause(ctx))
		}

		absPath := filepath.Join(s.dir, op.Path)
		dir := filepath.Dir(absPath)

		switch op.Op {
		case walOpPut:
			if op.Ticket == nil {
				return fmt.Errorf("%w: missing ticket for %s", ErrWALReplay, op.ID)
			}

			content, err := op.Ticket.marshalFile()
			if err != nil {
				return fmt.Errorf("render ticket %s: %w", op.ID, err)
			}

			err = s.fs.MkdirAll(dir, 0o750)
			if err != nil {
				return fmt.Errorf("mkdir %s: %w", absPath, err)
			}

			err = s.atomic.Write(absPath, bytes.NewReader(content), fs.AtomicWriteOptions{
				SyncDir: false,
				Perm:    0o644, // We handle dir sync ourselves at the end.
			})
			if err != nil {
				return fmt.Errorf("write %s: %w", absPath, err)
			}

			dirsToSync[dir] = struct{}{}

		case walOpDelete:
			err := s.fs.Remove(absPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete %s: %w", absPath, err)
			}

			dirsToSync[dir] = struct{}{}

		default:
			return fmt.Errorf("%w: replay op %q", ErrWALReplay, op.Op)
		}
	}

	for dir := range dirsToSync {
		fh, err := s.fs.Open(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("open dir %q: %w", dir, err)
		}

		syncErr := fh.Sync()
		closeErr := fh.Close()

		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
	}

	return nil
}

// updateSqliteIndexFromOps applies WAL operations to SQLite in a single transaction.
// Callers should wrap errors with context.
func (s *Store) updateSqliteIndexFromOps(ctx context.Context, ops []walOp) error {
	tx, err := s.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	inserter, err := prepareTicketInserter(ctx, tx)
	if err != nil {
		return err
	}

	defer inserter.Close()

	for _, op := range ops {
		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("canceled: %w", context.Cause(ctx))
		}

		switch op.Op {
		case walOpDelete:
			err = deleteTicket(ctx, tx, op.ID)
			if err != nil {
				return err
			}
		case walOpPut:
			if op.Ticket == nil {
				return fmt.Errorf("%w: missing ticket for %s", ErrWALReplay, op.ID)
			}

			err = inserter.Insert(ctx, tx, op.Ticket)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown op %q", op.Op)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	committed = true

	return nil
}

// readWalState inspects the WAL footer and checksum to decide whether the WAL
// is empty, uncommitted, committed, or corrupt. For committed WALs it returns
// the validated body bytes. Callers should wrap errors with context.
func readWalState(file fs.File) (walState, []byte, error) {
	info, err := file.Stat()
	if err != nil {
		return walEmpty, nil, fmt.Errorf("stat: %w", err)
	}

	size := info.Size()
	if size == 0 {
		return walEmpty, nil, nil
	}

	if size < walFooterSize {
		return walUncommitted, nil, nil
	}

	footerBuf := make([]byte, walFooterSize)

	_, err = file.Seek(size-walFooterSize, io.SeekStart)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("seek footer: %w", err)
	}

	_, err = io.ReadFull(file, footerBuf)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return walUncommitted, nil, nil
		}

		return walEmpty, nil, fmt.Errorf("read footer: %w", err)
	}

	if string(footerBuf[:8]) != walMagic {
		return walUncommitted, nil, nil
	}

	bodyLen := binary.LittleEndian.Uint64(footerBuf[8:16])

	bodyLenInv := binary.LittleEndian.Uint64(footerBuf[16:24])
	if ^bodyLen != bodyLenInv {
		return walUncommitted, nil, nil
	}

	crc := binary.LittleEndian.Uint32(footerBuf[24:28])

	crcInv := binary.LittleEndian.Uint32(footerBuf[28:32])
	if ^crc != crcInv {
		return walUncommitted, nil, nil
	}

	if bodyLen > math.MaxInt64 {
		return walUncommitted, nil, nil
	}

	maxBody := size - walFooterSize
	if int64(bodyLen) > maxBody {
		return walUncommitted, nil, nil
	}

	body := make([]byte, bodyLen)

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("seek body: %w", err)
	}

	_, err = io.ReadFull(file, body)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("read body: %w", err)
	}

	checksum := crc32.Checksum(body, walCRC32C)
	if checksum != crc {
		return walCommitted, nil, fmt.Errorf("%w: checksum mismatch (expected %08x got %08x)", ErrWALCorrupt, crc, checksum)
	}

	return walCommitted, body, nil
}

// truncateWal clears the WAL. No fsync - if truncate isn't persisted and we
// crash, recovery replays the WAL which is idempotent.
func truncateWal(file fs.File) error {
	fd := file.Fd()
	if fd == 0 {
		return errors.New("invalid file descriptor")
	}

	err := syscall.Ftruncate(int(fd), 0)
	if err != nil {
		return fmt.Errorf("ftruncate: %w", err)
	}

	return nil
}

// encodeWalContent serializes operations to a complete WAL payload (JSONL body + footer).
// The returned bytes can be written directly to the WAL file.
func encodeWalContent(ops []walOp) ([]byte, error) {
	var body bytes.Buffer

	enc := json.NewEncoder(&body)
	for _, op := range ops {
		err := enc.Encode(op)
		if err != nil {
			return nil, fmt.Errorf("encode op: %w", err)
		}
	}

	bodyBytes := body.Bytes()

	// Build 32-byte footer: magic + lengths + CRC.
	footer := make([]byte, walFooterSize)
	copy(footer[:8], walMagic)

	bodyLen := uint64(len(bodyBytes))
	binary.LittleEndian.PutUint64(footer[8:16], bodyLen)
	binary.LittleEndian.PutUint64(footer[16:24], ^bodyLen)

	crc := crc32.Checksum(bodyBytes, walCRC32C)
	binary.LittleEndian.PutUint32(footer[24:28], crc)
	binary.LittleEndian.PutUint32(footer[28:32], ^crc)

	result := make([]byte, len(bodyBytes)+walFooterSize)
	copy(result, bodyBytes)
	copy(result[len(bodyBytes):], footer)

	return result, nil
}

// decodeWalOps parses the JSONL body into validated operations.
// Callers should wrap errors with context.
func decodeWalOps(body []byte) ([]walOp, error) {
	reader := bufio.NewReader(bytes.NewReader(body))
	ops := make([]walOp, 0)

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}

			return nil, fmt.Errorf("read line: %w", readErr)
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}

			if readErr == nil {
				continue
			}
		}

		var op walOp

		unmarshalErr := json.Unmarshal(line, &op)
		if unmarshalErr != nil {
			return nil, fmt.Errorf("%w: parse line: %w", ErrWALReplay, unmarshalErr)
		}

		validated, err := validateWalOp(op)
		if err != nil {
			return nil, err
		}

		// Set mtime to now so the in-memory ticket and SQLite index are fresh
		// without needing to stat the file after replay. This is fine since
		// the ticket/SQLite always holds a potentially stale mtime anyway.
		if validated.Op == walOpPut && validated.Ticket != nil {
			validated.Ticket.MtimeNS = time.Now().UnixNano()
		}

		ops = append(ops, validated)

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	return ops, nil
}

// validateWalOp enforces op shape and validates the ticket for puts.
func validateWalOp(op walOp) (walOp, error) {
	if op.Op != walOpPut && op.Op != walOpDelete {
		return walOp{}, fmt.Errorf("%w: invalid op %q", ErrWALReplay, op.Op)
	}

	if op.Op == walOpPut {
		if op.Ticket == nil {
			return walOp{}, fmt.Errorf("%w: missing ticket for put", ErrWALReplay)
		}

		err := op.Ticket.validate()
		if err != nil {
			return walOp{}, fmt.Errorf("%w: invalid ticket: %w", ErrWALReplay, err)
		}

		if op.Path != op.Ticket.Path {
			return walOp{}, fmt.Errorf("%w: op path %q does not match ticket path %q", ErrWALReplay, op.Path, op.Ticket.Path)
		}

		if op.ID != op.Ticket.ID {
			return walOp{}, fmt.Errorf("%w: op id %q does not match ticket id %q", ErrWALReplay, op.ID, op.Ticket.ID)
		}
	}

	if op.Op == walOpDelete {
		// For deletes, just validate the ID is UUIDv7 and path matches
		if op.ID.Version() != 7 {
			return walOp{}, fmt.Errorf("%w: id %q is not UUIDv7", ErrWALReplay, op.ID)
		}

		expected, err := pathFromID(op.ID)
		if err != nil {
			return walOp{}, fmt.Errorf("%w: %w", ErrWALReplay, err)
		}

		if op.Path != expected {
			return walOp{}, fmt.Errorf("%w: path %q does not match id %q", ErrWALReplay, op.Path, op.ID)
		}
	}

	return op, nil
}
