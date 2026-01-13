package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// captureTempDir wraps a testing.T to capture the temp directory path.
type captureTempDir struct {
	t   *testing.T
	dir string
}

func (c *captureTempDir) TempDir() string {
	if c.dir == "" {
		c.dir = c.t.TempDir()
	}

	return c.dir
}

// mustFindLiveDir finds the crashfs-* working directory inside the captured temp dir.
func mustFindLiveDir(t *testing.T, baseDir string) string {
	t.Helper()

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", baseDir, err)
	}

	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "crashfs-") {
			return filepath.Join(baseDir, e.Name())
		}
	}

	t.Fatalf("no crashfs-* directory found in %q", baseDir)

	return ""
}

// mustSymlink creates a symlink in the crash filesystem's live directory.
// This bypasses fs.Crash to test symlink detection.
func mustSymlink(t *testing.T, baseDir, target, link string) {
	t.Helper()

	liveDir := mustFindLiveDir(t, baseDir)
	targetAbs := filepath.Join(liveDir, target)
	linkAbs := filepath.Join(liveDir, link)

	err := os.Symlink(targetAbs, linkAbs)
	if err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", link, target, err)
	}
}

// Test content constants used across crash tests.
const (
	testContentData  = "data"
	testContentOld   = "old"
	testContentNew   = "new"
	testContentHello = "hello"
)

func mustNewCrash(t *testing.T, config *fs.CrashConfig) *fs.Crash {
	t.Helper()

	crash, err := fs.NewCrash(t, fs.NewReal(), config)
	if err != nil {
		t.Fatalf("fs.NewCrash: %v", err)
	}

	return crash
}

func mustReadFile(t *testing.T, fileSystem fs.FS, path string) string {
	t.Helper()

	data, err := fileSystem.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	return string(data)
}

func requireNotExists(t *testing.T, fileSystem fs.FS, path string) {
	t.Helper()

	exists, err := fileSystem.Exists(path)
	if err != nil {
		t.Fatalf("Exists(%q): %v", path, err)
	}

	if exists {
		t.Fatalf("Exists(%q)=true, want false", path)
	}

	_, err = fileSystem.Stat(path)
	if err == nil {
		t.Fatalf("Stat(%q): want error", path)
	}

	if !os.IsNotExist(err) {
		t.Fatalf("Stat(%q): err=%v, want not-exist", path, err)
	}
}

func writeFile(t *testing.T, fileSystem fs.FS, path string, data string, perm os.FileMode, syncFile bool) {
	t.Helper()

	f, err := fileSystem.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", path, err)
	}

	_, err = f.Write([]byte(data))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write(%q): %v", path, err)
	}

	if syncFile {
		syncErr := f.Sync()
		if syncErr != nil {
			_ = f.Close()

			t.Fatalf("Sync(%q): %v", path, syncErr)
		}
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}
}

func syncDir(t *testing.T, fileSystem fs.FS, path string) {
	t.Helper()

	d, err := fileSystem.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}

	err = d.Sync()
	if err != nil {
		_ = d.Close()

		if errors.Is(err, syscall.EINVAL) {
			t.Fatalf("Sync(%q): %v (directory fsync unsupported)", path, err)
		}

		t.Fatalf("Sync(%q): %v", path, err)
	}

	err = d.Close()
	if err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}
}

func mustPanicSimulatedCrash(t *testing.T, fn func()) error {
	t.Helper()

	var recovered any

	func() {
		defer func() { recovered = recover() }()

		fn()
	}()

	if recovered == nil {
		t.Fatal("expected simulated crash")
	}

	err, ok := recovered.(error)
	if !ok {
		t.Fatalf("panic=%T, want error", recovered)
	}

	var crashErr *fs.CrashPanicError
	if !errors.As(err, &crashErr) {
		t.Fatalf("panic=%v, want fs.CrashPanicError", err)
	}

	return err
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
