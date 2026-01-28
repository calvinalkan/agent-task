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
	"strings"
	"syscall"

	"github.com/calvinalkan/fileproc"
	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/pkg/fs"
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
	Op          string            `json:"op"`
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Frontmatter TicketFrontmatter `json:"frontmatter,omitempty"`
	Content     string            `json:"content,omitempty"`
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

// truncateWal clears the WAL and fsyncs so readers see an empty log.
// Callers should wrap errors with context.
func truncateWal(file fs.File) error {
	fd := file.Fd()
	if fd == 0 {
		return errors.New("invalid file descriptor")
	}

	err := syscall.Ftruncate(int(fd), 0)
	if err != nil {
		return fmt.Errorf("ftruncate: %w", err)
	}

	err = file.Sync()
	if err != nil {
		return fmt.Errorf("fsync: %w", err)
	}

	return nil
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
			return nil, fmt.Errorf("parse line: %w: %w", ErrWALReplay, unmarshalErr)
		}

		validated, err := validateWalOp(op)
		if err != nil {
			return nil, err
		}

		ops = append(ops, validated)

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	return ops, nil
}

// validateWalOp enforces op shape, ID validity, and canonical path rules.
// Callers should wrap errors with context.
func validateWalOp(op walOp) (walOp, error) {
	if op.Op != walOpPut && op.Op != walOpDelete {
		return walOp{}, fmt.Errorf("invalid op %q: %w", op.Op, ErrWALReplay)
	}

	err := validateIDString(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("invalid id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	id, err := uuid.Parse(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("parse id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	err = validateUUIDv7(id)
	if err != nil {
		return walOp{}, fmt.Errorf("invalid id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	if op.Path == "" {
		return walOp{}, fmt.Errorf("empty path: %w", ErrWALReplay)
	}

	if strings.Contains(op.Path, "\\") {
		return walOp{}, fmt.Errorf("invalid path %q: %w", op.Path, ErrWALReplay)
	}

	if filepath.IsAbs(op.Path) {
		return walOp{}, fmt.Errorf("invalid path %q: %w", op.Path, ErrWALReplay)
	}

	clean := filepath.Clean(op.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return walOp{}, fmt.Errorf("invalid path %q: %w", op.Path, ErrWALReplay)
	}

	if clean != op.Path {
		return walOp{}, fmt.Errorf("invalid path %q: %w", op.Path, ErrWALReplay)
	}

	if !strings.HasSuffix(op.Path, ".md") {
		return walOp{}, fmt.Errorf("invalid path %q: %w", op.Path, ErrWALReplay)
	}

	expected, err := TicketPath(id)
	if err != nil {
		return walOp{}, fmt.Errorf("derive path for %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	if filepath.Clean(op.Path) != filepath.Clean(expected) {
		return walOp{}, fmt.Errorf("path mismatch %q for id %q: %w", op.Path, op.ID, ErrWALReplay)
	}

	if op.Op == walOpPut {
		if op.Frontmatter == nil {
			return walOp{}, fmt.Errorf("missing frontmatter for %q: %w", op.ID, ErrWALReplay)
		}
	}

	return op, nil
}

// replayWalOpsToFS applies WAL operations to the filesystem using atomic writes.
func (s *Store) replayWalOpsToFS(ctx context.Context, ops []walOp) error {
	for _, op := range ops {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("replay canceled: %w", context.Cause(ctx))
		}

		absPath := filepath.Join(s.dir, op.Path)

		switch op.Op {
		case walOpPut:
			content, err := ticketToFileContent(op.Frontmatter, op.Content)
			if err != nil {
				return fmt.Errorf("render ticket %s: %w", op.ID, err)
			}

			err = s.fs.MkdirAll(filepath.Dir(absPath), 0o750)
			if err != nil {
				return fmt.Errorf("mkdir %s: %w", absPath, err)
			}

			err = s.atomic.WriteWithDefaults(absPath, strings.NewReader(content))
			if err != nil {
				return fmt.Errorf("write %s: %w", absPath, err)
			}
		case walOpDelete:
			err := s.fs.Remove(absPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete %s: %w", absPath, err)
			}

			dir := filepath.Dir(absPath)

			fh, err := s.fs.Open(dir)
			if err != nil {
				return fmt.Errorf("open dir %q: %w", dir, err)
			}

			func() {
				defer func() { _ = fh.Close() }()

				err = fh.Sync()
			}()

			if err != nil {
				return fmt.Errorf("sync dir %q: %w", dir, err)
			}
		default:
			return fmt.Errorf("replay op %q: %w", op.Op, ErrWALReplay)
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
			fm := op.Frontmatter
			if fm == nil {
				return fmt.Errorf("missing frontmatter for %s: %w", op.ID, ErrWALReplay)
			}

			var entry Ticket

			entry, err = s.indexFromWAL(op, fm)
			if err != nil {
				return err
			}

			err = inserter.Insert(ctx, tx, &entry)
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

// indexFromWAL builds an index row for a WAL put after the file write.
// Callers should wrap errors with context.
func (s *Store) indexFromWAL(op walOp, fm TicketFrontmatter) (Ticket, error) {
	absPath := filepath.Join(s.dir, op.Path)

	info, err := s.fs.Stat(absPath)
	if err != nil {
		return Ticket{}, fmt.Errorf("stat %s: %w", absPath, err)
	}

	stat := fileproc.Stat{
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode()),
	}

	entry, _, err := ticketFromFrontmatter(op.Path, absPath, s.dir, stat, fm, strings.NewReader(op.Content))
	if err != nil {
		return Ticket{}, err // ticketFromFrontmatter already includes context
	}

	return entry, nil
}

// ticketToFileContent serializes frontmatter and body into a deterministic ticket payload.
// Callers should wrap errors with context.
func ticketToFileContent(fm TicketFrontmatter, body string) (string, error) {
	frontmatter, err := fm.MarshalYAML()
	if err != nil {
		return "", fmt.Errorf("marshal frontmatter: %w: %w", ErrWALReplay, err)
	}

	builder := strings.Builder{}
	builder.WriteString(frontmatter)
	builder.WriteString("\n")
	builder.WriteString(body)

	if !strings.HasSuffix(body, "\n") {
		builder.WriteString("\n")
	}

	return builder.String(), nil
}
