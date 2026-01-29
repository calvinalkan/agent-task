package mddb

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func Test_Wrap_Formats_Correctly_When_Various_Inputs(t *testing.T) {
	t.Parallel()

	base := errors.New("something failed")
	pathErr := &os.PathError{Op: "open", Path: "/abs/path.md", Err: errors.New("permission denied")}

	tests := []struct {
		name string
		err  error
		want string
	}{
		// Basic wrapping
		{
			name: "nil error",
			err:  wrap(nil),
			want: "",
		},
		{
			name: "bare error",
			err:  wrap(base),
			want: "something failed",
		},
		{
			name: "with ID",
			err:  wrap(base, withID("doc1")),
			want: "something failed (doc_id=doc1)",
		},
		{
			name: "with path",
			err:  wrap(base, withPath("foo.md")),
			want: "something failed (doc_path=foo.md)",
		},
		{
			name: "with ID and path",
			err:  wrap(base, withID("doc1"), withPath("foo.md")),
			want: "something failed (doc_id=doc1 doc_path=foo.md)",
		},

		// PathError handling - no special casing, shows full fs path
		{
			name: "PathError bare",
			err:  wrap(pathErr),
			want: "open /abs/path.md: permission denied",
		},
		{
			name: "PathError with ID",
			err:  wrap(pathErr, withID("doc1")),
			want: "open /abs/path.md: permission denied (doc_id=doc1)",
		},
		{
			name: "PathError with path",
			err:  wrap(pathErr, withPath("rel/path.md")),
			want: "open /abs/path.md: permission denied (doc_path=rel/path.md)",
		},
		{
			name: "PathError with ID and path",
			err:  wrap(pathErr, withID("doc1"), withPath("rel/path.md")),
			want: "open /abs/path.md: permission denied (doc_id=doc1 doc_path=rel/path.md)",
		},

		// Chained *Error - inherits context, no duplication
		{
			name: "wrap(*Error) no opts returns same",
			err:  wrap(wrap(base, withID("x"))),
			want: "something failed (doc_id=x)",
		},
		{
			name: "wrap(*Error) adds ID",
			err:  wrap(wrap(base, withPath("a.md")), withID("doc1")),
			want: "something failed (doc_id=doc1 doc_path=a.md)",
		},
		{
			name: "wrap(*Error) adds path",
			err:  wrap(wrap(base, withID("doc1")), withPath("a.md")),
			want: "something failed (doc_id=doc1 doc_path=a.md)",
		},
		{
			name: "wrap(*Error) overrides ID",
			err:  wrap(wrap(base, withID("old")), withID("new")),
			want: "something failed (doc_id=new)",
		},
		{
			name: "wrap(*Error) overrides path",
			err:  wrap(wrap(base, withPath("old.md")), withPath("new.md")),
			want: "something failed (doc_path=new.md)",
		},

		// fmt.Errorf in chain - preserved
		{
			name: "fmt.Errorf then wrap",
			err:  wrap(fmt.Errorf("query failed: %w", base), withID("doc1")),
			want: "query failed: something failed (doc_id=doc1)",
		},
		{
			name: "fmt.Errorf wrapping PathError then wrap",
			err:  wrap(fmt.Errorf("reading: %w", pathErr), withID("doc1")),
			want: "reading: open /abs/path.md: permission denied (doc_id=doc1)",
		},
		{
			name: "wrap then fmt.Errorf then wrap",
			err:  wrap(fmt.Errorf("outer: %w", wrap(base, withPath("inner.md"))), withID("doc1")),
			want: "outer: something failed (doc_path=inner.md) (doc_id=doc1)",
		},

		// Deep chains
		{
			name: "three level wrap",
			err:  wrap(wrap(wrap(base, withPath("a.md")), withID("id1")), withID("id2")),
			want: "something failed (doc_id=id2 doc_path=a.md)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.err == nil {
				if tt.want != "" {
					t.Errorf("got nil, want %q", tt.want)
				}

				return
			}

			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_Wrap_Supports_Unwrap_When_Using_Errors_Is(t *testing.T) {
	t.Parallel()

	base := errors.New("root cause")
	wrapped := wrap(base, withID("doc1"))

	if !errors.Is(wrapped, base) {
		t.Error("errors.Is should find base error")
	}

	var mErr *Error
	if !errors.As(wrapped, &mErr) {
		t.Error("errors.As should find *Error")
	}

	if mErr.ID != "doc1" {
		t.Errorf("ID = %q, want %q", mErr.ID, "doc1")
	}
}

func Test_Wrap_Supports_Unwrap_When_Inner_Is_PathError(t *testing.T) {
	t.Parallel()

	pathErr := &os.PathError{Op: "open", Path: "/tmp/x", Err: errors.New("denied")}
	wrapped := wrap(pathErr, withID("doc1"))

	var pe *os.PathError
	if !errors.As(wrapped, &pe) {
		t.Error("errors.As should find *os.PathError")
	}

	if pe.Path != "/tmp/x" {
		t.Errorf("PathError.Path = %q, want %q", pe.Path, "/tmp/x")
	}
}
