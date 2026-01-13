package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type crashFile struct {
	c     *Crash
	f     File
	rel   string
	live  string
	id    objID
	isDir bool

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

var _ File = (*crashFile)(nil)

func (cf *crashFile) Read(buf []byte) (int, error) {
	err := cf.c.guard(CrashOpFileRead, cf.rel, "", true)
	if err != nil {
		return 0, err
	}

	return cf.f.Read(buf)
}

func (cf *crashFile) Write(buf []byte) (int, error) {
	err := cf.c.guard(CrashOpFileWrite, cf.rel, "", true)
	if err != nil {
		return 0, err
	}

	return cf.f.Write(buf)
}

func (cf *crashFile) Seek(offset int64, whence int) (int64, error) {
	err := cf.c.guard(CrashOpFileSeek, cf.rel, "", true)
	if err != nil {
		return 0, err
	}

	return cf.f.Seek(offset, whence)
}

func (cf *crashFile) Fd() uintptr { return cf.f.Fd() }

func (cf *crashFile) Stat() (os.FileInfo, error) {
	err := cf.c.guard(CrashOpFileStat, cf.rel, "", true)
	if err != nil {
		return nil, err
	}

	return cf.f.Stat()
}

func (cf *crashFile) Chmod(mode os.FileMode) error {
	err := cf.c.guard(CrashOpFileChmod, cf.rel, "", true)
	if err != nil {
		return err
	}

	return cf.f.Chmod(mode)
}

// Sync records durability after the underlying file Sync succeeds.
//
// For directories, it snapshots the directory's current live entries.
// For regular files, it reads the full contents from the file descriptor.
// Handles from older work dirs are ignored to avoid recording stale state after a crash.
func (cf *crashFile) Sync() error {
	err := cf.c.guard(CrashOpFileSync, cf.rel, "", true)
	if err != nil {
		return err
	}

	err = cf.f.Sync()
	if err != nil {
		return err
	}

	info, err := cf.f.Stat()
	if err != nil {
		return err
	}

	cf.c.mu.Lock()
	defer cf.c.mu.Unlock()

	if cf.c.live != cf.live {
		return nil
	}

	if info.IsDir() {
		if !cf.c.dirReachableLocked(cf.id) {
			return nil
		}

		cf.c.durableChildren[cf.id] = copyChildren(cf.c.liveChildren[cf.id])

		return nil
	}

	if rel, ok := cf.c.findLivePathLocked(cf.id); ok {
		abs := filepath.Join(cf.c.live, rel)

		data, readErr := os.ReadFile(abs)
		if readErr == nil {
			cf.c.durableFiles[cf.id] = fileSnapshot{data: data, perm: info.Mode().Perm()}

			return nil
		}
	}

	data, err := readAllFromFD(cf.f.Fd(), info.Size())
	if err != nil {
		return CrashFSErr("snapshot file", fmt.Errorf("path %q: %w", cf.rel, err))
	}

	cf.c.durableFiles[cf.id] = fileSnapshot{data: data, perm: info.Mode().Perm()}

	return nil
}

func (cf *crashFile) Close() error {
	cf.mu.Lock()

	if cf.closed {
		cf.mu.Unlock()

		return nil
	}

	cf.mu.Unlock()

	err := cf.c.guard(CrashOpFileClose, cf.rel, "", true)
	if err != nil {
		_ = cf.closeUnderlying()

		return err
	}

	cf.mu.Lock()

	if cf.closed {
		cf.mu.Unlock()

		return nil
	}

	cf.closed = true
	cf.mu.Unlock()

	err = cf.closeUnderlying()

	cf.c.mu.Lock()
	delete(cf.c.open, cf)
	cf.c.mu.Unlock()

	return err
}

func (cf *crashFile) closeUnderlying() error {
	cf.closeOnce.Do(func() {
		cf.closeErr = cf.f.Close()
	})

	cf.mu.Lock()
	cf.closed = true
	cf.mu.Unlock()

	return cf.closeErr
}

func (c *Crash) dirReachableLocked(target objID) bool {
	if target == rootID {
		return true
	}

	var (
		found bool
		walk  func(dirID objID)
	)

	walk = func(dirID objID) {
		if found {
			return
		}

		children := c.liveChildren[dirID]
		for _, child := range children {
			if child == target {
				found = true

				return
			}

			if c.kind[child] == objDir {
				walk(child)
			}
		}
	}
	walk(rootID)

	return found
}

// findLivePathLocked finds a root-relative live path that currently refers to target.
//
// This walks the liveChildren tree deterministically (sorted by name) and returns the
// first matching path.
//
// Callers must hold [Crash.mu].
func (c *Crash) findLivePathLocked(target objID) (string, bool) {
	if target == rootID {
		return "", true
	}

	found := ""
	ok := false

	var walk func(dirID objID, prefix string)

	walk = func(dirID objID, prefix string) {
		if ok {
			return
		}

		children := c.liveChildren[dirID]
		for _, name := range sortedChildNames(children) {
			childID := children[name]

			var rel string
			if prefix != "" {
				rel = filepath.Join(prefix, name)
			} else {
				rel = name
			}

			if childID == target {
				found = rel
				ok = true

				return
			}

			if c.kind[childID] == objDir {
				walk(childID, rel)

				if ok {
					return
				}
			}
		}
	}

	walk(rootID, "")

	return found, ok
}

func readAllFromFD(fd uintptr, size int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}

	if size > int64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("file too large (%d bytes)", size)
	}

	buf := make([]byte, int(size))

	read := 0
	for read < len(buf) {
		bytesRead, err := syscall.Pread(int(fd), buf[read:], int64(read))
		if bytesRead > 0 {
			read += bytesRead
		}

		if err != nil {
			return nil, err
		}

		if bytesRead == 0 {
			break
		}
	}

	return buf[:read], nil
}

func parentRel(path string) string {
	if path == "" {
		return ""
	}

	parent := filepath.Dir(path)
	if parent == "." {
		return ""
	}

	return parent
}

func (c *Crash) rejectSymlinksInLiveTreeLocked() error {
	err := filepath.WalkDir(c.live, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return CrashFSErr("walk live paths", err)
		}

		if path == c.live {
			return nil
		}

		rel, err := filepath.Rel(c.live, path)
		if err != nil {
			return CrashFSErr("resolve live path", err)
		}

		rel = filepath.Clean(rel)
		if rel == "." {
			return nil
		}

		info, err := os.Lstat(path)
		if err != nil {
			return CrashFSErr("read live entry", fmt.Errorf("path %q: %w", rel, err))
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return CrashFSErr("read live entry", fmt.Errorf("unsupported symlink at %q", rel))
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, ErrCrashFS) {
			return err
		}

		return CrashFSErr("walk live paths", err)
	}

	return nil
}
