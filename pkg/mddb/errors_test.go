package mddb

import (
	"errors"
	"testing"
)

func Test_WithContext_Formats_When_ID_And_Path_Provided(t *testing.T) {
	t.Parallel()

	err := withContext(errors.New("something failed"), "doc1", "path.md")
	if err == nil {
		t.Fatal("expected error")
	}

	got := err.Error()

	want := "something failed (doc_id=doc1 doc_path=path.md)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func Test_WithContext_Unwraps_When_Sentinel_Error(t *testing.T) {
	t.Parallel()

	err := withContext(ErrNotFound, "doc1", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("errors.Is should find ErrNotFound")
	}

	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatal("errors.As should find *Error")
	}

	if mErr.ID != "doc1" {
		t.Fatalf("ID = %q, want %q", mErr.ID, "doc1")
	}
}
