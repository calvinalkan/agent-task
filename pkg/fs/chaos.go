package fs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// ChaosConfig controls fault injection probabilities.
// Each rate is a float64 from 0.0 (never) to 1.0 (always).
//
// The zero value disables all fault injection. Partially initialized configs
// only inject faults for the specified rates; unset fields default to 0.0.
//
// Fault injection is enabled by default ([ChaosModeActive]). Use
// [Chaos.SetMode] with [ChaosModeNoOp] to disable injection and pass
// all operations through to the underlying filesystem.
type ChaosConfig struct {
	// ReadFailRate controls how often FS.ReadFile and File.Read fail entirely,
	// returning zero bytes and an error. For ReadFile, the error may be an
	// open-phase failure (EACCES, EMFILE, ENFILE, ENOTDIR) or a read-phase
	// failure (EIO). For File.Read, always returns EIO.
	ReadFailRate float64

	// PartialReadRate controls how often reads return incomplete data.
	// For FS.ReadFile: returns a truncated prefix of the file contents along
	// with an EIO error, simulating a read that fails partway through.
	// For File.Read: returns a short read (n < len(data), err==nil) by limiting
	// the underlying read size. This is valid io.Reader behavior, not an error,
	// and tests that callers correctly loop until EOF.
	PartialReadRate float64

	// WriteFailRate controls how often File.Write fails entirely, writing zero
	// bytes and returning an error (EIO, ENOSPC, EDQUOT, or EROFS).
	WriteFailRate float64

	// PartialWriteRate controls how often File.Write writes only some bytes
	// before failing. Returns n > 0 bytes written along with an error.
	// The error type is controlled by ShortWriteRate.
	PartialWriteRate float64

	// ShortWriteRate controls the error type for partial writes. This fraction
	// of partial writes return io.ErrShortWrite (a write that stopped early
	// without a syscall error). The remainder return *fs.PathError with an
	// errno (EIO, ENOSPC, EDQUOT, or EROFS).
	ShortWriteRate float64

	// FileStatFailRate controls how often File.Stat fails on an open file
	// handle, returning EIO. This is distinct from StatFailRate which controls
	// FS.Stat on paths.
	FileStatFailRate float64

	// SeekFailRate controls how often File.Seek fails, returning position 0
	// and an EIO error.
	SeekFailRate float64

	// SyncFailRate controls how often File.Sync (fsync) fails. Returns EIO,
	// ENOSPC, EDQUOT, or EROFS. Sync failures can surface delayed write errors
	// that weren't reported during Write.
	SyncFailRate float64

	// CloseFailRate controls how often File.Close reports an error. The
	// underlying file descriptor is always closed (to avoid leaks) even when
	// an error is returned. Returns EIO.
	CloseFailRate float64

	// ChmodFailRate controls how often File.Chmod fails on an open file
	// handle, returning EACCES, EPERM, EIO, or EROFS.
	ChmodFailRate float64

	// OpenFailRate controls how often FS.Open, FS.Create, and FS.OpenFile fail
	// to open a file. For read-only opens: EACCES, EIO, EMFILE, ENFILE, ENOTDIR.
	// For write opens (Create, O_WRONLY, etc.): adds ENOSPC, EDQUOT, EROFS.
	OpenFailRate float64

	// RemoveFailRate controls how often FS.Remove and FS.RemoveAll fail.
	// Returns EACCES, EPERM, EBUSY, EIO, or EROFS.
	RemoveFailRate float64

	// RenameFailRate controls how often FS.Rename fails. Returns an
	// *os.LinkError (not *fs.PathError) with EACCES, EIO, ENOSPC, EXDEV
	// (cross-device), EROFS, or EPERM.
	RenameFailRate float64

	// StatFailRate controls how often FS.Stat and FS.Exists fail on a path.
	// Returns EACCES or EIO. This is distinct from FileStatFailRate which
	// controls File.Stat on open handles.
	StatFailRate float64

	// MkdirAllFailRate controls how often FS.MkdirAll fails to create
	// directories. Returns EACCES, EIO, ENOSPC, EDQUOT, EROFS, or ENOTDIR.
	MkdirAllFailRate float64

	// ReadDirFailRate controls how often FS.ReadDir fails entirely, returning
	// no entries. Returns EACCES, EIO, ENOTDIR, EMFILE, or ENFILE.
	ReadDirFailRate float64

	// ReadDirPartialRate controls how often FS.ReadDir returns an incomplete
	// directory listing. Returns a random prefix of the entries along with an
	// EIO error, simulating a directory read that fails partway through.
	ReadDirPartialRate float64

	// TraceCapacity is the max number of operations to keep in the trace log.
	// Set to 0 (default) to disable tracing. Tracing records all operations
	// including those where Chaos modified behavior without returning an error
	// (e.g., short reads with nil error).
	TraceCapacity int
}

// ChaosMode controls how [Chaos] behaves.
type ChaosMode uint8

const (
	// ChaosModeActive enables fault-rate injection.
	// This is the default mode for a new [Chaos].
	ChaosModeActive ChaosMode = iota

	// ChaosModeNoOp passes every operation directly to the underlying FS.
	ChaosModeNoOp
)

// ChaosStats contains counts of injected faults.
type ChaosStats struct {
	OpenFails       int64
	ReadFails       int64
	WriteFails      int64
	ReadDirFails    int64
	PartialReads    int64
	PartialWrites   int64
	PartialReadDirs int64
	RemoveFails     int64
	RenameFails     int64
	StatFails       int64
	MkdirAllFails   int64
	FileStatFails   int64
	SeekFails       int64
	SyncFails       int64
	CloseFails      int64
	ChmodFails      int64
}

// chaosError marks an error as intentionally injected by [Chaos].
//
// It wraps the underlying error so errors.Is/As continue to work.
//
// Note: For errno-style errors, [Chaos] wraps an [*fs.PathError] (or [*os.LinkError]
// for rename) with a [syscall.Errno] in PathError.Err so os.IsNotExist/os.IsPermission
// keep working via unwrapping, while [IsChaosErr] can still distinguish chaos vs
// real OS errors in tests.
//
// Error panics if receiver or Err is nil. Unwrap panics if receiver is nil.
type chaosError struct {
	Err error
}

// Error returns a formatted error message.
// Panics if e or e.Err is nil.
func (e *chaosError) Error() string {
	return "chaos: " + e.Err.Error()
}

// Unwrap returns the underlying error. Panics if e is nil.
func (e *chaosError) Unwrap() error {
	return e.Err
}

// IsChaosErr reports whether err (or any wrapped error) was injected by [Chaos].
// Returns false if err is nil.
func IsChaosErr(err error) bool {
	var injected *chaosError

	return errors.As(err, &injected)
}

// Chaos wraps an [FS] and injects random failures for testing.
//
// The fault model aims to match the surface semantics of Go's os package on
// Unix-ish systems, without overfitting to edge/undefined kernel behavior.
// It is a "real filesystem + fault injection" wrapper, not a full filesystem
// simulator. Chaos does not maintain per-path "sticky" fault state; each call
// independently decides whether to inject.
//
// Error model:
//   - Most injected filesystem errors are returned as an [*fs.PathError] with a
//     real [syscall.Errno] in PathError.Err, so [errors.Is] and helpers like
//     [os.IsPermission] behave like real OS errors.
//   - Rename failures are returned as an [*os.LinkError] with a real
//     [syscall.Errno] in LinkError.Err, like [os.Rename].
//   - Injected errors are marked so tests can distinguish injected vs real
//     filesystem errors using [IsChaosErr].
//   - Chaos never injects ENOENT (any os.IsNotExist result originates from the
//     wrapped [FS]) and never injects EINTR (the stdlib generally retries EINTR
//     internally). Injection may still overlay other failures regardless of
//     whether the target exists (e.g. RemoveAll can fail even if the path would
//     otherwise be missing due to simulated permission errors).
//   - Chaos does not inject os.ErrInvalid or other "API misuse" failures (nil
//     receiver/invalid handle); those are caller bugs, not filesystem faults.
//
// Return-shape constraints:
//   - File.Read injected failures return n==0 with a non-nil error (matching
//     os.File.Read on Unix-ish systems, which forces n=0 on syscall.Read errors).
//   - File.Write may return n>0 with a non-nil error (partial progress).
//   - File.Seek injected failures return pos==0 with a non-nil error.
//   - File.Stat injected failures return (nil, non-nil error).
//   - File.Sync injected failures return a non-nil error.
//   - File.Close injected failures still close the underlying file to avoid
//     descriptor leaks in tests.
//   - Chaos does not inject impossible anomalies like n>len(data) or "n==0 &&
//     err==nil" mid-write. EOF is not treated as an injected "failure"; it comes
//     from the wrapped filesystem as bare io.EOF.
//
// Partial operations:
//   - File.Read short: short read with err==nil by limiting the underlying
//     read size (does not skip bytes / advance offsets incorrectly). This is
//     a legal io.Reader outcome, not EOF or an error.
//   - File.Write partial: writes a prefix and returns a non-nil error; most
//     partial writes return an errno-style [*fs.PathError], but 10% return
//     an injected [io.ErrShortWrite] to model "short write without errno".
//   - FS.ReadFile partial: returns a prefix + non-nil error (like os.ReadFile
//     returning bytes read so far after a later read fails).
//   - FS.ReadDir partial: returns a subset + non-nil error (like os.ReadDir
//     returning entries read so far after a later directory read fails).
//
// Use [Chaos.SetMode] to control behavior and [Chaos.Stats] to inspect how many
// faults were injected.
type Chaos struct {
	fs     FS
	rng    *rand.Rand
	config ChaosConfig
	mode   atomic.Uint32
	trace  *chaosTrace

	rngMu sync.Mutex

	// Counters for testing verification
	openFails       atomic.Int64
	readFails       atomic.Int64
	writeFails      atomic.Int64
	readDirFails    atomic.Int64
	partialReads    atomic.Int64
	partialWrites   atomic.Int64
	partialReadDirs atomic.Int64
	removeFails     atomic.Int64
	renameFails     atomic.Int64
	statFails       atomic.Int64
	mkdirAllFails   atomic.Int64
	fileStatFails   atomic.Int64
	seekFails       atomic.Int64
	syncFails       atomic.Int64
	closeFails      atomic.Int64
	chmodFails      atomic.Int64
}

// NewChaos creates a new [Chaos] filesystem wrapping the given [FS].
// The seed controls random fault injection for reproducibility.
// Panics if underlying is nil.
func NewChaos(underlying FS, seed int64, config *ChaosConfig) *Chaos {
	if underlying == nil {
		panic("underlying fs is nil")
	}

	return &Chaos{
		fs:     underlying,
		rng:    rand.New(rand.NewPCG(uint64(seed), uint64(seed))),
		config: *config,
		trace:  newChaosTrace(config.TraceCapacity),
	}
}

// SetMode updates [Chaos] behavior.
//
// SetMode is safe to call concurrently with filesystem operations.
//
// Modes:
//   - [ChaosModeActive]: inject random failures according to [ChaosConfig].
//     This is the default.
//   - [ChaosModeNoOp]: pass all operations to the underlying filesystem.
func (c *Chaos) SetMode(m ChaosMode) { c.mode.Store(uint32(m)) }

// Trace returns a formatted string of recent FS operations.
// Returns an empty string if tracing is disabled (TraceCapacity == 0).
func (c *Chaos) Trace() string {
	return c.trace.String()
}

// TraceEvents returns a snapshot of the trace buffer.
// Returns nil if tracing is disabled (TraceCapacity == 0).
func (c *Chaos) TraceEvents() []TraceEvent {
	return c.trace.snapshot()
}

// Stats returns the current fault injection counts.
func (c *Chaos) Stats() ChaosStats {
	return ChaosStats{
		OpenFails:       c.openFails.Load(),
		ReadFails:       c.readFails.Load(),
		WriteFails:      c.writeFails.Load(),
		ReadDirFails:    c.readDirFails.Load(),
		PartialReads:    c.partialReads.Load(),
		PartialWrites:   c.partialWrites.Load(),
		PartialReadDirs: c.partialReadDirs.Load(),
		RemoveFails:     c.removeFails.Load(),
		RenameFails:     c.renameFails.Load(),
		StatFails:       c.statFails.Load(),
		MkdirAllFails:   c.mkdirAllFails.Load(),
		FileStatFails:   c.fileStatFails.Load(),
		SeekFails:       c.seekFails.Load(),
		SyncFails:       c.syncFails.Load(),
		CloseFails:      c.closeFails.Load(),
		ChmodFails:      c.chmodFails.Load(),
	}
}

// TotalFaults returns the total number of injected faults.
func (c *Chaos) TotalFaults() int64 {
	stats := c.Stats()

	return stats.OpenFails + stats.ReadFails + stats.WriteFails + stats.PartialReads +
		stats.PartialWrites + stats.ReadDirFails + stats.PartialReadDirs +
		stats.RemoveFails + stats.RenameFails + stats.StatFails + stats.MkdirAllFails +
		stats.FileStatFails + stats.SeekFails + stats.SyncFails + stats.CloseFails +
		stats.ChmodFails
}

// Open opens a file for reading with fault injection.
func (c *Chaos) Open(path string) (File, error) {
	return c.openWithChaos(path, chaosOpOpen, func() (File, error) {
		return c.fs.Open(path)
	})
}

// Create creates a file for writing with fault injection.
func (c *Chaos) Create(path string) (File, error) {
	return c.openWithChaos(path, chaosOpCreate, func() (File, error) {
		return c.fs.Create(path)
	})
}

// OpenFile opens a file with the specified flags and permissions with fault injection.
func (c *Chaos) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	op := chaosOpOpen
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		op = chaosOpCreate
	}

	return c.openWithChaos(path, op, func() (File, error) {
		return c.fs.OpenFile(path, flag, perm)
	})
}

// ReadFile reads a file's contents with fault injection.
func (c *Chaos) ReadFile(path string) ([]byte, error) {
	mode := c.getMode()
	if mode == ChaosModeNoOp {
		data, err := c.fs.ReadFile(path)

		c.trace.add("readfile", path, boolKind(err == nil), err, false,
			TraceAttr{"n", strconv.Itoa(len(data))})

		return data, err
	}

	if c.should(mode, c.config.ReadFailRate) {
		op, errno := c.pickReadFileError()
		c.readFails.Add(1)

		err := pathError(op, path, errno)

		c.trace.add("readfile", path, "fail", err, true, TraceAttr{"errno", errno.Error()})

		return nil, err
	}

	data, err := c.fs.ReadFile(path)
	if err != nil {
		c.trace.add("readfile", path, "fail", err, false)

		return nil, err
	}

	// Partial read - return truncated data + error (like os.ReadFile returning
	// bytes read so far after a later Read fails).
	if c.should(mode, c.config.PartialReadRate) && len(data) > 1 {
		c.partialReads.Add(1)
		cutoff := c.randIntn(len(data)-1) + 1
		err := pathError("read", path, syscall.EIO)

		c.trace.add("readfile", path, "partial_read", err, true,
			TraceAttr{"cutoff", strconv.Itoa(cutoff)},
			TraceAttr{"total", strconv.Itoa(len(data))})

		return data[:cutoff], err
	}

	c.trace.add("readfile", path, "ok", nil, false,
		TraceAttr{"n", strconv.Itoa(len(data))})

	return data, nil
}

// WriteFile writes data to a file via OpenFile + Write + Close.
// Fault injection flows through naturally: OpenFailRate affects the create,
// WriteFailRate and PartialWriteRate affect the write, CloseFailRate affects
// the close.
func (c *Chaos) WriteFile(path string, data []byte, perm os.FileMode) error {
	file, err := c.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		c.trace.add("writefile", path, "fail", err, IsChaosErr(err),
			TraceAttr{"phase", "open"})

		return err
	}

	written, err := file.Write(data)
	if err != nil {
		_ = file.Close() // best-effort close on write error

		c.trace.add("writefile", path, "fail", err, IsChaosErr(err),
			TraceAttr{"phase", "write"},
			TraceAttr{"n", strconv.Itoa(written)},
			TraceAttr{"len", strconv.Itoa(len(data))})

		return err
	}

	closeErr := file.Close()
	if closeErr != nil {
		c.trace.add("writefile", path, "fail", closeErr, IsChaosErr(closeErr),
			TraceAttr{"phase", "close"})

		return closeErr
	}

	c.trace.add("writefile", path, "ok", nil, false,
		TraceAttr{"n", strconv.Itoa(written)})

	return nil
}

// ReadDir reads directory contents with fault injection.
func (c *Chaos) ReadDir(path string) ([]os.DirEntry, error) {
	mode := c.getMode()
	if mode == ChaosModeNoOp {
		entries, err := c.fs.ReadDir(path)

		c.trace.add("readdir", path, boolKind(err == nil), err, false,
			TraceAttr{"n", strconv.Itoa(len(entries))})

		return entries, err
	}

	if c.should(mode, c.config.ReadDirFailRate) {
		errno := c.pickError("readdir")
		c.readDirFails.Add(1)

		err := pathError("readdir", path, errno)

		c.trace.add("readdir", path, "fail", err, true, TraceAttr{"errno", errno.Error()})

		return nil, err
	}

	entries, err := c.fs.ReadDir(path)
	if err != nil {
		c.trace.add("readdir", path, "fail", err, false)

		return nil, err
	}

	// Partial listing - return subset + error (like os.ReadDir returning entries
	// read so far after a later directory read fails).
	if c.should(mode, c.config.ReadDirPartialRate) && len(entries) > 1 {
		c.partialReadDirs.Add(1)
		cutoff := c.randIntn(len(entries)-1) + 1
		err := pathError("readdir", path, syscall.EIO)

		c.trace.add("readdir", path, "partial_readdir", err, true,
			TraceAttr{"cutoff", strconv.Itoa(cutoff)},
			TraceAttr{"total", strconv.Itoa(len(entries))})

		return entries[:cutoff], err
	}

	c.trace.add("readdir", path, "ok", nil, false,
		TraceAttr{"n", strconv.Itoa(len(entries))})

	return entries, nil
}

// MkdirAll creates a directory and parents with fault injection.
func (c *Chaos) MkdirAll(path string, perm os.FileMode) error {
	err := c.introduceChaos(path, faultMkdirAll)
	if err != nil {
		return err
	}

	err = c.fs.MkdirAll(path, perm)

	c.trace.add("mkdirall", path, boolKind(err == nil), err, false,
		TraceAttr{"perm", fmt.Sprintf("%#o", perm)})

	return err
}

// Stat returns file info with fault injection.
func (c *Chaos) Stat(path string) (os.FileInfo, error) {
	err := c.introduceChaos(path, faultStat)
	if err != nil {
		return nil, err
	}

	info, err := c.fs.Stat(path)

	c.trace.add("stat", path, boolKind(err == nil), err, false)

	if err != nil {
		return nil, err
	}

	return info, nil
}

// Exists checks file existence with fault injection.
func (c *Chaos) Exists(path string) (bool, error) {
	err := c.introduceChaos(path, faultStat)
	if err != nil {
		return false, err
	}

	exists, err := c.fs.Exists(path)

	c.trace.add("exists", path, boolKind(err == nil), err, false,
		TraceAttr{"exists", strconv.FormatBool(exists)})

	return exists, err
}

// Remove removes a file with fault injection.
func (c *Chaos) Remove(path string) error {
	err := c.introduceChaos(path, faultRemove)
	if err != nil {
		return err
	}

	err = c.fs.Remove(path)

	c.trace.add("remove", path, boolKind(err == nil), err, false)

	return err
}

// RemoveAll removes a path and its contents with fault injection.
func (c *Chaos) RemoveAll(path string) error {
	err := c.introduceChaos(path, faultRemoveAll)
	if err != nil {
		return err
	}

	err = c.fs.RemoveAll(path)

	c.trace.add("removeall", path, boolKind(err == nil), err, false)

	return err
}

// Rename renames a file with fault injection.
func (c *Chaos) Rename(oldpath, newpath string) error {
	mode := c.getMode()
	if mode == ChaosModeNoOp {
		err := c.fs.Rename(oldpath, newpath)

		c.trace.add("rename", oldpath, boolKind(err == nil), err, false,
			TraceAttr{"newpath", newpath})

		return err
	}

	if c.should(mode, c.config.RenameFailRate) {
		errno := c.pickError("rename")
		c.renameFails.Add(1)

		err := linkError("rename", oldpath, newpath, errno)

		c.trace.add("rename", oldpath, "fail", err, true,
			TraceAttr{"newpath", newpath}, TraceAttr{"errno", errno.Error()})

		return err
	}

	err := c.fs.Rename(oldpath, newpath)

	c.trace.add("rename", oldpath, boolKind(err == nil), err, false,
		TraceAttr{"newpath", newpath})

	return err
}

// getMode returns the current ChaosMode safely.
func (c *Chaos) getMode() ChaosMode {
	v := c.mode.Load()
	if v > uint32(ChaosModeNoOp) {
		return ChaosModeActive
	}

	return ChaosMode(v)
}

// openWithChaos wraps file-open operations with fault injection.
// The op parameter controls which errno set is used (via pickError).
// Returns the wrapped chaosFile on success, or an injected error.
func (c *Chaos) openWithChaos(path, op string, openFn func() (File, error)) (File, error) {
	mode := c.getMode()
	if mode == ChaosModeNoOp {
		file, err := openFn()
		if err != nil {
			c.trace.add(op, path, "fail", err, false)

			return nil, err
		}

		c.trace.add(op, path, "ok", nil, false)

		return &chaosFile{f: file, chaos: c, path: path}, nil
	}

	if c.should(mode, c.config.OpenFailRate) {
		errno := c.pickError(op)
		c.openFails.Add(1)

		err := pathError("open", path, errno)

		c.trace.add(op, path, "fail", err, true, TraceAttr{"errno", errno.Error()})

		return nil, err
	}

	file, err := openFn()
	if err != nil {
		c.trace.add(op, path, "fail", err, false)

		return nil, err
	}

	c.trace.add(op, path, "ok", nil, false)

	return &chaosFile{f: file, chaos: c, path: path}, nil
}

// chaosOp identifies operation names used in Chaos fault injection.
const (
	chaosOpOpen   = "open"
	chaosOpCreate = "create"
)

// faultKind identifies a type of fault that can be injected.
// The string value is used as the operation name in error messages.
type faultKind string

const (
	faultStat      faultKind = "stat"
	faultRemove    faultKind = "remove"
	faultRemoveAll faultKind = "removeall"
	faultMkdirAll  faultKind = "mkdirall"
)

// fileFaultKind identifies a type of fault for file handle operations.
// The string value is used as the operation name in error messages.
type fileFaultKind string

const (
	fileFaultSeek  fileFaultKind = "seek"
	fileFaultStat  fileFaultKind = fileFaultKind(faultStat)
	fileFaultSync  fileFaultKind = "sync"
	fileFaultChmod fileFaultKind = "chmod"
)

// introduceChaos checks if a fault should be injected for the given operation.
// Returns a non-nil error if a fault was injected, nil otherwise.
//
// Chaos never injects ENOENT or EINTR:
//   - ENOENT ("no such file or directory") should come from the wrapped FS so
//     Chaos doesn't manufacture "missing" results the real filesystem wouldn't
//     have produced.
//   - EINTR ("interrupted system call") is generally retried internally by the
//     Go stdlib, so surfacing it is usually less os-like than surfacing EIO.
func (c *Chaos) introduceChaos(path string, kind faultKind) error {
	mode := c.getMode()
	if mode != ChaosModeActive {
		return nil
	}

	var (
		rate    float64
		counter *atomic.Int64
		errnos  []syscall.Errno
	)

	switch kind {
	case faultStat:
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		rate = c.config.StatFailRate
		counter = &c.statFails
		errnos = []syscall.Errno{syscall.EACCES, syscall.EIO}

	case faultRemove, faultRemoveAll:
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EPERM: operation not permitted (policy/flags disallow the operation)
		// EBUSY: resource/device busy (in use)
		// EIO: I/O error (device/filesystem failure)
		// EROFS: read-only filesystem (writes/mutations are rejected)
		rate = c.config.RemoveFailRate
		counter = &c.removeFails
		errnos = []syscall.Errno{syscall.EACCES, syscall.EPERM, syscall.EBUSY, syscall.EIO, syscall.EROFS}

	case faultMkdirAll:
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		// ENOSPC: no space left on device
		// EDQUOT: disk quota exceeded
		// EROFS: read-only filesystem (writes/mutations are rejected)
		// ENOTDIR: a path component is not a directory
		rate = c.config.MkdirAllFailRate
		counter = &c.mkdirAllFails
		errnos = []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.ENOSPC, syscall.EDQUOT, syscall.EROFS, syscall.ENOTDIR}

	default:
		panic("unknown fault kind: " + string(kind))
	}

	if c.should(mode, rate) {
		counter.Add(1)

		errno := errnos[c.randIntn(len(errnos))]
		err := pathError(string(kind), path, errno)

		c.trace.add(string(kind), path, "fail", err, true, TraceAttr{"errno", errno.Error()})

		return err
	}

	return nil
}

// should returns true with the given probability when chaos is injecting.
func (c *Chaos) should(mode ChaosMode, rate float64) bool {
	if mode != ChaosModeActive {
		return false
	}

	return c.randFloat() < rate
}

// randFloat returns a random float64 in [0.0, 1.0) (thread-safe).
func (c *Chaos) randFloat() float64 {
	c.rngMu.Lock()
	result := c.rng.Float64()
	c.rngMu.Unlock()

	return result
}

// randIntn returns a random int in [0, n) (thread-safe).
func (c *Chaos) randIntn(n int) int {
	c.rngMu.Lock()
	result := c.rng.IntN(n)
	c.rngMu.Unlock()

	return result
}

// pathError creates an injected [*fs.PathError] with the given operation, path, and errno.
// The error is wrapped in [chaosError] so [IsChaosErr] can identify it, while
// [errors.As] and helpers like [os.IsPermission] still work via unwrapping.
func pathError(op, path string, errno syscall.Errno) error {
	pe := &fs.PathError{Op: op, Path: path, Err: errno}

	return &chaosError{Err: pe}
}

// linkError creates an injected [*os.LinkError] with the given operation, paths, and errno.
// The error is wrapped in [chaosError] so [IsChaosErr] can identify it, while
// [errors.As] and helpers like [os.IsPermission] still work via unwrapping.
func linkError(op, oldpath, newpath string, errno syscall.Errno) error {
	le := &os.LinkError{Op: op, Old: oldpath, New: newpath, Err: errno}

	return &chaosError{Err: le}
}

// pickRandom selects a random error from the slice.
func (c *Chaos) pickRandom(errs []syscall.Errno) syscall.Errno {
	return errs[c.randIntn(len(errs))]
}

// pickReadFileError returns an injected error consistent with os.ReadFile:
// the failure can be either an open-time error or a later read-time error.
func (c *Chaos) pickReadFileError() (string, syscall.Errno) {
	// Only include errors that keep os.Is* classification working and avoid
	// injecting ENOENT (missing-path errors should come from the wrapped FS).
	if c.randFloat() < 0.5 {
		return chaosOpOpen, c.pickRandom([]syscall.Errno{
			syscall.EACCES,
			syscall.EMFILE,
			syscall.ENFILE,
			syscall.ENOTDIR,
		})
	}

	return "read", syscall.EIO
}

// pickError selects an injected errno for the given operation.
//
// Note: Some operations are handled by [Chaos.introduceChaos] or
// [chaosFile.introduceChaos] instead, which have inline errno documentation.
//
// Operation → injected errnos:
//   - open: EACCES, EIO, EMFILE, ENFILE, ENOTDIR
//   - create: EACCES, EIO, ENOSPC, EDQUOT, EROFS, EMFILE, ENFILE, ENOTDIR
//   - readdir: EACCES, EIO, ENOTDIR, EMFILE, ENFILE
//   - rename: EACCES, EIO, ENOSPC, EXDEV, EROFS, EPERM
//   - fdread: EIO only (avoid EACCES/ENOENT post-open; match os.File.Read shape)
//   - fdwrite: EIO, ENOSPC, EDQUOT, EROFS (avoid EACCES/ENOENT post-open)
//   - fdclose: EIO only (avoid EACCES/ENOENT post-open)
func (c *Chaos) pickError(op string) syscall.Errno {
	switch op {
	case chaosOpOpen:
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		// EMFILE: too many open files for this process (per-process FD limit)
		// ENFILE: too many open files in the system (system-wide FD limit)
		// ENOTDIR: expected a directory, but a path component is not a directory
		return c.pickRandom([]syscall.Errno{
			syscall.EACCES,
			syscall.EIO,
			syscall.EMFILE,
			syscall.ENFILE,
			syscall.ENOTDIR,
		})

	case chaosOpCreate:
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		// ENOSPC: no space left on device
		// EDQUOT: disk quota exceeded
		// EROFS: read-only filesystem (writes/mutations are rejected)
		// EMFILE: too many open files for this process (per-process FD limit)
		// ENFILE: too many open files in the system (system-wide FD limit)
		// ENOTDIR: expected a directory, but a path component is not a directory
		return c.pickRandom([]syscall.Errno{
			syscall.EACCES,
			syscall.EIO,
			syscall.ENOSPC,
			syscall.EDQUOT,
			syscall.EROFS,
			syscall.EMFILE,
			syscall.ENFILE,
			syscall.ENOTDIR,
		})

	case "readdir":
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		// ENOTDIR: expected a directory, but a path component is not a directory
		// EMFILE: too many open files for this process (per-process FD limit)
		// ENFILE: too many open files in the system (system-wide FD limit)
		return c.pickRandom([]syscall.Errno{
			syscall.EACCES,
			syscall.EIO,
			syscall.ENOTDIR,
			syscall.EMFILE,
			syscall.ENFILE,
		})

	case "rename":
		// EACCES: permission denied (file/directory permissions or ACLs)
		// EIO: I/O error (device/filesystem failure)
		// ENOSPC: no space left on device
		// EXDEV: cross-device link (rename across filesystems/mount points)
		// EROFS: read-only filesystem (writes/mutations are rejected)
		// EPERM: operation not permitted (policy/flags disallow the operation)
		return c.pickRandom([]syscall.Errno{
			syscall.EACCES,
			syscall.EIO,
			syscall.ENOSPC,
			syscall.EXDEV,
			syscall.EROFS,
			syscall.EPERM,
		})

	case "fdwrite":
		// EIO: I/O error (device/filesystem failure)
		// ENOSPC: no space left on device
		// EDQUOT: disk quota exceeded
		// EROFS: read-only filesystem (writes/mutations are rejected)
		// Avoid EACCES/ENOENT post-open.
		return c.pickRandom([]syscall.Errno{
			syscall.EIO,
			syscall.ENOSPC,
			syscall.EDQUOT,
			syscall.EROFS,
		})

	default:
		// For fdread/fdclose: EIO only to avoid EACCES/ENOENT post-open; match os.File.Read shape
		return syscall.EIO
	}
}

// chaosFile wraps a [File] and injects faults on Read/Write.
type chaosFile struct {
	f     File
	chaos *Chaos
	path  string
}

// Interface compliance.
var _ File = (*chaosFile)(nil)

func (cf *chaosFile) Read(buf []byte) (int, error) {
	mode := cf.chaos.getMode()
	if mode == ChaosModeNoOp {
		n, err := cf.f.Read(buf)

		cf.chaos.trace.add("file.read", cf.path, boolKind(err == nil), err, false,
			TraceAttr{"n", strconv.Itoa(n)})

		return n, err
	}

	if cf.chaos.should(mode, cf.chaos.config.ReadFailRate) {
		errno := cf.chaos.pickError("fdread")
		cf.chaos.readFails.Add(1)
		err := pathError("read", cf.path, errno)

		cf.chaos.trace.add("file.read", cf.path, "fail", err, true,
			TraceAttr{"errno", errno.Error()})

		return 0, err
	}

	// Partial read: return a short read WITHOUT skipping bytes.
	// This must limit the underlying read, not just shrink the returned count,
	// otherwise the file offset advances too far and callers silently lose data.
	if cf.chaos.should(mode, cf.chaos.config.PartialReadRate) && len(buf) > 1 {
		cf.chaos.partialReads.Add(1)
		cutoff := cf.chaos.randIntn(len(buf)-1) + 1 // [1, len(buf)-1]

		bytesRead, err := cf.f.Read(buf[:cutoff])

		// Short read with nil error is valid io.Reader behavior
		cf.chaos.trace.add("file.read", cf.path, "short_read", err, true,
			TraceAttr{"n", strconv.Itoa(bytesRead)},
			TraceAttr{"requested", strconv.Itoa(len(buf))},
			TraceAttr{"cutoff", strconv.Itoa(cutoff)})

		return bytesRead, err
	}

	n, err := cf.f.Read(buf)

	cf.chaos.trace.add("file.read", cf.path, boolKind(err == nil), err, false,
		TraceAttr{"n", strconv.Itoa(n)})

	return n, err
}

func (cf *chaosFile) Write(data []byte) (int, error) {
	mode := cf.chaos.getMode()
	if mode == ChaosModeNoOp {
		n, err := cf.f.Write(data)

		cf.chaos.trace.add("file.write", cf.path, boolKind(err == nil), err, false,
			TraceAttr{"n", strconv.Itoa(n)})

		return n, err
	}

	if cf.chaos.should(mode, cf.chaos.config.WriteFailRate) {
		errno := cf.chaos.pickError("fdwrite")
		cf.chaos.writeFails.Add(1)
		err := pathError("write", cf.path, errno)

		cf.chaos.trace.add("file.write", cf.path, "fail", err, true,
			TraceAttr{"errno", errno.Error()})

		return 0, err
	}

	// Partial write
	if cf.chaos.should(mode, cf.chaos.config.PartialWriteRate) && len(data) > 1 {
		cf.chaos.partialWrites.Add(1)
		cutoff := cf.chaos.randIntn(len(data)-1) + 1 // [1, len(data)-1]

		wrote, err := cf.f.Write(data[:cutoff])
		if err != nil {
			cf.chaos.trace.add("file.write", cf.path, "fail", err, false,
				TraceAttr{"n", strconv.Itoa(wrote)})

			return wrote, err
		}

		// Some portion of partial writes should look like a "short write without an errno"
		// (io.ErrShortWrite). In the stdlib, this is the fallback when a write returns
		// n != len(b) without a syscall error.
		if cf.chaos.randFloat() < cf.chaos.config.ShortWriteRate {
			err := &chaosError{Err: io.ErrShortWrite}

			cf.chaos.trace.add("file.write", cf.path, "short_write", err, true,
				TraceAttr{"n", strconv.Itoa(wrote)},
				TraceAttr{"requested", strconv.Itoa(len(data))})

			return wrote, err
		}

		errno := cf.chaos.pickError("fdwrite")
		err = pathError("write", cf.path, errno)

		cf.chaos.trace.add("file.write", cf.path, "partial_write", err, true,
			TraceAttr{"n", strconv.Itoa(wrote)},
			TraceAttr{"requested", strconv.Itoa(len(data))},
			TraceAttr{"errno", errno.Error()})

		return wrote, err
	}

	n, err := cf.f.Write(data)

	cf.chaos.trace.add("file.write", cf.path, boolKind(err == nil), err, false,
		TraceAttr{"n", strconv.Itoa(n)})

	return n, err
}

func (cf *chaosFile) Close() error {
	mode := cf.chaos.getMode()
	if mode == ChaosModeNoOp {
		err := cf.f.Close()

		cf.chaos.trace.add("file.close", cf.path, boolKind(err == nil), err, false)

		return err
	}

	injectClose := cf.chaos.should(mode, cf.chaos.config.CloseFailRate)

	// Always close the underlying file to avoid descriptor leaks, even when
	// returning an injected error.
	err := cf.f.Close()
	if err != nil {
		cf.chaos.trace.add("file.close", cf.path, "fail", err, false)

		return err
	}

	if injectClose {
		cf.chaos.closeFails.Add(1)
		errno := cf.chaos.pickError("fdclose")
		err := pathError("close", cf.path, errno)

		cf.chaos.trace.add("file.close", cf.path, "fail", err, true,
			TraceAttr{"errno", errno.Error()})

		return err
	}

	cf.chaos.trace.add("file.close", cf.path, "ok", nil, false)

	return nil
}

func (cf *chaosFile) Seek(offset int64, whence int) (int64, error) {
	err := cf.introduceChaos(fileFaultSeek)
	if err != nil {
		return 0, err
	}

	pos, err := cf.f.Seek(offset, whence)

	cf.chaos.trace.add("file.seek", cf.path, boolKind(err == nil), err, false,
		TraceAttr{"offset", strconv.FormatInt(offset, 10)},
		TraceAttr{"whence", strconv.Itoa(whence)},
		TraceAttr{"pos", strconv.FormatInt(pos, 10)})

	return pos, err
}

func (cf *chaosFile) Fd() uintptr {
	return cf.f.Fd()
}

func (cf *chaosFile) Stat() (os.FileInfo, error) {
	err := cf.introduceChaos(fileFaultStat)
	if err != nil {
		return nil, err
	}

	info, err := cf.f.Stat()

	cf.chaos.trace.add("file.stat", cf.path, boolKind(err == nil), err, false)

	if err != nil {
		return nil, err
	}

	return info, nil
}

func (cf *chaosFile) Sync() error {
	err := cf.introduceChaos(fileFaultSync)
	if err != nil {
		return err
	}

	err = cf.f.Sync()

	cf.chaos.trace.add("file.sync", cf.path, boolKind(err == nil), err, false)

	return err
}

func (cf *chaosFile) Chmod(mode os.FileMode) error {
	err := cf.introduceChaos(fileFaultChmod)
	if err != nil {
		return err
	}

	err = cf.f.Chmod(mode)

	cf.chaos.trace.add("file.chmod", cf.path, boolKind(err == nil), err, false,
		TraceAttr{"mode", fmt.Sprintf("%#o", mode)})

	return err
}

// introduceChaos checks if a fault should be injected for file handle operations.
// Returns a non-nil error if a fault was injected, nil otherwise.
func (cf *chaosFile) introduceChaos(kind fileFaultKind) error {
	mode := cf.chaos.getMode()
	if mode != ChaosModeActive {
		return nil
	}

	var (
		rate    float64
		counter *atomic.Int64
		errnos  []syscall.Errno
	)

	switch kind {
	case fileFaultSeek:
		// EIO: I/O error (avoid EACCES/ENOENT post-open)
		rate = cf.chaos.config.SeekFailRate
		counter = &cf.chaos.seekFails
		errnos = []syscall.Errno{syscall.EIO}

	case fileFaultStat:
		// EIO: I/O error (avoid EACCES/ENOENT post-open)
		rate = cf.chaos.config.FileStatFailRate
		counter = &cf.chaos.fileStatFails
		errnos = []syscall.Errno{syscall.EIO}

	case fileFaultSync:
		// EIO: I/O error (device/filesystem failure)
		// ENOSPC: no space left on device
		// EDQUOT: disk quota exceeded
		// EROFS: read-only filesystem (writes/mutations are rejected)
		// fsync can surface delayed write failures
		rate = cf.chaos.config.SyncFailRate
		counter = &cf.chaos.syncFails
		errnos = []syscall.Errno{syscall.EIO, syscall.ENOSPC, syscall.EDQUOT, syscall.EROFS}

	case fileFaultChmod:
		// EACCES: permission denied
		// EPERM: operation not permitted
		// EIO: I/O error
		// EROFS: read-only filesystem
		rate = cf.chaos.config.ChmodFailRate
		counter = &cf.chaos.chmodFails
		errnos = []syscall.Errno{syscall.EACCES, syscall.EPERM, syscall.EIO, syscall.EROFS}

	default:
		panic("unknown file fault kind: " + string(kind))
	}

	if cf.chaos.should(mode, rate) {
		counter.Add(1)

		errno := errnos[cf.chaos.randIntn(len(errnos))]
		err := pathError(string(kind), cf.path, errno)

		cf.chaos.trace.add("file."+string(kind), cf.path, "fail", err, true,
			TraceAttr{"errno", errno.Error()})

		return err
	}

	return nil
}

var _ FS = (*Chaos)(nil)

// TraceEvent records a single Chaos operation with injection details.
//
// Unlike external tracing (which can only observe errors), TraceEvent captures
// operations that Chaos altered but returned successfully, such as short reads
// that returned fewer bytes with err==nil.
type TraceEvent struct {
	// Seq is the monotonically increasing sequence number.
	Seq uint64
	// Op is the operation name (e.g., "open", "read", "file.write").
	Op string
	// Path is the filesystem path involved.
	Path string
	// Err is the error returned by the operation (nil for success).
	Err error
	// Injected is true if Chaos modified the operation's behavior.
	// This includes both error injection and non-error alterations
	// like short reads.
	Injected bool
	// Kind is a short label for what happened: "ok", "fail", "short_read",
	// "short_write", "partial_readdir", etc. Use [TraceEvent.Injected] to
	// distinguish injected behavior from passthrough outcomes.
	Kind string
	// Attrs contains additional key-value details (e.g., "cutoff=42", "errno=EIO").
	Attrs []TraceAttr
}

// TraceAttr is a key-value pair for trace event context.
type TraceAttr struct {
	Key   string
	Value string
}

func (e TraceEvent) String() string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "#%d", e.Seq)

	if e.Injected {
		fmt.Fprintf(&sb, " [CHAOS:%s]", e.Kind)
	}

	fmt.Fprintf(&sb, " %s", e.Op)

	if e.Path != "" {
		fmt.Fprintf(&sb, " path=%q", e.Path)
	}

	for _, a := range e.Attrs {
		fmt.Fprintf(&sb, " %s=%s", a.Key, a.Value)
	}

	if !e.Injected {
		sb.WriteString(" ")
		sb.WriteString(e.Kind)
	}

	if e.Err != nil {
		fmt.Fprintf(&sb, " err=%v", e.Err)
	}

	return sb.String()
}

// chaosTrace is a bounded circular buffer of [TraceEvent].
type chaosTrace struct {
	mu       sync.Mutex
	capacity int
	events   []TraceEvent
	next     int
	full     bool
	seq      uint64
}

func newChaosTrace(capacity int) *chaosTrace {
	if capacity <= 0 {
		return nil
	}

	return &chaosTrace{
		capacity: capacity,
		events:   make([]TraceEvent, 0, capacity),
	}
}

func (t *chaosTrace) String() string {
	events := t.snapshot()
	if len(events) == 0 {
		return ""
	}

	var sb strings.Builder

	for i, e := range events {
		if i > 0 {
			sb.WriteByte('\n')
		}

		sb.WriteString(e.String())
	}

	return sb.String()
}

func (t *chaosTrace) add(op, path, kind string, err error, injected bool, attrs ...TraceAttr) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.seq++

	event := TraceEvent{
		Seq:      t.seq,
		Op:       op,
		Path:     path,
		Err:      err,
		Injected: injected,
		Kind:     kind,
		Attrs:    attrs,
	}

	if len(t.events) < t.capacity {
		t.events = append(t.events, event)

		return
	}

	t.events[t.next] = event
	t.next = (t.next + 1) % t.capacity
	t.full = true
}

func (t *chaosTrace) snapshot() []TraceEvent {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.full {
		return append([]TraceEvent(nil), t.events...)
	}

	out := make([]TraceEvent, 0, len(t.events))
	out = append(out, t.events[t.next:]...)
	out = append(out, t.events[:t.next]...)

	return out
}

func boolKind(ok bool) string {
	if ok {
		return "ok"
	}

	return "fail"
}
