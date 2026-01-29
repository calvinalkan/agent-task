package mddb

import (
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

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

const (
	walMagic      = "MDDB0001"
	walFooterSize = 32
)

const (
	walOpPut    = "put"
	walOpDelete = "delete"
)

var walCRC32C = crc32.MakeTable(crc32.Castagnoli)

// ErrWALCorrupt reports a committed WAL with a mismatched checksum.
// ErrWALCorrupt indicates the WAL file has invalid structure or checksum.
// Recovery: delete the WAL file and reindex. Data in uncommitted transactions is lost.
var ErrWALCorrupt = errors.New("corrupt")

// ErrWALReplay reports WAL validation or replay failures.
var ErrWALReplay = errors.New("replay")

type walState uint8

const (
	walEmpty walState = iota
	walUncommitted
	walCommitted
)

// walOp represents a single WAL operation.
type walOp[T Document] struct {
	Op      string `json:"op"`
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Doc     *T     `json:"-"`
}

// recoverWalLocked recovers any pending WAL state.
// Must be called under the WAL write lock.
func (mddb *MDDB[T]) recoverWalLocked(ctx context.Context) error {
	state, body, err := readWalState(mddb.wal)
	if err != nil {
		return err
	}

	switch state {
	case walEmpty:
		return nil
	case walUncommitted:
		err := truncateWal(mddb.wal)
		if err != nil {
			return err
		}

		return nil
	case walCommitted:
		ops, err := decodeWalOps[T](body)
		if err != nil {
			return err
		}

		err = mddb.applyOpsToFS(ctx, ops)
		if err != nil {
			return err
		}

		err = mddb.updateSqliteIndexFromOps(ctx, ops)
		if err != nil {
			return err
		}

		err = truncateWal(mddb.wal)
		if err != nil {
			return err
		}

		return nil
	default:
		return fmt.Errorf("wal: unknown state %d", state)
	}
}

// applyOpsToFS applies operations to the filesystem.
// Used for both committed WAL recovery and live transaction commits.
func (mddb *MDDB[T]) applyOpsToFS(ctx context.Context, ops []walOp[T]) error {
	dirsToSync := make(map[string]struct{})
	existingDirs := make(map[string]struct{})
	createdDirs := make(map[string]struct{})

	rootDir := filepath.Clean(mddb.dataDir)
	if rootDir == "" {
		rootDir = "."
	}

	existingDirs[rootDir] = struct{}{}

	for _, op := range ops {
		err := mddb.validateRelPath(op.Path)
		if err != nil {
			return fmt.Errorf("wal: %w: path %q: %w", ErrWALReplay, op.Path, err)
		}

		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("wal: %w: canceled: %w", ErrWALReplay, context.Cause(ctx))
		}

		absPath := filepath.Join(mddb.dataDir, op.Path)
		dir := filepath.Dir(absPath)

		switch op.Op {
		case walOpPut:
			if op.Content == "" {
				return fmt.Errorf("wal: %w: missing content for %s", ErrWALReplay, op.ID)
			}

			err = ensureDir(mddb.fs, dir, rootDir, existingDirs, createdDirs, dirsToSync)
			if err != nil {
				return err
			}

			err = mddb.atomic.Write(absPath, strings.NewReader(op.Content), fs.AtomicWriteOptions{
				SyncDir: false, // We track our own syncing deduplicated per dir.
				Perm:    0o644,
			})
			if err != nil {
				return fmt.Errorf("fs: write %s: %w", absPath, err)
			}

			// Even when a file is just "updated", we need to sync it parent,
			// because atomic.write uses tmp file + rename to atomically update (which updates the file's inode)
			dirsToSync[dir] = struct{}{}

		case walOpDelete:
			err := mddb.fs.Remove(absPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("fs: delete %s: %w", absPath, err)
			}

			dirsToSync[dir] = struct{}{}

		default:
			return fmt.Errorf("wal: %w: op %q", ErrWALReplay, op.Op)
		}
	}

	for dir := range dirsToSync {
		fh, err := mddb.fs.Open(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("fs: open dir %q: %w", dir, err)
		}

		syncErr := fh.Sync()
		closeErr := fh.Close()

		if syncErr != nil || closeErr != nil {
			if syncErr != nil {
				syncErr = fmt.Errorf("fs: sync dir %q: %w", dir, syncErr)
			}

			if closeErr != nil {
				closeErr = fmt.Errorf("fs: close dir %q: %w", dir, closeErr)
			}

			return errors.Join(syncErr, closeErr)
		}
	}

	return nil
}

func ensureDir(fsys fs.FS, dir string, root string, existing map[string]struct{}, created map[string]struct{}, toSync map[string]struct{}) error {
	// Hot-path optimization: cache dirs so each unique directory is stat'd once per WAL replay/commit.
	// Only newly created dirs (and their parent) are synced for durability.
	if _, ok := existing[dir]; ok {
		return nil
	}

	if _, ok := created[dir]; ok {
		return nil
	}

	if dir == root {
		existing[dir] = struct{}{}

		return nil
	}

	info, err := fsys.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("fs: path %s is not a directory", dir)
		}

		existing[dir] = struct{}{}

		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs: stat %s: %w", dir, err)
	}

	missing := []string{}
	current := dir

	for current != root {
		if _, ok := existing[current]; ok {
			break
		}

		if _, ok := created[current]; ok {
			break
		}

		missing = append(missing, current)

		parent := filepath.Dir(current)
		if parent == current {
			break
		}

		if parent == root {
			break
		}

		if _, ok := existing[parent]; ok {
			break
		}

		if _, ok := created[parent]; ok {
			break
		}

		parentInfo, statErr := fsys.Stat(parent)
		if statErr == nil {
			if !parentInfo.IsDir() {
				return fmt.Errorf("fs: path %s is not a directory", parent)
			}

			existing[parent] = struct{}{}

			break
		}

		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("fs: stat %s: %w", parent, statErr)
		}

		current = parent
	}

	err = fsys.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("fs: mkdir %s: %w", dir, err)
	}

	for _, createdDir := range missing {
		created[createdDir] = struct{}{}
		toSync[createdDir] = struct{}{}
	}

	if len(missing) > 0 {
		parent := filepath.Dir(missing[len(missing)-1])
		if parent != "" {
			toSync[parent] = struct{}{}
		}
	}

	return nil
}

// updateSqliteIndexFromOps applies WAL operations to SQLite in one transaction.
// Note, this is not used for reindexing, only for recovery (reindex is super-hot path and has different logic).
func (mddb *MDDB[T]) updateSqliteIndexFromOps(ctx context.Context, ops []walOp[T]) error {
	tx, err := mddb.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin txn: %w", err)
	}

	committed := false

	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		deleteStmt *sql.Stmt
		putDocs    []IndexableDocument
		afterDocs  []*T // for AfterPut callback only
	)

	for _, op := range ops {
		err = mddb.validateRelPath(op.Path)
		if err != nil {
			return fmt.Errorf("wal: %w: path %q: %w", ErrWALReplay, op.Path, err)
		}

		ctxErr := ctx.Err()
		if ctxErr != nil {
			return fmt.Errorf("wal: canceled: %w", context.Cause(ctx))
		}

		switch op.Op {
		case walOpDelete:
			// Delete uses id + derived path; no content needed for replay.
			if deleteStmt == nil {
				stmt, prepErr := tx.PrepareContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", mddb.schema.tableName))
				if prepErr != nil {
					return fmt.Errorf("sqlite: prepare delete: %w", prepErr)
				}

				deleteStmt = stmt
			}

			_, delErr := deleteStmt.ExecContext(ctx, op.ID)
			if delErr != nil {
				return fmt.Errorf("sqlite: delete id %s: %w", op.ID, delErr)
			}

			if mddb.cfg.AfterDelete != nil {
				callbackErr := mddb.cfg.AfterDelete(ctx, tx, op.ID)
				if callbackErr != nil {
					return fmt.Errorf("after delete %s: %w", op.ID, callbackErr)
				}
			}
		case walOpPut:
			if op.Content == "" {
				return fmt.Errorf("wal: %w: missing content for %s", ErrWALReplay, op.ID)
			}

			absPath := filepath.Join(mddb.dataDir, op.Path)

			info, statErr := mddb.fs.Stat(absPath)
			if statErr != nil {
				return fmt.Errorf("fs: stat file %s: %w", op.Path, statErr)
			}

			// Re-parse from WAL content to avoid relying on in-memory Doc serialization.
			parsed, parseErr := mddb.parseIndexable([]byte(op.Path), []byte(op.Content), info.ModTime().UnixNano(), op.ID)
			if parseErr != nil {
				return fmt.Errorf("wal: parse document %s: %w", op.Path, parseErr)
			}

			putDocs = append(putDocs, parsed)

			if mddb.cfg.AfterPut != nil {
				if op.Doc != nil {
					afterDocs = append(afterDocs, op.Doc)
				} else {
					doc, docErr := mddb.parseDocument(op.Path, []byte(op.Content), info.ModTime().UnixNano(), op.ID)
					if docErr != nil {
						return fmt.Errorf("wal: parse document %s: %w", op.Path, docErr)
					}

					afterDocs = append(afterDocs, doc)
				}
			}
		default:
			return fmt.Errorf("wal: unknown op %q", op.Op)
		}
	}

	if deleteStmt != nil {
		defer func() { _ = deleteStmt.Close() }()
	}

	// Insert docs one-by-one so AfterPut callbacks fire immediately per doc.
	// WAL recovery typically has few entries, so batching is unnecessary.
	if len(putDocs) > 0 {
		colCount := len(mddb.schema.columnNames())

		stmt, prepErr := mddb.prepareUpsertStmt(ctx, tx, 1)
		if prepErr != nil {
			return prepErr
		}

		defer func() { _ = stmt.Close() }()

		args := make([]any, colCount)

		for i := range putDocs {
			err = mddb.fillBatchUpsertSQLArgs([]IndexableDocument{putDocs[i]}, colCount, args)
			if err != nil {
				return err
			}

			_, err = stmt.ExecContext(ctx, args...)
			if err != nil {
				return fmt.Errorf("sqlite: insert row: %w", err)
			}

			// Call AfterPut immediately after each insert.
			if mddb.cfg.AfterPut != nil {
				callbackErr := mddb.cfg.AfterPut(ctx, tx, afterDocs[i])
				if callbackErr != nil {
					return fmt.Errorf("after put %s: %w", string(putDocs[i].ID), callbackErr)
				}
			}
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("sqlite: commit txn: %w", err)
	}

	committed = true

	return nil
}

// marshalDocument renders a document to file bytes.
func (mddb *MDDB[T]) marshalDocument(doc T) ([]byte, error) {
	d, ok := any(doc).(Document)
	if !ok {
		return nil, errors.New("document type assertion failed")
	}

	fm := d.Frontmatter()

	// Inject reserved fields
	id := d.ID()
	if id == "" {
		return nil, ErrEmptyID
	}

	if err := fm.Set(frontmatterKeyID, frontmatter.StringValue(id)); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}

	if err := fm.Set(frontmatterKeySchemaVersion, frontmatter.IntValue(int64(mddb.schema.fingerprint()))); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}

	if err := fm.Set(frontmatterKeyTitle, frontmatter.StringValue(d.Title())); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}

	yamlStr, err := fm.MarshalYAML(frontmatter.WithKeyPriority(frontmatterKeyID, frontmatterKeySchemaVersion, frontmatterKeyTitle))
	if err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}

	var b strings.Builder
	b.WriteString(yamlStr)

	body := d.Body()
	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)

		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}

	return []byte(b.String()), nil
}

// readWalState inspects the WAL to determine its state.
func readWalState(file fs.File) (walState, []byte, error) {
	info, err := file.Stat()
	if err != nil {
		return walEmpty, nil, fmt.Errorf("wal: stat: %w", err)
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
		return walEmpty, nil, fmt.Errorf("wal: seek footer: %w", err)
	}

	_, err = io.ReadFull(file, footerBuf)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return walUncommitted, nil, nil
		}

		return walEmpty, nil, fmt.Errorf("wal: read footer: %w", err)
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
		return walEmpty, nil, fmt.Errorf("wal: seek body: %w", err)
	}

	_, err = io.ReadFull(file, body)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("wal: read body: %w", err)
	}

	checksum := crc32.Checksum(body, walCRC32C)
	if checksum != crc {
		// Deliberate hard failure: corrupt WAL requires manual inspection.
		// WAL is JSON so users can recover without code.
		return walCommitted, nil, fmt.Errorf("wal: %w: checksum mismatch", ErrWALCorrupt)
	}

	return walCommitted, body, nil
}

func truncateWal(file fs.File) error {
	fd := file.Fd()
	if fd == 0 {
		return errors.New("wal: invalid file descriptor")
	}

	err := syscall.Ftruncate(int(fd), 0)
	if err != nil {
		return fmt.Errorf("wal: ftruncate: %w", err)
	}

	return nil
}

func encodeWalContent[T Document](ops []walOp[T]) ([]byte, error) {
	var body bytes.Buffer

	enc := json.NewEncoder(&body)
	for _, op := range ops {
		err := enc.Encode(op)
		if err != nil {
			return nil, fmt.Errorf("json: encode op: %w", err)
		}
	}

	bodyBytes := body.Bytes()

	footer := make([]byte, walFooterSize)
	copy(footer[:8], walMagic)

	bodyLen := uint64(len(bodyBytes))
	binary.LittleEndian.PutUint64(footer[8:16], bodyLen)
	binary.LittleEndian.PutUint64(footer[16:24], ^bodyLen)

	crc := crc32.Checksum(bodyBytes, walCRC32C)
	binary.LittleEndian.PutUint32(footer[24:28], crc)
	binary.LittleEndian.PutUint32(footer[28:32], ^crc)

	_, err := body.Write(footer)
	if err != nil {
		return nil, fmt.Errorf("wal: encode footer: %w", err)
	}

	return body.Bytes(), nil
}

func decodeWalOps[T Document](body []byte) ([]walOp[T], error) {
	var ops []walOp[T]

	decoder := json.NewDecoder(bytes.NewReader(body))

	for {
		var op walOp[T]

		err := decoder.Decode(&op)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("wal: %w: json decode: %w", ErrWALReplay, err)
		}

		if op.Op != walOpPut && op.Op != walOpDelete {
			return nil, fmt.Errorf("wal: %w: invalid op %q", ErrWALReplay, op.Op)
		}

		if op.Op == walOpPut && op.Content == "" {
			return nil, fmt.Errorf("wal: %w: missing content for put", ErrWALReplay)
		}

		if op.ID == "" {
			return nil, fmt.Errorf("wal: %w: %w", ErrWALReplay, ErrEmptyID)
		}

		if op.Path == "" {
			return nil, fmt.Errorf("wal: %w: %w", ErrWALReplay, ErrEmptyPath)
		}

		ops = append(ops, op)
	}

	return ops, nil
}
