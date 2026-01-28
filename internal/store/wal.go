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

// ErrIndexUpdate reports failures updating the SQLite index from WAL ops.
// Callers should use errors.Is(err, ErrIndexUpdate).
var ErrIndexUpdate = errors.New("index update")

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

type walRecovery struct {
	state walState
	ops   []walOp
}

// recoverWalLocked replays or discards WAL entries under the WAL lock (must be held by caller).
// It only handles WAL and filesystem recovery, leaving index policy to the caller.
func (s *Store) recoverWalLocked(ctx context.Context) (walRecovery, error) {
	// Recovery runs under the WAL lock so readers never observe mid-commit state.
	state, body, err := readWalState(s.wal)
	if err != nil {
		return walRecovery{}, err
	}

	switch state {
	case walEmpty:
		return walRecovery{state: walEmpty}, nil
	case walUncommitted:
		// Stage: discard partial WAL (no commit point reached).
		err = truncateWal(s.wal)
		if err != nil {
			return walRecovery{}, fmt.Errorf("truncate uncommitted: %w", err)
		}

		return walRecovery{state: walUncommitted}, nil
	case walCommitted:
		// Stage: decode WAL ops.
		ops, err := decodeWalOps(body)
		if err != nil {
			return walRecovery{}, fmt.Errorf("decode wal ops: %w", err)
		}

		// Stage: replay filesystem changes.
		err = s.replayWalOps(ctx, ops)
		if err != nil {
			return walRecovery{}, fmt.Errorf("replay wal ops: %w", err)
		}

		return walRecovery{state: walCommitted, ops: ops}, nil
	default:
		return walRecovery{}, fmt.Errorf("unknown state %d", state)
	}
}

// readWalState inspects the WAL footer and checksum to decide whether the WAL
// is empty, uncommitted, committed, or corrupt. For committed WALs it returns
// the validated body bytes.
func readWalState(file fs.File) (walState, []byte, error) {
	info, err := file.Stat()
	if err != nil {
		return walEmpty, nil, fmt.Errorf("stat wal: %w", err)
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
		return walEmpty, nil, fmt.Errorf("seek wal footer: %w", err)
	}

	_, err = io.ReadFull(file, footerBuf)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return walUncommitted, nil, nil
		}

		return walEmpty, nil, fmt.Errorf("read wal footer: %w", err)
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
		return walEmpty, nil, fmt.Errorf("seek wal body: %w", err)
	}

	_, err = io.ReadFull(file, body)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("read wal body: %w", err)
	}

	checksum := crc32.Checksum(body, walCRC32C)
	if checksum != crc {
		return walCommitted, nil, fmt.Errorf("wal checksum mismatch (expected %08x got %08x): %w", crc, checksum, ErrWALCorrupt)
	}

	return walCommitted, body, nil
}

// truncateWal clears the WAL and fsyncs so readers see an empty log.
func truncateWal(file fs.File) error {
	fd := file.Fd()
	if fd == 0 {
		return errors.New("truncate wal: invalid file descriptor")
	}

	err := syscall.Ftruncate(int(fd), 0)
	if err != nil {
		return fmt.Errorf("truncate wal: %w", err)
	}

	err = file.Sync()
	if err != nil {
		return fmt.Errorf("sync wal: %w", err)
	}

	return nil
}

// decodeWalOps parses the JSONL body into validated operations.
func decodeWalOps(body []byte) ([]walOp, error) {
	reader := bufio.NewReader(bytes.NewReader(body))
	ops := make([]walOp, 0)

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}

			return nil, fmt.Errorf("read wal line: %w", readErr)
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
			return nil, fmt.Errorf("parse wal line: %w: %w", ErrWALReplay, unmarshalErr)
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
func validateWalOp(op walOp) (walOp, error) {
	if op.Op != walOpPut && op.Op != walOpDelete {
		return walOp{}, fmt.Errorf("validate wal op %q: %w", op.Op, ErrWALReplay)
	}

	err := validateIDString(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("validate wal id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	id, err := uuid.Parse(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("parse wal id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	err = validateUUIDv7(id)
	if err != nil {
		return walOp{}, fmt.Errorf("validate wal id %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	if op.Path == "" {
		return walOp{}, fmt.Errorf("validate wal path: %w", ErrWALReplay)
	}

	if strings.Contains(op.Path, "\\") {
		return walOp{}, fmt.Errorf("validate wal path %q: %w", op.Path, ErrWALReplay)
	}

	if filepath.IsAbs(op.Path) {
		return walOp{}, fmt.Errorf("validate wal path %q: %w", op.Path, ErrWALReplay)
	}

	clean := filepath.Clean(op.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return walOp{}, fmt.Errorf("validate wal path %q: %w", op.Path, ErrWALReplay)
	}

	if clean != op.Path {
		return walOp{}, fmt.Errorf("validate wal path %q: %w", op.Path, ErrWALReplay)
	}

	if !strings.HasSuffix(op.Path, ".md") {
		return walOp{}, fmt.Errorf("validate wal path %q: %w", op.Path, ErrWALReplay)
	}

	expected, err := TicketPath(id)
	if err != nil {
		return walOp{}, fmt.Errorf("derive wal path for %q: %w: %w", op.ID, ErrWALReplay, err)
	}

	if filepath.Clean(op.Path) != filepath.Clean(expected) {
		return walOp{}, fmt.Errorf("validate wal path %q for id %q: %w", op.Path, op.ID, ErrWALReplay)
	}

	if op.Op == walOpPut {
		if op.Frontmatter == nil {
			return walOp{}, fmt.Errorf("validate wal op %q missing frontmatter: %w", op.ID, ErrWALReplay)
		}
	}

	return op, nil
}

// replayWalOps applies WAL operations to the filesystem using atomic writes.
func (s *Store) replayWalOps(ctx context.Context, ops []walOp) error {
	for _, op := range ops {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("replay canceled: %w", context.Cause(ctx))
		}

		absPath := filepath.Join(s.dir, op.Path)

		switch op.Op {
		case walOpPut:
			content, err := renderTicketFile(op.Frontmatter, op.Content)
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

// updateIndexFromOps applies WAL operations to SQLite in a single transaction.
func (s *Store) updateIndexFromOps(ctx context.Context, ops []walOp) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update index begin txn: %w: %w", ErrIndexUpdate, err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	insertTicket, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO tickets (
			id,
			short_id,
			path,
			mtime_ns,
			status,
			type,
			priority,
			assignee,
			parent,
			created_at,
			closed_at,
			external_ref,
			title
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("update index prepare insert: %w: %w", ErrIndexUpdate, err)
	}

	defer func() { _ = insertTicket.Close() }()

	insertBlocker, err := tx.PrepareContext(ctx, `
		INSERT INTO ticket_blockers (ticket_id, blocker_id) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("update index prepare blocker insert: %w: %w", ErrIndexUpdate, err)
	}

	defer func() { _ = insertBlocker.Close() }()

	for _, op := range ops {
		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("update index canceled: %w: %w", ErrIndexUpdate, context.Cause(ctx))
		}

		switch op.Op {
		case walOpDelete:
			_, err = tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", op.ID)
			if err != nil {
				return fmt.Errorf("update index delete blockers %s: %w: %w", op.ID, ErrIndexUpdate, err)
			}

			_, err = tx.ExecContext(ctx, "DELETE FROM tickets WHERE id = ?", op.ID)
			if err != nil {
				return fmt.Errorf("update index delete ticket %s: %w: %w", op.ID, ErrIndexUpdate, err)
			}
		case walOpPut:
			fm := op.Frontmatter
			if fm == nil {
				return fmt.Errorf("update index missing frontmatter for %s: %w", op.ID, ErrWALReplay)
			}

			var entry Ticket

			entry, err = s.indexFromWAL(op, fm)
			if err != nil {
				return err
			}

			_, err = tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", entry.ID)
			if err != nil {
				return fmt.Errorf("update index clear blockers %s: %w: %w", entry.ID, ErrIndexUpdate, err)
			}

			assignee := sql.NullString{String: entry.Assignee, Valid: entry.Assignee != ""}
			parent := sql.NullString{String: entry.Parent, Valid: entry.Parent != ""}
			external := sql.NullString{String: entry.ExternalRef, Valid: entry.ExternalRef != ""}
			createdAt := entry.CreatedAt.Unix()

			closedAt := sql.NullInt64{}
			if entry.ClosedAt != nil {
				closedAt = sql.NullInt64{Int64: entry.ClosedAt.Unix(), Valid: true}
			}

			_, err = insertTicket.ExecContext(
				ctx,
				entry.ID,
				entry.ShortID,
				entry.Path,
				entry.MtimeNS,
				entry.Status,
				entry.Type,
				entry.Priority,
				assignee,
				parent,
				createdAt,
				closedAt,
				external,
				entry.Title,
			)
			if err != nil {
				return fmt.Errorf("update index insert ticket %s: %w: %w", entry.ID, ErrIndexUpdate, err)
			}

			for _, blocker := range entry.BlockedBy {
				_, err = insertBlocker.ExecContext(ctx, entry.ID, blocker)
				if err != nil {
					return fmt.Errorf("update index insert blocker %s: %w: %w", entry.ID, ErrIndexUpdate, err)
				}
			}
		default:
			return fmt.Errorf("update index unknown op %q: %w", op.Op, ErrIndexUpdate)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("update index commit: %w: %w", ErrIndexUpdate, err)
	}

	committed = true

	return nil
}

// indexFromWAL builds an index row for a WAL put after the file write.
func (s *Store) indexFromWAL(op walOp, fm TicketFrontmatter) (Ticket, error) {
	absPath := filepath.Join(s.dir, op.Path)

	info, err := s.fs.Stat(absPath)
	if err != nil {
		return Ticket{}, fmt.Errorf("index stat %s: %w: %w", absPath, ErrIndexUpdate, err)
	}

	stat := fileproc.Stat{
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode()),
	}

	entry, _, err := ticketFromFrontmatter(op.Path, absPath, s.dir, stat, fm, strings.NewReader(op.Content))
	if err != nil {
		return Ticket{}, fmt.Errorf("index parse %s: %w: %w", op.ID, ErrIndexUpdate, err)
	}

	return entry, nil
}

// renderTicketFile serializes frontmatter and body into a deterministic ticket payload.
func renderTicketFile(fm TicketFrontmatter, content string) (string, error) {
	frontmatter, err := fm.MarshalYAML()
	if err != nil {
		return "", fmt.Errorf("render frontmatter: %w: %w", ErrWALReplay, err)
	}

	builder := strings.Builder{}
	builder.WriteString(frontmatter)
	builder.WriteString("\n")
	builder.WriteString(content)

	if !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}

	return builder.String(), nil
}
