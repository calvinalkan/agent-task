package fs

import (
	"io/fs"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
)

// ChaosConfig controls fault injection probabilities.
// Each rate is a float64 from 0.0 (never) to 1.0 (always).
type ChaosConfig struct {
	// Read faults
	ReadFailRate    float64 // Fail read operations entirely
	PartialReadRate float64 // Return truncated data on reads

	// Write faults
	WriteFailRate    float64 // Fail write operations entirely
	PartialWriteRate float64 // Write partial data then fail (simulates crash)

	// Other faults
	OpenFailRate       float64 // Fail Open/Create/OpenFile
	RemoveFailRate     float64 // Fail Remove/RemoveAll
	RenameFailRate     float64 // Fail Rename
	StatFailRate       float64 // Fail Stat/Exists
	ReadDirFailRate    float64 // Fail ReadDir entirely
	ReadDirPartialRate float64 // Return partial directory listing
	LockFailRate       float64 // Fail Lock acquisition
}

// DefaultChaosConfig returns a config with reasonable fault rates for testing.
func DefaultChaosConfig() ChaosConfig {
	return ChaosConfig{
		ReadFailRate:       0.02,
		PartialReadRate:    0.02,
		WriteFailRate:      0.02,
		PartialWriteRate:   0.03,
		OpenFailRate:       0.02,
		RemoveFailRate:     0.02,
		RenameFailRate:     0.02,
		StatFailRate:       0.01,
		ReadDirFailRate:    0.02,
		ReadDirPartialRate: 0.02,
		LockFailRate:       0.02,
	}
}

// PathState tracks the fault state of a path for consistent error injection.
type PathState int

const (
	// PathNormal means no persistent fault - errors are transient.
	// This is the zero value, so untracked paths are normal.
	PathNormal PathState = iota
	// PathIOError is sticky - the path has a "bad sector" and always returns EIO.
	PathIOError
	// PathReadOnly is sticky for writes - filesystem is read-only, returns EROFS.
	PathReadOnly
	// PathNoPermission is semi-sticky - path-based operations return EACCES 80% of the time.
	PathNoPermission
)

// ChaosMode controls how Chaos behaves.
type ChaosMode uint8

const (
	// ChaosModePassthrough behaves like the underlying FS.
	// It ignores fault rates and also ignores any sticky path state.
	// Sticky state is not cleared; it is simply not consulted while in this mode.
	ChaosModePassthrough ChaosMode = iota

	// ChaosModeInject enables fault-rate injection and sticky path state.
	ChaosModeInject

	// ChaosModeStickyOnly applies only sticky path state. Fault rates are disabled.
	ChaosModeStickyOnly
)

// Chaos wraps an [FS] and injects random failures for testing.
//
// Errors are state-aware: once a path gets EIO (bad sector), it stays broken.
// Errors are also reality-aware: ENOENT is only returned if the file really
// doesn't exist on the underlying filesystem.
//
// All injected errors are real OS errors (syscall.Errno wrapped in os.PathError)
// so they behave identically to real filesystem errors. Code using errors.Is()
// will work correctly.
//
// Use [Chaos.SetMode] to control behavior.
// Use [Chaos.Stats] to inspect how many faults were injected.
type Chaos struct {
	fs     FS
	rng    *rand.Rand
	config ChaosConfig
	mode   atomic.Uint32

	// Path state tracking for consistent errors
	mu         sync.RWMutex
	pathStates map[string]PathState

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
	lockFails       atomic.Int64
}

// NewChaos creates a new Chaos filesystem wrapping the given [FS].
// The seed controls random fault injection for reproducibility.
func NewChaos(fs FS, seed int64, config ChaosConfig) *Chaos {
	return &Chaos{
		fs:         fs,
		rng:        rand.New(rand.NewSource(seed)),
		config:     config,
		pathStates: make(map[string]PathState),
	}
}

// SetMode updates Chaos behavior.
//
// SetMode is safe to call concurrently with filesystem operations.
//
// Modes:
//   - [ChaosModePassthrough]: behave like the wrapped filesystem; ignore fault
//     rates and ignore sticky path state.
//   - [ChaosModeInject]: inject random failures according to [ChaosConfig] and
//     apply sticky path state (errors may become "sticky" as a consequence of
//     injection).
//   - [ChaosModeStickyOnly]: apply existing sticky path state only; fault rates
//     are disabled.
//
// Switching modes never clears sticky path state. In particular, moving to
// [ChaosModePassthrough] only stops consulting sticky state; switching back to
// [ChaosModeInject] or [ChaosModeStickyOnly] will make any existing sticky
// state take effect again.
//
// The zero value (and default for a new [Chaos]) is [ChaosModePassthrough].
func (c *Chaos) SetMode(m ChaosMode) { c.mode.Store(uint32(m)) }

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
	LockFails       int64
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
		LockFails:       c.lockFails.Load(),
	}
}

// TotalFaults returns the total number of injected faults.
func (c *Chaos) TotalFaults() int64 {
	s := c.Stats()

	return s.OpenFails + s.ReadFails + s.WriteFails + s.PartialReads +
		s.PartialWrites + s.ReadDirFails + s.PartialReadDirs +
		s.RemoveFails + s.RenameFails + s.StatFails + s.LockFails
}

// PathState returns the current fault state for a path (for testing).
func (c *Chaos) PathState(path string) PathState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.pathStates[path]
}

// ResetPathState clears the fault state for a path (for testing).
func (c *Chaos) ResetPathState(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.pathStates, path)
}

// ResetAllPathStates clears all fault states (for testing).
func (c *Chaos) ResetAllPathStates() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pathStates = make(map[string]PathState)
}

// should returns true with the given probability when chaos is injecting.
func (c *Chaos) should(mode ChaosMode, rate float64) bool {
	if mode != ChaosModeInject {
		return false
	}

	return c.randFloat() < rate
}

// randFloat returns a random float64 in [0.0, 1.0) (thread-safe).
func (c *Chaos) randFloat() float64 {
	c.mu.Lock()
	result := c.rng.Float64()
	c.mu.Unlock()

	return result
}

// randIntn returns a random int in [0, n) (thread-safe).
func (c *Chaos) randIntn(n int) int {
	c.mu.Lock()
	result := c.rng.Intn(n)
	c.mu.Unlock()

	return result
}

// getState returns the current fault state for a path.
func (c *Chaos) getState(path string) PathState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.pathStates[path]
}

// setState updates the fault state for a path.
func (c *Chaos) setState(path string, state PathState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state == PathNormal {
		delete(c.pathStates, path)
	} else {
		c.pathStates[path] = state
	}
}

// errToState converts an error to a path state for tracking.
func errToState(err syscall.Errno) PathState {
	switch err {
	case syscall.EIO:
		return PathIOError // Sticky - bad sector
	case syscall.EROFS:
		return PathReadOnly // Sticky for writes
	case syscall.EACCES, syscall.EPERM:
		return PathNoPermission // Semi-sticky
	default:
		return PathNormal // Transient
	}
}

// isWriteOp returns true if the operation modifies the filesystem.
func isWriteOp(op string) bool {
	switch op {
	case "write", "create", "remove", "rename":
		return true
	}

	return false
}

// pathError creates an *os.PathError with the given operation, path, and errno.
// This matches what the real OS returns, so errors.Is() works correctly.
func pathError(op, path string, errno syscall.Errno) error {
	pe := &fs.PathError{Op: op, Path: path, Err: errno}
	markInjectedPathError(pe)

	return pe
}

// pickRandom selects a random error from the slice.
func (c *Chaos) pickRandom(errs []syscall.Errno) syscall.Errno {
	return errs[c.randIntn(len(errs))]
}

// pickError selects an appropriate error based on operation, path state, and real existence.
// This ensures errors are logically consistent with the actual filesystem state.
func (c *Chaos) pickError(op string, path string) (syscall.Errno, error) {
	state := c.getState(path)

	// 1. Sticky states - always return same error
	switch state {
	case PathIOError:
		return syscall.EIO, nil
	case PathReadOnly:
		if isWriteOp(op) {
			return syscall.EROFS, nil
		}
		// Reads still work on read-only filesystem
	}

	// 2. Check real filesystem state to pick valid errors
	// If the existence check itself fails, surface the real error rather than
	// fabricating an injected error based on a guess.
	var realExists bool

	switch op {
	case "open", "remove", "rename", "stat":
		exists, err := c.fs.Exists(path)
		if err != nil {
			return 0, err
		}

		realExists = exists
	}

	var valid []syscall.Errno

	switch op {
	case "open":
		if realExists {
			// File exists - can't return ENOENT
			valid = []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.EMFILE, syscall.ENFILE}
		} else {
			// File doesn't exist - ENOENT is valid
			valid = []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.EIO, syscall.EMFILE}
		}

	case "read":
		// Reading from open file - can't get ENOENT (already opened)
		valid = []syscall.Errno{syscall.EIO, syscall.EINTR}

	case "write", "create":
		// Writes can fail for many reasons regardless of existence
		valid = []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.ENOSPC, syscall.EDQUOT, syscall.EROFS}

	case "remove":
		if realExists {
			valid = []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.EBUSY, syscall.EPERM}
		} else {
			// Can only return ENOENT if file really doesn't exist
			valid = []syscall.Errno{syscall.ENOENT}
		}

	case "rename":
		if realExists {
			valid = []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.ENOSPC, syscall.EXDEV, syscall.EROFS}
		} else {
			valid = []syscall.Errno{syscall.ENOENT, syscall.EIO}
		}

	case "stat":
		if realExists {
			// File exists - can't return ENOENT
			valid = []syscall.Errno{syscall.EACCES, syscall.EIO}
		} else {
			valid = []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.EIO}
		}

	default:
		valid = []syscall.Errno{syscall.EIO}
	}

	// 3. Pick error and update state
	err := c.pickRandom(valid)
	c.setState(path, errToState(err))

	return err, nil
}

// --- File Operations ---

func (c *Chaos) Open(path string) (File, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		f, err := c.fs.Open(path)
		if err != nil {
			return nil, err
		}

		return &chaosFile{f: f, chaos: c, path: path}, nil
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.openFails.Add(1)

			return nil, pathError("open", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	// Sticky EIO - always fail
	if state == PathIOError {
		c.openFails.Add(1)

		return nil, pathError("open", path, syscall.EIO)
	}

	if c.should(mode, c.config.OpenFailRate) {
		errno, err := c.pickError("open", path)
		if err != nil {
			return nil, err
		}

		c.openFails.Add(1)

		return nil, pathError("open", path, errno)
	}

	f, err := c.fs.Open(path)
	if err != nil {
		return nil, err
	}

	return &chaosFile{f: f, chaos: c, path: path}, nil
}

func (c *Chaos) Create(path string) (File, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		f, err := c.fs.Create(path)
		if err != nil {
			return nil, err
		}

		return &chaosFile{f: f, chaos: c, path: path}, nil
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.openFails.Add(1)

			return nil, pathError("create", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	// Sticky errors
	if state == PathIOError {
		c.openFails.Add(1)

		return nil, pathError("create", path, syscall.EIO)
	}

	if state == PathReadOnly {
		c.openFails.Add(1)

		return nil, pathError("create", path, syscall.EROFS)
	}

	if c.should(mode, c.config.OpenFailRate) {
		errno, err := c.pickError("create", path)
		if err != nil {
			return nil, err
		}

		c.openFails.Add(1)

		return nil, pathError("create", path, errno)
	}

	f, err := c.fs.Create(path)
	if err != nil {
		return nil, err
	}

	return &chaosFile{f: f, chaos: c, path: path}, nil
}

func (c *Chaos) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		f, err := c.fs.OpenFile(path, flag, perm)
		if err != nil {
			return nil, err
		}

		return &chaosFile{f: f, chaos: c, path: path}, nil
	}

	state := c.getState(path)
	isWrite := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) != 0

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.openFails.Add(1)

			return nil, pathError("open", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	// Sticky errors
	if state == PathIOError {
		c.openFails.Add(1)

		return nil, pathError("open", path, syscall.EIO)
	}

	if state == PathReadOnly && isWrite {
		c.openFails.Add(1)

		return nil, pathError("open", path, syscall.EROFS)
	}

	if c.should(mode, c.config.OpenFailRate) {
		op := "open"
		if isWrite {
			op = "create"
		}

		errno, err := c.pickError(op, path)
		if err != nil {
			return nil, err
		}

		c.openFails.Add(1)

		return nil, pathError("open", path, errno)
	}

	f, err := c.fs.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}

	return &chaosFile{f: f, chaos: c, path: path}, nil
}

// --- Convenience Methods ---

func (c *Chaos) ReadFile(path string) ([]byte, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.ReadFile(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.readFails.Add(1)

			return nil, pathError("read", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	// Sticky EIO
	if state == PathIOError {
		c.readFails.Add(1)

		return nil, pathError("read", path, syscall.EIO)
	}

	if c.should(mode, c.config.ReadFailRate) {
		errno, err := c.pickError("read", path)
		if err != nil {
			return nil, err
		}

		c.readFails.Add(1)

		return nil, pathError("read", path, errno)
	}

	data, err := c.fs.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Partial read - return truncated data
	if c.should(mode, c.config.PartialReadRate) && len(data) > 1 {
		c.partialReads.Add(1)
		cutoff := c.randIntn(len(data)-1) + 1

		return data[:cutoff], nil
	}

	return data, nil
}

func (c *Chaos) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.WriteFileAtomic(path, data, perm)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.writeFails.Add(1)

			return pathError("write", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	// Sticky errors
	if state == PathIOError {
		c.writeFails.Add(1)

		return pathError("write", path, syscall.EIO)
	}

	if state == PathReadOnly {
		c.writeFails.Add(1)

		return pathError("write", path, syscall.EROFS)
	}

	if c.should(mode, c.config.WriteFailRate) {
		errno, err := c.pickError("write", path)
		if err != nil {
			return err
		}

		c.writeFails.Add(1)

		return pathError("write", path, errno)
	}

	// Partial write: bypass atomic, write truncated data directly
	if c.should(mode, c.config.PartialWriteRate) && len(data) > 1 {
		c.partialWrites.Add(1)
		cutoff := c.randIntn(len(data)-1) + 1
		// Raw write, not atomic. Use the wrapped FS, not os.WriteFile, so
		// Chaos can decorate other FS implementations without leaking to os.
		f, err := c.fs.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return err
		}

		_, err = f.Write(data[:cutoff])
		closeErr := f.Close()

		if err != nil {
			return err
		}

		if closeErr != nil {
			return closeErr
		}

		errno, err := c.pickError("write", path)
		if err != nil {
			return err
		}

		return pathError("write", path, errno)
	}

	return c.fs.WriteFileAtomic(path, data, perm)
}

// --- Directory Operations ---

func (c *Chaos) ReadDir(path string) ([]os.DirEntry, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.ReadDir(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.readDirFails.Add(1)

			return nil, pathError("readdir", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.readDirFails.Add(1)

		return nil, pathError("readdir", path, syscall.EIO)
	}

	if c.should(mode, c.config.ReadDirFailRate) {
		errno, err := c.pickError("stat", path)
		if err != nil {
			return nil, err
		}

		c.readDirFails.Add(1)

		return nil, pathError("readdir", path, errno)
	}

	entries, err := c.fs.ReadDir(path)
	if err != nil {
		return nil, err
	}

	// Partial listing
	if c.should(mode, c.config.ReadDirPartialRate) && len(entries) > 1 {
		c.partialReadDirs.Add(1)
		cutoff := c.randIntn(len(entries)-1) + 1

		return entries[:cutoff], nil
	}

	return entries, nil
}

func (c *Chaos) MkdirAll(path string, perm os.FileMode) error {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.MkdirAll(path, perm)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			return pathError("mkdir", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		return pathError("mkdir", path, syscall.EIO)
	}

	if state == PathReadOnly {
		return pathError("mkdir", path, syscall.EROFS)
	}

	return c.fs.MkdirAll(path, perm)
}

// --- Metadata ---

func (c *Chaos) Stat(path string) (os.FileInfo, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.Stat(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.statFails.Add(1)

			return nil, pathError("stat", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.statFails.Add(1)

		return nil, pathError("stat", path, syscall.EIO)
	}

	if c.should(mode, c.config.StatFailRate) {
		errno, err := c.pickError("stat", path)
		if err != nil {
			return nil, err
		}

		c.statFails.Add(1)

		return nil, pathError("stat", path, errno)
	}

	return c.fs.Stat(path)
}

func (c *Chaos) Exists(path string) (bool, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.Exists(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.statFails.Add(1)

			return false, pathError("stat", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.statFails.Add(1)

		return false, pathError("stat", path, syscall.EIO)
	}

	if c.should(mode, c.config.StatFailRate) {
		errno, err := c.pickError("stat", path)
		if err != nil {
			return false, err
		}

		c.statFails.Add(1)

		return false, pathError("stat", path, errno)
	}

	return c.fs.Exists(path)
}

// --- Mutations ---

func (c *Chaos) Remove(path string) error {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.Remove(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.removeFails.Add(1)

			return pathError("remove", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.removeFails.Add(1)

		return pathError("remove", path, syscall.EIO)
	}

	if state == PathReadOnly {
		c.removeFails.Add(1)

		return pathError("remove", path, syscall.EROFS)
	}

	if c.should(mode, c.config.RemoveFailRate) {
		errno, err := c.pickError("remove", path)
		if err != nil {
			return err
		}

		c.removeFails.Add(1)

		return pathError("remove", path, errno)
	}

	return c.fs.Remove(path)
}

func (c *Chaos) RemoveAll(path string) error {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.RemoveAll(path)
	}

	// Match os.RemoveAll semantics and our FS contract: no error if the path
	// doesn't exist.
	exists, err := c.fs.Exists(path)
	if err != nil {
		return err
	}

	if !exists {
		return nil
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.removeFails.Add(1)

			return pathError("remove", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.removeFails.Add(1)

		return pathError("remove", path, syscall.EIO)
	}

	if state == PathReadOnly {
		c.removeFails.Add(1)

		return pathError("remove", path, syscall.EROFS)
	}

	if c.should(mode, c.config.RemoveFailRate) {
		errno, err := c.pickError("remove", path)
		if err != nil {
			return err
		}

		c.removeFails.Add(1)

		return pathError("remove", path, errno)
	}

	return c.fs.RemoveAll(path)
}

func (c *Chaos) Rename(oldpath, newpath string) error {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.Rename(oldpath, newpath)
	}

	// Check both paths for sticky errors
	oldState := c.getState(oldpath)
	newState := c.getState(newpath)

	// Semi-sticky permissions.
	if oldState == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.renameFails.Add(1)

			return pathError("rename", oldpath, syscall.EACCES)
		}

		c.setState(oldpath, PathNormal)
		oldState = PathNormal
	}

	if newState == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.renameFails.Add(1)

			return pathError("rename", newpath, syscall.EACCES)
		}

		c.setState(newpath, PathNormal)
		newState = PathNormal
	}

	if oldState == PathIOError || newState == PathIOError {
		c.renameFails.Add(1)

		return pathError("rename", oldpath, syscall.EIO)
	}

	if oldState == PathReadOnly || newState == PathReadOnly {
		c.renameFails.Add(1)

		return pathError("rename", oldpath, syscall.EROFS)
	}

	if c.should(mode, c.config.RenameFailRate) {
		errno, err := c.pickError("rename", oldpath)
		if err != nil {
			return err
		}

		c.renameFails.Add(1)

		return pathError("rename", oldpath, errno)
	}

	return c.fs.Rename(oldpath, newpath)
}

// --- Locking ---

func (c *Chaos) Lock(path string) (Locker, error) {
	mode := ChaosMode(c.mode.Load())
	if mode == ChaosModePassthrough {
		return c.fs.Lock(path)
	}

	state := c.getState(path)

	// Semi-sticky permissions.
	if state == PathNoPermission {
		// 80%: still denied, 20%: "permission recovered"
		if c.randFloat() < 0.8 {
			c.lockFails.Add(1)

			return nil, pathError("lock", path, syscall.EACCES)
		}

		c.setState(path, PathNormal)
		state = PathNormal
	}

	if state == PathIOError {
		c.lockFails.Add(1)

		return nil, pathError("lock", path, syscall.EIO)
	}

	if state == PathReadOnly {
		c.lockFails.Add(1)

		return nil, pathError("lock", path, syscall.EROFS)
	}

	if c.should(mode, c.config.LockFailRate) {
		c.lockFails.Add(1)
		// Lock timeout is returned as ErrDeadlineExceeded in real code
		return nil, inject(os.ErrDeadlineExceeded)
	}

	return c.fs.Lock(path)
}

// --- chaosFile wraps a File and injects faults on Read/Write ---

type chaosFile struct {
	f     File
	chaos *Chaos
	path  string
}

func (cf *chaosFile) Read(p []byte) (int, error) {
	mode := ChaosMode(cf.chaos.mode.Load())
	if mode == ChaosModePassthrough {
		return cf.f.Read(p)
	}

	state := cf.chaos.getState(cf.path)

	if state == PathIOError {
		cf.chaos.readFails.Add(1)

		return 0, pathError("read", cf.path, syscall.EIO)
	}

	if cf.chaos.should(mode, cf.chaos.config.ReadFailRate) {
		errno, err := cf.chaos.pickError("read", cf.path)
		if err != nil {
			return 0, err
		}

		cf.chaos.readFails.Add(1)

		return 0, pathError("read", cf.path, errno)
	}

	// Partial read: return a short read WITHOUT skipping bytes.
	// This must limit the underlying read, not just shrink the returned count,
	// otherwise the file offset advances too far and callers silently lose data.
	if cf.chaos.should(mode, cf.chaos.config.PartialReadRate) && len(p) > 1 {
		cf.chaos.partialReads.Add(1)

		cutoff := cf.chaos.randIntn(len(p)-1) + 1 // [1, len(p)-1]

		return cf.f.Read(p[:cutoff])
	}

	return cf.f.Read(p)
}

func (cf *chaosFile) Write(p []byte) (int, error) {
	mode := ChaosMode(cf.chaos.mode.Load())
	if mode == ChaosModePassthrough {
		return cf.f.Write(p)
	}

	state := cf.chaos.getState(cf.path)

	if state == PathIOError {
		cf.chaos.writeFails.Add(1)

		return 0, pathError("write", cf.path, syscall.EIO)
	}

	if state == PathReadOnly {
		cf.chaos.writeFails.Add(1)

		return 0, pathError("write", cf.path, syscall.EROFS)
	}

	if cf.chaos.should(mode, cf.chaos.config.WriteFailRate) {
		errno, err := cf.chaos.pickError("write", cf.path)
		if err != nil {
			return 0, err
		}

		cf.chaos.writeFails.Add(1)

		return 0, pathError("write", cf.path, errno)
	}

	// Partial write
	if cf.chaos.should(mode, cf.chaos.config.PartialWriteRate) && len(p) > 1 {
		cf.chaos.partialWrites.Add(1)

		n := len(p) / 2

		wrote, err := cf.f.Write(p[:n])
		if err != nil {
			return wrote, err
		}

		errno, err := cf.chaos.pickError("write", cf.path)
		if err != nil {
			return wrote, err
		}

		return wrote, pathError("write", cf.path, errno)
	}

	return cf.f.Write(p)
}

func (cf *chaosFile) Close() error {
	return cf.f.Close()
}
func (cf *chaosFile) Seek(offset int64, whence int) (int64, error) {
	return cf.f.Seek(offset, whence)
}
func (cf *chaosFile) Fd() uintptr {
	return cf.f.Fd()
}
func (cf *chaosFile) Stat() (os.FileInfo, error) {
	return cf.f.Stat()
}
func (cf *chaosFile) Sync() error {
	return cf.f.Sync()
}

// Compile-time interface checks.
var _ FS = (*Chaos)(nil)
var _ File = (*chaosFile)(nil)
