package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errTestCallback is used for testing error handling in callbacks.
var errTestCallback = errors.New("test callback error")

func TestWithTicketLock_BasicOperation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	initialContent := []byte("hello world")

	writeErr := os.WriteFile(path, initialContent, filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test read and modify
	lockErr := WithTicketLock(path, func(content []byte) ([]byte, error) {
		if !bytes.Equal(content, initialContent) {
			t.Errorf("expected content %q, got %q", initialContent, content)
		}

		return []byte("modified content"), nil
	})
	if lockErr != nil {
		t.Fatalf("WithTicketLock failed: %v", lockErr)
	}

	// Verify the file was modified
	result, readErr := os.ReadFile(path) //nolint:gosec // test file
	if readErr != nil {
		t.Fatalf("failed to read file: %v", readErr)
	}

	if string(result) != "modified content" {
		t.Errorf("expected 'modified content', got %q", result)
	}
}

func TestWithTicketLock_ReadOnlyOperation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	initialContent := []byte("original content")

	writeErr := os.WriteFile(path, initialContent, filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test read-only (return nil to skip write)
	var readContent []byte

	lockErr := WithTicketLock(path, func(content []byte) ([]byte, error) {
		readContent = content

		return nil, nil // nil content = no write
	})
	if lockErr != nil {
		t.Fatalf("WithTicketLock failed: %v", lockErr)
	}

	if !bytes.Equal(readContent, initialContent) {
		t.Errorf("expected %q, got %q", initialContent, readContent)
	}

	// Verify file unchanged
	result, readErr := os.ReadFile(path) //nolint:gosec // test file
	if readErr != nil {
		t.Fatalf("failed to read file: %v", readErr)
	}

	if !bytes.Equal(result, initialContent) {
		t.Errorf("file should be unchanged, got %q", result)
	}
}

func TestWithTicketLock_ErrorInCallback(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	initialContent := []byte("original content")

	writeErr := os.WriteFile(path, initialContent, filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test error in callback - should not write
	lockErr := WithTicketLock(path, func(_ []byte) ([]byte, error) {
		return []byte("should not be written"), errTestCallback
	})

	if !errors.Is(lockErr, errTestCallback) {
		t.Errorf("expected test callback error, got %v", lockErr)
	}

	// Verify file unchanged
	result, readErr := os.ReadFile(path) //nolint:gosec // test file
	if readErr != nil {
		t.Fatalf("failed to read file: %v", readErr)
	}

	if !bytes.Equal(result, initialContent) {
		t.Errorf("file should be unchanged after error, got %q", result)
	}
}

func TestWithTicketLock_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file with counter
	writeErr := os.WriteFile(path, []byte("0"), filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Run concurrent increments
	const numGoroutines = 10

	const incrementsPerGoroutine = 10

	var waitGroup sync.WaitGroup

	for range numGoroutines {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			for range incrementsPerGoroutine {
				lockErr := WithTicketLock(path, func(content []byte) ([]byte, error) {
					// Parse current value
					val, _ := strconv.Atoi(string(content))

					// Increment
					val++

					// Return new value
					return []byte(strconv.Itoa(val)), nil
				})
				if lockErr != nil {
					t.Errorf("concurrent WithTicketLock failed: %v", lockErr)
				}
			}
		}()
	}

	waitGroup.Wait()

	// Verify final value
	result, readErr := os.ReadFile(path) //nolint:gosec // test file
	if readErr != nil {
		t.Fatalf("failed to read file: %v", readErr)
	}

	finalVal, _ := strconv.Atoi(string(result))

	expected := numGoroutines * incrementsPerGoroutine
	if finalVal != expected {
		t.Errorf("expected %d, got %d (lost updates!)", expected, finalVal)
	}
}

func TestAcquireLockWithTimeout_Timeout(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	writeErr := os.WriteFile(path, []byte("test"), filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Acquire lock in goroutine and hold it
	lockAcquired := make(chan struct{})
	releaseLock := make(chan struct{})

	go func() {
		lock, acquireErr := acquireLock(path)
		if acquireErr != nil {
			t.Errorf("failed to acquire lock: %v", acquireErr)

			return
		}

		close(lockAcquired)
		<-releaseLock

		lock.release()
	}()

	<-lockAcquired

	// Try to acquire lock with short timeout - should fail
	_, lockErr := acquireLockWithTimeout(path, 50*time.Millisecond)
	if lockErr == nil {
		t.Error("expected timeout error, got nil")
	}

	if !errors.Is(lockErr, errLockTimeout) {
		t.Errorf("expected lock timeout error, got %v", lockErr)
	}

	close(releaseLock)
}

func TestWithTicketLock_LockReleasedAfterError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	writeErr := os.WriteFile(path, []byte("test"), filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// First call with error
	_ = WithTicketLock(path, func(_ []byte) ([]byte, error) {
		return nil, errTestCallback
	})

	// Second call should succeed (lock was released)
	lockErr := WithTicketLock(path, func(_ []byte) ([]byte, error) {
		return []byte("success"), nil
	})
	if lockErr != nil {
		t.Errorf("lock was not released after error: %v", lockErr)
	}
}

func TestWithTicketLock_FileNotExists(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.md")

	lockErr := WithTicketLock(path, func(content []byte) ([]byte, error) {
		return content, nil
	})
	if lockErr == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestAcquireLock_Concurrent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	writeErr := os.WriteFile(path, []byte("test"), filePerms)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Track which goroutine holds the lock
	var lockHolder atomic.Int32

	const numGoroutines = 5

	var waitGroup sync.WaitGroup

	for idx := range numGoroutines {
		waitGroup.Add(1)

		go func(goroutineID int) {
			defer waitGroup.Done()

			lock, acquireErr := acquireLock(path)
			if acquireErr != nil {
				t.Errorf("goroutine %d failed to acquire lock: %v", goroutineID, acquireErr)

				return
			}

			// Check that no other goroutine holds the lock
			if !lockHolder.CompareAndSwap(0, int32(goroutineID+1)) { //nolint:gosec // small test value
				t.Errorf("goroutine %d acquired lock while %d holds it", goroutineID, lockHolder.Load()-1)
			}

			// Hold the lock briefly
			time.Sleep(10 * time.Millisecond)

			lockHolder.Store(0)
			lock.release()
		}(idx)
	}

	waitGroup.Wait()
}
