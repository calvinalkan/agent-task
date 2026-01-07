package fs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test_RealFS_Exists_Returns_False_When_Path_Does_Not_Exist(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()

	exists, err := fs.Exists(filepath.Join(dir, "does-not-exist.txt"))

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, false; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}

func Test_RealFS_Exists_Returns_True_When_Path_Is_A_File(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")

	// Create file
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := fs.Exists(path)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, true; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}

func Test_RealFS_Exists_Returns_True_When_Path_Is_A_Directory(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := fs.Exists(subdir)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, true; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}
