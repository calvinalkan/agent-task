package ticket_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"tk/internal/ticket"
)

// errTestCallback is used for testing error handling in callbacks.
var errTestCallback = errors.New("test callback error")

func TestWithTicketLock_BasicOperation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	initialContent := []byte("hello world")

	writeErr := os.WriteFile(path, initialContent, 0o600)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test read and modify
	lockErr := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		if !bytes.Equal(content, initialContent) {
			t.Errorf("expected content %q, got %q", initialContent, content)
		}

		return []byte("modified content"), nil
	})
	if lockErr != nil {
		t.Fatalf("WithTicketLock failed: %v", lockErr)
	}

	// Verify the file was modified
	result, readErr := os.ReadFile(path)
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

	writeErr := os.WriteFile(path, initialContent, 0o600)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test read-only (return nil to skip write)
	var readContent []byte

	lockErr := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
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
	result, readErr := os.ReadFile(path)
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

	writeErr := os.WriteFile(path, initialContent, 0o600)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Test error in callback - should not write
	lockErr := ticket.WithTicketLock(path, func(_ []byte) ([]byte, error) {
		return []byte("should not be written"), errTestCallback
	})

	if !errors.Is(lockErr, errTestCallback) {
		t.Errorf("expected test callback error, got %v", lockErr)
	}

	// Verify file unchanged
	result, readErr := os.ReadFile(path)
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
	writeErr := os.WriteFile(path, []byte("0"), 0o600)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// Run concurrent increments
	const numGoroutines = 10

	const incrementsPerGoroutine = 10

	var waitGroup sync.WaitGroup

	for range numGoroutines {
		waitGroup.Go(func() {
			for range incrementsPerGoroutine {
				lockErr := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
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
		})
	}

	waitGroup.Wait()

	// Verify final value
	result, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("failed to read file: %v", readErr)
	}

	finalVal, _ := strconv.Atoi(string(result))

	expected := numGoroutines * incrementsPerGoroutine
	if finalVal != expected {
		t.Errorf("expected %d, got %d (lost updates!)", expected, finalVal)
	}
}

func TestWithTicketLock_LockReleasedAfterError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.md")

	// Create test file
	writeErr := os.WriteFile(path, []byte("test"), 0o600)
	if writeErr != nil {
		t.Fatalf("failed to create test file: %v", writeErr)
	}

	// First call with error
	_ = ticket.WithTicketLock(path, func(_ []byte) ([]byte, error) {
		return nil, errTestCallback
	})

	// Second call should succeed (lock was released)
	lockErr := ticket.WithTicketLock(path, func(_ []byte) ([]byte, error) {
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

	lockErr := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		return content, nil
	})
	if lockErr == nil {
		t.Error("expected error for nonexistent file")
	}
}
