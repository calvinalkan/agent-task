//go:build slotcache_impl

package slotcache_test

import (
	"fmt"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// operation is a single public-API call we apply to both the model and the real cache.
//
// NOTE: These ops are intentionally "behavior-level". They do not model internal
// on-disk details.
type operation interface {
	Name() string
	String() string
}

// -----------------------------------------------------------------------------
// Cache operations.
// -----------------------------------------------------------------------------

type opLen struct{}

func (opLen) Name() string   { return "Len" }
func (opLen) String() string { return "Len()" }

type opGet struct {
	Key []byte
}

func (opGet) Name() string { return "Get" }
func (operation opGet) String() string {
	return fmt.Sprintf("Get(%x)", operation.Key)
}

type opScan struct {
	Options slotcache.ScanOpts
}

func (opScan) Name() string { return "Scan" }
func (operation opScan) String() string {
	return fmt.Sprintf("Scan(%+v)", operation.Options)
}

type opScanPrefix struct {
	Prefix  []byte
	Options slotcache.ScanOpts
}

func (opScanPrefix) Name() string { return "ScanPrefix" }
func (operation opScanPrefix) String() string {
	return fmt.Sprintf("ScanPrefix(%x,%+v)", operation.Prefix, operation.Options)
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

// -----------------------------------------------------------------------------
// Writer operations.
// -----------------------------------------------------------------------------

type opBeginWrite struct{}

func (opBeginWrite) Name() string   { return "BeginWrite" }
func (opBeginWrite) String() string { return "BeginWrite()" }

type opPut struct {
	Key      []byte
	Revision int64
	Index    []byte
}

func (opPut) Name() string { return "Writer.Put" }
func (operation opPut) String() string {
	return fmt.Sprintf("Writer.Put(%x, revision=%d, index=%x)", operation.Key, operation.Revision, operation.Index)
}

type opDelete struct {
	Key []byte
}

func (opDelete) Name() string { return "Writer.Delete" }
func (operation opDelete) String() string {
	return fmt.Sprintf("Writer.Delete(%x)", operation.Key)
}

type opCommit struct{}

func (opCommit) Name() string   { return "Writer.Commit" }
func (opCommit) String() string { return "Writer.Commit()" }

type opAbort struct{}

func (opAbort) Name() string   { return "Writer.Abort" }
func (opAbort) String() string { return "Writer.Abort()" }

// -----------------------------------------------------------------------------
// Typed operation results.
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
