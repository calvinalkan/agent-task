package testutil

import (
	"fmt"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Operation is a single public-API call we apply to both the model and the real cache.
//
// NOTE: These ops are intentionally "behavior-level". They do not model internal
// on-disk details.
type Operation interface {
	Name() string
	String() string
}

// -----------------------------------------------------------------------------
// Cache operations.
// -----------------------------------------------------------------------------

// OpLen represents a Len() call.
type OpLen struct{}

// Name returns the operation name.
func (OpLen) Name() string   { return "Len" }
func (OpLen) String() string { return "Len()" }

// OpGet represents a Get(key) call.
type OpGet struct {
	Key []byte
}

// Name returns the operation name.
func (OpGet) Name() string { return "Get" }
func (operation OpGet) String() string {
	return fmt.Sprintf("Get(%x)", operation.Key)
}

// OpScan represents a Scan(opts) call.
type OpScan struct {
	Filter  *FilterSpec
	Options slotcache.ScanOptions
}

// Name returns the operation name.
func (OpScan) Name() string { return "Scan" }
func (operation OpScan) String() string {
	if operation.Filter == nil {
		return fmt.Sprintf("Scan(%+v)", operation.Options)
	}

	return fmt.Sprintf("Scan(filter=%s,%+v)", operation.Filter.String(), operation.Options)
}

// OpScanPrefix represents a ScanPrefix(prefix, opts) call.
type OpScanPrefix struct {
	Prefix  []byte
	Filter  *FilterSpec
	Options slotcache.ScanOptions
}

// Name returns the operation name.
func (OpScanPrefix) Name() string { return "ScanPrefix" }
func (operation OpScanPrefix) String() string {
	if operation.Filter == nil {
		return fmt.Sprintf("ScanPrefix(%x,%+v)", operation.Prefix, operation.Options)
	}

	return fmt.Sprintf("ScanPrefix(%x,filter=%s,%+v)", operation.Prefix, operation.Filter.String(), operation.Options)
}

// OpScanMatch represents a ScanMatch(spec, opts) call.
type OpScanMatch struct {
	Spec    slotcache.Prefix
	Filter  *FilterSpec
	Options slotcache.ScanOptions
}

// Name returns the operation name.
func (OpScanMatch) Name() string { return "ScanMatch" }
func (operation OpScanMatch) String() string {
	if operation.Filter == nil {
		return fmt.Sprintf("ScanMatch(offset=%d,bits=%d,bytes=%x,%+v)", operation.Spec.Offset, operation.Spec.Bits, operation.Spec.Bytes, operation.Options)
	}

	return fmt.Sprintf("ScanMatch(offset=%d,bits=%d,bytes=%x,filter=%s,%+v)", operation.Spec.Offset, operation.Spec.Bits, operation.Spec.Bytes, operation.Filter.String(), operation.Options)
}

// OpScanRange represents a ScanRange(start, end, opts) call.
type OpScanRange struct {
	Start   []byte
	End     []byte
	Filter  *FilterSpec
	Options slotcache.ScanOptions
}

// Name returns the operation name.
func (OpScanRange) Name() string { return "ScanRange" }
func (operation OpScanRange) String() string {
	if operation.Filter == nil {
		return fmt.Sprintf("ScanRange(start=%x,end=%x,%+v)", operation.Start, operation.End, operation.Options)
	}

	return fmt.Sprintf("ScanRange(start=%x,end=%x,filter=%s,%+v)", operation.Start, operation.End, operation.Filter.String(), operation.Options)
}

// OpClose represents a Cache.Close() call.
type OpClose struct{}

// Name returns the operation name.
func (OpClose) Name() string   { return "Close" }
func (OpClose) String() string { return "Close()" }

// OpReopen simulates a process restart.
//
// It attempts to close the current cache handle (if any), then opens a new
// handle on the same underlying persistent file.
//
// If Close returns ErrBusy, the cache remains open and we do not open a new
// handle.
type OpReopen struct{}

// Name returns the operation name.
func (OpReopen) Name() string   { return "Reopen" }
func (OpReopen) String() string { return "Reopen()" }

// -----------------------------------------------------------------------------
// Writer operations.
// -----------------------------------------------------------------------------

// OpBeginWrite represents a BeginWrite() call.
type OpBeginWrite struct{}

// Name returns the operation name.
func (OpBeginWrite) Name() string   { return "BeginWrite" }
func (OpBeginWrite) String() string { return "BeginWrite()" }

// OpPut represents a Writer.Put call.
type OpPut struct {
	Key      []byte
	Revision int64
	Index    []byte
}

// Name returns the operation name.
func (OpPut) Name() string { return "Writer.Put" }
func (operation OpPut) String() string {
	return fmt.Sprintf("Writer.Put(%x, revision=%d, index=%x)", operation.Key, operation.Revision, operation.Index)
}

// OpDelete represents a Writer.Delete call.
type OpDelete struct {
	Key []byte
}

// Name returns the operation name.
func (OpDelete) Name() string { return "Writer.Delete" }
func (operation OpDelete) String() string {
	return fmt.Sprintf("Writer.Delete(%x)", operation.Key)
}

// OpCommit represents a Writer.Commit call.
type OpCommit struct{}

// Name returns the operation name.
func (OpCommit) Name() string   { return "Writer.Commit" }
func (OpCommit) String() string { return "Writer.Commit()" }

// OpWriterClose represents a Writer.Close call.
type OpWriterClose struct{}

// Name returns the operation name.
func (OpWriterClose) Name() string   { return "Writer.Close" }
func (OpWriterClose) String() string { return "Writer.Close()" }

// -----------------------------------------------------------------------------
// Typed operation results.
// -----------------------------------------------------------------------------

// OperationResult is a typed result produced by applying an Operation.
type OperationResult interface {
	isResult()
}

// ResErr captures an error-only result.
type ResErr struct {
	Error error
}

func (ResErr) isResult() {}

// ResLen captures a Len() result.
type ResLen struct {
	Length int
	Error  error
}

func (ResLen) isResult() {}

// ResGet captures a Get() result.
type ResGet struct {
	Entry  slotcache.Entry
	Exists bool
	Error  error
}

func (ResGet) isResult() {}

// ResDel captures a Delete() result.
type ResDel struct {
	Existed bool
	Error   error
}

func (ResDel) isResult() {}

// ResScan captures a Scan-style result.
type ResScan struct {
	Entries []slotcache.Entry
	Error   error
}

func (ResScan) isResult() {}

// ResReopen captures the Close/Open result pair from a Reopen operation.
type ResReopen struct {
	CloseError error
	OpenError  error
}

func (ResReopen) isResult() {}
