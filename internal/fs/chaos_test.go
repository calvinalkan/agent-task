package fs

import (
	"bytes"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// Chaos FS Tests
//
// These tests verify Chaos fault injection and OS-like error semantics.
//
// Chaos never injects ENOENT: missing-path errors must come from the wrapped FS.
// =============================================================================

func TestChaos_PassesThroughWhenDisabled(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		ReadFailRate:   1.0,
		WriteFailRate:  1.0,
		OpenFailRate:   1.0,
		RemoveFailRate: 1.0,
		StatFailRate:   1.0,
	})
	chaosFS.SetMode(ChaosModeNoOp)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if _, err := writeFileOnce(chaosFS, path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := chaosFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if got, want := string(got), "hello"; got != want {
		t.Fatalf("ReadFile=%q, want %q", got, want)
	}
}

func TestChaos_CanToggleModes(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()

	// Active by default - should fail
	_, err := writeFileOnce(chaosFS, filepath.Join(dir, "1.txt"), []byte("a"), 0o644)
	if err == nil {
		t.Fatalf("active: expected error")
	}

	// NoOp - should succeed
	chaosFS.SetMode(ChaosModeNoOp)

	_, err = writeFileOnce(chaosFS, filepath.Join(dir, "2.txt"), []byte("b"), 0o644)
	if err != nil {
		t.Fatalf("noop: %v", err)
	}

	// Active again - should fail
	chaosFS.SetMode(ChaosModeActive)

	_, err = writeFileOnce(chaosFS, filepath.Join(dir, "3.txt"), []byte("c"), 0o644)
	if err == nil {
		t.Fatalf("active: expected error")
	}
}

func TestChaos_InjectsFileWriteFault(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	_, err := writeFileOnce(chaosFS, path, []byte("hello"), 0o644)
	if err == nil {
		t.Fatalf("write unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("write should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}

	validErrs := []error{
		syscall.EIO,
		syscall.ENOSPC,
		syscall.EDQUOT,
		syscall.EROFS,
	}

	for _, e := range validErrs {
		if errors.Is(err, e) {
			return
		}
	}

	t.Fatalf("err=%v, want one of %v", err, validErrs)
}

func TestChaos_InjectsReadFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{ReadFailRate: 1.0})

	_, err := chaosFS.ReadFile(path)
	if err == nil {
		t.Fatalf("ReadFile unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("ReadFile should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}
}

func TestChaos_InjectsOpenFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{OpenFailRate: 1.0})

	_, err := chaosFS.Open(path)
	if err == nil {
		t.Fatalf("Open unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("Open should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Open err should be *os.PathError, got %T (%v)", err, err)
	}

	_, err = chaosFS.Create(filepath.Join(dir, "new.txt"))
	if err == nil {
		t.Fatalf("Create unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("Create should never inject ENOENT: %v", err)
	}
}

func TestChaos_InjectsMkdirAllFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir", "subdir")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{MkdirAllFailRate: 1.0})

	err := chaosFS.MkdirAll(path, 0o755)
	if err == nil {
		t.Fatalf("MkdirAll unexpectedly succeeded")
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("MkdirAll should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("MkdirAll err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "mkdirall"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EIO,
		syscall.ENOSPC,
		syscall.EDQUOT,
		syscall.EROFS,
		syscall.ENOTDIR,
	}

	var validErr bool

	for _, e := range validErrs {
		if errors.Is(err, e) {
			validErr = true

			break
		}
	}

	if !validErr {
		t.Fatalf("err=%v, want one of %v", err, validErrs)
	}

	if got, want := chaosFS.Stats().MkdirAllFails, int64(1); got != want {
		t.Fatalf("MkdirAllFails=%d, want %d", got, want)
	}

	// Verify directory was not created
	exists, _ := realFS.Exists(path)
	if exists {
		t.Fatalf("directory should not exist after injected failure")
	}
}

func TestChaos_MkdirAll_Passthrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir", "subdir")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{MkdirAllFailRate: 1.0})
	chaosFS.SetMode(ChaosModeNoOp) // Passthrough despite 100% fail rate

	err := chaosFS.MkdirAll(path, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	exists, err := realFS.Exists(path)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatalf("directory should exist after MkdirAll")
	}
}

func TestChaos_InjectsRemoveAllFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{RemoveFailRate: 1.0})

	err := chaosFS.RemoveAll(path)
	if err == nil {
		t.Fatalf("RemoveAll unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("RemoveAll should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("RemoveAll err should be *os.PathError, got %T (%v)", err, err)
	}
}

func TestChaos_InjectsRenameFault_IsLinkError(t *testing.T) {
	dir := t.TempDir()
	oldpath := filepath.Join(dir, "old.txt")
	newpath := filepath.Join(dir, "new.txt")

	realFS := NewReal()

	mustWriteFile(t, oldpath, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{RenameFailRate: 1.0})

	err := chaosFS.Rename(oldpath, newpath)
	if err == nil {
		t.Fatalf("Rename unexpectedly succeeded")
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("Rename should never inject ENOENT: %v", err)
	}

	var linkErr *os.LinkError
	if got, want := errors.As(err, &linkErr), true; got != want {
		t.Fatalf("Rename err should be *os.LinkError, got %T (%v)", err, err)
	}

	if got, want := linkErr.Op, "rename"; got != want {
		t.Fatalf("LinkError.Op=%q, want %q", got, want)
	}

	if got, want := linkErr.Old, oldpath; got != want {
		t.Fatalf("LinkError.Old=%q, want %q", got, want)
	}

	if got, want := linkErr.New, newpath; got != want {
		t.Fatalf("LinkError.New=%q, want %q", got, want)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EIO,
		syscall.ENOSPC,
		syscall.EXDEV,
		syscall.EROFS,
		syscall.EPERM,
	}

	var validErr bool

	for _, e := range validErrs {
		if errors.Is(err, e) {
			validErr = true

			break
		}
	}

	if !validErr {
		t.Fatalf("err=%v, want one of %v", err, validErrs)
	}

	if got, want := chaosFS.Stats().RenameFails, int64(1); got != want {
		t.Fatalf("RenameFails=%d, want %d", got, want)
	}
}

func TestNewChaos_NilFS_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for nil FS")
		}
	}()

	_ = NewChaos(nil, 0, ChaosConfig{})
}

func TestChaos_StatsCountFaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0,
		ReadFailRate:  1.0,
	})

	_, _ = writeFileOnce(chaosFS, path, []byte("x"), 0o644)
	_, _ = writeFileOnce(chaosFS, path, []byte("y"), 0o644)
	_, _ = chaosFS.ReadFile(path)

	stats := chaosFS.Stats()
	if got, want := stats.WriteFails, int64(2); got != want {
		t.Fatalf("WriteFails=%d, want %d", got, want)
	}

	if got, want := stats.ReadFails, int64(1); got != want {
		t.Fatalf("ReadFails=%d, want %d", got, want)
	}
}

func TestChaos_TotalFaults(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate:    1.0,
		RemoveFailRate:   1.0,
		MkdirAllFailRate: 1.0,
	})

	dir := t.TempDir()

	_, _ = writeFileOnce(chaosFS, filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	_ = chaosFS.Remove(filepath.Join(dir, "b.txt"))
	_ = chaosFS.MkdirAll(filepath.Join(dir, "c"), 0o755)

	if got, want := chaosFS.TotalFaults(), int64(3); got != want {
		t.Fatalf("TotalFaults=%d, want %d", got, want)
	}
}

func TestChaosFile_SeekWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello world"), 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	pos, err := f.Seek(6, 0)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}

	if got, want := pos, int64(6); got != want {
		t.Fatalf("Seek pos=%d, want %d", got, want)
	}

	buf := make([]byte, 5)

	n, err := f.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got, want := string(buf[:n]), "world"; got != want {
		t.Fatalf("Read=%q, want %q", got, want)
	}
}

func TestChaosFile_StatChaosError_IsPathError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world")

	realFS := NewReal()

	mustWriteFile(t, path, content, 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		FileStatFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err == nil {
		t.Fatalf("Stat unexpectedly succeeded")
	}

	if info != nil {
		t.Fatalf("Stat info=%v, want nil on error", info)
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("File.Stat should never inject ENOENT: %v", err)
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("Stat err=%v, want EIO", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Stat err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "stat"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	if got, want := chaosFS.Stats().FileStatFails, int64(1); got != want {
		t.Fatalf("FileStatFails=%d, want %d", got, want)
	}
}

func TestChaosFile_SyncChaosError_IsPathError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		SyncFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	err = f.Sync()
	if err == nil {
		t.Fatalf("Sync unexpectedly succeeded")
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("File.Sync should never inject ENOENT: %v", err)
	}

	validErrs := []error{
		syscall.EIO,
		syscall.ENOSPC,
		syscall.EDQUOT,
		syscall.EROFS,
	}

	var validErr bool

	for _, e := range validErrs {
		if errors.Is(err, e) {
			validErr = true

			break
		}
	}

	if !validErr {
		t.Fatalf("Sync err=%v, want one of %v", err, validErrs)
	}

	var pathErr *os.PathError

	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Sync err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "sync"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	if got, want := chaosFS.Stats().SyncFails, int64(1); got != want {
		t.Fatalf("SyncFails=%d, want %d", got, want)
	}
}

func TestChaosFile_SeekChaosError_ReturnsZeroAndDoesNotAdvance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("abc")

	realFS := NewReal()

	mustWriteFile(t, path, content, 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		SeekFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	pos, err := f.Seek(1, 0)
	if err == nil {
		t.Fatalf("Seek unexpectedly succeeded")
	}

	if got, want := pos, int64(0); got != want {
		t.Fatalf("Seek pos=%d, want %d on error", got, want)
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("File.Seek should never inject ENOENT: %v", err)
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("Seek err=%v, want EIO", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Seek err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "seek"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	// Ensure injected seek does not change file offset.
	chaosFS.SetMode(ChaosModeNoOp)

	buf := make([]byte, 1)

	n, readErr := f.Read(buf)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}

	if got, want := n, 1; got != want {
		t.Fatalf("Read n=%d, want %d", got, want)
	}

	if got, want := buf[0], content[0]; got != want {
		t.Fatalf("Read byte=%q, want %q", got, want)
	}

	if got, want := chaosFS.Stats().SeekFails, int64(1); got != want {
		t.Fatalf("SeekFails=%d, want %d", got, want)
	}
}

func TestChaosFile_CloseChaosError_StillCloses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("hello"), 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		CloseFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = f.Close()
	if err == nil {
		t.Fatalf("Close unexpectedly succeeded")
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("File.Close should never inject ENOENT: %v", err)
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("Close err=%v, want EIO", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Close err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "close"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	// Chaos always closes the underlying file to avoid descriptor leaks.
	err = f.Close()
	if got, want := errors.Is(err, os.ErrClosed), true; got != want {
		t.Fatalf("2nd Close errors.Is(err, os.ErrClosed)=%t, want %t (err=%v)", got, want, err)
	}

	if got, want := chaosFS.Stats().CloseFails, int64(1); got != want {
		t.Fatalf("CloseFails=%d, want %d", got, want)
	}
}

func TestChaos_ReadFile_PartialReturnsPrefixAndError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")

	realFS := NewReal()

	mustWriteFile(t, path, content, 0o644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{PartialReadRate: 1.0})

	data, err := chaosFS.ReadFile(path)
	if err == nil {
		t.Fatalf("ReadFile unexpectedly succeeded")
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("err=%v, want EIO", err)
	}

	if got, want := bytes.HasPrefix(content, data), true; got != want {
		t.Fatalf("partial read must be prefix\noriginal: %q\ngot: %q", content, data)
	}

	if got, want := len(data) < len(content), true; got != want {
		t.Fatalf("len(data)=%d, want < %d", len(data), len(content))
	}
}

func TestChaos_ReadDir_PartialReturnsSubsetAndError(t *testing.T) {
	dir := t.TempDir()
	realFS := NewReal()

	paths := []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "b.txt"),
		filepath.Join(dir, "c.txt"),
	}
	for _, p := range paths {
		mustWriteFile(t, p, []byte("x"), 0o644)
	}

	full, err := realFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(real): %v", err)
	}

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{ReadDirPartialRate: 1.0})

	entries, err := chaosFS.ReadDir(dir)
	if err == nil {
		t.Fatalf("ReadDir unexpectedly succeeded")
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("err=%v, want EIO", err)
	}

	if got, want := len(entries) > 0 && len(entries) < len(full), true; got != want {
		t.Fatalf("len(entries)=%d, want in (0,%d)", len(entries), len(full))
	}

	for i := range entries {
		if got, want := entries[i].Name(), full[i].Name(); got != want {
			t.Fatalf("entries[%d]=%q, want %q", i, got, want)
		}
	}
}

func TestChaosFile_PartialWrite_WritesPrefixAndError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")
	realFS := NewReal()

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		PartialWriteRate: 1.0,
	})

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	wrote, err := f.Write(content)
	if err == nil {
		t.Fatalf("Write unexpectedly succeeded (wrote=%d)", wrote)
	}

	if os.IsPermission(err) || os.IsNotExist(err) {
		t.Fatalf("Write should not return permission/not-exist after open: %v", err)
	}

	data, readErr := realFS.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}

	if got, want := bytes.HasPrefix(content, data), true; got != want {
		t.Fatalf("partial write must be prefix\noriginal: %q\ngot: %q", content, data)
	}

	if got, want := len(data) < len(content), true; got != want {
		t.Fatalf("len(data)=%d, want < %d", len(data), len(content))
	}
}

func TestChaosFile_PartialWrite_ShortWriteRate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	const shortWriteRate = 0.10

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		PartialWriteRate: 1.0, // Always partial
		ShortWriteRate:   shortWriteRate,
	})

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	const (
		iterations = 1000
		tolerance  = 0.05
	)

	content := []byte("ab") // len>1 so partial writes are possible

	var shortWrites int

	for range iterations {
		n, err := f.Write(content)
		if err == nil {
			t.Fatalf("Write unexpectedly succeeded (n=%d)", n)
		}

		if got, want := n > 0 && n < len(content), true; got != want {
			t.Fatalf("Write n=%d, want in (0,%d)", n, len(content))
		}

		if errors.Is(err, io.ErrShortWrite) {
			shortWrites++

			if got, want := IsChaosErr(err), true; got != want {
				t.Fatalf("IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
			}

			continue
		}

		var pathErr *os.PathError
		if got, want := errors.As(err, &pathErr), true; got != want {
			t.Fatalf("Write err should be *os.PathError or io.ErrShortWrite, got %T (%v)", err, err)
		}
	}

	min := int(float64(iterations) * (shortWriteRate - tolerance))

	max := int(float64(iterations) * (shortWriteRate + tolerance))
	if shortWrites < min || shortWrites > max {
		t.Fatalf("io.ErrShortWrite count=%d, want in [%d,%d] (%.0f%% ± %.0f%%)", shortWrites, min, max, shortWriteRate*100, tolerance*100)
	}
}

func TestChaos_Concurrency_NoRaceOrPanic(t *testing.T) {
	dir := t.TempDir()
	realFS := NewReal()

	// Seed + non-zero rates to exercise RNG under contention.
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		ReadFailRate:     0.3,
		PartialReadRate:  0.3,
		WriteFailRate:    0.3,
		OpenFailRate:     0.3,
		RemoveFailRate:   0.3,
		RenameFailRate:   0.3,
		StatFailRate:     0.3,
		MkdirAllFailRate: 0.3,
		ReadDirFailRate:  0.3,
	})

	// Create some files for operations.
	for i := range 10 {
		p := filepath.Join(dir, "file"+string(rune('0'+i))+".txt")
		mustWriteFile(t, p, []byte("test"), 0o644)
	}

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			path := filepath.Join(dir, "file"+string(rune('0'+id))+".txt")
			for range 200 {
				_, _ = chaosFS.ReadFile(path)
				if f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
					_, _ = f.Write([]byte("x"))
					_ = f.Close()
				}

				_, _ = chaosFS.Stat(path)
				_, _ = chaosFS.Exists(path)
				_, _ = chaosFS.ReadDir(dir)
				_ = chaosFS.RemoveAll(filepath.Join(dir, "missing"))
				_ = chaosFS.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
			}
		}(i)
	}

	wg.Wait()
}

func TestChaos_ErrorInjectionDoesNotDeadlock(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		WriteFailRate: 1.0, // Always inject an error (exercise pickError/pickRandom).
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	done := make(chan error, 1)

	go func() {
		f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			done <- err

			return
		}

		_, err = f.Write([]byte("x"))
		_ = f.Close()

		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("write unexpectedly succeeded")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("write hung (possible deadlock in chaos error injection)")
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

	mustWriteFile(t, path, content, 0o644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		PartialReadRate: 1.0, // Always partial.
	})

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

// TestChaosErrors_PreserveErrorsIs verifies that injected errors work correctly
// with errors.Is and errors.As. Note: we use errors.Is instead of legacy os.IsNotExist
// etc. as recommended by the Go documentation.
func TestChaosErrors_PreserveErrorsIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "path")

	cases := []struct {
		name   string
		errno  syscall.Errno
		target error // expected errors.Is target, nil if none
	}{
		{name: "ENOENT", errno: syscall.ENOENT, target: iofs.ErrNotExist},
		{name: "EACCES", errno: syscall.EACCES, target: iofs.ErrPermission},
		{name: "EPERM", errno: syscall.EPERM, target: iofs.ErrPermission},
		{name: "EROFS", errno: syscall.EROFS, target: nil},
		{name: "EIO", errno: syscall.EIO, target: nil},
		{name: "ENOSPC", errno: syscall.ENOSPC, target: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &iofs.PathError{Op: "op", Path: path, Err: tc.errno}
			injected := pathError("op", path, tc.errno)

			// IsChaosErr distinguishes injected from real errors
			if got, want := IsChaosErr(base), false; got != want {
				t.Fatalf("IsChaosErr(base)=%t, want %t", got, want)
			}

			if got, want := IsChaosErr(injected), true; got != want {
				t.Fatalf("IsChaosErr(injected)=%t, want %t", got, want)
			}

			// errors.As extracts the underlying PathError
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

			// errors.Is matches the errno
			if got, want := errors.Is(injected, tc.errno), true; got != want {
				t.Fatalf("errors.Is(injected, %s)=%t, want %t", tc.name, got, want)
			}

			// errors.Is matches the sentinel error (if applicable)
			if tc.target != nil {
				if got, want := errors.Is(injected, tc.target), errors.Is(base, tc.target); got != want {
					t.Fatalf("errors.Is(injected, %v)=%t, want %t", tc.target, got, want)
				}
			}
		})
	}
}

func TestChaosError_PreservesErrorsIs(t *testing.T) {
	base := os.ErrDeadlineExceeded
	injected := &ChaosError{Err: base}

	if got, want := IsChaosErr(injected), true; got != want {
		t.Fatalf("IsChaosErr(injected)=%t, want %t", got, want)
	}

	if got, want := IsChaosErr(base), false; got != want {
		t.Fatalf("IsChaosErr(base)=%t, want %t", got, want)
	}

	if got, want := errors.Is(injected, os.ErrDeadlineExceeded), true; got != want {
		t.Fatalf("errors.Is(injected, os.ErrDeadlineExceeded)=%t, want %t", got, want)
	}
}

func TestChaos_RemoveAll_Missing_PassthroughMatchesOsRemoveAll(t *testing.T) {
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
	chaosFS.SetMode(ChaosModeNoOp)

	err = chaosFS.RemoveAll(path)
	if err != nil {
		t.Fatalf("Chaos.RemoveAll: %v", err)
	}
}

func TestChaos_RemoveAll_Missing_CanInject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		RemoveFailRate: 1.0, // Always inject.
	})

	err := chaosFS.RemoveAll(path)
	if err == nil {
		t.Fatalf("Chaos.RemoveAll unexpectedly succeeded")
	}

	if os.IsNotExist(err) {
		t.Fatalf("Chaos.RemoveAll should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}
}

func TestChaosFile_WriteFailureDoesNotModifyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()

	mustWriteFile(t, path, []byte("old"), 0o644)

	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		WriteFailRate: 1.0, // Always fail.
	})

	f, err := chaosFS.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	if _, err := f.Write([]byte("new")); err == nil {
		t.Fatalf("Write unexpectedly succeeded")
	}

	got, err := realFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if got, want := string(got), "old"; got != want {
		t.Fatalf("Write failure must not modify file: got %q, want %q", got, want)
	}
}

func TestIsChaosErr_ReturnsTrueForChaosErrors(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		_, err = f.Write([]byte("x"))
		_ = f.Close()
	}

	if err == nil {
		t.Fatalf("expected error")
	}

	if got, want := IsChaosErr(err), true; got != want {
		t.Fatalf("IsChaosErr=%v, want %v (err=%v)", got, want, err)
	}
}

func TestIsChaosErr_ReturnsFalseForRealErrors(t *testing.T) {
	realFS := NewReal()

	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")

	_, err := realFS.Open(path)
	if err == nil {
		t.Fatalf("expected error")
	}

	if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
		t.Fatalf("expected ENOENT, got %v", err)
	}

	if got, want := IsChaosErr(err), false; got != want {
		t.Fatalf("IsChaosErr=%v, want %v (err=%v)", got, want, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()

	err := os.WriteFile(path, data, perm)
	if err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
}

func writeFileOnce(fs FS, path string, data []byte, perm os.FileMode) (int, error) {
	f, err := fs.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return 0, err
	}

	n, writeErr := f.Write(data)
	closeErr := f.Close()

	if writeErr != nil {
		return n, writeErr
	}

	if n != len(data) {
		return n, io.ErrShortWrite
	}

	return n, closeErr
}
