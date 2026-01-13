package fs_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// =============================================================================
// Chaos fs.FS Tests
//
// These tests verify Chaos fault injection and OS-like error semantics.
//
// Chaos never injects ENOENT: missing-path errors must come from the wrapped fs.FS.
// =============================================================================

func Test_Chaos_Passes_Through_When_Mode_Is_NoOp(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
		ReadFailRate:   1.0,
		WriteFailRate:  1.0,
		OpenFailRate:   1.0,
		RemoveFailRate: 1.0,
		StatFailRate:   1.0,
	})
	chaosFS.SetMode(fs.ChaosModeNoOp)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := writeFileOnce(chaosFS, path, []byte(testContentHello))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := chaosFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if got, want := string(got), testContentHello; got != want {
		t.Fatalf("ReadFile=%q, want %q", got, want)
	}
}

func Test_Chaos_Toggles_Injection_When_Mode_Changes(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()

	// Active by default - should fail
	err := writeFileOnce(chaosFS, filepath.Join(dir, "1.txt"), []byte("a"))
	if err == nil {
		t.Fatal("active: expected error")
	}

	// NoOp - should succeed
	chaosFS.SetMode(fs.ChaosModeNoOp)

	err = writeFileOnce(chaosFS, filepath.Join(dir, "2.txt"), []byte("b"))
	if err != nil {
		t.Fatalf("noop: %v", err)
	}

	// Active again - should fail
	chaosFS.SetMode(fs.ChaosModeActive)

	err = writeFileOnce(chaosFS, filepath.Join(dir, "3.txt"), []byte("c"))
	if err == nil {
		t.Fatal("active: expected error")
	}
}

func Test_Chaos_Injects_Write_Error_When_Write_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := writeFileOnce(chaosFS, path, []byte(testContentHello))
	if err == nil {
		t.Fatal("write unexpectedly succeeded")
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

func Test_Chaos_Injects_Read_Error_When_Read_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{ReadFailRate: 1.0})

	_, err := chaosFS.ReadFile(path)
	if err == nil {
		t.Fatal("ReadFile unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("ReadFile should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}
}

func Test_Chaos_Injects_Open_Error_When_Open_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{OpenFailRate: 1.0})

	_, err := chaosFS.Open(path)
	if err == nil {
		t.Fatal("Open unexpectedly succeeded")
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
		t.Fatal("Create unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) {
		t.Fatalf("Create should never inject ENOENT: %v", err)
	}
}

func Test_Chaos_Passes_Through_Real_NotExist_Errors_When_Path_Is_Missing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missingFile := filepath.Join(dir, "missing.txt")
	missingDir := filepath.Join(dir, "missing-dir")

	t.Run("Open", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		_, err := chaosFS.Open(missingFile)
		if err == nil {
			t.Fatal("Open unexpectedly succeeded")
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("OpenFileReadOnly", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		_, err := chaosFS.OpenFile(missingFile, os.O_RDONLY, 0)
		if err == nil {
			t.Fatal("OpenFile unexpectedly succeeded")
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("ReadFile", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		data, err := chaosFS.ReadFile(missingFile)
		if err == nil {
			t.Fatal("ReadFile unexpectedly succeeded")
		}

		if data != nil {
			t.Fatalf("ReadFile data=%v, want nil on error", data)
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("ReadDir", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		entries, err := chaosFS.ReadDir(missingDir)
		if err == nil {
			t.Fatal("ReadDir unexpectedly succeeded")
		}

		if entries != nil {
			t.Fatalf("ReadDir entries=%v, want nil on error", entries)
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("Stat", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		info, err := chaosFS.Stat(missingFile)
		if err == nil {
			t.Fatal("Stat unexpectedly succeeded")
		}

		if info != nil {
			t.Fatalf("Stat info=%v, want nil on error", info)
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("Remove", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

		err := chaosFS.Remove(missingFile)
		if err == nil {
			t.Fatal("Remove unexpectedly succeeded")
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}
	})

	t.Run("Rename", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})
		newpath := filepath.Join(dir, "new.txt")

		err := chaosFS.Rename(missingFile, newpath)
		if err == nil {
			t.Fatal("Rename unexpectedly succeeded")
		}

		if got, want := fs.IsChaosErr(err), false; got != want {
			t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := os.IsNotExist(err), true; got != want {
			t.Fatalf("os.IsNotExist(err)=%t, want %t (err=%v)", got, want, err)
		}

		if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
			t.Fatalf("errors.Is(err, ENOENT)=%t, want %t (err=%v)", got, want, err)
		}

		var linkErr *os.LinkError
		if got, want := errors.As(err, &linkErr), true; got != want {
			t.Fatalf("Rename err should be *os.LinkError, got %T (%v)", err, err)
		}
	})
}

func Test_Chaos_OpenFile_Uses_Open_Or_Create_Op_Based_On_Flags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	t.Run("ReadOnlyUsesOpen", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
			OpenFailRate:   1.0,
			TraceCapacity:  10,
			WriteFailRate:  1.0,
			ReadFailRate:   1.0,
			RemoveFailRate: 1.0,
		})

		_, _ = chaosFS.OpenFile(path, os.O_RDONLY, 0)

		events := chaosFS.TraceEvents()
		if got, want := len(events), 1; got != want {
			t.Fatalf("TraceEvents() count: want %d, got %d\ntrace:\n%s", want, got, chaosFS.Trace())
		}

		if got, want := events[0].Op, "open"; got != want {
			t.Fatalf("TraceEvents()[0].Op=%q, want %q\ntrace:\n%s", got, want, chaosFS.Trace())
		}
	})

	t.Run("WriteUsesCreate", func(t *testing.T) {
		t.Parallel()

		chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
			OpenFailRate:   1.0,
			TraceCapacity:  10,
			WriteFailRate:  1.0,
			ReadFailRate:   1.0,
			RemoveFailRate: 1.0,
		})

		_, _ = chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)

		events := chaosFS.TraceEvents()
		if got, want := len(events), 1; got != want {
			t.Fatalf("TraceEvents() count: want %d, got %d\ntrace:\n%s", want, got, chaosFS.Trace())
		}

		if got, want := events[0].Op, "create"; got != want {
			t.Fatalf("TraceEvents()[0].Op=%q, want %q\ntrace:\n%s", got, want, chaosFS.Trace())
		}
	})
}

func Test_Chaos_Injects_MkdirAll_Error_When_MkdirAll_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir", "subdir")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{MkdirAllFailRate: 1.0})

	err := chaosFS.MkdirAll(path, 0o755)
	if err == nil {
		t.Fatal("MkdirAll unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
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
		t.Fatal("directory should not exist after injected failure")
	}
}

func Test_Chaos_MkdirAll_Succeeds_When_Mode_Is_NoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir", "subdir")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{MkdirAllFailRate: 1.0})
	chaosFS.SetMode(fs.ChaosModeNoOp) // Passthrough despite 100% fail rate

	err := chaosFS.MkdirAll(path, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	exists, err := realFS.Exists(path)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("directory should exist after MkdirAll")
	}
}

func Test_Chaos_Injects_Stat_Error_When_Stat_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{StatFailRate: 1.0})

	info, err := chaosFS.Stat(path)
	if err == nil {
		t.Fatal("Stat unexpectedly succeeded")
	}

	if info != nil {
		t.Fatalf("Stat info=%v, want nil on error", info)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("Stat should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Stat err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "stat"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EIO,
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

	if got, want := chaosFS.Stats().StatFails, int64(1); got != want {
		t.Fatalf("StatFails=%d, want %d", got, want)
	}

	exists, err := realFS.Exists(path)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("file should still exist after injected Stat failure")
	}
}

func Test_Chaos_Injects_Remove_Error_When_Remove_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{RemoveFailRate: 1.0})

	err := chaosFS.Remove(path)
	if err == nil {
		t.Fatal("Remove unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("Remove should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Remove err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "remove"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EPERM,
		syscall.EBUSY,
		syscall.EIO,
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
		t.Fatalf("err=%v, want one of %v", err, validErrs)
	}

	if got, want := chaosFS.Stats().RemoveFails, int64(1); got != want {
		t.Fatalf("RemoveFails=%d, want %d", got, want)
	}

	exists, err := realFS.Exists(path)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("file should still exist after injected Remove failure")
	}
}

func Test_Chaos_ReadDir_Prefers_Full_Failure_Over_Partial_When_Both_Rates_Are_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	realFS := fs.NewReal()

	mustWriteFile(t, filepath.Join(dir, "a.txt"), []byte("x"))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		ReadDirFailRate:    1.0,
		ReadDirPartialRate: 1.0,
	})

	entries, err := chaosFS.ReadDir(dir)
	if err == nil {
		t.Fatal("ReadDir unexpectedly succeeded")
	}

	if entries != nil {
		t.Fatalf("ReadDir entries=%v, want nil on error", entries)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("ReadDir should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("ReadDir err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "readdir"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EIO,
		syscall.ENOTDIR,
		syscall.EMFILE,
		syscall.ENFILE,
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

	stats := chaosFS.Stats()
	if got, want := stats.ReadDirFails, int64(1); got != want {
		t.Fatalf("ReadDirFails=%d, want %d", got, want)
	}

	if got, want := stats.PartialReadDirs, int64(0); got != want {
		t.Fatalf("PartialReadDirs=%d, want %d", got, want)
	}
}

func Test_Chaos_Injects_RemoveAll_Error_When_Remove_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{RemoveFailRate: 1.0})

	err := chaosFS.RemoveAll(path)
	if err == nil {
		t.Fatal("RemoveAll unexpectedly succeeded")
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("RemoveAll should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("RemoveAll err should be *os.PathError, got %T (%v)", err, err)
	}
}

func Test_Chaos_Returns_Link_Error_When_Rename_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldpath := filepath.Join(dir, "old.txt")
	newpath := filepath.Join(dir, "new.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, oldpath, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{RenameFailRate: 1.0})

	err := chaosFS.Rename(oldpath, newpath)
	if err == nil {
		t.Fatal("Rename unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
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

func Test_Chaos_Rename_Succeeds_When_No_Fault_Configured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldpath := filepath.Join(dir, "old.txt")
	newpath := filepath.Join(dir, "new.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, oldpath, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{})

	err := chaosFS.Rename(oldpath, newpath)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	oldExists, err := realFS.Exists(oldpath)
	if err != nil {
		t.Fatalf("Exists(oldpath): %v", err)
	}

	if oldExists {
		t.Fatal("old path should not exist after Rename")
	}

	newExists, err := realFS.Exists(newpath)
	if err != nil {
		t.Fatalf("Exists(newpath): %v", err)
	}

	if !newExists {
		t.Fatal("new path should exist after Rename")
	}
}

func Test_NewChaos_Panics_When_FS_Is_Nil(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil fs.FS")
		}
	}()

	_ = fs.NewChaos(nil, 0, &fs.ChaosConfig{})
}

func Test_Chaos_Counts_Faults_When_Faults_Are_Injected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
		WriteFailRate: 1.0,
		ReadFailRate:  1.0,
	})

	_ = writeFileOnce(chaosFS, path, []byte("x"))
	_ = writeFileOnce(chaosFS, path, []byte("y"))
	_, _ = chaosFS.ReadFile(path)

	stats := chaosFS.Stats()
	if got, want := stats.WriteFails, int64(2); got != want {
		t.Fatalf("WriteFails=%d, want %d", got, want)
	}

	if got, want := stats.ReadFails, int64(1); got != want {
		t.Fatalf("ReadFails=%d, want %d", got, want)
	}
}

func Test_Chaos_TotalFaults_Returns_Sum_When_Multiple_Fault_Types_Injected(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
		WriteFailRate:    1.0,
		RemoveFailRate:   1.0,
		MkdirAllFailRate: 1.0,
	})

	dir := t.TempDir()

	_ = writeFileOnce(chaosFS, filepath.Join(dir, "a.txt"), []byte("x"))
	_ = chaosFS.Remove(filepath.Join(dir, "b.txt"))
	_ = chaosFS.MkdirAll(filepath.Join(dir, "c"), 0o755)

	if got, want := chaosFS.TotalFaults(), int64(3); got != want {
		t.Fatalf("TotalFaults=%d, want %d", got, want)
	}
}

func Test_ChaosFile_Seek_Succeeds_When_No_Fault_Configured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte("hello world"))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{})

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

func Test_ChaosFile_Fd_Returns_Valid_File_Descriptor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fd.txt")

	chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	var st syscall.Stat_t

	err = syscall.Fstat(int(f.Fd()), &st)
	if err != nil {
		t.Fatalf("syscall.Fstat: %v", err)
	}
}

func Test_ChaosFile_Stat_Returns_Path_Error_When_File_Stat_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world")

	realFS := fs.NewReal()

	mustWriteFile(t, path, content)

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		FileStatFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err == nil {
		t.Fatal("Stat unexpectedly succeeded")
	}

	if info != nil {
		t.Fatalf("Stat info=%v, want nil on error", info)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Stat should never inject ENOENT: %v", err)
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

func Test_ChaosFile_Sync_Returns_Path_Error_When_Sync_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		SyncFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	err = f.Sync()
	if err == nil {
		t.Fatal("Sync unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Sync should never inject ENOENT: %v", err)
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

func Test_ChaosFile_Seek_Returns_Zero_And_Preserves_Offset_When_Seek_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("abc")

	realFS := fs.NewReal()

	mustWriteFile(t, path, content)

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		SeekFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	pos, err := f.Seek(1, 0)
	if err == nil {
		t.Fatal("Seek unexpectedly succeeded")
	}

	if got, want := pos, int64(0); got != want {
		t.Fatalf("Seek pos=%d, want %d on error", got, want)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Seek should never inject ENOENT: %v", err)
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
	chaosFS.SetMode(fs.ChaosModeNoOp)

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

func Test_ChaosFile_Close_Still_Closes_File_When_Close_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		CloseFailRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = f.Close()
	if err == nil {
		t.Fatal("Close unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Close should never inject ENOENT: %v", err)
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

func Test_ChaosFile_Chmod_Returns_Path_Error_When_Chmod_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		ChmodFailRate: 1.0,
	})

	f, err := chaosFS.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	err = f.Chmod(0o600)
	if err == nil {
		t.Fatal("Chmod unexpectedly succeeded")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Chmod should never inject ENOENT: %v", err)
	}

	validErrs := []error{
		syscall.EACCES,
		syscall.EPERM,
		syscall.EIO,
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
		t.Fatalf("Chmod err=%v, want one of %v", err, validErrs)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Chmod err should be *os.PathError, got %T (%v)", err, err)
	}

	if got, want := pathErr.Op, "chmod"; got != want {
		t.Fatalf("PathError.Op=%q, want %q", got, want)
	}

	if got, want := chaosFS.Stats().ChmodFails, int64(1); got != want {
		t.Fatalf("ChmodFails=%d, want %d", got, want)
	}
}

func Test_Chaos_ReadFile_Returns_Prefix_And_Error_When_Partial_Read_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")

	realFS := fs.NewReal()

	mustWriteFile(t, path, content)

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{PartialReadRate: 1.0})

	data, err := chaosFS.ReadFile(path)
	if err == nil {
		t.Fatal("ReadFile unexpectedly succeeded")
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

func Test_Chaos_ReadFile_Prefers_Full_Failure_Over_Partial_When_Both_Rates_Are_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		ReadFailRate:    1.0,
		PartialReadRate: 1.0,
	})

	data, err := chaosFS.ReadFile(path)
	if err == nil {
		t.Fatal("ReadFile unexpectedly succeeded")
	}

	if data != nil {
		t.Fatalf("ReadFile data=%v, want nil on error", data)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	stats := chaosFS.Stats()
	if got, want := stats.ReadFails, int64(1); got != want {
		t.Fatalf("ReadFails=%d, want %d", got, want)
	}

	if got, want := stats.PartialReads, int64(0); got != want {
		t.Fatalf("PartialReads=%d, want %d", got, want)
	}
}

// =============================================================================
// Chaos WriteFile Tests
// =============================================================================

func Test_Chaos_WriteFile_Succeeds_When_No_Faults_Configured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{})

	err := chaosFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := realFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != testContentHello {
		t.Fatalf("content=%q, want %q", got, testContentHello)
	}
}

func Test_Chaos_WriteFile_Fails_When_Open_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{OpenFailRate: 1.0})

	err := chaosFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err == nil {
		t.Fatal("WriteFile unexpectedly succeeded")
	}

	if !fs.IsChaosErr(err) {
		t.Fatalf("fs.IsChaosErr(err)=%t, want true (err=%v)", fs.IsChaosErr(err), err)
	}

	// fs.File should not exist
	exists, _ := realFS.Exists(path)
	if exists {
		t.Fatal("file should not exist after open failure")
	}

	if got, want := chaosFS.Stats().OpenFails, int64(1); got != want {
		t.Fatalf("OpenFails=%d, want %d", got, want)
	}
}

func Test_Chaos_WriteFile_Fails_When_Write_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{WriteFailRate: 1.0})

	err := chaosFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err == nil {
		t.Fatal("WriteFile unexpectedly succeeded")
	}

	if !fs.IsChaosErr(err) {
		t.Fatalf("fs.IsChaosErr(err)=%t, want true (err=%v)", fs.IsChaosErr(err), err)
	}

	if got, want := chaosFS.Stats().WriteFails, int64(1); got != want {
		t.Fatalf("WriteFails=%d, want %d", got, want)
	}
}

func Test_Chaos_WriteFile_Writes_Partial_Data_When_Partial_Write_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a longer test string")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{PartialWriteRate: 1.0})

	err := chaosFS.WriteFile(path, content, 0o644)
	if err == nil {
		t.Fatal("WriteFile unexpectedly succeeded")
	}

	// fs.File should exist with partial content
	got, readErr := realFS.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}

	if len(got) == 0 {
		t.Fatal("expected partial data, got empty file")
	}

	if len(got) >= len(content) {
		t.Fatalf("len(got)=%d, want < %d", len(got), len(content))
	}

	if !bytes.HasPrefix(content, got) {
		t.Fatalf("partial write must be prefix\noriginal: %q\ngot: %q", content, got)
	}

	if got, want := chaosFS.Stats().PartialWrites, int64(1); got != want {
		t.Fatalf("PartialWrites=%d, want %d", got, want)
	}
}

func Test_Chaos_WriteFile_Fails_When_Close_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{CloseFailRate: 1.0})

	err := chaosFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err == nil {
		t.Fatal("WriteFile unexpectedly succeeded")
	}

	if !fs.IsChaosErr(err) {
		t.Fatalf("fs.IsChaosErr(err)=%t, want true (err=%v)", fs.IsChaosErr(err), err)
	}

	// fs.File should still have content (close error happens after write)
	got, readErr := realFS.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}

	if string(got) != testContentHello {
		t.Fatalf("content=%q, want %q", got, testContentHello)
	}

	if got, want := chaosFS.Stats().CloseFails, int64(1); got != want {
		t.Fatalf("CloseFails=%d, want %d", got, want)
	}
}

func Test_Chaos_WriteFile_Passes_Through_When_Mode_Is_NoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
		OpenFailRate:     1.0,
		WriteFailRate:    1.0,
		PartialWriteRate: 1.0,
		CloseFailRate:    1.0,
	})
	chaosFS.SetMode(fs.ChaosModeNoOp)

	err := chaosFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := realFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != testContentHello {
		t.Fatalf("content=%q, want %q", got, testContentHello)
	}
}

func Test_Chaos_ReadDir_Returns_Subset_And_Error_When_ReadDir_Partial_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realFS := fs.NewReal()

	paths := []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "b.txt"),
		filepath.Join(dir, "c.txt"),
	}
	for _, p := range paths {
		mustWriteFile(t, p, []byte("x"))
	}

	full, err := realFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(real): %v", err)
	}

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{ReadDirPartialRate: 1.0})

	entries, err := chaosFS.ReadDir(dir)
	if err == nil {
		t.Fatal("ReadDir unexpectedly succeeded")
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

func Test_ChaosFile_Write_Returns_Prefix_And_Error_When_Partial_Write_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")
	realFS := fs.NewReal()

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
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

func Test_ChaosFile_Write_Prefers_Full_Failure_Over_Partial_When_Both_Rates_Are_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	chaosFS := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
		WriteFailRate:    1.0,
		PartialWriteRate: 1.0,
	})

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	n, err := f.Write([]byte(testContentHello))
	if err == nil {
		t.Fatal("Write unexpectedly succeeded")
	}

	if got, want := n, 0; got != want {
		t.Fatalf("Write n=%d, want %d on full failure", got, want)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	stats := chaosFS.Stats()
	if got, want := stats.WriteFails, int64(1); got != want {
		t.Fatalf("WriteFails=%d, want %d", got, want)
	}

	if got, want := stats.PartialWrites, int64(0); got != want {
		t.Fatalf("PartialWrites=%d, want %d", got, want)
	}
}

func Test_ChaosFile_Write_Returns_Short_Write_Error_When_Short_Write_Rate_Is_Non_Zero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	const shortWriteRate = 0.10

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
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

			if got, want := fs.IsChaosErr(err), true; got != want {
				t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
			}

			continue
		}

		var pathErr *os.PathError
		if got, want := errors.As(err, &pathErr), true; got != want {
			t.Fatalf("Write err should be *os.PathError or io.ErrShortWrite, got %T (%v)", err, err)
		}
	}

	minExpected := int(float64(iterations) * (shortWriteRate - tolerance))

	maxExpected := int(float64(iterations) * (shortWriteRate + tolerance))
	if shortWrites < minExpected || shortWrites > maxExpected {
		t.Fatalf("io.ErrShortWrite count=%d, want in [%d,%d] (%.0f%% ± %.0f%%)", shortWrites, minExpected, maxExpected, shortWriteRate*100, tolerance*100)
	}
}

func Test_Chaos_Does_Not_Race_Or_Panic_When_Accessed_Concurrently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realFS := fs.NewReal()

	// Seed + non-zero rates to exercise RNG under contention.
	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
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
		mustWriteFile(t, p, []byte("test"))
	}

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			path := filepath.Join(dir, "file"+string(rune('0'+i))+".txt")
			for range 200 {
				_, _ = chaosFS.ReadFile(path)

				f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
				if err == nil {
					_, _ = f.Write([]byte("x"))
					_ = f.Close()
				}

				_, _ = chaosFS.Stat(path)
				_, _ = chaosFS.Exists(path)
				_, _ = chaosFS.ReadDir(dir)
				_ = chaosFS.RemoveAll(filepath.Join(dir, "missing"))
				_ = chaosFS.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
			}
		})
	}

	wg.Wait()
}

func Test_Chaos_Does_Not_Deadlock_When_Error_Is_Injected(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
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
			t.Fatal("write unexpectedly succeeded")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("write hung (possible deadlock in chaos error injection)")
	}
}

func Test_ChaosFile_Read_Does_Not_Skip_Bytes_When_Partial_Read_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 200) // > io.ReadAll initial buffer

	realFS := fs.NewReal()

	mustWriteFile(t, path, content)

	chaosFS := fs.NewChaos(realFS, 12345, &fs.ChaosConfig{
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

func Test_ChaosFile_Read_Prefers_Full_Failure_Over_Short_Read_When_Both_Rates_Are_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte(testContentHello))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		ReadFailRate:    1.0,
		PartialReadRate: 1.0,
	})

	f, err := chaosFS.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	n, err := f.Read(make([]byte, 5))
	if err == nil {
		t.Fatal("Read unexpectedly succeeded")
	}

	if got, want := n, 0; got != want {
		t.Fatalf("Read n=%d, want %d on full failure", got, want)
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr(err)=%t, want %t (err=%v)", got, want, err)
	}

	if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
		t.Fatalf("fs.File.Read should never inject ENOENT: %v", err)
	}

	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("Read err=%v, want EIO", err)
	}

	stats := chaosFS.Stats()
	if got, want := stats.ReadFails, int64(1); got != want {
		t.Fatalf("ReadFails=%d, want %d", got, want)
	}

	if got, want := stats.PartialReads, int64(0); got != want {
		t.Fatalf("PartialReads=%d, want %d", got, want)
	}
}

func Test_Chaos_Injected_Error_Is_Detectable_When_Stat_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file first so Stat has something to fail on
	err := os.WriteFile(path, []byte("test"), 0o644)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Chaos with 100% stat failure rate
	chaos := fs.NewChaos(fs.NewReal(), 12345, &fs.ChaosConfig{
		StatFailRate: 1.0,
	})

	// Get an injected error
	_, injectedErr := chaos.Stat(path)
	if injectedErr == nil {
		t.Fatal("Stat: expected error with 100% failure rate")
	}

	// Get a real error (from non-existent path, no injection)
	chaos.SetMode(fs.ChaosModeNoOp)

	_, realErr := chaos.Stat(filepath.Join(dir, "nonexistent"))
	if realErr == nil {
		t.Fatal("Stat(nonexistent): expected error")
	}

	// fs.IsChaosErr distinguishes injected from real errors
	if got, want := fs.IsChaosErr(injectedErr), true; got != want {
		t.Fatalf("fs.IsChaosErr(injected)=%t, want %t", got, want)
	}

	if got, want := fs.IsChaosErr(realErr), false; got != want {
		t.Fatalf("fs.IsChaosErr(real)=%t, want %t", got, want)
	}

	// errors.As extracts the underlying PathError
	var pathErr *os.PathError
	if got, want := errors.As(injectedErr, &pathErr), true; got != want {
		t.Fatalf("errors.As(injected, *os.PathError)=%t, want %t", got, want)
	}

	if got, want := pathErr.Path, path; got != want {
		t.Fatalf("PathError.Path=%q, want %q", got, want)
	}

	// errors.Is matches expected errno (EACCES or EIO for stat failures)
	if !errors.Is(injectedErr, syscall.EACCES) && !errors.Is(injectedErr, syscall.EIO) {
		t.Fatalf("errors.Is: expected EACCES or EIO, got %v", injectedErr)
	}

	// errors.Is matches sentinel when applicable
	if errors.Is(injectedErr, syscall.EACCES) {
		if got, want := errors.Is(injectedErr, iofs.ErrPermission), true; got != want {
			t.Fatalf("errors.Is(injected, ErrPermission)=%t, want %t", got, want)
		}
	}
}

func Test_Chaos_RemoveAll_Succeeds_When_Path_Missing_And_Mode_Is_NoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	// Real os.RemoveAll treats a missing path as success.
	err := os.RemoveAll(path)
	if err != nil {
		t.Fatalf("os.RemoveAll: %v", err)
	}

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		RemoveFailRate: 1.0, // Would inject if allowed.
	})
	chaosFS.SetMode(fs.ChaosModeNoOp)

	err = chaosFS.RemoveAll(path)
	if err != nil {
		t.Fatalf("Chaos.RemoveAll: %v", err)
	}
}

func Test_Chaos_RemoveAll_Injects_Error_When_Path_Missing_And_Remove_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		RemoveFailRate: 1.0, // Always inject.
	})

	err := chaosFS.RemoveAll(path)
	if err == nil {
		t.Fatal("Chaos.RemoveAll unexpectedly succeeded")
	}

	if os.IsNotExist(err) {
		t.Fatalf("Chaos.RemoveAll should never inject ENOENT: %v", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}
}

func Test_ChaosFile_Write_Does_Not_Modify_File_When_Write_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := fs.NewReal()

	mustWriteFile(t, path, []byte("old"))

	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{
		WriteFailRate: 1.0, // Always fail.
	})

	f, err := chaosFS.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	_, err = f.Write([]byte("new"))
	if err == nil {
		t.Fatal("Write unexpectedly succeeded")
	}

	got, err := realFS.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if got, want := string(got), "old"; got != want {
		t.Fatalf("Write failure must not modify file: got %q, want %q", got, want)
	}
}

func Test_IsChaosErr_Returns_True_When_Error_Is_Injected(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	chaosFS := fs.NewChaos(realFS, 0, &fs.ChaosConfig{WriteFailRate: 1.0})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	f, err := chaosFS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		_, err = f.Write([]byte("x"))
		_ = f.Close()
	}

	if err == nil {
		t.Fatal("expected error")
	}

	if got, want := fs.IsChaosErr(err), true; got != want {
		t.Fatalf("fs.IsChaosErr=%v, want %v (err=%v)", got, want, err)
	}
}

func Test_IsChaosErr_Returns_False_When_Error_Is_Real(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()

	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")

	_, err := realFS.Open(path)
	if err == nil {
		t.Fatal("expected error")
	}

	if got, want := errors.Is(err, syscall.ENOENT), true; got != want {
		t.Fatalf("expected ENOENT, got %v", err)
	}

	if got, want := fs.IsChaosErr(err), false; got != want {
		t.Fatalf("fs.IsChaosErr=%v, want %v (err=%v)", got, want, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()

	err := os.WriteFile(path, data, 0o644)
	if err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
}

func writeFileOnce(fileSystem fs.FS, path string, data []byte) error {
	f, err := fileSystem.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	n, writeErr := f.Write(data)
	closeErr := f.Close()

	if writeErr != nil {
		return fmt.Errorf("write: %w", writeErr)
	}

	if n != len(data) {
		return io.ErrShortWrite
	}

	if closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}

	return nil
}

// =============================================================================
// Chaos Trace Tests
//
// These tests verify Chaos tracing captures operations with injection details.
// =============================================================================

func Test_ChaosTrace_Is_Empty_When_No_Ops_Performed(t *testing.T) {
	t.Parallel()

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{TraceCapacity: 100})

	if got := chaos.Trace(); got != "" {
		t.Fatalf("Trace(): want empty string, got %q", got)
	}
}

func Test_ChaosTrace_Is_Empty_When_Trace_Capacity_Is_Zero(t *testing.T) {
	t.Parallel()

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{TraceCapacity: 0})
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	f, err := chaos.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}

	_, err = f.Write([]byte(testContentHello))
	if err != nil {
		t.Fatalf("Write(%q): %v", path, err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}

	if got := chaos.Trace(); got != "" {
		t.Fatalf("Trace() with TraceCapacity=0: want empty string, got %q", got)
	}

	if got := chaos.TraceEvents(); got != nil {
		t.Fatalf("TraceEvents() with TraceCapacity=0: want nil, got %v", got)
	}
}

func Test_ChaosTrace_Drops_Oldest_Events_When_Capacity_Exceeded(t *testing.T) {
	t.Parallel()

	t.Run("DefaultCapacityIsZero", func(t *testing.T) {
		t.Parallel()

		chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{})
		dir := t.TempDir()

		for i := range 10 {
			path := filepath.Join(dir, fmt.Sprintf("exists-%03d", i))
			_, _ = chaos.Exists(path)
		}

		if got := chaos.Trace(); got != "" {
			t.Fatalf("Trace() with default capacity: want empty, got %q", got)
		}
	})

	t.Run("CustomCapacity", func(t *testing.T) {
		t.Parallel()

		chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{TraceCapacity: 3})
		chaos.SetMode(fs.ChaosModeNoOp)

		dir := t.TempDir()

		paths := []string{
			filepath.Join(dir, "missing-1"),
			filepath.Join(dir, "missing-2"),
			filepath.Join(dir, "missing-3"),
			filepath.Join(dir, "missing-4"),
			filepath.Join(dir, "missing-5"),
		}

		for _, p := range paths {
			_, _ = chaos.Exists(p)
		}

		events := chaos.TraceEvents()
		if got, want := len(events), 3; got != want {
			t.Fatalf("TraceEvents() count: want %d, got %d", want, got)
		}

		trace := chaos.Trace()

		// Should not contain oldest entries
		for _, shouldNotContain := range paths[:2] {
			if strings.Contains(trace, fmt.Sprintf("path=%q", shouldNotContain)) {
				t.Fatalf("Trace() should not include %q\ntrace:\n%s", shouldNotContain, trace)
			}
		}

		// Should contain newest entries
		for _, shouldContain := range paths[2:] {
			if !strings.Contains(trace, fmt.Sprintf("path=%q", shouldContain)) {
				t.Fatalf("Trace() should include %q\ntrace:\n%s", shouldContain, trace)
			}
		}
	})
}

func Test_ChaosTrace_Records_Ops_In_Order_When_Multiple_Ops_Performed(t *testing.T) {
	t.Parallel()

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{TraceCapacity: 100})
	chaos.SetMode(fs.ChaosModeNoOp) // Don't inject faults for this test

	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.txt")
	subdir := filepath.Join(dir, "sub")
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")

	var f, f2, f3 fs.File

	steps := []struct {
		op  string
		run func() error
	}{
		{op: "exists", run: func() error {
			_, err := chaos.Exists(missing)
			if err != nil {
				return fmt.Errorf("exists: %w", err)
			}

			return nil
		}},
		{op: "mkdirall", run: func() error {
			err := chaos.MkdirAll(subdir, 0o755)
			if err != nil {
				return fmt.Errorf("mkdirall: %w", err)
			}

			return nil
		}},
		{op: "create", run: func() error {
			var err error

			f, err = chaos.Create(a)
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}

			return nil
		}},
		{op: "file.write", run: func() error {
			_, err := f.Write([]byte(testContentHello))
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}

			return nil
		}},
		{op: "file.sync", run: func() error {
			err := f.Sync()
			if err != nil {
				return fmt.Errorf("sync: %w", err)
			}

			return nil
		}},
		{op: "file.stat", run: func() error {
			_, err := f.Stat()
			if err != nil {
				return fmt.Errorf("stat: %w", err)
			}

			return nil
		}},
		{op: "file.seek", run: func() error {
			_, err := f.Seek(0, io.SeekStart)
			if err != nil {
				return fmt.Errorf("seek: %w", err)
			}

			return nil
		}},
		{op: "file.read", run: func() error {
			_, err := f.Read(make([]byte, 5))
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}

			return nil
		}},
		{op: "file.close", run: func() error {
			err := f.Close()
			if err != nil {
				return fmt.Errorf("close: %w", err)
			}

			return nil
		}},
		{op: "readfile", run: func() error {
			_, err := chaos.ReadFile(a)
			if err != nil {
				return fmt.Errorf("readfile: %w", err)
			}

			return nil
		}},
		{op: "readdir", run: func() error {
			_, err := chaos.ReadDir(dir)
			if err != nil {
				return fmt.Errorf("readdir: %w", err)
			}

			return nil
		}},
		{op: "stat", run: func() error {
			_, err := chaos.Stat(a)
			if err != nil {
				return fmt.Errorf("stat: %w", err)
			}

			return nil
		}},
		{op: "open", run: func() error {
			var err error

			f2, err = chaos.Open(a)
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}

			return nil
		}},
		{op: "file.close", run: func() error {
			err := f2.Close()
			if err != nil {
				return fmt.Errorf("close: %w", err)
			}

			return nil
		}},
		{op: "create", run: func() error {
			var err error

			f3, err = chaos.Create(b)
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}

			return nil
		}},
		{op: "file.write", run: func() error {
			_, err := f3.Write([]byte("x"))
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}

			return nil
		}},
		{op: "file.close", run: func() error {
			err := f3.Close()
			if err != nil {
				return fmt.Errorf("close: %w", err)
			}

			return nil
		}},
		{op: "rename", run: func() error { return chaos.Rename(b, c) }},
		{op: "remove", run: func() error { return chaos.Remove(c) }},
		{op: "removeall", run: func() error { return chaos.RemoveAll(subdir) }},
	}

	wantOps := make([]string, 0, len(steps))
	for _, s := range steps {
		wantOps = append(wantOps, s.op)
	}

	for _, s := range steps {
		err := s.run()
		if err != nil {
			t.Fatalf("%s: %v", s.op, err)
		}
	}

	events := chaos.TraceEvents()
	if got, want := len(events), len(wantOps); got != want {
		t.Fatalf("TraceEvents() count: want %d, got %d\ntrace:\n%s", want, got, chaos.Trace())
	}

	for i, e := range events {
		if got, want := e.Op, wantOps[i]; got != want {
			t.Fatalf("events[%d].Op: want %q, got %q\ntrace:\n%s", i, want, got, chaos.Trace())
		}
	}
}

func Test_ChaosTrace_Records_Injected_Fault_When_Open_Fail_Rate_Is_One(t *testing.T) {
	t.Parallel()

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
		TraceCapacity: 100,
		OpenFailRate:  1.0, // Always inject open failure
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	_, err := chaos.Open(path)
	if err == nil {
		t.Fatalf("Open(%q): want error, got nil", path)
	}

	events := chaos.TraceEvents()
	if len(events) != 1 {
		t.Fatalf("TraceEvents() count: want 1, got %d", len(events))
	}

	e := events[0]
	if got, want := e.Op, "open"; got != want {
		t.Fatalf("event.Op: want %q, got %q", want, got)
	}

	if got, want := e.Injected, true; got != want {
		t.Fatalf("event.Injected: want %t, got %t", want, got)
	}

	if got, want := e.Kind, "fail"; got != want {
		t.Fatalf("event.Kind: want %q, got %q", want, got)
	}

	if e.Err == nil {
		t.Fatal("event.Err: want non-nil, got nil")
	}

	// Check trace string format
	trace := chaos.Trace()
	if !strings.Contains(trace, "[CHAOS:fail]") {
		t.Fatalf("Trace() should contain '[CHAOS:fail]'\ntrace: %s", trace)
	}

	if !strings.Contains(trace, "errno=") {
		t.Fatalf("Trace() should contain 'errno='\ntrace: %s", trace)
	}
}

func Test_ChaosTrace_Records_Short_Read_When_Partial_Read_Rate_Is_One(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	mustWriteFile(t, path, []byte("hello world"))

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
		TraceCapacity:   100,
		PartialReadRate: 1.0, // Always short read
	})

	f, err := chaos.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer f.Close()

	buf := make([]byte, 100)
	n, err := f.Read(buf)

	// Short read with nil error is valid io.Reader behavior
	if err != nil {
		t.Fatalf("Read: want nil error for short read, got %v", err)
	}

	if n >= 100 {
		t.Fatalf("Read n=%d, want < 100 (short read)", n)
	}

	events := chaos.TraceEvents()

	// Find the read event (skip open event)
	var readEvent *fs.TraceEvent

	for i := range events {
		if events[i].Op == "file.read" {
			readEvent = &events[i]

			break
		}
	}

	if readEvent == nil {
		t.Fatalf("no file.read event in trace:\n%s", chaos.Trace())
	}

	if got, want := readEvent.Injected, true; got != want {
		t.Fatalf("readEvent.Injected: want %t, got %t", want, got)
	}

	if got, want := readEvent.Kind, "short_read"; got != want {
		t.Fatalf("readEvent.Kind: want %q, got %q", want, got)
	}

	// Short reads return nil error
	if readEvent.Err != nil {
		t.Fatalf("readEvent.Err: want nil, got %v", readEvent.Err)
	}

	// Trace should show the short read
	trace := chaos.Trace()
	if !strings.Contains(trace, "[CHAOS:short_read]") {
		t.Fatalf("Trace() should contain '[CHAOS:short_read]'\ntrace: %s", trace)
	}

	if !strings.Contains(trace, "cutoff=") {
		t.Fatalf("Trace() should contain 'cutoff='\ntrace: %s", trace)
	}
}

func Test_ChaosTrace_Records_Partial_Write_When_Partial_Write_Rate_Is_One(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{
		TraceCapacity:    100,
		PartialWriteRate: 1.0, // Always partial write
		ShortWriteRate:   0.0, // No short writes, always errno
	})

	f, err := chaos.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	defer f.Close()

	_, err = f.Write([]byte("hello world"))
	if err == nil {
		t.Fatal("Write: want error for partial write, got nil")
	}

	events := chaos.TraceEvents()

	var writeEvent *fs.TraceEvent

	for i := range events {
		if events[i].Op == "file.write" {
			writeEvent = &events[i]

			break
		}
	}

	if writeEvent == nil {
		t.Fatalf("no file.write event in trace:\n%s", chaos.Trace())
	}

	if got, want := writeEvent.Injected, true; got != want {
		t.Fatalf("writeEvent.Injected: want %t, got %t", want, got)
	}

	if got, want := writeEvent.Kind, "partial_write"; got != want {
		t.Fatalf("writeEvent.Kind: want %q, got %q", want, got)
	}

	trace := chaos.Trace()
	if !strings.Contains(trace, "[CHAOS:partial_write]") {
		t.Fatalf("Trace() should contain '[CHAOS:partial_write]'\ntrace: %s", trace)
	}
}

func Test_ChaosTrace_Records_Passthrough_Ok_When_Mode_Is_NoOp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	chaos := fs.NewChaos(fs.NewReal(), 0, &fs.ChaosConfig{TraceCapacity: 100})
	chaos.SetMode(fs.ChaosModeNoOp)

	f, err := chaos.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}

	_, err = f.Write([]byte(testContentHello))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := chaos.TraceEvents()

	for _, e := range events {
		if e.Injected {
			t.Fatalf("event.Injected should be false for passthrough: %+v", e)
		}

		if e.Kind != "ok" && e.Err != nil {
			t.Fatalf("event.Kind should be 'ok' for successful passthrough: %+v", e)
		}
	}

	trace := chaos.Trace()
	if !strings.Contains(trace, " ok") {
		t.Fatalf("Trace() should contain ' ok' for passthrough\ntrace: %s", trace)
	}
}

func Test_TraceEvent_Formats_Correctly_When_Fields_Are_Set(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event fs.TraceEvent
		want  string
	}{
		{
			name: "ok no attrs",
			event: fs.TraceEvent{
				Seq:  1,
				Op:   "open",
				Path: "/tmp/file.txt",
				Kind: "ok",
			},
			want: `#1 open path="/tmp/file.txt" ok`,
		},
		{
			name: "injected fail with error",
			event: fs.TraceEvent{
				Seq:      2,
				Op:       "readfile",
				Path:     "/tmp/file.txt",
				Err:      errors.New("permission denied"),
				Kind:     "fail",
				Injected: true,
				Attrs:    []fs.TraceAttr{{"errno", "EACCES"}},
			},
			want: `#2 [CHAOS:fail] readfile path="/tmp/file.txt" errno=EACCES err=permission denied`,
		},
		{
			name: "injected short read (nil error)",
			event: fs.TraceEvent{
				Seq:      3,
				Op:       "file.read",
				Path:     "/tmp/data.bin",
				Kind:     "short_read",
				Injected: true,
				Attrs:    []fs.TraceAttr{{"n", "50"}, {"cutoff", "50"}, {"requested", "100"}},
			},
			want: `#3 [CHAOS:short_read] file.read path="/tmp/data.bin" n=50 cutoff=50 requested=100`,
		},
		{
			name: "real error (not injected)",
			event: fs.TraceEvent{
				Seq:      4,
				Op:       "open",
				Path:     "/tmp/missing.txt",
				Err:      errors.New("no such file"),
				Kind:     "fail",
				Injected: false,
			},
			want: `#4 open path="/tmp/missing.txt" fail err=no such file`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.event.String()
			if got != tt.want {
				t.Fatalf("fs.TraceEvent.String():\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Seed Determinism Tests
//
// These tests verify that the same seed produces identical fault sequences
// when operations are called in the same order.
// =============================================================================

func Test_Chaos_Same_Seed_Produces_Identical_Partial_Read_Length(t *testing.T) {
	t.Parallel()

	const seed = 98765

	config := fs.ChaosConfig{PartialReadRate: 1.0}
	content := []byte("hello world this is test content for determinism")

	run := func() int {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		mustWriteFile(t, path, content)

		chaos := fs.NewChaos(fs.NewReal(), seed, &config)

		data, err := chaos.ReadFile(path)
		if err == nil {
			t.Fatal("expected partial read error")
		}

		return len(data)
	}

	first := run()
	second := run()
	third := run()

	if first != second || second != third {
		t.Fatalf("same seed produced different lengths: %d, %d, %d", first, second, third)
	}
}

func Test_Chaos_Same_Seed_Produces_Identical_Error_Types(t *testing.T) {
	t.Parallel()

	const seed = 22222

	config := fs.ChaosConfig{WriteFailRate: 1.0}

	run := func() syscall.Errno {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")

		chaos := fs.NewChaos(fs.NewReal(), seed, &config)

		f, err := chaos.Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		_, err = f.Write([]byte("test"))
		_ = f.Close()

		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("expected PathError, got %T", err)
		}

		var errno syscall.Errno

		ok := errors.As(pathErr.Err, &errno)
		if !ok {
			t.Fatalf("expected syscall.Errno, got %T", pathErr.Err)
		}

		return errno
	}

	first := run()
	second := run()
	third := run()

	if first != second || second != third {
		t.Fatalf("same seed produced different errnos: %v, %v, %v", first, second, third)
	}
}

func Test_Chaos_Different_Seeds_Produce_Different_Results(t *testing.T) {
	t.Parallel()

	config := fs.ChaosConfig{PartialReadRate: 1.0}
	content := []byte("hello world this is a longer test content string for variety")

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	mustWriteFile(t, path, content)

	realFS := fs.NewReal()

	// Run with many different seeds and collect unique lengths
	seen := make(map[int]bool)

	for seed := range int64(100) {
		chaos := fs.NewChaos(realFS, seed, &config)
		data, _ := chaos.ReadFile(path)
		seen[len(data)] = true
	}

	// With 100 seeds and ~60 possible lengths, we should see variety
	if len(seen) < 5 {
		t.Fatalf("expected variety in partial read lengths, only got %d unique values", len(seen))
	}
}

func Test_Chaos_Same_Seeds_Produce_Identical_Results_With_Random_Ops(t *testing.T) {
	t.Parallel()

	const opSeed = 11111 // controls which operations are performed

	const chaosSeed = 33333 // controls fault injection

	config := fs.ChaosConfig{
		ReadFailRate:     0.3,
		WriteFailRate:    0.3,
		OpenFailRate:     0.3,
		PartialReadRate:  0.3,
		PartialWriteRate: 0.3,
	}

	type result struct {
		op      string
		failed  bool
		n       int    // bytes written/read
		content string // actual content read or written to disk
	}

	run := func() []result {
		dir := t.TempDir()
		realFS := fs.NewReal()
		opRng := rand.New(rand.NewPCG(uint64(opSeed), uint64(opSeed)))
		chaos := fs.NewChaos(realFS, chaosSeed, &config)

		var results []result

		existingContent := "test content"

		// Pre-create some files for read operations
		for i := range 5 {
			path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", i))
			mustWriteFile(t, path, []byte(existingContent))
		}

		for i := range 30 {
			op := opRng.IntN(4) // 0=create+write, 1=read, 2=stat, 3=remove

			switch op {
			case 0: // create and write
				path := filepath.Join(dir, fmt.Sprintf("new%d.txt", i))
				writeData := []byte("data")

				f, err := chaos.Create(path)
				if err != nil {
					results = append(results, result{"create", true, 0, ""})

					continue
				}

				n, writeErr := f.Write(writeData)
				_ = f.Close()

				// Read back what's actually on disk
				var onDisk string

				data, readErr := realFS.ReadFile(path)
				if readErr == nil {
					onDisk = string(data)
				}

				results = append(results, result{"write", writeErr != nil, n, onDisk})

			case 1: // read existing file
				path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", opRng.IntN(5)))
				data, err := chaos.ReadFile(path)
				results = append(results, result{"read", err != nil, len(data), string(data)})

			case 2: // stat existing file
				path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", opRng.IntN(5)))
				info, err := chaos.Stat(path)

				size := 0
				if info != nil {
					size = int(info.Size())
				}

				results = append(results, result{"stat", err != nil, size, ""})

			case 3: // remove (may or may not exist)
				path := filepath.Join(dir, fmt.Sprintf("new%d.txt", opRng.IntN(i+1)))
				err := chaos.Remove(path)
				// Only count as chaos failure if it's a chaos error
				results = append(results, result{"remove", fs.IsChaosErr(err), 0, ""})
			}
		}

		return results
	}

	first := run()
	second := run()

	if len(first) != len(second) {
		t.Fatalf("different result lengths: %d vs %d", len(first), len(second))
	}

	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("diverged at operation %d:\n  first:  %+v\n  second: %+v", i, first[i], second[i])
		}
	}
}
