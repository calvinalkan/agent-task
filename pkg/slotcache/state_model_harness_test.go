//go:build slotcache_impl

package slotcache_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/model"
)

// harness holds:
//   - a simple in-memory model (committed state + optional active writer), and
//   - the real implementation (file-backed)
//
// We always apply the same operation to both sides, then compare:
//  1. the direct operation result, and
//  2. the observable committed state (Len/Get/Scan/ScanPrefix).
//
// IMPORTANT: This harness compares PUBLIC API behavior only.
type harness struct {
	opts slotcache.Options

	model struct {
		file   *model.FileState
		cache  *model.CacheModel
		writer *model.WriterModel
	}

	real struct {
		cache  slotcache.Cache
		writer slotcache.Writer
	}
}

func newHarness(t *testing.T, options slotcache.Options) *harness {
	t.Helper()

	file, modelError := model.NewFile(options)
	if modelError != nil {
		t.Fatalf("model.NewFile failed: %v", modelError)
	}

	realCache, openError := slotcache.Open(options)
	if openError != nil {
		t.Fatalf("slotcache.Open failed: %v", openError)
	}

	testHarness := &harness{opts: options}
	testHarness.model.file = file
	testHarness.model.cache = model.Open(file)

	testHarness.real.cache = realCache

	return testHarness
}

// -----------------------------------------------------------------------------
// Apply operations to model + real
// -----------------------------------------------------------------------------

func applyModel(testHarness *harness, operationValue operation) operationResult {
	switch concreteOperation := operationValue.(type) {
	case opLen:
		length, lengthError := testHarness.model.cache.Len()

		return resLen{Length: length, Error: lengthError}

	case opGet:
		modelEntry, exists, getError := testHarness.model.cache.Get(concreteOperation.Key)

		return resGet{Entry: toSlotcacheEntry(modelEntry), Exists: exists, Error: getError}

	case opScan:
		modelEntries, scanError := testHarness.model.cache.Scan(concreteOperation.Options)
		if scanError != nil {
			return resScan{Entries: nil, Error: scanError}
		}

		return resScan{Entries: toSlotcacheEntries(modelEntries), Error: nil}

	case opScanPrefix:
		modelEntries, scanError := testHarness.model.cache.ScanPrefix(concreteOperation.Prefix, concreteOperation.Options)
		if scanError != nil {
			return resScan{Entries: nil, Error: scanError}
		}

		return resScan{Entries: toSlotcacheEntries(modelEntries), Error: nil}

	case opBeginWrite:
		writerHandle, beginError := testHarness.model.cache.BeginWrite()
		if beginError == nil {
			testHarness.model.writer = writerHandle
		}

		return resErr{Error: beginError}

	case opPut:
		if testHarness.model.writer == nil {
			panic("test harness bug: Writer.Put without an active model writer")
		}

		putError := testHarness.model.writer.Put(concreteOperation.Key, concreteOperation.Revision, concreteOperation.Index)

		return resErr{Error: putError}

	case opDelete:
		if testHarness.model.writer == nil {
			panic("test harness bug: Writer.Delete without an active model writer")
		}

		existed, deleteError := testHarness.model.writer.Delete(concreteOperation.Key)

		return resDel{Existed: existed, Error: deleteError}

	case opCommit:
		if testHarness.model.writer == nil {
			panic("test harness bug: Writer.Commit without an active model writer")
		}

		commitError := testHarness.model.writer.Commit()
		testHarness.model.writer = nil

		return resErr{Error: commitError}

	case opAbort:
		if testHarness.model.writer == nil {
			panic("test harness bug: Writer.Abort without an active model writer")
		}

		abortError := testHarness.model.writer.Abort()
		testHarness.model.writer = nil

		return resErr{Error: abortError}

	case opClose:
		closeError := testHarness.model.cache.Close()

		return resErr{Error: closeError}

	case opReopen:
		closeError := testHarness.model.cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return resReopen{CloseError: closeError, OpenError: nil}
		}

		// Whether close succeeded or it was already closed, we can create a new handle.
		testHarness.model.cache = model.Open(testHarness.model.file)
		testHarness.model.writer = nil

		return resReopen{CloseError: closeError, OpenError: nil}

	default:
		panic("unknown operation type")
	}
}

func applyReal(testHarness *harness, operationValue operation) operationResult {
	switch concreteOperation := operationValue.(type) {
	case opLen:
		length, lengthError := testHarness.real.cache.Len()

		return resLen{Length: length, Error: lengthError}

	case opGet:
		entry, exists, getError := testHarness.real.cache.Get(concreteOperation.Key)

		return resGet{Entry: entry, Exists: exists, Error: getError}

	case opScan:
		sequence, scanError := testHarness.real.cache.Scan(concreteOperation.Options)
		if scanError != nil {
			return resScan{Entries: nil, Error: scanError}
		}

		return resScan{Entries: collectSeq(sequence), Error: nil}

	case opScanPrefix:
		sequence, scanError := testHarness.real.cache.ScanPrefix(concreteOperation.Prefix, concreteOperation.Options)
		if scanError != nil {
			return resScan{Entries: nil, Error: scanError}
		}

		return resScan{Entries: collectSeq(sequence), Error: nil}

	case opBeginWrite:
		writerHandle, beginError := testHarness.real.cache.BeginWrite()
		if beginError == nil {
			testHarness.real.writer = writerHandle
		}

		return resErr{Error: beginError}

	case opPut:
		if testHarness.real.writer == nil {
			panic("test harness bug: Writer.Put without an active real writer")
		}

		putError := testHarness.real.writer.Put(concreteOperation.Key, concreteOperation.Revision, concreteOperation.Index)

		return resErr{Error: putError}

	case opDelete:
		if testHarness.real.writer == nil {
			panic("test harness bug: Writer.Delete without an active real writer")
		}

		existed, deleteError := testHarness.real.writer.Delete(concreteOperation.Key)

		return resDel{Existed: existed, Error: deleteError}

	case opCommit:
		if testHarness.real.writer == nil {
			panic("test harness bug: Writer.Commit without an active real writer")
		}

		commitError := testHarness.real.writer.Commit()
		testHarness.real.writer = nil

		return resErr{Error: commitError}

	case opAbort:
		if testHarness.real.writer == nil {
			panic("test harness bug: Writer.Abort without an active real writer")
		}

		abortError := testHarness.real.writer.Abort()
		testHarness.real.writer = nil

		return resErr{Error: abortError}

	case opClose:
		closeError := testHarness.real.cache.Close()

		return resErr{Error: closeError}

	case opReopen:
		closeError := testHarness.real.cache.Close()
		if errors.Is(closeError, slotcache.ErrBusy) {
			// Keep existing open handle.
			return resReopen{CloseError: closeError, OpenError: nil}
		}

		reopenedCache, openError := slotcache.Open(testHarness.opts)
		if openError == nil {
			testHarness.real.cache = reopenedCache
			testHarness.real.writer = nil
		}

		return resReopen{CloseError: closeError, OpenError: openError}

	default:
		panic("unknown operation type")
	}
}

// -----------------------------------------------------------------------------
// Compare operation results
// -----------------------------------------------------------------------------

func assertMatch(t *testing.T, operationValue operation, modelResult operationResult, realResult operationResult) {
	t.Helper()

	switch modelTyped := modelResult.(type) {
	case resErr:
		realTyped := realResult.(resErr)
		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

	case resLen:
		realTyped := realResult.(resLen)
		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Length != realTyped.Length {
			t.Fatalf("%s: length mismatch\nmodel=%d\nreal=%d", operationValue.String(), modelTyped.Length, realTyped.Length)
		}

	case resGet:
		realTyped := realResult.(resGet)
		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Exists != realTyped.Exists {
			t.Fatalf("%s: exists mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Exists, realTyped.Exists)
		}

		if diff := cmp.Diff(modelTyped.Entry, realTyped.Entry); diff != "" {
			t.Fatalf("%s: entry mismatch (-model +real):\n%s", operationValue.String(), diff)
		}

	case resDel:
		realTyped := realResult.(resDel)
		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if modelTyped.Existed != realTyped.Existed {
			t.Fatalf("%s: existed mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Existed, realTyped.Existed)
		}

	case resScan:
		realTyped := realResult.(resScan)
		if !errorsMatch(modelTyped.Error, realTyped.Error) {
			t.Fatalf("%s: error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.Error, realTyped.Error)
		}

		if diff := cmp.Diff(modelTyped.Entries, realTyped.Entries); diff != "" {
			t.Fatalf("%s: entries mismatch (-model +real):\n%s", operationValue.String(), diff)
		}

	case resReopen:
		realTyped := realResult.(resReopen)
		if !errorsMatch(modelTyped.CloseError, realTyped.CloseError) {
			t.Fatalf("%s: close error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.CloseError, realTyped.CloseError)
		}

		if !errorsMatch(modelTyped.OpenError, realTyped.OpenError) {
			t.Fatalf("%s: open error mismatch\nmodel=%v\nreal=%v", operationValue.String(), modelTyped.OpenError, realTyped.OpenError)
		}

	default:
		panic("unknown result type")
	}
}

// rememberKeyAfterSuccessfulPutIfValid appends the Put key to keyHistory if:
//   - the operation is a Put
//   - the model result is success (err == nil)
//   - the key length is correct (so it's actually usable later)
//
// We use the MODEL result here intentionally: if model vs real disagree, the test
// will already fail at assertMatch.
func rememberKeyAfterSuccessfulPutIfValid(operationValue operation, modelResult operationResult, keySize int, keyHistory *[][]byte) {
	putOperation, isPut := operationValue.(opPut)
	if !isPut {
		return
	}

	errorResult, isErrorResult := modelResult.(resErr)
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
