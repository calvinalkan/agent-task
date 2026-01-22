package slotcache

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// Locking architecture
//
//  1. Cache.mu — per-handle closed state.
//
//  2. registryEntry.mu — per-file in-process guard:
//     - Readers hold RLock while touching the mmap.
//     - Writers (Commit/Invalidate) hold Lock while mutating the mmap.
//     - activeWriter is guarded here as well.
//     Needed because flock is per-process: multiple Cache handles in one
//     process would otherwise write concurrently.
//
//  3. interprocess writer lock — advisory lock file at Path+".lock",
//     used only by writers/Invalidate to exclude other processes.
//
//  4. seqlock generation — header counter that lets readers detect
//     overlapping writes and retry.
//
// Lock ordering: Cache.mu → registryEntry.mu → interprocess writer lock

// fileRegistry maps file identities to their per-file lock state.
var fileRegistry sync.Map // map[fileIdentity]*fileRegistryEntry

// lock is the package-level file locker for cross-process writer coordination.
// Uses fs.Real for production use with proper inode verification and EINTR handling.
var lock = fs.NewLocker(fs.NewReal())

// fileIdentity uniquely identifies a file by device and inode.
type fileIdentity struct {
	dev uint64
	ino uint64
}

// fileRegistryEntry tracks per-file state shared across all Cache handles
// backed by the same file (identified by device:inode pair).
//
// Multiple Cache instances may exist for the same file (e.g., opened multiple
// times in the same process). They share a single fileRegistryEntry to coordinate
// writer access and mmap visibility.
type fileRegistryEntry struct {
	// mu protects mmap reads vs writes across all Cache handles for this file.
	// Readers (Get, Scan, etc.) take RLock; writers (Commit) take Lock.
	// This ensures readers see consistent memory during seqlock reads.
	mu sync.RWMutex

	// activeWriter is the Cache instance that currently owns the active writer,
	// or nil if no writer is active. Used to:
	//   - Prevent multiple concurrent writers (BeginWrite checks activeWriter != nil)
	//   - Allow Cache.Close to check if it owns an uncommitted writer
	activeWriter *Cache

	// openCount tracks the number of open Cache handles for this file.
	// When it reaches zero, the entry is removed from fileRegistry.
	openCount atomic.Int32
}

// tryAquireWriteLock acquires an exclusive, non-blocking lock on the lock file.
// Returns the lock on success. On lock contention, returns ErrBusy.
func tryAquireWriteLock(cachePath string) (*fs.Lock, error) {
	lockPath := cachePath + ".lock"

	lock, err := lock.TryLock(lockPath)
	if err != nil {
		if errors.Is(err, fs.ErrWouldBlock) {
			return nil, ErrBusy
		}

		return nil, fmt.Errorf("acquire writer lock: %w", err)
	}

	return lock, nil
}

// releaseWriteLock releases the lock. Safe to call with nil.
// Does NOT delete the lock file (per spec: lock file persists).
func releaseWriteLock(lock *fs.Lock) {
	if lock == nil {
		return
	}

	_ = lock.Close()
}

// getFileIdentity returns the device and inode for a file.
func getFileIdentity(fd int) (fileIdentity, error) {
	var stat syscall.Stat_t

	err := syscall.Fstat(fd, &stat)
	if err != nil {
		return fileIdentity{}, fmt.Errorf("stat: %w", err)
	}

	return fileIdentity{dev: stat.Dev, ino: stat.Ino}, nil
}

// getOrCreateRegistryEntry gets or creates a fileRegistryEntry for the given identity,
// incrementing its open count. Callers must call releaseRegistryEntry when done.
func getOrCreateRegistryEntry(id fileIdentity) *fileRegistryEntry {
	for {
		if val, loaded := fileRegistry.Load(id); loaded {
			entry, ok := val.(*fileRegistryEntry)
			if !ok {
				fileRegistry.CompareAndDelete(id, val)

				continue
			}

			// Try to increment openCount. If it's 0, the entry is being removed.
			for {
				old := entry.openCount.Load()
				if old <= 0 {
					// Entry is being removed, try to create a new one.
					break
				}

				if entry.openCount.CompareAndSwap(old, old+1) {
					return entry
				}
			}
		}

		// Create new entry with openCount = 1.
		entry := &fileRegistryEntry{}
		entry.openCount.Store(1)

		_, loaded := fileRegistry.LoadOrStore(id, entry)
		if !loaded {
			// We stored our new entry.
			return entry
		}

		// Another goroutine created the entry first, retry the loop.
	}
}

// releaseRegistryEntry decrements the open count for a fileRegistryEntry
// and removes it from fileRegistry when the count reaches zero.
func releaseRegistryEntry(id fileIdentity) {
	val, ok := fileRegistry.Load(id)
	if !ok {
		return
	}

	entry, ok := val.(*fileRegistryEntry)
	if !ok {
		fileRegistry.CompareAndDelete(id, val)

		return
	}

	if entry.openCount.Add(-1) <= 0 {
		// We're the last reference, remove from fileRegistry.
		// Use CompareAndDelete to avoid race with concurrent getOrCreateRegistryEntry.
		fileRegistry.CompareAndDelete(id, entry)
	}
}
