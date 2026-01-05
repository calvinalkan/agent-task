package fs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type fakeTB struct {
	failed   bool
	fatalMsg string
}

func (t *fakeTB) Helper()             {}
func (t *fakeTB) Cleanup(func())      {}
func (t *fakeTB) Failed() bool        { return t.failed }
func (t *fakeTB) Logf(string, ...any) {}

func (t *fakeTB) Fatalf(format string, args ...any) {
	t.failed = true
	t.fatalMsg = fmt.Sprintf(format, args...)

	panic("fatal")
}

func TestIsInjected_MarksChaosErrors(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{WriteFailRate: 1.0})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := chaosFS.WriteFileAtomic(path, []byte("x"), 0o644)
	if err == nil {
		t.Fatalf("expected error")
	}

	if got, want := IsInjected(err), true; got != want {
		t.Fatalf("IsInjected=%v, want %v (err=%v)", got, want, err)
	}
}

func TestIsInjected_DoesNotMarkRealErrors(t *testing.T) {
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

	if got, want := IsInjected(err), false; got != want {
		t.Fatalf("IsInjected=%v, want %v (err=%v)", got, want, err)
	}
}

func TestStrictTestFS_AllowsInjectedErrors(t *testing.T) {
	tb := &fakeTB{}

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{WriteFailRate: 1.0})
	chaosFS.SetMode(ChaosModeInject)

	strict := NewStrictTestFS(tb, chaosFS)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StrictTestFS should not fatal on injected errors")
		}
	}()

	err := strict.WriteFileAtomic(path, []byte("x"), 0o644)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestStrictTestFS_FailsOnRealErrors(t *testing.T) {
	tb := &fakeTB{}
	realFS := NewReal()
	strict := NewStrictTestFS(tb, realFS)

	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected StrictTestFS to fatal")
		}

		if tb.fatalMsg == "" {
			t.Fatalf("expected fatal message")
		}

		if got, want := tb.failed, true; got != want {
			t.Fatalf("failed=%v, want %v", got, want)
		}

		if got, want := strings.Contains(tb.fatalMsg, "fs trace:"), true; got != want {
			t.Fatalf("expected fatal message to include trace, got: %q", tb.fatalMsg)
		}
	}()

	_, _ = strict.Open(path)
}
