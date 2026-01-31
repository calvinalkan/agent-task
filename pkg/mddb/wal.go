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

// ErrWALCorrupt indicates the WAL file has invalid structure or checksum.
// This is a permanent failure - the WAL cannot be recovered automatically.
// Recovery: manually inspect the WAL (it's JSON), then delete and reindex.
var ErrWALCorrupt = errors.New("wal corrupt")

// ErrWALReplay indicates WAL replay failed due to a potentially temporary issue
// (e.g., fs permission errors, disk full, invalid document data).
// The WAL itself is not corrupt - retry may succeed after fixing the underlying issue.
var ErrWALReplay = errors.New("wal replay")

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
		return err // error already has context
	}

	switch state {
	case walEmpty:
		return nil
	case walUncommitted:
		err := truncateWal(mddb.wal)
		if err != nil {
			return fmt.Errorf("truncating uncommitted wal: %w", err)
		}

		return nil
	case walCommitted:
		ops, err := decodeWalOps[T](body)
		if err != nil {
			return fmt.Errorf("%w: decoding ops: %w", ErrWALReplay, err)
		}

		err = mddb.applyOpsToFS(ctx, ops)
		if err != nil {
			return fmt.Errorf("%w: applying ops to fs: %w", ErrWALReplay, err)
		}

		err = mddb.updateSqliteIndexFromOps(ctx, ops)
		if err != nil {
			return fmt.Errorf("%w: updating index: %w", ErrWALReplay, err)
		}

		err = truncateWal(mddb.wal)
		if err != nil {
			return fmt.Errorf("%w: truncating wal: %w", ErrWALReplay, err)
		}

		return nil
	default:
		return fmt.Errorf("unknown wal state %d", state)
	}
}

// applyOpsToFS applies operations to the filesystem.
// Used for both committed WAL recovery and live transaction commits.
func (mddb *MDDB[T]) applyOpsToFS(ctx context.Context, ops []walOp[T]) error {
	dirsToSync := make(map[string]struct{})
	existingDirs := make(map[string]struct{})
	createdDirs := make(map[string]struct{})

	rootDir := filepath.Clean(mddb.dataDir)
	existingDirs[rootDir] = struct{}{}

	for _, op := range ops {
		err := mddb.validateRelPath(op.Path)
		if err != nil {
			return fmt.Errorf("invalid path: %w (doc_id=%s doc_path=%s)", err, op.ID, op.Path)
		}

		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("canceled: %w", context.Cause(ctx))
		}

		absPath := filepath.Join(mddb.dataDir, op.Path)
		dir := filepath.Dir(absPath)

		switch op.Op {
		case walOpPut:
			if op.Content == "" {
				return fmt.Errorf("missing content (doc_id=%s doc_path=%s)", op.ID, op.Path)
			}

			err = ensureDir(mddb.fs, dir, rootDir, existingDirs, createdDirs, dirsToSync)
			if err != nil {
				return fmt.Errorf("creating dir: %w", err)
			}

			err = mddb.atomic.Write(absPath, strings.NewReader(op.Content), fs.AtomicWriteOptions{
				SyncDir: false, // We track our own syncing deduplicated per dir.
				Perm:    0o644,
			})
			if err != nil {
				return fmt.Errorf("fs: %w", err)
			}

			// Even when a file is just "updated", we need to sync it parent,
			// because atomic.write uses tmp file + rename to atomically update (which updates the file's inode)
			dirsToSync[dir] = struct{}{}

		case walOpDelete:
			err := mddb.fs.Remove(absPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("fs: %w", err)
			}

			dirsToSync[dir] = struct{}{}

		default:
			return fmt.Errorf("unknown op %q (doc_id=%s doc_path=%s)", op.Op, op.ID, op.Path)
		}
	}

	for dir := range dirsToSync {
		fh, err := mddb.fs.Open(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("fs: %w", err)
		}

		syncErr := fh.Sync()
		closeErr := fh.Close()

		if syncErr != nil || closeErr != nil {
			if syncErr != nil {
				syncErr = fmt.Errorf("fs: %w", syncErr)
			}

			if closeErr != nil {
				closeErr = fmt.Errorf("fs: %w", closeErr)
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
		return fmt.Errorf("fs: %w", err)
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
			return fmt.Errorf("fs: %w", statErr)
		}

		current = parent
	}

	err = fsys.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("fs: %w", err)
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
		return fmt.Errorf("sqlite: %w", err)
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
			return fmt.Errorf("invalid path: %w (doc_id=%s doc_path=%s)", err, op.ID, op.Path)
		}

		ctxErr := ctx.Err()
		if ctxErr != nil {
			return fmt.Errorf("canceled: %w (doc_id=%s doc_path=%s)", context.Cause(ctx), op.ID, op.Path)
		}

		switch op.Op {
		case walOpDelete:
			// Delete one-by-one so AfterDelete callbacks fire immediately per doc.
			// WAL recovery typically has few entries, so batching is unnecessary.
			if deleteStmt == nil {
				stmt, prepErr := mddb.prepareDeleteByIDStmt(ctx, tx, 1)
				if prepErr != nil {
					return fmt.Errorf("preparing delete statement: %w", prepErr)
				}

				deleteStmt = stmt
			}

			_, delErr := deleteStmt.ExecContext(ctx, op.ID)
			if delErr != nil {
				return fmt.Errorf("sqlite: %w (doc_id=%s doc_path=%s)", delErr, op.ID, op.Path)
			}

			if mddb.cfg.AfterDelete != nil {
				callbackErr := mddb.cfg.AfterDelete(ctx, tx, op.ID)
				if callbackErr != nil {
					return fmt.Errorf("AfterDelete callback: %w (doc_id=%s doc_path=%s)", callbackErr, op.ID, op.Path)
				}
			}
		case walOpPut:
			if op.Content == "" {
				return fmt.Errorf("missing content (doc_id=%s doc_path=%s)", op.ID, op.Path)
			}

			absPath := filepath.Join(mddb.dataDir, op.Path)

			info, statErr := mddb.fs.Stat(absPath)
			if statErr != nil {
				return fmt.Errorf("fs: %w (doc_id=%s doc_path=%s)", statErr, op.ID, op.Path)
			}

			// Re-parse from WAL content to avoid relying on in-memory Doc serialization.
			parsed, parseErr := mddb.parseIndexable([]byte(op.Path), []byte(op.Content), info.ModTime().UnixNano(), info.Size(), op.ID)
			if parseErr != nil {
				return fmt.Errorf("parsing document: %w (doc_id=%s doc_path=%s)", parseErr, op.ID, op.Path)
			}

			putDocs = append(putDocs, parsed)

			if mddb.cfg.AfterPut != nil {
				if op.Doc != nil {
					afterDocs = append(afterDocs, op.Doc)
				} else {
					doc, docErr := mddb.parseDocument(op.Path, []byte(op.Content), info.ModTime().UnixNano(), info.Size(), op.ID)
					if docErr != nil {
						return fmt.Errorf("parsing document: %w (doc_id=%s doc_path=%s)", docErr, op.ID, op.Path)
					}

					afterDocs = append(afterDocs, doc)
				}
			}
		default:
			return fmt.Errorf("unknown op %q (doc_id=%s doc_path=%s)", op.Op, op.ID, op.Path)
		}
	}

	if deleteStmt != nil {
		defer func() { _ = deleteStmt.Close() }()
	}

	// Insert docs one-by-one so AfterPut callbacks fire immediately per doc.
	// WAL recovery typically has few entries, so batching is unnecessary.
	if len(putDocs) > 0 {
		colCount := len(mddb.schema.columnNames())

		stmt, prepErr := mddb.prepareUpsertStmt(ctx, tx, 1, true)
		if prepErr != nil {
			return fmt.Errorf("preparing upsert statement: %w", prepErr)
		}

		defer func() { _ = stmt.Close() }()

		args := make([]any, colCount)

		for i := range putDocs {
			err = mddb.fillBatchUpsertSQLArgs([]IndexableDocument{putDocs[i]}, colCount, args)
			if err != nil {
				return fmt.Errorf("adding upsert statement args: %w (doc_id=%s doc_path=%s)", err, putDocs[i].ID, putDocs[i].RelPath)
			}

			_, err = stmt.ExecContext(ctx, args...)
			if err != nil {
				return fmt.Errorf("sqlite: %w (doc_id=%s doc_path=%s)", err, putDocs[i].ID, putDocs[i].RelPath)
			}

			// Call AfterPut immediately after each insert.
			if mddb.cfg.AfterPut != nil {
				callbackErr := mddb.cfg.AfterPut(ctx, tx, afterDocs[i])
				if callbackErr != nil {
					return fmt.Errorf("AfterPut callback: %w (doc_id=%s doc_path=%s)", callbackErr, putDocs[i].ID, putDocs[i].RelPath)
				}
			}
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
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
		return nil, errEmptyID
	}

	if err := fm.Set(frontmatterKeyID, frontmatter.StringValue(id)); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}

	if err := fm.Set(frontmatterKeySchemaVersion, frontmatter.IntValue(mddb.schema.fingerprint())); err != nil {
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
// Returns [ErrWALCorrupt] if the WAL's CRC is invalid, and otherwise only returns io errors.
func readWalState(file fs.File) (walState, []byte, error) {
	info, err := file.Stat()
	if err != nil {
		return walEmpty, nil, fmt.Errorf("fs: %w", err)
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
		return walEmpty, nil, fmt.Errorf("fs: %w", err)
	}

	_, err = io.ReadFull(file, footerBuf)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return walUncommitted, nil, nil
		}

		return walEmpty, nil, fmt.Errorf("fs: %w", err)
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
		return walEmpty, nil, fmt.Errorf("fs: %w", err)
	}

	_, err = io.ReadFull(file, body)
	if err != nil {
		return walEmpty, nil, fmt.Errorf("fs: %w", err)
	}

	checksum := crc32.Checksum(body, walCRC32C)
	if checksum != crc {
		// Deliberate hard failure: corrupt WAL requires manual inspection.
		// WAL is JSON so users can recover without code.
		return walCommitted, nil, fmt.Errorf("%w: checksum mismatch: stored %d, actual %d", ErrWALCorrupt, crc, checksum)
	}

	return walCommitted, body, nil
}

func truncateWal(file fs.File) error {
	fd := file.Fd()
	if fd == 0 {
		return errors.New("invalid file descriptor")
	}

	err := syscall.Ftruncate(int(fd), 0)
	if err != nil {
		return fmt.Errorf("syscall: %w", err)
	}

	return nil
}

func encodeWalContent[T Document](ops []walOp[T]) ([]byte, error) {
	var body bytes.Buffer

	enc := json.NewEncoder(&body)
	for _, op := range ops {
		err := enc.Encode(op)
		if err != nil {
			return nil, fmt.Errorf("json: %w", err)
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
		return nil, fmt.Errorf("write footer buf: %w", err)
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
			return nil, fmt.Errorf("json: %w", err)
		}

		if op.Op != walOpPut && op.Op != walOpDelete {
			return nil, fmt.Errorf("unknown op %q (doc_id=%s doc_path=%s)", op.Op, op.ID, op.Path)
		}

		if op.Op == walOpPut && op.Content == "" {
			return nil, fmt.Errorf("missing content for put (doc_id=%s doc_path=%s)", op.ID, op.Path)
		}

		if op.ID == "" {
			return nil, fmt.Errorf("%w (doc_path=%s)", errEmptyID, op.Path)
		}

		if op.Path == "" {
			return nil, fmt.Errorf("%w (doc_id=%s)", errEmptyPath, op.ID)
		}

		ops = append(ops, op)
	}

	return ops, nil
}
