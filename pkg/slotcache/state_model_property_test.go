//go:build slotcache_impl

package slotcache_test

import (
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/model"
	"github.com/google/go-cmp/cmp"
)

// This file contains the core *state-model property tests*.
//
// Purpose:
//
// - We model slotcache's PUBLICLY observable behavior (what callers can see
//   through the API).
// - We apply identical operations to:
//     1) a deliberately-simple in-memory model, and
//     2) the real implementation,
//   and assert that operation results and observable state match.
//
// These tests are NOT on-disk-format compliance tests.

// -----------------------------------------------------------------------------
// Property test (many inputs, but not random/fuzzed)
// -----------------------------------------------------------------------------
func Test_Slotcache_Matches_Model_Property(t *testing.T) {
	// Keep this deterministic for easy reproduction: seed N is the subtest name.
	seedCount := 50
	opsPerSeed := 200

	for i := 0; i < seedCount; i++ {
		seed := int64(i + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "test.slc")

			rand := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         path,
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			h := newHarness(t, options)
			defer func() {
				_ = h.real.cache.Close()
			}()

			var keys [][]byte

			for i := 0; i < opsPerSeed; i++ {
				op := randOp(rand, h, keys)

				mRes := applyModel(h, op)
				rRes := applyReal(h, op)

				// If this operation was a successful Writer.Put with a valid-length key,
				// remember it for future operations.
				if put, isPutOp := op.(opPut); isPutOp {
					if errResult, isErrorResult := mRes.(resErr); isErrorResult {
						if errResult.Error == nil && len(put.Key) == options.KeySize {
							keys = append(keys, append([]byte(nil), put.Key...))
						}
					}
				}

				// Compare this operation's direct result.
				assertMatch(t, op, mRes, rRes)

				// Compare the observable committed state.
				// This is useful even after errors: invalid inputs should not mutate state.
				compareObservableState(t, h)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Operation types
// -----------------------------------------------------------------------------

type operation interface {
	Name() string
	String() string
}

// Cache operations.

type opLen struct{}

func (opLen) Name() string   { return "Len" }
func (opLen) String() string { return "Len()" }

type opGet struct {
	Key []byte
}

func (opGet) Name() string { return "Get" }
func (o opGet) String() string {
	return fmt.Sprintf("Get(%x)", o.Key)
}

type opScan struct {
	Options slotcache.ScanOpts
}

func (opScan) Name() string { return "Scan" }
func (o opScan) String() string {
	return fmt.Sprintf("Scan(%+v)", o.Options)
}

type opScanPrefix struct {
	Prefix  []byte
	Options slotcache.ScanOpts
}

func (opScanPrefix) Name() string { return "ScanPrefix" }
func (o opScanPrefix) String() string {
	return fmt.Sprintf("ScanPrefix(%x,%+v)", o.Prefix, o.Options)
}

type opClose struct{}

func (opClose) Name() string   { return "Close" }
func (opClose) String() string { return "Close()" }

// opReopen simulates a process restart.
//
// It attempts to close the current cache handle (if any), then opens a new
// handle on the same underlying persistent file.
//
// If Close returns ErrBusy, the cache remains open and we do not open a new
// handle.
type opReopen struct{}

func (opReopen) Name() string   { return "Reopen" }
func (opReopen) String() string { return "Reopen()" }

// Writer operations.

type opBeginWrite struct{}

func (opBeginWrite) Name() string   { return "BeginWrite" }
func (opBeginWrite) String() string { return "BeginWrite()" }

type opPut struct {
	Key      []byte
	Revision int64
	Index    []byte
}

func (opPut) Name() string { return "Writer.Put" }
func (o opPut) String() string {
	return fmt.Sprintf("Writer.Put(%x, revision=%d, index=%x)", o.Key, o.Revision, o.Index)
}

type opDelete struct {
	Key []byte
}

func (opDelete) Name() string { return "Writer.Delete" }
func (o opDelete) String() string {
	return fmt.Sprintf("Writer.Delete(%x)", o.Key)
}

type opCommit struct{}

func (opCommit) Name() string   { return "Writer.Commit" }
func (opCommit) String() string { return "Writer.Commit()" }

type opAbort struct{}

func (opAbort) Name() string   { return "Writer.Abort" }
func (opAbort) String() string { return "Writer.Abort()" }

// -----------------------------------------------------------------------------
// Typed operation results
// -----------------------------------------------------------------------------

type operationResult interface {
	isResult()
}

type resErr struct {
	Error error
}

func (resErr) isResult() {}

type resLen struct {
	Length int
	Error  error
}

func (resLen) isResult() {}

type resGet struct {
	Entry  slotcache.Entry
	Exists bool
	Error  error
}

func (resGet) isResult() {}

type resDel struct {
	Existed bool
	Error   error
}

func (resDel) isResult() {}

type resScan struct {
	Entries []slotcache.Entry
	Error   error
}

func (resScan) isResult() {}

type resReopen struct {
	CloseError error
	OpenError  error
}

func (resReopen) isResult() {}

// -----------------------------------------------------------------------------
// Test h (model + real)
// -----------------------------------------------------------------------------

type harness struct {
	opts  slotcache.Options
	model struct {
		file   *model.FileState
		cache  *model.CacheModel
		writer *model.WriterModel
	}
	real struct {
		cache  *slotcache.Cache
		writer *slotcache.Writer
	}
}

func newHarness(t *testing.T, opts slotcache.Options) *harness {
	t.Helper()

	file, err := model.NewFile(opts)
	if err != nil {
		t.Fatalf("model.NewFile failed: %v", err)
	}

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("slotcache.Open failed: %v", err)
	}

	h := &harness{opts: opts}
	h.model.file = file
	h.model.cache = model.Open(file)
	h.real.cache = cache
	return h
}

// -----------------------------------------------------------------------------
// Operation application
// -----------------------------------------------------------------------------

func applyModel(h *harness, op operation) operationResult {
	switch op := op.(type) {
	case opLen:
		length, err := h.model.cache.Len()
		return resLen{Length: length, Error: err}

	case opGet:
		me, exists, err := h.model.cache.Get(op.Key)
		// Convert model.ModelEntry to slotcache.Entry for uniform result comparison.
		entry := toSlotcacheEntry(me)
		return resGet{Entry: entry, Exists: exists, Error: err}

	case opScan:
		entries, err := h.model.cache.Scan(op.Options)
		if err != nil {
			return resScan{Entries: nil, Error: err}
		}
		return resScan{Entries: toSlotcacheEntries(entries), Error: nil}

	case opScanPrefix:
		entries, err := h.model.cache.ScanPrefix(op.Prefix, op.Options)
		if err != nil {
			return resScan{Entries: nil, Error: err}
		}
		return resScan{Entries: toSlotcacheEntries(entries), Error: nil}

	case opBeginWrite:
		w, err := h.model.cache.BeginWrite()
		if err == nil {
			h.model.writer = w
		}
		return resErr{Error: err}

	case opPut:
		if h.model.writer == nil {
			panic("test harness bug: Writer.Put without an active model writer")
		}
		err := h.model.writer.Put(op.Key, op.Revision, op.Index)
		return resErr{Error: err}

	case opDelete:
		if h.model.writer == nil {
			panic("test harness bug: Writer.Delete without an active model writer")
		}
		existed, err := h.model.writer.Delete(op.Key)
		return resDel{Existed: existed, Error: err}

	case opCommit:
		if h.model.writer == nil {
			panic("test harness bug: Writer.Commit without an active model writer")
		}
		err := h.model.writer.Commit()
		h.model.writer = nil
		return resErr{Error: err}

	case opAbort:
		if h.model.writer == nil {
			panic("test harness bug: Writer.Abort without an active model writer")
		}
		err := h.model.writer.Abort()
		h.model.writer = nil
		return resErr{Error: err}

	case opClose:
		err := h.model.cache.Close()
		return resErr{Error: err}

	case opReopen:
		closeError := h.model.cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return resReopen{CloseError: closeError, OpenError: nil}
		}

		// Whether close succeeded or it was already closed, we can create a new handle.
		h.model.cache = model.Open(h.model.file)
		h.model.writer = nil
		return resReopen{CloseError: closeError, OpenError: nil}

	default:
		panic("unknown operation type")
	}
}

func applyReal(h *harness, op operation) operationResult {
	switch op := op.(type) {
	case opLen:
		length, err := h.real.cache.Len()
		return resLen{Length: length, Error: err}

	case opGet:
		entry, exists, err := h.real.cache.Get(op.Key)
		return resGet{Entry: entry, Exists: exists, Error: err}

	case opScan:
		sequence, err := h.real.cache.Scan(op.Options)
		if err != nil {
			return resScan{Entries: nil, Error: err}
		}
		return resScan{Entries: collectSeq(sequence), Error: nil}

	case opScanPrefix:
		sequence, err := h.real.cache.ScanPrefix(op.Prefix, op.Options)
		if err != nil {
			return resScan{Entries: nil, Error: err}
		}
		return resScan{Entries: collectSeq(sequence), Error: nil}

	case opBeginWrite:
		w, err := h.real.cache.BeginWrite()
		if err == nil {
			h.real.writer = w
		}
		return resErr{Error: err}

	case opPut:
		if h.real.writer == nil {
			panic("test harness bug: Writer.Put without an active real writer")
		}
		err := h.real.writer.Put(op.Key, op.Revision, op.Index)
		return resErr{Error: err}

	case opDelete:
		if h.real.writer == nil {
			panic("test harness bug: Writer.Delete without an active real writer")
		}
		existed, err := h.real.writer.Delete(op.Key)
		return resDel{Existed: existed, Error: err}

	case opCommit:
		if h.real.writer == nil {
			panic("test harness bug: Writer.Commit without an active real writer")
		}
		err := h.real.writer.Commit()
		h.real.writer = nil
		return resErr{Error: err}

	case opAbort:
		if h.real.writer == nil {
			panic("test harness bug: Writer.Abort without an active real writer")
		}
		err := h.real.writer.Abort()
		h.real.writer = nil
		return resErr{Error: err}

	case opClose:
		err := h.real.cache.Close()
		return resErr{Error: err}

	case opReopen:
		closeError := h.real.cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return resReopen{CloseError: closeError, OpenError: nil}
		}

		c, err := slotcache.Open(h.opts)
		if err == nil {
			h.real.cache = c
			h.real.writer = nil
		}

		return resReopen{CloseError: closeError, OpenError: err}

	default:
		panic("unknown operation type")
	}
}

// -----------------------------------------------------------------------------
// Comparing operation results
// -----------------------------------------------------------------------------

func assertMatch(t *testing.T, op operation, mRes operationResult, rRes operationResult) {
	t.Helper()

	switch m := mRes.(type) {
	case resErr:
		r := rRes.(resErr)
		if !errorsMatch(m.Error, r.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", op.String(), m.Error, r.Error)
		}

	case resLen:
		r := rRes.(resLen)
		if !errorsMatch(m.Error, r.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", op.String(), m.Error, r.Error)
		}
		if m.Length != r.Length {
			t.Fatalf("%s: length mismatch\nmodel=%d\nreal=%d", op.String(), m.Length, r.Length)
		}

	case resGet:
		r := rRes.(resGet)
		if !errorsMatch(m.Error, r.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", op.String(), m.Error, r.Error)
		}
		if m.Exists != r.Exists {
			t.Fatalf("%s: exists mismatch\nmodel=%v\nreal=%v", op.String(), m.Exists, r.Exists)
		}
		if diff := cmp.Diff(m.Entry, r.Entry); diff != "" {
			t.Fatalf("%s: entry mismatch (-model +real):\n%s", op.String(), diff)
		}

	case resDel:
		r := rRes.(resDel)
		if !errorsMatch(m.Error, r.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", op.String(), m.Error, r.Error)
		}
		if m.Existed != r.Existed {
			t.Fatalf("%s: existed mismatch\nmodel=%v\nreal=%v", op.String(), m.Existed, r.Existed)
		}

	case resScan:
		r := rRes.(resScan)
		if !errorsMatch(m.Error, r.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", op.String(), m.Error, r.Error)
		}
		if diff := cmp.Diff(m.Entries, r.Entries); diff != "" {
			t.Fatalf("%s: entries mismatch (-model +real):\n%s", op.String(), diff)
		}

	case resReopen:
		r := rRes.(resReopen)
		if !errorsMatch(m.CloseError, r.CloseError) {
			t.Fatalf("%s: close error mismatch\nmodel=%v\nreal=%v", op.String(), m.CloseError, r.CloseError)
		}
		if !errorsMatch(m.OpenError, r.OpenError) {
			t.Fatalf("%s: open error mismatch\nmodel=%v\nreal=%v", op.String(), m.OpenError, r.OpenError)
		}

	default:
		panic("unknown result type")
	}
}

// -----------------------------------------------------------------------------
// Random operation generation
// -----------------------------------------------------------------------------

// randOp produces a single random operation.
//
// It generates a mix of valid and invalid inputs, and it accounts for whether a
// writer session is currently active.
func randOp(rand *rand.Rand, h *harness, keys [][]byte) operation {
	hasWriter := h.model.writer != nil && h.real.writer != nil

	// Occasionally try to reopen (close + open) to test persistence.
	if rand.Intn(100) < 5 {
		return opReopen{}
	}

	if rand.Intn(100) < 5 {
		return opClose{}
	}

	if !hasWriter {
		// No writer: choose among read operations and BeginWrite.
		switch rand.Intn(5) {
		case 0:
			return opLen{}
		case 1:
			return opGet{Key: randKey(rand, h.opts.KeySize, keys)}
		case 2:
			return opScan{Options: randScanOpts(rand)}
		case 3:
			return opScanPrefix{Prefix: randPrefix(rand, h.opts.KeySize, keys), Options: randScanOpts(rand)}
		case 4:
			return opBeginWrite{}
		default:
			return opLen{}
		}
	}

	// Writer is active: choose among writer ops plus reads.
	switch rand.Intn(8) {
	case 0:
		return opPut{
			Key:      randKey(rand, h.opts.KeySize, keys),
			Revision: int64(rand.Intn(1000)),
			Index:    randIndex(rand, h.opts.IndexSize),
		}
	case 1:
		return opDelete{Key: randKey(rand, h.opts.KeySize, keys)}
	case 2:
		return opCommit{}
	case 3:
		return opAbort{}
	case 4:
		return opLen{}
	case 5:
		return opGet{Key: randKey(rand, h.opts.KeySize, keys)}
	case 6:
		return opScan{Options: randScanOpts(rand)}
	case 7:
		return opScanPrefix{Prefix: randPrefix(rand, h.opts.KeySize, keys), Options: randScanOpts(rand)}
	default:
		return opLen{}
	}
}

func randKey(rand *rand.Rand, keySize int, keys [][]byte) []byte {
	// 15%: invalid (nil or wrong length).
	if rand.Intn(100) < 15 {
		if rand.Intn(2) == 0 {
			return nil
		}
		n := rand.Intn(keySize + 2)
		if n == keySize {
			n = keySize + 1
		}
		key := make([]byte, n)
		_, _ = rand.Read(key)
		return key
	}

	// 60%: reuse a previously seen key.
	if len(keys) > 0 && rand.Intn(100) < 60 {
		key := keys[rand.Intn(len(keys))]
		return append([]byte(nil), key...)
	}

	// Otherwise: new random valid key.
	key := make([]byte, keySize)
	_, _ = rand.Read(key)

	return key
}

func randIndex(rand *rand.Rand, indexSize int) []byte {
	// 10%: invalid length.
	if rand.Intn(100) < 10 {
		n := rand.Intn(indexSize + 2)
		if n == indexSize {
			n = indexSize + 1
		}
		idx := make([]byte, n)
		_, _ = rand.Read(idx)
		return idx
	}

	idx := make([]byte, indexSize)
	_, _ = rand.Read(idx)
	return idx
}

func randPrefix(rand *rand.Rand, keySize int, keys [][]byte) []byte {
	// 20%: invalid prefix (nil, empty, or too long).
	if rand.Intn(100) < 20 {
		i := rand.Intn(3)
		switch i {
		case 0:
			return nil
		case 1:
			return []byte{}
		case 2:
			return make([]byte, keySize+1)
		}
	}

	// Prefer deriving a prefix from a known key.
	if len(keys) > 0 {
		key := keys[rand.Intn(len(keys))]
		n := 1 + rand.Intn(keySize) // 1..keySize
		return append([]byte(nil), key[:n]...)
	}

	// Otherwise generate arbitrary prefix bytes.
	n := 1 + rand.Intn(keySize)
	prefix := make([]byte, n)
	_, _ = rand.Read(prefix)
	return prefix
}

func randScanOpts(rand *rand.Rand) slotcache.ScanOpts {
	// 10%: invalid options.
	if rand.Intn(100) < 10 {
		if rand.Intn(2) == 0 {
			return slotcache.ScanOpts{Reverse: false, Offset: -1, Limit: 0}
		}
		return slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: -1}
	}

	off := rand.Intn(5)
	lim := rand.Intn(4) // 0..3 (0 means unlimited)

	return slotcache.ScanOpts{
		Reverse: rand.Intn(2) == 0,
		Offset:  off,
		Limit:   lim,
	}
}
