package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/calvinalkan/fileproc"
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

type walState uint8

const (
	walEmpty walState = iota
	walUncommitted
	walCommitted
	walCorrupt
)

type walFooter struct {
	bodyLen uint64
	crc32c  uint32
}

type walOp struct {
	Op          string         `json:"op"`
	ID          string         `json:"id"`
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Content     string         `json:"content,omitempty"`
}

func walHasData(file fs.File) (bool, error) {
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat wal: %w", err)
	}

	return info.Size() > 0, nil
}

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

	footer, ok := parseWalFooter(footerBuf)
	if !ok {
		return walUncommitted, nil, nil
	}

	if footer.bodyLen > math.MaxInt64 {
		return walUncommitted, nil, nil
	}

	maxBody := size - walFooterSize
	if int64(footer.bodyLen) > maxBody {
		return walUncommitted, nil, nil
	}

	body := make([]byte, footer.bodyLen)

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("seek wal body: %w", err)
	}

	_, err = io.ReadFull(file, body)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("read wal body: %w", err)
	}

	checksum := crc32.Checksum(body, walCRC32C)
	if checksum != footer.crc32c {
		return walCorrupt, nil, nil
	}

	return walCommitted, body, nil
}

func parseWalFooter(buf []byte) (walFooter, bool) {
	if len(buf) != walFooterSize {
		return walFooter{}, false
	}

	if string(buf[:8]) != walMagic {
		return walFooter{}, false
	}

	bodyLen := binary.LittleEndian.Uint64(buf[8:16])

	bodyLenInv := binary.LittleEndian.Uint64(buf[16:24])
	if ^bodyLen != bodyLenInv {
		return walFooter{}, false
	}

	crc := binary.LittleEndian.Uint32(buf[24:28])

	crcInv := binary.LittleEndian.Uint32(buf[28:32])
	if ^crc != crcInv {
		return walFooter{}, false
	}

	return walFooter{bodyLen: bodyLen, crc32c: crc}, true
}

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
			return nil, fmt.Errorf("parse wal line: %w", unmarshalErr)
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

func validateWalOp(op walOp) (walOp, error) {
	if op.Op != walOpPut && op.Op != walOpDelete {
		return walOp{}, fmt.Errorf("%w: unknown op %q", ErrWALReplay, op.Op)
	}

	err := validateIDString(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("%w: invalid id %q: %w", ErrWALReplay, op.ID, err)
	}

	id, err := uuid.Parse(op.ID)
	if err != nil {
		return walOp{}, fmt.Errorf("%w: parse id %q: %w", ErrWALReplay, op.ID, err)
	}

	err = validateUUIDv7(id)
	if err != nil {
		return walOp{}, fmt.Errorf("%w: validate id %q: %w", ErrWALReplay, op.ID, err)
	}

	err = validateWalPath(op.Path)
	if err != nil {
		return walOp{}, err
	}

	expected, err := TicketPath(id)
	if err != nil {
		return walOp{}, fmt.Errorf("%w: derive path for %q: %w", ErrWALReplay, op.ID, err)
	}

	if filepath.Clean(op.Path) != filepath.Clean(expected) {
		return walOp{}, fmt.Errorf("%w: path %q does not match id %q", ErrWALReplay, op.Path, op.ID)
	}

	if op.Op == walOpPut {
		if op.Frontmatter == nil {
			return walOp{}, fmt.Errorf("%w: missing frontmatter for %q", ErrWALReplay, op.ID)
		}
	}

	return op, nil
}

func validateWalPath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: path is empty", ErrWALReplay)
	}

	if strings.Contains(path, "\\") {
		return fmt.Errorf("%w: path contains backslash", ErrWALReplay)
	}

	if filepath.IsAbs(path) {
		return fmt.Errorf("%w: path is absolute", ErrWALReplay)
	}

	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: path escapes root", ErrWALReplay)
	}

	if clean != path {
		return fmt.Errorf("%w: path is not clean", ErrWALReplay)
	}

	if !strings.HasSuffix(path, ".md") {
		return fmt.Errorf("%w: path must end with .md", ErrWALReplay)
	}

	return nil
}

func (s *Store) recoverWal(ctx context.Context, rebuildIndex bool) error {
	// Recovery runs under the WAL lock so readers never observe mid-commit state.
	state, body, err := readWalState(s.wal)
	if err != nil {
		return err
	}

	switch state {
	case walEmpty:
		if rebuildIndex {
			err = s.rebuildIndex(ctx)
			if err != nil {
				return err
			}
		}

		return nil
	case walUncommitted:
		err = truncateWal(s.wal)
		if err != nil {
			return err
		}

		return s.rebuildIndex(ctx)
	case walCorrupt:
		return ErrWALCorrupt
	case walCommitted:
		ops, err := decodeWalOps(body)
		if err != nil {
			return err
		}

		err = s.replayWalOps(ctx, ops)
		if err != nil {
			return err
		}

		if rebuildIndex {
			err = s.rebuildIndex(ctx)
		} else {
			err = s.updateIndexFromOps(ctx, ops)
		}

		if err != nil {
			return err
		}

		return truncateWal(s.wal)
	default:
		return fmt.Errorf("wal: unknown state %d", state)
	}
}

func (s *Store) rebuildIndex(ctx context.Context) error {
	entries, scanErr := scanTicketFiles(ctx, s.dir)
	if scanErr != nil {
		return scanErr
	}

	_, err := rebuildIndexInTxn(ctx, s.sql, entries)
	if err != nil {
		return fmt.Errorf("rebuild index: %w", err)
	}

	return nil
}

func (s *Store) replayWalOps(ctx context.Context, ops []walOp) error {
	for _, op := range ops {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("replay wal: %w", context.Cause(ctx))
		}

		absPath := filepath.Join(s.dir, op.Path)

		switch op.Op {
		case walOpPut:
			fm, err := frontmatterFromWAL(op.Frontmatter)
			if err != nil {
				return err
			}

			content, err := renderTicketFile(fm, op.Content)
			if err != nil {
				return err
			}

			err = s.fs.MkdirAll(filepath.Dir(absPath), 0o750)
			if err != nil {
				return fmt.Errorf("%w: mkdir %s: %w", ErrWALReplay, absPath, err)
			}

			err = s.atomic.WriteWithDefaults(absPath, strings.NewReader(content))
			if err != nil {
				return fmt.Errorf("%w: write %s: %w", ErrWALReplay, absPath, err)
			}
		case walOpDelete:
			err := s.fs.Remove(absPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: delete %s: %w", ErrWALReplay, absPath, err)
			}

			err = syncParentDir(s.fs, absPath)
			if err != nil {
				return fmt.Errorf("%w: sync dir for %s: %w", ErrWALReplay, absPath, err)
			}
		default:
			return fmt.Errorf("%w: unknown op %q", ErrWALReplay, op.Op)
		}
	}

	return nil
}

func (s *Store) updateIndexFromOps(ctx context.Context, ops []walOp) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin txn: %w", ErrIndexUpdate, err)
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
		return fmt.Errorf("%w: prepare insert: %w", ErrIndexUpdate, err)
	}

	defer func() { _ = insertTicket.Close() }()

	insertBlocker, err := tx.PrepareContext(ctx, `
		INSERT INTO ticket_blockers (ticket_id, blocker_id) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("%w: prepare blocker insert: %w", ErrIndexUpdate, err)
	}

	defer func() { _ = insertBlocker.Close() }()

	for _, op := range ops {
		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrIndexUpdate, context.Cause(ctx))
		}

		switch op.Op {
		case walOpDelete:
			_, err = tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", op.ID)
			if err != nil {
				return fmt.Errorf("%w: delete blockers %s: %w", ErrIndexUpdate, op.ID, err)
			}

			_, err = tx.ExecContext(ctx, "DELETE FROM tickets WHERE id = ?", op.ID)
			if err != nil {
				return fmt.Errorf("%w: delete ticket %s: %w", ErrIndexUpdate, op.ID, err)
			}
		case walOpPut:
			var fm Frontmatter

			fm, err = frontmatterFromWAL(op.Frontmatter)
			if err != nil {
				return err
			}

			var entry indexTicket

			entry, err = s.indexFromWAL(op, fm)
			if err != nil {
				return err
			}

			_, err = tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", entry.ID)
			if err != nil {
				return fmt.Errorf("%w: clear blockers %s: %w", ErrIndexUpdate, entry.ID, err)
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
				entry.Assignee,
				entry.Parent,
				entry.CreatedAt,
				entry.ClosedAt,
				entry.ExternalRef,
				entry.Title,
			)
			if err != nil {
				return fmt.Errorf("%w: insert ticket %s: %w", ErrIndexUpdate, entry.ID, err)
			}

			for _, blocker := range entry.BlockedBy {
				_, err = insertBlocker.ExecContext(ctx, entry.ID, blocker)
				if err != nil {
					return fmt.Errorf("%w: insert blocker %s: %w", ErrIndexUpdate, entry.ID, err)
				}
			}
		default:
			return fmt.Errorf("%w: unknown op %q", ErrIndexUpdate, op.Op)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("%w: commit: %w", ErrIndexUpdate, err)
	}

	committed = true

	return nil
}

func (s *Store) indexFromWAL(op walOp, fm Frontmatter) (indexTicket, error) {
	absPath := filepath.Join(s.dir, op.Path)

	info, err := s.fs.Stat(absPath)
	if err != nil {
		return indexTicket{}, fmt.Errorf("%w: stat %s: %w", ErrIndexUpdate, absPath, err)
	}

	stat := fileproc.Stat{
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode()),
	}

	entry, _, err := indexTicketFromFrontmatter(op.Path, absPath, s.dir, stat, fm, strings.NewReader(op.Content))
	if err != nil {
		return indexTicket{}, fmt.Errorf("%w: index %s: %w", ErrIndexUpdate, op.ID, err)
	}

	return entry, nil
}

func frontmatterFromWAL(raw map[string]any) (Frontmatter, error) {
	if raw == nil {
		return nil, fmt.Errorf("%w: frontmatter missing", ErrWALReplay)
	}

	out := make(Frontmatter, len(raw))

	for key, value := range raw {
		parsed, err := walValueToFrontmatter(value)
		if err != nil {
			return nil, fmt.Errorf("%w: frontmatter %s: %w", ErrWALReplay, key, err)
		}

		out[key] = parsed
	}

	return out, nil
}

func walValueToFrontmatter(value any) (Value, error) {
	switch typed := value.(type) {
	case string:
		return Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarString, String: typed}}, nil
	case bool:
		return Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarBool, Bool: typed}}, nil
	case float64:
		if typed != float64(int64(typed)) {
			return Value{}, errors.New("numeric value must be integer")
		}

		return Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarInt, Int: int64(typed)}}, nil
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return Value{}, errors.New("list items must be strings")
			}

			list = append(list, str)
		}

		return Value{Kind: ValueList, List: list}, nil
	case map[string]any:
		obj := make(map[string]Scalar, len(typed))
		for k, v := range typed {
			scalar, err := walScalarToFrontmatter(v)
			if err != nil {
				return Value{}, fmt.Errorf("object %s: %w", k, err)
			}

			obj[k] = scalar
		}

		return Value{Kind: ValueObject, Object: obj}, nil
	default:
		return Value{}, errors.New("unsupported value type")
	}
}

func walScalarToFrontmatter(value any) (Scalar, error) {
	switch typed := value.(type) {
	case string:
		return Scalar{Kind: ScalarString, String: typed}, nil
	case bool:
		return Scalar{Kind: ScalarBool, Bool: typed}, nil
	case float64:
		if typed != float64(int64(typed)) {
			return Scalar{}, errors.New("numeric value must be integer")
		}

		return Scalar{Kind: ScalarInt, Int: int64(typed)}, nil
	default:
		return Scalar{}, errors.New("unsupported scalar type")
	}
}

func renderTicketFile(fm Frontmatter, content string) (string, error) {
	ordered, err := orderFrontmatterKeys(fm)
	if err != nil {
		return "", err
	}

	builder := strings.Builder{}
	builder.WriteString("---\n")

	for _, key := range ordered {
		value := fm[key]

		line, err := formatFrontmatterValue(key, &value)
		if err != nil {
			return "", err
		}

		builder.WriteString(line)
	}

	builder.WriteString("---\n\n")
	builder.WriteString(content)

	if !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}

	return builder.String(), nil
}

func orderFrontmatterKeys(fm Frontmatter) ([]string, error) {
	keys := make([]string, 0, len(fm))
	for key := range fm {
		keys = append(keys, key)
	}

	if _, ok := fm["id"]; !ok {
		return nil, fmt.Errorf("%w: missing id", ErrWALReplay)
	}

	if _, ok := fm["schema_version"]; !ok {
		return nil, fmt.Errorf("%w: missing schema_version", ErrWALReplay)
	}

	slices.Sort(keys)

	ordered := make([]string, 0, len(keys))
	ordered = append(ordered, "id", "schema_version")

	for _, key := range keys {
		if key == "id" || key == "schema_version" {
			continue
		}

		ordered = append(ordered, key)
	}

	return ordered, nil
}

func formatFrontmatterValue(key string, value *Value) (string, error) {
	var builder strings.Builder
	builder.WriteString(key)
	builder.WriteString(": ")

	switch value.Kind {
	case ValueScalar:
		switch value.Scalar.Kind {
		case ScalarString:
			builder.WriteString(value.Scalar.String)
		case ScalarInt:
			builder.WriteString(strconv.FormatInt(value.Scalar.Int, 10))
		case ScalarBool:
			if value.Scalar.Bool {
				builder.WriteString("true")
			} else {
				builder.WriteString("false")
			}
		default:
			return "", fmt.Errorf("%w: unsupported scalar kind", ErrWALReplay)
		}

		builder.WriteString("\n")
	case ValueList:
		if len(value.List) == 0 {
			builder.WriteString("[]\n")

			break
		}

		builder.WriteString("\n")

		for _, item := range value.List {
			if item == "" {
				return "", fmt.Errorf("%w: empty list item for %s", ErrWALReplay, key)
			}

			builder.WriteString("  - ")
			builder.WriteString(item)
			builder.WriteString("\n")
		}
	case ValueObject:
		if len(value.Object) == 0 {
			return "", fmt.Errorf("%w: empty object for %s", ErrWALReplay, key)
		}

		builder.WriteString("\n")

		keys := make([]string, 0, len(value.Object))
		for k := range value.Object {
			keys = append(keys, k)
		}

		slices.Sort(keys)

		for _, objKey := range keys {
			scalar := value.Object[objKey]

			builder.WriteString("  ")
			builder.WriteString(objKey)
			builder.WriteString(": ")
			builder.WriteString(formatScalar(scalar))
			builder.WriteString("\n")
		}
	default:
		return "", fmt.Errorf("%w: unsupported value kind", ErrWALReplay)
	}

	return builder.String(), nil
}

func formatScalar(s Scalar) string {
	switch s.Kind {
	case ScalarString:
		return s.String
	case ScalarInt:
		return strconv.FormatInt(s.Int, 10)
	case ScalarBool:
		if s.Bool {
			return "true"
		}

		return "false"
	default:
		return ""
	}
}

func syncParentDir(fsys fs.FS, path string) error {
	dir := filepath.Dir(path)

	fh, err := fsys.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %q: %w", dir, err)
	}

	defer func() { _ = fh.Close() }()

	err = fh.Sync()
	if err != nil {
		return fmt.Errorf("sync dir %q: %w", dir, err)
	}

	return nil
}
