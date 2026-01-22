package testutil

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil/model"
)

// DefaultMaxFuzzOperations is the default maximum number of operations
// to run in a single fuzz iteration or deterministic behavior test.
//
// This value is shared across behavior fuzz tests, guard tests, and seed
// helpers to ensure consistent operation counts when validating seeds.
//
// The value of 200 provides enough depth to exercise multi-operation
// sequences (writer sessions, scan iterations, close/reopen cycles) while
// keeping individual fuzz iterations fast enough for good throughput.
const DefaultMaxFuzzOperations = 200

// BehaviorRunConfig configures a model-vs-real behavior test run.
//
// Heavy comparisons (CompareState) can be triggered two ways:
//   - Cadence-based: every HeavyCompareEveryN operations (set to 0 to disable)
//   - Event-based: after commits (CompareOnCommit) or close/reopen (CompareOnCloseReopen)
//
// Light comparisons (CompareStateLight) run every LightCompareEveryN operations.
type BehaviorRunConfig struct {
	// MaxOps is the maximum number of operations to execute.
	MaxOps int

	// LightCompareEveryN runs CompareStateLight every N operations (0 to disable).
	LightCompareEveryN int

	// HeavyCompareEveryN runs CompareState every N operations (0 to disable).
	HeavyCompareEveryN int

	// CompareOnCommit runs CompareState after Writer.Commit.
	CompareOnCommit bool

	// CompareOnCloseReopen runs CompareState after Close/Reopen.
	CompareOnCloseReopen bool
}

// OpSource produces operations for RunBehavior.
//
// The writerActive parameter indicates whether both model and real writers
// are currently active. This allows operation selection without coupling
// to the Harness type.
type OpSource interface {
	NextOp(writerActive bool, seen [][]byte) Operation
}

// RunBehavior executes a deterministic stream of operations and compares
// the public API behavior between the model and the real cache.
func RunBehavior(tb testing.TB, opts slotcache.Options, src OpSource, cfg BehaviorRunConfig) {
	tb.Helper()

	if cfg.MaxOps <= 0 {
		tb.Fatalf("RunBehavior requires MaxOps > 0")
	}

	harness := NewHarness(tb, opts)
	tb.Cleanup(func() {
		_ = harness.Real.Cache.Close()
	})

	var previouslySeenKeys [][]byte

	for opIndex := 1; opIndex <= cfg.MaxOps; opIndex++ {
		writerActive := harness.Model.Writer != nil && harness.Real.Writer != nil
		operationValue := src.NextOp(writerActive, previouslySeenKeys)

		modelResult := ApplyModel(harness, operationValue)
		realResult := ApplyReal(harness, operationValue)

		RememberPutKey(operationValue, modelResult, opts.KeySize, &previouslySeenKeys)

		AssertOpMatch(tb, operationValue, modelResult, realResult)

		if shouldRunHeavyCompare(operationValue, opIndex, cfg) {
			CompareState(tb, harness)

			continue
		}

		if shouldRunLightCompare(opIndex, cfg) {
			CompareStateLight(tb, harness)
		}
	}
}

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
	Cache  *slotcache.Cache
	Writer *slotcache.Writer
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
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.Scan(opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanPrefix:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.ScanPrefix(concreteOperation.Prefix, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanMatch:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		modelEntries, scanError := testHarness.Model.Cache.ScanMatch(concreteOperation.Spec, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: ToEntries(modelEntries), Error: nil}

	case OpScanRange:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
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

	case OpUserHeader:
		header, headerError := testHarness.Model.Cache.UserHeader()

		return ResUserHeader{Header: header, Error: headerError}

	case OpSetUserHeaderFlags:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.SetUserHeaderFlags without an active model writer")
		}

		setError := testHarness.Model.Writer.SetUserHeaderFlags(concreteOperation.Flags)

		return ResErr{Error: setError}

	case OpSetUserHeaderData:
		if testHarness.Model.Writer == nil {
			panic("test harness bug: Writer.SetUserHeaderData without an active model writer")
		}

		setError := testHarness.Model.Writer.SetUserHeaderData(concreteOperation.Data)

		return ResErr{Error: setError}

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
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.Scan(opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanPrefix:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanPrefix(concreteOperation.Prefix, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanMatch:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanMatch(concreteOperation.Spec, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpScanRange:
		opts := concreteOperation.Options
		if concreteOperation.Filter != nil {
			opts.Filter = buildFilter(*concreteOperation.Filter)
		}

		entries, scanError := testHarness.Real.Cache.ScanRange(concreteOperation.Start, concreteOperation.End, opts)
		if scanError != nil {
			return ResScan{Entries: nil, Error: scanError}
		}

		return ResScan{Entries: entries, Error: nil}

	case OpBeginWrite:
		writerHandle, beginError := testHarness.Real.Cache.Writer()
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

	case OpUserHeader:
		header, headerError := testHarness.Real.Cache.UserHeader()

		return ResUserHeader{Header: header, Error: headerError}

	case OpSetUserHeaderFlags:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.SetUserHeaderFlags without an active real writer")
		}

		setError := testHarness.Real.Writer.SetUserHeaderFlags(concreteOperation.Flags)

		return ResErr{Error: setError}

	case OpSetUserHeaderData:
		if testHarness.Real.Writer == nil {
			panic("test harness bug: Writer.SetUserHeaderData without an active real writer")
		}

		setError := testHarness.Real.Writer.SetUserHeaderData(concreteOperation.Data)

		return ResErr{Error: setError}

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

// RememberKey appends key to keyHistory if it is a valid key and is not
// already present.
func RememberKey(key []byte, keySize int, keyHistory *[][]byte) {
	if len(key) != keySize {
		return
	}

	for _, existing := range *keyHistory {
		if bytes.Equal(existing, key) {
			return
		}
	}

	*keyHistory = append(*keyHistory, append([]byte(nil), key...))
}

// RememberPutKey appends the Put key to keyHistory if:
//   - the operation is a Put
//   - the model result is success (err == nil)
//   - the key length is correct (so it's actually usable later)
//   - the key was not already present in keyHistory
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

	RememberKey(putOperation.Key, keySize, keyHistory)
}

// Fnv1a64 computes the FNV-1a 64-bit hash over key bytes.
func fnv1a64(keyBytes []byte) uint64 {
	const (
		offsetBasis = 14695981039346656037
		prime       = 1099511628211
	)

	var hash uint64 = offsetBasis

	for _, b := range keyBytes {
		hash ^= uint64(b)
		hash *= prime
	}

	return hash
}

func shouldRunHeavyCompare(operationValue Operation, opIndex int, cfg BehaviorRunConfig) bool {
	if cfg.HeavyCompareEveryN > 0 && opIndex%cfg.HeavyCompareEveryN == 0 {
		return true
	}

	switch operationValue.(type) {
	case OpCommit:
		return cfg.CompareOnCommit
	case OpClose, OpReopen:
		return cfg.CompareOnCloseReopen
	default:
		return false
	}
}

func shouldRunLightCompare(opIndex int, cfg BehaviorRunConfig) bool {
	return cfg.LightCompareEveryN > 0 && opIndex%cfg.LightCompareEveryN == 0
}
