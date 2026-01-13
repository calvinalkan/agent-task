package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func Test_RealFS_Exists_Returns_False_When_Path_Does_Not_Exist(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	dir := t.TempDir()

	exists, err := realFS.Exists(filepath.Join(dir, "does-not-exist.txt"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if exists {
		t.Fatal("exists=true, want=false")
	}
}

func Test_RealFS_Exists_Returns_True_When_Path_Is_A_File(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")

	// Create file
	err := os.WriteFile(path, []byte(testContentHello), 0o644)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := realFS.Exists(path)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("exists=false, want=true")
	}
}

func Test_RealFS_Exists_Returns_True_When_Path_Is_A_Directory(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	err := os.MkdirAll(subdir, 0o755)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := realFS.Exists(subdir)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("exists=false, want=true")
	}
}

func Test_RealFS_WriteFile_Creates_New_File(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	err := realFS.WriteFile(path, []byte(testContentHello), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != testContentHello {
		t.Fatalf("content=%q, want %q", got, testContentHello)
	}
}

func Test_RealFS_WriteFile_Truncates_Existing_File(t *testing.T) {
	t.Parallel()

	realFS := fs.NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")

	// Create file with initial content
	err := os.WriteFile(path, []byte("original content here"), 0o644)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Overwrite with shorter content
	err = realFS.WriteFile(path, []byte("short"), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != "short" {
		t.Fatalf("content=%q, want %q", got, "short")
	}
}
