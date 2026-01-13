package fs

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// TempDirer is the minimal subset of *testing.T/*testing.B that [NewCrash] needs.
//
// It is intentionally tiny so crashfs can remain in non-test packages without
// importing the standard library testing package.
//
// In tests, pass *testing.T (or anything else that implements TempDir()).
// Outside of tests, use a small shim that provides a stable directory.
type TempDirer interface {
	// TempDir returns a temporary directory path.
	TempDir() string
}

// ErrCrashFS marks errors originating from crashfs internals.
//
// Use [errors.Is] with this sentinel to detect crashfs-generated errors.
var ErrCrashFS = errors.New("crashfs")

type crashFSError struct {
	op  string
	err error
}

func (e *crashFSError) Error() string {
	return fmt.Sprintf("crashfs: %s: %v", e.op, e.err)
}

func (e *crashFSError) Unwrap() error { return e.err }

func (*crashFSError) Is(target error) bool { return target == ErrCrashFS }

// CrashFSErr wraps a crashfs-internal error with a consistent prefix.
//
// op must be a static, verb-first description of the action being attempted
// (for example, "read inode key"). Avoid dynamic values in op; include them in
// err instead (for example, fmt.Errorf("path %q: %w", path, err)).
//
// It panics if err is nil.
func CrashFSErr(op string, err error) error {
	if err == nil {
		panic(fmt.Sprintf("crashfs: internal error: nil error for %q", op))
	}

	return &crashFSError{op: op, err: err}
}

// Crash is a test-only filesystem wrapper that simulates crash consistency.
//
// Crash implements [FS] and can be passed anywhere an [FS] is expected.
//
// Crash runs operations against a real on-disk working directory (so returned
// [File] values have real OS file descriptors), while tracking an in-memory
// durable snapshot.
//
// Calling [Crash.SimulateCrash] rotates to a fresh empty working directory and
// restores only the durable snapshot, simulating a process crash/power loss.
//
// Durability model (strict, pessimistic):
//   - File contents become durable only when [File.Sync] succeeds on that handle.
//   - Directory entries become durable only when [File.Sync] succeeds on an open
//     directory handle for the containing directory.
//
// Optional writeback ([CrashConfig.Writeback]) can retain some unsynced changes
// at crash time.
//
// Typical usage:
//
//	real := fs.NewReal()
//	crash, _ := fs.NewCrash(t, real, fs.CrashConfig{})
//
//	// Run code under test using crash as an fs.FS.
//	// ...
//
//	_ = crash.SimulateCrash()
//	// Assert using crash.ReadFile/Exists in the post-crash view.
//
// Crash is not meant for production use.
type Crash struct {
	baseDir string
	fs      FS

	mu   sync.Mutex
	live string
	open map[*crashFile]struct{}

	// Durable snapshot (directory entries + file contents).
	nextID          objID
	kind            map[objID]objKind
	durableChildren map[objID]map[string]objID
	durableFiles    map[objID]fileSnapshot

	// Live namespace tracking.
	liveChildren map[objID]map[string]objID

	// Crash injection state.
	failpoint    crashFailpoint
	latched      bool
	latchedPanic *CrashPanicError

	writeback *crashWriteback

	config CrashConfig
}

// CrashConfig controls [Crash] behavior.
//
// The zero value is usable.
type CrashConfig struct {
	// Failpoint configures optional crash injection.
	Failpoint CrashFailpointConfig

	// Writeback configures optional crash writeback behavior.
	Writeback CrashWritebackConfig
}

// NewCrash creates a new crash-simulating filesystem.
//
// tb is typically a *testing.T and is used only to obtain an owned temporary
// directory.
//
// fs is used for the operations performed by the code under test and should be
// OS-backed. In practice this should be [NewReal].
func NewCrash(tb TempDirer, fs FS, config *CrashConfig) (*Crash, error) {
	if tb == nil {
		return nil, errors.New("crashfs: tb is nil")
	}

	if fs == nil {
		return nil, errors.New("crashfs: fs is nil")
	}

	baseDir := tb.TempDir()
	if baseDir == "" {
		return nil, errors.New("crashfs: temp dir is empty")
	}

	crash := &Crash{
		baseDir: baseDir,
		fs:      fs,
		open:    make(map[*crashFile]struct{}),

		nextID:          rootID + 1,
		kind:            map[objID]objKind{rootID: objDir},
		durableChildren: map[objID]map[string]objID{rootID: {}},
		durableFiles:    make(map[objID]fileSnapshot),
		liveChildren:    map[objID]map[string]objID{rootID: {}},

		config: *config,
	}

	fp, err := newCrashFailpoint(crash, &config.Failpoint)
	if err != nil {
		return nil, err
	}

	if fp != nil {
		crash.failpoint = *fp
	}

	wb, err := newCrashWriteback(config.Writeback)
	if err != nil {
		return nil, err
	}

	crash.writeback = wb

	crash.mu.Lock()
	err = crash.rotateLocked(true)
	crash.mu.Unlock()

	if err != nil {
		return nil, err
	}

	return crash, nil
}

// Recover resets [Crash] after an injected crash.
//
// Call this after recovering a simulated crash panic (in panic mode) before making
// assertions via [Crash]'s filesystem methods.
func (c *Crash) Recover() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.latched = false
	c.latchedPanic = nil
}

// SimulateCrash simulates a crash/power loss.
//
// It closes all open files, rotates to a fresh empty working directory, and restores
// the durable snapshot.
func (c *Crash) SimulateCrash() error {
	var (
		panicVal *CrashPanicError
		exitCode int
	)

	c.mu.Lock()

	if c.latched {
		panicVal, exitCode = c.latchedTerminationLocked(crashOpCrash, "", "")
		c.mu.Unlock()
		terminateCrash(panicVal, exitCode)

		return nil
	}

	var err error
	if c.writeback == nil {
		err = c.rotateLocked(false)
	} else {
		err = c.simulateWritebackLocked()
	}

	c.mu.Unlock()

	return err
}

var _ FS = (*Crash)(nil)

// Open implements [FS.Open].
func (c *Crash) Open(path string) (File, error) {
	return c.openWith(path, CrashOpOpen, c.fs.Open, false)
}

// Create implements [FS.Create].
func (c *Crash) Create(path string) (File, error) {
	return c.openWith(path, CrashOpCreate, c.fs.Create, true)
}

// OpenFile implements [FS.OpenFile].
func (c *Crash) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	op := CrashOpOpen
	createIfMissing := false

	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_TRUNC) != 0 {
		op = CrashOpCreate
	}

	if flag&os.O_CREATE != 0 {
		createIfMissing = true
	}

	return c.openWith(path, op, func(abs string) (File, error) {
		return c.fs.OpenFile(abs, flag, perm)
	}, createIfMissing)
}

// ReadFile implements [FS.ReadFile].
func (c *Crash) ReadFile(path string) ([]byte, error) {
	guardErr := c.guard(CrashOpReadFile, path, "", false)
	if guardErr != nil {
		return nil, guardErr
	}

	abs, err := c.resolveAbs(path)
	if err != nil {
		return nil, err
	}

	return c.fs.ReadFile(abs)
}

// WriteFile implements [FS.WriteFile].
func (c *Crash) WriteFile(path string, data []byte, perm os.FileMode) error {
	guardErr := c.guard(CrashOpWriteFile, path, "", false)
	if guardErr != nil {
		return guardErr
	}

	res, err := c.resolveWithLive(path)
	if err != nil {
		return err
	}

	writeErr := c.fs.WriteFile(res.abs, data, perm)
	if writeErr != nil {
		return writeErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		return nil
	}

	_, _ = c.liveAddFileLocked(res.rel)

	return nil
}

// ReadDir implements [FS.ReadDir].
func (c *Crash) ReadDir(path string) ([]os.DirEntry, error) {
	err := c.guard(CrashOpReadDir, path, "", false)
	if err != nil {
		return nil, err
	}

	abs, err := c.resolveAbs(path)
	if err != nil {
		return nil, err
	}

	return c.fs.ReadDir(abs)
}

// MkdirAll implements [FS.MkdirAll].
func (c *Crash) MkdirAll(path string, perm os.FileMode) error {
	err := c.guard(CrashOpMkdirAll, path, "", false)
	if err != nil {
		return err
	}

	res, err := c.resolveWithLive(path)
	if err != nil {
		return err
	}

	mkdirErr := c.fs.MkdirAll(res.abs, perm)
	if mkdirErr != nil {
		return mkdirErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		return nil
	}

	_, err = c.liveEnsureDirPathLocked(res.rel)

	return err
}

// Stat implements [FS.Stat].
func (c *Crash) Stat(path string) (os.FileInfo, error) {
	err := c.guard(CrashOpStat, path, "", false)
	if err != nil {
		return nil, err
	}

	abs, err := c.resolveAbs(path)
	if err != nil {
		return nil, err
	}

	return c.fs.Stat(abs)
}

// Exists implements [FS.Exists].
func (c *Crash) Exists(path string) (bool, error) {
	err := c.guard(CrashOpExists, path, "", false)
	if err != nil {
		return false, err
	}

	abs, err := c.resolveAbs(path)
	if err != nil {
		return false, err
	}

	return c.fs.Exists(abs)
}

// Remove implements [FS.Remove].
func (c *Crash) Remove(path string) error {
	err := c.guard(CrashOpRemove, path, "", false)
	if err != nil {
		return err
	}

	res, err := c.resolveWithLive(path)
	if err != nil {
		return err
	}

	err = c.fs.Remove(res.abs)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		return nil
	}

	c.liveRemoveEntryLocked(res.rel)

	return nil
}

// RemoveAll implements [FS.RemoveAll].
func (c *Crash) RemoveAll(path string) error {
	err := c.guard(CrashOpRemoveAll, path, "", false)
	if err != nil {
		return err
	}

	res, err := c.resolveWithLive(path)
	if err != nil {
		return err
	}

	err = c.fs.RemoveAll(res.abs)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		return nil
	}

	c.liveRemoveEntryLocked(res.rel)

	return nil
}

// Rename implements [FS.Rename].
func (c *Crash) Rename(oldpath, newpath string) error {
	err := c.guard(CrashOpRename, oldpath, newpath, false)
	if err != nil {
		return err
	}

	res, err := c.resolvePairWithLive(oldpath, newpath)
	if err != nil {
		return err
	}

	renameErr := c.fs.Rename(res.oldAbs, res.newAbs)
	if renameErr != nil {
		return renameErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		return nil
	}

	if res.oldRel == "" || res.newRel == "" {
		return CrashFSErr("rename", errors.New("cannot rename crashfs root"))
	}

	oldParentRel := parentRel(res.oldRel)
	newParentRel := parentRel(res.newRel)
	oldBase := filepath.Base(res.oldRel)
	newBase := filepath.Base(res.newRel)

	oldParentID, err := c.liveDirIDLocked(oldParentRel)
	if err != nil {
		return nil
	}

	newParentID, err := c.liveDirIDLocked(newParentRel)
	if err != nil {
		return nil
	}

	movedID, ok := c.liveChildren[oldParentID][oldBase]
	if !ok {
		return nil
	}

	delete(c.liveChildren[oldParentID], oldBase)
	delete(c.liveChildren[newParentID], newBase)
	c.liveChildren[newParentID][newBase] = movedID

	return nil
}

func (c *Crash) latchedTerminationLocked(op CrashOp, path, newPath string) (*CrashPanicError, int) {
	if c.latchedPanic != nil {
		if c.failpoint.action == CrashFailpointExit {
			return nil, c.failpoint.exitCode
		}

		return c.latchedPanic, 0
	}

	panicVal := &CrashPanicError{Op: op, Path: path, NewPath: newPath}
	if op == crashOpCrash {
		panicVal.Path = ""
		panicVal.NewPath = ""
	}

	return panicVal, 0
}

// guard checks whether [Crash] should inject a crash at this operation.
//
// It is called at the start of FS and [File] methods. If newPath is non-empty,
// guard matches filters against both paths and records the rename in [CrashPanicError].
//
// If [Crash] is in a latched crash state, guard re-triggers the crash termination
// until the test harness calls [Crash.Recover].
func (c *Crash) guard(op CrashOp, pathOrRel, newPath string, alreadyRel bool) error {
	var (
		panicVal *CrashPanicError
		exitCode int
	)

	c.mu.Lock()

	defer func() {
		c.mu.Unlock()
		terminateCrash(panicVal, exitCode)
	}()

	if c.latched {
		panicVal, exitCode = c.latchedTerminationLocked(op, pathOrRel, newPath)

		return nil
	}

	if !c.failpoint.armed {
		return nil
	}

	rel := pathOrRel

	if !alreadyRel {
		var err error

		rel, err = c.virtualRel(pathOrRel)
		if err != nil {
			return nil
		}
	}

	newRel := ""

	if newPath != "" {
		var err error

		newRel, err = c.virtualRel(newPath)
		if err != nil {
			return nil
		}
	}

	if !c.failpoint.eligible(op, rel, newRel) {
		return nil
	}

	if !c.failpoint.shouldTrigger() {
		return nil
	}

	// Disable future injections to keep assertions stable.
	c.failpoint.armed = false

	rotateErr := c.rotateLocked(false)

	c.latched = true

	crashPanic := &CrashPanicError{Op: op, Path: pathOrRel, Rel: rel, Seq: c.failpoint.count, Cause: rotateErr}
	if newPath != "" {
		crashPanic.NewPath = newPath
		crashPanic.NewRel = newRel
	}

	c.latchedPanic = crashPanic

	switch c.failpoint.action {
	case CrashFailpointExit:
		exitCode = c.failpoint.exitCode
	default:
		panicVal = crashPanic
	}

	return nil
}

func (c *Crash) openWith(path string, op CrashOp, openFn func(string) (File, error), createIfMissing bool) (File, error) {
	err := c.guard(op, path, "", false)
	if err != nil {
		return nil, err
	}

	res, err := c.resolveWithLive(path)
	if err != nil {
		return nil, err
	}

	file, err := openFn(res.abs)
	if err != nil {
		return nil, err
	}

	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()

		return nil, statErr
	}

	isDir := info.IsDir()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != res.live {
		_ = file.Close()

		return nil, CrashFSErr("open", errors.New("crashfs rotated during open"))
	}

	id, k, ok := c.liveLookupLocked(res.rel)
	if ok {
		if (k == objDir) != isDir {
			_ = file.Close()

			return nil, CrashFSErr("open", fmt.Errorf("type mismatch for %q", res.rel))
		}
	} else {
		switch {
		case res.rel == "":
			id = rootID
		case createIfMissing:
			if isDir {
				_ = file.Close()

				return nil, CrashFSErr("open", fmt.Errorf("unexpected directory creation at %q", res.rel))
			}

			newID, addErr := c.liveAddFileLocked(res.rel)
			if addErr != nil {
				_ = file.Close()

				return nil, addErr
			}

			id = newID
		default:
			_ = file.Close()

			return nil, CrashFSErr("open", fmt.Errorf("untracked path %q (out-of-band mutation?)", res.rel))
		}
	}

	cf := &crashFile{c: c, f: file, rel: res.rel, live: res.live, id: id, isDir: isDir}
	c.open[cf] = struct{}{}

	return cf, nil
}

// copyChildren makes a defensive copy of a directory's child map.
func copyChildren(in map[string]objID) map[string]objID {
	if len(in) == 0 {
		return map[string]objID{}
	}

	out := make(map[string]objID, len(in))
	maps.Copy(out, in)

	return out
}

// sortedChildNames returns the directory entry names in deterministic order.
func sortedChildNames(children map[string]objID) []string {
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
