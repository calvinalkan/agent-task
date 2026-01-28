package frontmatter_test

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/frontmatter"
)

// Contract: enforce the restricted YAML subset so store parsing stays deterministic.
func Test_FrontmatterParser_ReturnsValues_When_SubsetValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		fm    string
		tail  string
		check func(t *testing.T, fm frontmatter.Frontmatter)
	}{
		{
			name: "scalar values",
			fm: strings.Join([]string{
				"id: 018f5f25-7e7d-7f0a-8c5c-123456789abc",
				"schema_version: 1",
				"status: open",
				"priority: 2",
				"flag: true",
				"owner: 'ops team'",
				"note: \"\"",
				"empty: ''",
			}, "\n"),
			tail: "# Title\n",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireScalarString(t, fm, "id", "018f5f25-7e7d-7f0a-8c5c-123456789abc")
				requireScalarInt(t, fm, "schema_version", 1)
				requireScalarString(t, fm, "status", "open")
				requireScalarInt(t, fm, "priority", 2)
				requireScalarBool(t, fm, "flag", true)
				requireScalarString(t, fm, "owner", "ops team")
				requireScalarString(t, fm, "note", "")
				requireScalarString(t, fm, "empty", "")
			},
		},
		{
			name: "lists and objects",
			fm: strings.Join([]string{
				"blocked-by:",
				"  - abc",
				"  - def",
				"",
				"meta:",
				"  owner: alice",
				"  retries: 3",
				"  urgent: false",
				"tags: [ops, \"on-call\"]",
			}, "\n"),
			tail: "body text\n",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireList(t, fm, "blocked-by", []string{"abc", "def"})
				requireList(t, fm, "tags", []string{"ops", "on-call"})
				requireObject(t, fm, "meta", map[string]frontmatter.Scalar{
					"owner":   {Kind: frontmatter.ScalarString, String: "alice"},
					"retries": {Kind: frontmatter.ScalarInt, Int: 3},
					"urgent":  {Kind: frontmatter.ScalarBool, Bool: false},
				})
			},
		},
		{
			name: "empty list",
			fm:   "blocked-by: []",
			tail: "",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireList(t, fm, "blocked-by", []string{})
			},
		},
		{
			name: "list followed by key",
			fm: strings.Join([]string{
				"blocked-by:",
				"  - abc",
				"status: open",
			}, "\n"),
			tail: "",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireList(t, fm, "blocked-by", []string{"abc"})
				requireScalarString(t, fm, "status", "open")
			},
		},
		{
			name: "object followed by key",
			fm: strings.Join([]string{
				"meta:",
				"  owner: alice",
				"status: open",
			}, "\n"),
			tail: "",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireObject(t, fm, "meta", map[string]frontmatter.Scalar{
					"owner": {Kind: frontmatter.ScalarString, String: "alice"},
				})
				requireScalarString(t, fm, "status", "open")
			},
		},
		{
			name: "negative integer scalar",
			fm: strings.Join([]string{
				"delta: -12",
			}, "\n"),
			tail: "",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireScalarInt(t, fm, "delta", -12)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := wrapFrontmatter(tc.fm, tc.tail)

			fm, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
			if err != nil {
				t.Fatalf("parse frontmatter: %v", err)
			}

			if string(tail) != tc.tail {
				t.Fatalf("tail mismatch: got %q want %q", string(tail), tc.tail)
			}

			tc.check(t, fm)
		})
	}
}

// Contract: frontmatter marshal keeps delimiter defaults and key ordering stable.
func Test_Frontmatter_MarshalYAML_Returns_Delimited_Output_When_Defaults(t *testing.T) {
	t.Parallel()

	fm := frontmatter.Frontmatter{
		"type":           {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "task"}},
		"id":             {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "123"}},
		"schema_version": {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarInt, Int: 1}},
		"status":         {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "open"}},
		"priority":       {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarInt, Int: 2}},
	}

	got, err := fm.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	want := strings.Join([]string{
		"---",
		"id: 123",
		"schema_version: 1",
		"priority: 2",
		"status: open",
		"type: task",
		"---",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("yaml output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Contract: frontmatter marshal can omit delimiters when requested.
func Test_Frontmatter_MarshalYAML_Omits_Delimiters_When_Option_Set(t *testing.T) {
	t.Parallel()

	fm := frontmatter.Frontmatter{
		"id":             {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "123"}},
		"schema_version": {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarInt, Int: 1}},
		"status":         {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "open"}},
	}

	got, err := fm.MarshalYAML(frontmatter.WithYAMLDelimiters(false))
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	want := strings.Join([]string{
		"id: 123",
		"schema_version: 1",
		"status: open",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("yaml output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Contract: frontmatter marshal respects custom key order.
func Test_Frontmatter_MarshalYAML_Uses_Custom_Key_Order_When_Option_Set(t *testing.T) {
	t.Parallel()

	fm := frontmatter.Frontmatter{
		"type":           {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "task"}},
		"id":             {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "123"}},
		"schema_version": {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarInt, Int: 1}},
		"status":         {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarString, String: "open"}},
		"priority":       {Kind: frontmatter.ValueScalar, Scalar: frontmatter.Scalar{Kind: frontmatter.ScalarInt, Int: 2}},
	}

	got, err := fm.MarshalYAML(frontmatter.WithKeyOrder([]string{"id", "schema_version", "type", "status"}))
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	// priority is omitted because it's not in the key order
	want := strings.Join([]string{
		"---",
		"id: 123",
		"schema_version: 1",
		"type: task",
		"status: open",
		"---",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("yaml output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Contract: invalid shapes should fail fast instead of being silently coerced.
func Test_FrontmatterParser_ReturnsError_When_ShapeInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		fm   string
	}{
		{
			name: "duplicate keys",
			fm:   "id: a\nid: b",
		},
		{
			name: "missing colon",
			fm:   "id a",
		},
		{
			name: "whitespace in key",
			fm:   "bad key: value",
		},
		{
			name: "unexpected indentation",
			fm:   " id: a",
		},
		{
			name: "missing block contents",
			fm:   "blocked-by:\nstatus: open",
		},
		{
			name: "tabs are not allowed",
			fm:   "meta:\n\tkey: value",
		},
		{
			name: "object value required",
			fm:   "meta:\n  key:",
		},
		{
			name: "object entry missing colon",
			fm:   "meta:\n  key value",
		},
		{
			name: "object duplicate key",
			fm:   "meta:\n  key: one\n  key: two",
		},
		{
			name: "list item missing marker",
			fm:   "blocked-by:\n  abc",
		},
		{
			name: "empty list item",
			fm:   "blocked-by:\n  - ",
		},
		{
			name: "inconsistent list indentation",
			fm:   "blocked-by:\n  - abc\n   - def",
		},
		{
			name: "inline list empty item",
			fm:   "blocked-by: [a,,b]",
		},
		{
			name: "tab-indented list item",
			fm:   "blocked-by:\n\t- abc",
		},
		{
			name: "object value is list",
			fm:   "meta:\n  key: [a]",
		},
		{
			name: "unterminated double quote",
			fm:   "title: \"oops",
		},
		{
			name: "unterminated single quote",
			fm:   "title: 'oops",
		},
		{
			name: "unterminated quote in list",
			fm:   "blocked-by:\n  - \"oops",
		},
		{
			name: "outdented key after object",
			fm:   "meta:\n  key: value\n status: open",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := wrapFrontmatter(tc.fm, "tail\n")

			_, _, err := frontmatter.ParseFrontmatter([]byte(payload))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: reject YAML constructs outside the supported subset.
func Test_FrontmatterParser_ReturnsError_When_UnsupportedScalar(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		fm   string
	}{
		{
			name: "unterminated inline list",
			fm:   "blocked-by: [a, b",
		},
		{
			name: "inline object",
			fm:   "meta: {a: b}",
		},
		{
			name: "block scalar indicator",
			fm:   "note: |",
		},
		{
			name: "tag indicator",
			fm:   "note: !tag",
		},
		{
			name: "alias indicator",
			fm:   "note: *anchor",
		},
		{
			name: "anchor indicator",
			fm:   "note: &anchor",
		},
		{
			name: "inline list with quoted comma",
			fm:   "tags: [\"a,b\"]",
		},
		{
			name: "comment line",
			fm:   "# comment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := wrapFrontmatter(tc.fm, "")

			_, _, err := frontmatter.ParseFrontmatter([]byte(payload))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: cap frontmatter scans to avoid unbounded work on malformed files.
func Test_FrontmatterParser_ReturnsError_When_LineLimitExceeded(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	for i := range 201 {
		_, _ = fmt.Fprintf(&builder, "k%d: v\n", i)
	}

	content := strings.TrimSuffix(builder.String(), "\n")
	payload := wrapFrontmatter(content, "")

	_, _, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: parser should honor custom line limits.
func Test_FrontmatterParser_ReturnsValues_When_LineLimitDisabled(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	for i := range 201 {
		_, _ = fmt.Fprintf(&builder, "k%d: v\n", i)
	}

	content := strings.TrimSuffix(builder.String(), "\n")
	payload := wrapFrontmatter(content, "")

	_, _, err := frontmatter.ParseFrontmatter([]byte(payload), frontmatter.WithLineLimit(0))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// Contract: parser should trim leading blank lines when configured.
func Test_FrontmatterParser_ReturnsTail_When_DefaultTrimEnabled(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "\n\nBody\n")

	_, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if string(tail) != "Body\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}
}

// Contract: parser should preserve leading blank lines when trimming is disabled.
func Test_FrontmatterParser_ReturnsTail_When_TrimLeadingBlankTailDisabled(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "\n\nBody\n")

	_, tail, err := frontmatter.ParseFrontmatter([]byte(payload), frontmatter.WithTrimLeadingBlankTail(false))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if string(tail) != "\n\nBody\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}
}

// Contract: parser should allow frontmatter-only payloads when delimiters are optional.
func Test_FrontmatterParser_ReturnsValues_When_DelimitersOptional(t *testing.T) {
	t.Parallel()

	payload := "status: open\npriority: 2\n"

	fm, tail, err := frontmatter.ParseFrontmatter([]byte(payload), frontmatter.WithRequireDelimiter(false))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if len(tail) != 0 {
		t.Fatalf("expected empty tail, got %q", string(tail))
	}

	requireScalarString(t, fm, "status", "open")
	requireScalarInt(t, fm, "priority", 2)
}

// Contract: byte parsing requires frontmatter delimiters.
func Test_FrontmatterParser_ReturnsError_When_DelimitersMissing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "empty input",
			src:  "",
		},
		{
			name: "missing opening delimiter",
			src:  "id: 1\n---\n",
		},
		{
			name: "missing closing delimiter",
			src:  "---\nid: 1\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := frontmatter.ParseFrontmatter([]byte(tc.src))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: byte parser should handle missing trailing newline after delimiter.
func Test_FrontmatterParser_ReturnsTail_When_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	payload := "---\nstatus: open\n---"

	fm, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if len(tail) != 0 {
		t.Fatalf("expected empty tail, got %q", string(tail))
	}

	requireScalarString(t, fm, "status", "open")
}

// Contract: parser should stop at the first closing delimiter.
func Test_FrontmatterParser_ReturnsTail_When_MultipleDelimiters(t *testing.T) {
	t.Parallel()

	payload := "---\nstatus: open\n---\n---\nbody\n"

	fm, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if string(tail) != "---\nbody\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}

	requireScalarString(t, fm, "status", "open")
}

// Contract: reader parsing should honor delimiters and share the same rules.
func Test_FrontmatterReader_ReturnsValues_When_Delimited(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter(strings.Join([]string{
		"id: 018f5f25-7e7d-7f0a-8c5c-123456789abc",
		"schema_version: 1",
		"status: open",
	}, "\n"), "# Title\nBody\n")

	fm, tailReader, err := frontmatter.ParseFrontmatterReader(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	body, err := io.ReadAll(tailReader)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if string(body) != "# Title\nBody\n" {
		t.Fatalf("tail mismatch: got %q", string(body))
	}

	requireScalarString(t, fm, "id", "018f5f25-7e7d-7f0a-8c5c-123456789abc")
	requireScalarInt(t, fm, "schema_version", 1)
	requireScalarString(t, fm, "status", "open")
}

// Contract: byte parser should handle CRLF and preserve tail.
func Test_FrontmatterParser_ReturnsTail_When_CRLFInput(t *testing.T) {
	t.Parallel()

	payload := "---\r\nstatus: open\r\n---\r\n---\r\nbody\r\n"

	_, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if string(tail) != "---\r\nbody\r\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}
}

// Contract: byte parser should allow empty frontmatter.
func Test_FrontmatterParser_ReturnsEmpty_When_NoKeys(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("", "body\n")

	fm, tail, err := frontmatter.ParseFrontmatter([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if len(fm) != 0 {
		t.Fatal("expected empty frontmatter")
	}

	if string(tail) != "body\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}
}

// Contract: reader parsing should reject missing delimiters and runaway scans.
func Test_FrontmatterReader_ReturnsError_When_DelimitersMissing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "empty input",
			src:  "",
		},
		{
			name: "missing opening delimiter",
			src:  "id: 1\n---\n",
		},
		{
			name: "missing closing delimiter",
			src:  "---\nid: 1\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := frontmatter.ParseFrontmatterReader(strings.NewReader(tc.src))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: reader parsing should fail once frontmatter exceeds the line cap.
func Test_FrontmatterReader_ReturnsError_When_LineLimitExceeded(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	builder.WriteString("---\n")

	for i := range 201 {
		_, _ = fmt.Fprintf(&builder, "k%d: v\n", i)
	}

	builder.WriteString("---\n")

	_, _, err := frontmatter.ParseFrontmatterReader(strings.NewReader(builder.String()))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: reader parser should trim leading blank lines when configured.
func Test_FrontmatterReader_ReturnsTail_When_DefaultTrimEnabled(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "\n\nBody\n")

	_, tailReader, err := frontmatter.ParseFrontmatterReader(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	body, err := io.ReadAll(tailReader)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if string(body) != "Body\n" {
		t.Fatalf("tail mismatch: got %q", string(body))
	}
}

// Contract: reader parser should preserve leading blank lines when trimming is disabled.
func Test_FrontmatterReader_ReturnsTail_When_TrimLeadingBlankTailDisabled(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "\n\nBody\n")

	_, tailReader, err := frontmatter.ParseFrontmatterReader(strings.NewReader(payload), frontmatter.WithTrimLeadingBlankTail(false))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	body, err := io.ReadAll(tailReader)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if string(body) != "\n\nBody\n" {
		t.Fatalf("tail mismatch: got %q", string(body))
	}
}

// Contract: reader parser should allow frontmatter-only payloads when delimiters are optional.
func Test_FrontmatterReader_ReturnsValues_When_DelimitersOptional(t *testing.T) {
	t.Parallel()

	payload := "status: open\npriority: 2\n"

	fm, tailReader, err := frontmatter.ParseFrontmatterReader(strings.NewReader(payload), frontmatter.WithRequireDelimiter(false))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	body, err := io.ReadAll(tailReader)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if len(body) != 0 {
		t.Fatalf("expected empty tail, got %q", string(body))
	}

	requireScalarString(t, fm, "status", "open")
	requireScalarInt(t, fm, "priority", 2)
}

// Contract: reader parser should allow empty tail after closing delimiter.
func Test_FrontmatterReader_ReturnsEmptyTail_When_NoBody(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "")

	fm, tailReader, err := frontmatter.ParseFrontmatterReader(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	body, err := io.ReadAll(tailReader)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}

	if len(body) != 0 {
		t.Fatalf("expected empty tail, got %q", string(body))
	}

	requireScalarString(t, fm, "status", "open")
}

func wrapFrontmatter(fmContent string, tail string) string {
	if fmContent == "" {
		return strings.Join([]string{
			"---",
			"---",
			tail,
		}, "\n")
	}

	return strings.Join([]string{
		"---",
		fmContent,
		"---",
		tail,
	}, "\n")
}

func Benchmark_FrontmatterParser_Parse(b *testing.B) {
	payload := []byte(wrapFrontmatter(strings.Join([]string{
		"id: 018f5f25-7e7d-7f0a-8c5c-123456789abc",
		"schema_version: 1",
		"status: open",
		"priority: 2",
		"blocked-by:",
		"  - abc",
		"  - def",
		"tags: [ops, \"on-call\"]",
	}, "\n"), "# Title\nBody\n"))

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))

	b.Run("bytes", func(b *testing.B) {
		for range b.N {
			_, _, _ = frontmatter.ParseFrontmatter(payload)
		}
	})

	b.Run("reader", func(b *testing.B) {
		for range b.N {
			_, _, _ = frontmatter.ParseFrontmatterReader(bytes.NewReader(payload))
		}
	})

	b.Run("typical-ticket", func(b *testing.B) {
		typical := []byte(wrapFrontmatter(strings.Join([]string{
			"id: 018f5f25-7e7d-7f0a-8c5c-123456789abc",
			"schema_version: 1",
			"status: open",
			"type: task",
			"priority: 2",
			"assignee: alice",
			"parent: 018f5f25-7e7d-7f0a-8c5c-123456789abd",
			"blocked-by:",
			"  - 018f5f25-7e7d-7f0a-8c5c-123456789abe",
			"  - 018f5f25-7e7d-7f0a-8c5c-123456789abf",
			"external-ref: ENG-1422",
			"created: 2026-01-27T15:23:10Z",
		}, "\n"), strings.Join([]string{
			"# Typical ticket",
			"",
			"Description text here.",
			"",
			"## Design",
			"",
			"- Option A",
			"- Option B",
			"",
			"## Acceptance Criteria",
			"",
			"- Criterion 1",
			"- Criterion 2",
			"",
		}, "\n")))

		b.ReportAllocs()
		b.SetBytes(int64(len(typical)))

		for range b.N {
			_, _, _ = frontmatter.ParseFrontmatter(typical)
		}
	})
}

func requireScalarString(t *testing.T, fm frontmatter.Frontmatter, key, want string) {
	t.Helper()

	value := requireValue(t, fm, key)
	if value.Kind != frontmatter.ValueScalar {
		t.Fatalf("%s: expected scalar", key)
	}

	if value.Scalar.Kind != frontmatter.ScalarString || value.Scalar.String != want {
		t.Fatalf("%s: expected string %q", key, want)
	}
}

func requireScalarInt(t *testing.T, fm frontmatter.Frontmatter, key string, want int64) {
	t.Helper()

	value := requireValue(t, fm, key)
	if value.Kind != frontmatter.ValueScalar {
		t.Fatalf("%s: expected scalar", key)
	}

	if value.Scalar.Kind != frontmatter.ScalarInt || value.Scalar.Int != want {
		t.Fatalf("%s: expected int %d", key, want)
	}
}

func requireScalarBool(t *testing.T, fm frontmatter.Frontmatter, key string, want bool) {
	t.Helper()

	value := requireValue(t, fm, key)
	if value.Kind != frontmatter.ValueScalar {
		t.Fatalf("%s: expected scalar", key)
	}

	if value.Scalar.Kind != frontmatter.ScalarBool || value.Scalar.Bool != want {
		t.Fatalf("%s: expected bool %v", key, want)
	}
}

func requireList(t *testing.T, fm frontmatter.Frontmatter, key string, want []string) {
	t.Helper()

	value := requireValue(t, fm, key)
	if value.Kind != frontmatter.ValueList {
		t.Fatalf("%s: expected list", key)
	}

	if !reflect.DeepEqual(value.List, want) {
		t.Fatalf("%s: list mismatch: got %v want %v", key, value.List, want)
	}
}

func requireObject(t *testing.T, fm frontmatter.Frontmatter, key string, want map[string]frontmatter.Scalar) {
	t.Helper()

	value := requireValue(t, fm, key)
	if value.Kind != frontmatter.ValueObject {
		t.Fatalf("%s: expected object", key)
	}

	if !reflect.DeepEqual(value.Object, want) {
		t.Fatalf("%s: object mismatch: got %v want %v", key, value.Object, want)
	}
}

func requireValue(t *testing.T, fm frontmatter.Frontmatter, key string) frontmatter.Value {
	t.Helper()

	value, ok := fm[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}

	return value
}
