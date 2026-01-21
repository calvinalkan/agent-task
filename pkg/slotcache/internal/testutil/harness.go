package testutil

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil/model"
)

// Harness holds:
//   - a simple in-memory model (committed state + optional active writer), and
//   - the real implementation (file-backed)
//
// We always apply the same operation to both sides, then compare:
//  1. the direct operation result, and
//  2. the observable committed state (Len/Get/Scan/ScanPrefix).
//
// IMPORTANT: This harness compares PUBLIC API behavior only.
type Harness struct {
	Options slotcache.Options
	Model   HarnessModel
	Real    HarnessReal
	Scratch HarnessScratch
}

// HarnessScratch holds reusable buffers for expensive test helpers.
//
// This reduces per-operation allocations in property tests that repeatedly
// scan/collect/convert entries.
type HarnessScratch struct {
	modelFwd []slotcache.Entry
	realFwd  []slotcache.Entry

	modelRev []slotcache.Entry
	realRev  []slotcache.Entry

	modelTmp1 []slotcache.Entry
	realTmp1  []slotcache.Entry
	modelTmp2 []slotcache.Entry
	realTmp2  []slotcache.Entry

	keysTmp [][]byte
}

// HarnessModel holds the in-memory model state.
type HarnessModel struct {
	File   *model.FileState
	Cache  *model.CacheModel
	Writer *model.WriterModel
}

// HarnessReal holds the real implementation state.
type HarnessReal struct {
	Cache  slotcache.Cache
	Writer slotcache.Writer
}

// NewHarness constructs a model-vs-real harness for the given options.
func NewHarness(tb testing.TB, options slotcache.Options) *Harness {
	tb.Helper()

	file, modelError := model.NewFile(options)
	if modelError != nil {
		tb.Fatalf("model.NewFile failed: %v", modelError)
	}

	realCache, openError := slotcache.Open(options)
	if openError != nil {
		tb.Fatalf("slotcache.Open failed: %v", openError)
	}

	testHarness := &Harness{Options: options}
	testHarness.Model.File = file
	testHarness.Model.Cache = model.Open(file)

	testHarness.Real.Cache = realCache

	return testHarness
}

// -----------------------------------------------------------------------------
// Apply operations to model + real
// -----------------------------------------------------------------------------

// ApplyModel applies an operation to the in-memory model side.
func ApplyModel(testHarness *Harness, operationValue Operation) OperationResult {
	switch concreteOperation := operationValue.(type) {
	case OpLen:
		length, lengthError := testHarness.Model.Cache.Len()

		return ResLen{Length: length, Error: lengthError}

	case OpGet:
		modelEntry, exists, getError := testHarness.Model.Cache.Get(concreteOperation.Key)

		return ResGet{Entry: toSlotcacheEntry(modelEntry), Exists: exists, Error: getError}

	case OpScan:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.Scan(opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanPrefix:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.ScanPrefix(concreteOperation.Prefix, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanMatch:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.ScanMatch(concreteOperation.Spec, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanRange:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.ScanRange(concreteOperation.Start, concreteOperation.End, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpBeginWrite:
		writerHandle, beginError := testHarness.Model.Cache.BeginWrite()
		if beginError == nil {
			testHarness.Model.Writer = writerHandle
		}

		return ResErr{Error: beginError}

	case OpPut:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.Put without an active model writer")
		}

		putError := testHarness.Model.Writer.Put(concreteOperation.Key, concreteOperation.Revision, concreteOperation.Index)

		return ResErr{Error: putError}

	case OpDelete:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.Delete without an active model writer")
		}

		existed, deleteError := testHarness.Model.Writer.Delete(concreteOperation.Key)

		return ResDel{Existed: existed, Error: deleteError}

	case OpCommit:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.Commit without an active model writer")
		}

		commitError := testHarness.Model.Writer.Commit()
		testHarness.Model.Writer = nil

		return ResErr{Error: commitError}

	case OpWriterClose:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.Close without an active model writer")
		}

		testHarness.Model.Writer.Close()
		testHarness.Model.Writer = nil

		return ResErr{Error: nil}

	case OpClose:
		closeError := testHarness.Model.Cache.Close()

		return ResErr{Error: closeError}

	case OpReopen:
		closeError := testHarness.Model.Cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return ResReopen{CloseError: closeError, OpenError: nil}
		}

		// Whether close succeeded or it was already closed, we can create a new handle.
		testHarness.Model.Cache = model.Open(testHarness.Model.File)
		testHarness.Model.Writer = nil

		return ResReopen{CloseError: closeError, OpenError: nil}

	default:
		panic("unknown operation type")
	}
}

// ApplyReal applies an operation to the real, file-backed cache.
func ApplyReal(testHarness *Harness, operationValue Operation) OperationResult {
	switch concreteOperation := operationValue.(type) {
	case OpLen:
		length, lengthError := testHarness.Real.Cache.Len()

		return ResLen{Length: length, Error: lengthError}

	case OpGet:
		entry, exists, getError := testHarness.Real.Cache.Get(concreteOperation.Key)

		return ResGet{Entry: entry, Exists: exists, Error: getError}

	case OpScan:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.Scan(opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanPrefix:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanPrefix(concreteOperation.Prefix, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanMatch:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanMatch(concreteOperation.Spec, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanRange:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = BuildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanRange(concreteOperation.Start, concreteOperation.End, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpBeginWrite:
		writerHandle, beginError := testHarness.Real.Cache.BeginWrite()
		if beginError == nil {
			testHarness.Real.Writer = writerHandle
		}

		return ResErr{Error: beginError}

	case OpPut:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.Put without an active real writer")
		}

		putError := testHarness.Real.Writer.Put(concreteOperation.Key, concreteOperation.Revision, concreteOperation.Index)

		return ResErr{Error: putError}

	case OpDelete:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.Delete without an active real writer")
		}

		existed, deleteError := testHarness.Real.Writer.Delete(concreteOperation.Key)

		return ResDel{Existed: existed, Error: deleteError}

	case OpCommit:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.Commit without an active real writer")
		}

		commitError := testHarness.Real.Writer.Commit()
		testHarness.Real.Writer = nil

		return ResErr{Error: commitError}

	case OpWriterClose:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.Close without an active real writer")
		}

		closeError := testHarness.Real.Writer.Close()
		testHarness.Real.Writer = nil

		return ResErr{Error: closeError}

	case OpClose:
		closeError := testHarness.Real.Cache.Close()

		return ResErr{Error: closeError}

	case OpReopen:
		closeError := testHarness.Real.Cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return ResReopen{CloseError: closeError, OpenError: nil}
		}

		reopenedCache, openError := slotcache.Open(testHarness.Options)
		if openError == nil {
			testHarness.Real.Cache = reopenedCache
			testHarness.Real.Writer = nil
		}

		return ResReopen{CloseError: closeError, OpenError: openError}

	default:
		panic("unknown operation type")
	}
}

// -----------------------------------------------------------------------------
// Compare operation results
// -----------------------------------------------------------------------------

// AssertOpMatch compares model and real operation results and fails the test if they differ.
func AssertOpMatch(tb testing.TB, operationValue Operation, modelResult OperationResult, realResult OperationResult) {
	tb.Helper()

	switch modelTyped := modelResult.(type) {
	case ResErr:
		realTyped, ok := realResult.(ResErr)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

	case ResLen:
		realTyped, ok := realResult.(ResLen)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Length != realTyped.Length {
			tb.Fatalf("%s: length mismatch\nmodel=%d\nreal=%d", operationValue.String(), modelTyped.Length, realTyped.Length)
		}

	case ResGet:
		realTyped, ok := realResult.(ResGet)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Exists != realTyped.Exists {
			tb.Fatalf("%s: exists mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Exists, realTyped.Exists)
		}

		if !entriesEqual(modelTyped.Entry, realTyped.Entry) {
			diff := cmp.Diff(modelTyped.Entry, realTyped.Entry, cmpopts.EquateEmpty())
			tb.Fatalf("%s: entry mismatch (-model +real):\n%s", operationValue.String(), diff)
		}

	case ResDel:
		realTyped, ok := realResult.(ResDel)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Existed != realTyped.Existed {
			tb.Fatalf("%s: existed mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Existed, realTyped.Existed)
		}

	case ResScan:
		realTyped, ok := realResult.(ResScan)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		// Use a fast compare first to avoid go-cmp overhead on success.
		// (nil and empty slices/bytes are equivalent under the public API semantics.)
		if !entriesSliceEqual(modelTyped.Entries, realTyped.Entries) {
			diff := cmp.Diff(modelTyped.Entries, realTyped.Entries, cmpopts.EquateEmpty())
			tb.Fatalf("%s: entries mismatch (-model +real):\n%s", operationValue.String(), diff)
		}

	case ResReopen:
		realTyped, ok := realResult.(ResReopen)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.CloseError, realTyped.CloseError) {
			tb.Fatalf("%s: close error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.CloseError, realTyped.CloseError)
		}

		if !errorsMatch(modelTyped.OpenError, realTyped.OpenError) {
			tb.Fatalf("%s: open error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.OpenError, realTyped.OpenError)
		}

	case ResUserHeader:
		realTyped, ok := realResult.(ResUserHeader)
		if !ok {
			panic("test harness bug: real result type does not match model result type")
		}

		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			tb.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Header.Flags != realTyped.Header.Flags {
			tb.Fatalf("%s: flags mismatch\nmodel=%d\nreal=%d", operationValue.String(), modelTyped.Header.Flags, realTyped.Header.Flags)
		}

		if modelTyped.Header.Data != realTyped.Header.Data {
			tb.Fatalf("%s: data mismatch\nmodel=%x\nreal=%x", operationValue.String(), modelTyped.Header.Data, realTyped.Header.Data)
		}

	default:
		panic("unknown result type")
	}
}

// RememberPutKey appends the Put key to keyHistory if:
//   - the operation is a Put
//   - the model result is success (err == nil)
//   - the key length is correct (so it's actually usable later)
//
// We use the MODEL result here intentionally: if model vs real disagree, the test
// will already fail at AssertOpMatch.
func RememberPutKey(operationValue Operation, modelResult OperationResult, keySize int, keyHistory *[][]byte) {
	putOperation, isPut := operationValue.(OpPut)
	if !isPut {
		return
	}

	errorResult, isErrorResult := modelResult.(ResErr)
	if !isErrorResult {
		return
	}

	if errorResult.Error != nil {
		return
	}

	if len(putOperation.Key) != keySize {
		return
	}

	*keyHistory = append(*keyHistory, append([]byte(nil), putOperation.Key...))
}
