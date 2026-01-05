package fs

import (
	"bytes"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestChaos_ErrorInjectionDoesNotDeadlock(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		WriteFailRate: 1.0, // Always inject an error (exercise pickError/pickRandom).
	})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	done := make(chan error, 1)

	go func() {
		done <- chaosFS.WriteFileAtomic(path, []byte("x"), 0o644)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("WriteFileAtomic unexpectedly succeeded")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("WriteFileAtomic hung (possible deadlock in chaos error injection)")
	}
}

// TestChaosFile_PartialReadDoesNotSkipBytes verifies that partial reads don't
// corrupt data. When ChaosFS truncates a read (returning fewer bytes than
// requested), the file offset must advance only by the bytes actually returned,
// not the bytes requested. A buggy implementation that advances by the request
// size would skip bytes, causing io.ReadAll to return incomplete data.
func TestChaosFile_PartialReadDoesNotSkipBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 200) // > io.ReadAll initial buffer

	realFS := NewReal()
	if err := realFS.WriteFileAtomic(path, content, 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		PartialReadRate: 1.0, // Always partial.
	})
	chaosFS.SetMode(ChaosModeInject)

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("partial reads must not drop bytes: got=%d bytes, want=%d", len(got), len(content))
	}
}

func TestInjectedErrors_PreserveOsErrorClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "path")

	cases := []struct {
		name  string
		errno syscall.Errno
	}{
		{name: "ENOENT", errno: syscall.ENOENT},
		{name: "EACCES", errno: syscall.EACCES},
		{name: "EPERM", errno: syscall.EPERM},
		{name: "EROFS", errno: syscall.EROFS},
		{name: "EIO", errno: syscall.EIO},
		{name: "ENOSPC", errno: syscall.ENOSPC},
	}

	classifiers := []struct {
		name string
		fn   func(error) bool
	}{
		{name: "os.IsNotExist", fn: os.IsNotExist},
		{name: "os.IsPermission", fn: os.IsPermission},
		{name: "os.IsExist", fn: os.IsExist},
		{name: "os.IsTimeout", fn: os.IsTimeout},
	}

	targets := []struct {
		name string
		err  error
	}{
		{name: "io/fs.ErrNotExist", err: iofs.ErrNotExist},
		{name: "io/fs.ErrPermission", err: iofs.ErrPermission},
		{name: "io/fs.ErrExist", err: iofs.ErrExist},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &iofs.PathError{Op: "op", Path: path, Err: tc.errno}
			injected := pathError("op", path, tc.errno)

			if got, want := IsInjected(base), false; got != want {
				t.Fatalf("IsInjected(base)=%t, want %t", got, want)
			}

			if got, want := IsInjected(injected), true; got != want {
				t.Fatalf("IsInjected(injected)=%t, want %t", got, want)
			}

			var pathErr *os.PathError
			if got, want := errors.As(injected, &pathErr), true; got != want {
				t.Fatalf("errors.As(injected, *os.PathError)=%t, want %t (got %T)", got, want, injected)
			}

			if got, want := pathErr.Op, "op"; got != want {
				t.Fatalf("PathError.Op=%q, want %q", got, want)
			}

			if got, want := pathErr.Path, path; got != want {
				t.Fatalf("PathError.Path=%q, want %q", got, want)
			}

			// Our injection marker must not break stdlib error classification helpers.
			for _, c := range classifiers {
				if got, want := c.fn(injected), c.fn(base); got != want {
					t.Fatalf("%s(injected)=%t, want %t (base=%v injected=%v)", c.name, got, want, base, injected)
				}
			}

			// errors.Is should behave the same as a plain *fs.PathError with the errno.
			if got, want := errors.Is(injected, tc.errno), errors.Is(base, tc.errno); got != want {
				t.Fatalf("errors.Is(err, %s)=%t, want %t (base=%v injected=%v)", tc.name, got, want, base, injected)
			}

			for _, target := range targets {
				if got, want := errors.Is(injected, target.err), errors.Is(base, target.err); got != want {
					t.Fatalf("errors.Is(injected, %s)=%t, want %t (base=%v injected=%v)", target.name, got, want, base, injected)
				}
			}
		})
	}
}

func TestInjectedError_PreservesOsIsTimeout(t *testing.T) {
	base := os.ErrDeadlineExceeded
	injected := inject(base)

	if got, want := IsInjected(injected), true; got != want {
		t.Fatalf("IsInjected(injected)=%t, want %t", got, want)
	}

	if got, want := IsInjected(base), false; got != want {
		t.Fatalf("IsInjected(base)=%t, want %t", got, want)
	}

	if got, want := os.IsTimeout(injected), os.IsTimeout(base); got != want {
		t.Fatalf("os.IsTimeout(injected)=%t, want %t", got, want)
	}

	if got, want := errors.Is(injected, os.ErrDeadlineExceeded), true; got != want {
		t.Fatalf("errors.Is(injected, os.ErrDeadlineExceeded)=%t, want %t", got, want)
	}
}

func TestChaos_RemoveAll_NonExistentMatchesOsRemoveAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	// Real os.RemoveAll treats a missing path as success.
	err := os.RemoveAll(path)
	if err != nil {
		t.Fatalf("os.RemoveAll: %v", err)
	}

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		RemoveFailRate: 1.0, // Would inject if allowed.
	})
	chaosFS.SetMode(ChaosModeInject)

	err = chaosFS.RemoveAll(path)
	if err != nil {
		t.Fatalf("Chaos.RemoveAll: %v", err)
	}
}
