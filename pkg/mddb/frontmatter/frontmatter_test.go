package frontmatter_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
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
				"note_hash: \"with#comment\"",
				"note_empty: \"\"",
				"empty: ''",
			}, "\n"),
			tail: "# Title\n",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireScalarString(t, fm, []byte("id"), "018f5f25-7e7d-7f0a-8c5c-123456789abc")
				requireScalarInt(t, fm, []byte("schema_version"), 1)
				requireScalarString(t, fm, []byte("status"), "open")
				requireScalarInt(t, fm, []byte("priority"), 2)
				requireScalarBool(t, fm, []byte("flag"), true)
				requireScalarString(t, fm, []byte("owner"), "ops team")
				requireScalarString(t, fm, []byte("note_hash"), "with#comment")
				requireScalarString(t, fm, []byte("note_empty"), "")
				requireScalarString(t, fm, []byte("empty"), "")
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
				requireList(t, fm, []byte("blocked-by"), []string{"abc", "def"})
				requireList(t, fm, []byte("tags"), []string{"ops", "on-call"})
				requireObject(t, fm, []byte("meta"), map[string]any{
					"owner":   "alice",
					"retries": int64(3),
					"urgent":  false,
				})
			},
		},
		{
			name: "empty list",
			fm:   "blocked-by: []",
			tail: "",
			check: func(t *testing.T, fm frontmatter.Frontmatter) {
				t.Helper()
				requireList(t, fm, []byte("blocked-by"), []string{})
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
				requireList(t, fm, []byte("blocked-by"), []string{"abc"})
				requireScalarString(t, fm, []byte("status"), "open")
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
				requireObject(t, fm, []byte("meta"), map[string]any{
					"owner": "alice",
				})
				requireScalarString(t, fm, []byte("status"), "open")
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
				requireScalarInt(t, fm, []byte("delta"), -12)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := wrapFrontmatter(tc.fm, tc.tail)

			fm, tail, err := frontmatter.ParseBytes([]byte(payload))
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

// Contract: frontmatter marshal keeps delimiter defaults and key ordering stable (alphabetical).
func Test_Frontmatter_MarshalYAML_Returns_Delimited_Output_When_Defaults(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("type"), frontmatter.StringValue("task"))
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))
	fm.MustSet([]byte("schema_version"), frontmatter.IntValue(1))
	fm.MustSet([]byte("status"), frontmatter.StringValue("open"))
	fm.MustSet([]byte("priority"), frontmatter.IntValue(2))

	got, err := fm.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	// Keys sorted alphabetically
	want := strings.Join([]string{
		"---",
		"id: \"123\"",
		"priority: 2",
		"schema_version: 1",
		"status: open",
		"type: task",
		"---",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("yaml output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Contract: frontmatter marshal allows empty frontmatter.
func Test_Frontmatter_MarshalYAML_Allows_EmptyFrontmatter(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter

	got, err := fm.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	want := strings.Join([]string{
		"---",
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

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))
	fm.MustSet([]byte("schema_version"), frontmatter.IntValue(1))
	fm.MustSet([]byte("status"), frontmatter.StringValue("open"))

	got, err := fm.MarshalYAML(frontmatter.WithYAMLDelimiters(false))
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	want := strings.Join([]string{
		"id: \"123\"",
		"schema_version: 1",
		"status: open",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("yaml output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Contract: frontmatter marshal rejects duplicate keys in key order options.
func Test_Frontmatter_MarshalYAML_ReturnsError_When_KeyOrderDuplicates(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))

	_, err := fm.MarshalYAML(frontmatter.WithKeyOrder([]byte("id"), []byte("id")))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: frontmatter marshal rejects invalid keys in key order options.
func Test_Frontmatter_MarshalYAML_ReturnsError_When_KeyOrderKeyInvalid(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))

	_, err := fm.MarshalYAML(frontmatter.WithKeyOrder([]byte("bad:key")))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: frontmatter marshal rejects duplicate keys in priority options.
func Test_Frontmatter_MarshalYAML_ReturnsError_When_PriorityKeyDuplicates(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))

	_, err := fm.MarshalYAML(frontmatter.WithKeyPriority([]byte("id"), []byte("id")))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: frontmatter marshal rejects invalid keys in priority options.
func Test_Frontmatter_MarshalYAML_ReturnsError_When_PriorityKeyInvalid(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))

	_, err := fm.MarshalYAML(frontmatter.WithKeyPriority([]byte("bad:key")))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: frontmatter marshal rejects duplicate keys in object values.
func Test_Frontmatter_MarshalYAML_ReturnsError_When_ObjectKeysDuplicate(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("meta"), &frontmatter.Value{
		Kind: frontmatter.ValueObject,
		Object: []frontmatter.ObjectEntry{
			{Key: []byte("owner"), Value: frontmatter.Scalar{Kind: frontmatter.ScalarString, Bytes: []byte("alice")}},
			{Key: []byte("owner"), Value: frontmatter.Scalar{Kind: frontmatter.ScalarString, Bytes: []byte("bob")}},
		},
	})

	_, err := fm.MarshalYAML()
	if err == nil {
		t.Fatal("expected error")
	}
}

// Contract: frontmatter marshal respects custom key order.
func Test_Frontmatter_MarshalYAML_Uses_Custom_Key_Order_When_Option_Set(t *testing.T) {
	t.Parallel()

	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("type"), frontmatter.StringValue("task"))
	fm.MustSet([]byte("id"), frontmatter.StringValue("123"))
	fm.MustSet([]byte("schema_version"), frontmatter.IntValue(1))
	fm.MustSet([]byte("status"), frontmatter.StringValue("open"))
	fm.MustSet([]byte("priority"), frontmatter.IntValue(2))

	got, err := fm.MarshalYAML(frontmatter.WithKeyOrder([]byte("id"), []byte("schema_version"), []byte("type"), []byte("status")))
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}

	// priority is omitted because it's not in the key order
	want := strings.Join([]string{
		"---",
		"id: \"123\"",
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

// Contract: Set rejects invalid keys.
func Test_Frontmatter_Set_ReturnsError_When_KeyInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  []byte
	}{
		{name: "empty", key: nil},
		{name: "whitespace", key: []byte("bad key")},
		{name: "tab", key: []byte("bad\tkey")},
		{name: "colon", key: []byte("bad:key")},
		{name: "newline", key: []byte("bad\nkey")},
		{name: "carriage", key: []byte("bad\rkey")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var fm frontmatter.Frontmatter
			if err := fm.Set(tc.key, frontmatter.StringValue("value")); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: marshal should preserve string scalars that look like other types or invalid tokens.
func Test_Frontmatter_MarshalYAML_RoundTrips_StringScalars_When_Ambiguous(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "bool-true", value: "true"},
		{name: "bool-false", value: "false"},
		{name: "int", value: "123"},
		{name: "negative-int", value: "-12"},
		{name: "list-like", value: "[a]"},
		{name: "object-like", value: "{a}"},
		{name: "dash-space", value: "- x"},
		{name: "quoted-content", value: "\"hello\""},
		{name: "leading-space", value: " leading"},
		{name: "trailing-space", value: "trailing "},
		{name: "newline-escape", value: "a\nb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var fm frontmatter.Frontmatter
			fm.MustSet([]byte("id"), frontmatter.StringValue("abc"))
			fm.MustSet([]byte("schema_version"), frontmatter.IntValue(1))
			fm.MustSet([]byte("note"), frontmatter.StringValue(tc.value))

			serialized, err := fm.MarshalYAML()
			if err != nil {
				t.Fatalf("marshal yaml: %v", err)
			}

			parsed, _, err := frontmatter.ParseBytes([]byte(serialized))
			if err != nil {
				t.Fatalf("parse frontmatter: %v", err)
			}

			requireScalarString(t, parsed, []byte("note"), tc.value)
		})
	}
}

// Contract: parser rejects quoted empty list items.
func Test_FrontmatterParser_ReturnsError_When_ListItemEmptyQuoted(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("tags:"+"\n"+"  - \"\"", "")

	_, _, err := frontmatter.ParseBytes([]byte(payload))
	if err == nil {
		t.Fatal("expected error")
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
			name: "missing space after colon",
			fm:   "status:open",
		},
		{
			name: "double space after colon",
			fm:   "status:  open",
		},
		{
			name: "trailing whitespace after scalar",
			fm:   "status: open ",
		},
		{
			name: "comment only scalar",
			fm:   "status: # comment",
		},
		{
			name: "comment only list item",
			fm:   "blocked-by:\n  - # comment",
		},
		{
			name: "comment only object value",
			fm:   "meta:\n  owner: # comment",
		},
		{
			name: "inline comment double space",
			fm:   "status: open  # comment",
		},
		{
			name: "unquoted hash in scalar",
			fm:   "status: with#comment",
		},
		{
			name: "comment line",
			fm:   "# comment",
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
			name: "object entry double space after colon",
			fm:   "meta:\n  key:  value",
		},
		{
			name: "object entry trailing whitespace",
			fm:   "meta:\n  key: value ",
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
			name: "list item extra whitespace",
			fm:   "blocked-by:\n  -  abc",
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
			name: "inline list missing space after comma",
			fm:   "blocked-by: [a,b]",
		},
		{
			name: "inline list extra whitespace",
			fm:   "blocked-by: [a,  b]",
		},
		{
			name: "inline list leading whitespace",
			fm:   "blocked-by: [ a, b]",
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

			_, _, err := frontmatter.ParseBytes([]byte(payload))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Contract: parser should surface comment errors with line numbers.
func Test_FrontmatterParser_ReturnsError_When_CommentLinePresent(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("id: 1\n# comment\nstatus: open", "")

	_, _, err := frontmatter.ParseBytes([]byte(payload))
	if err == nil {
		t.Fatal("expected error")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "comments are not supported") {
		t.Fatalf("expected comment error, got %q", errMsg)
	}

	if !strings.Contains(errMsg, "line 3") {
		t.Fatalf("expected line 3 in error, got %q", errMsg)
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := wrapFrontmatter(tc.fm, "")

			_, _, err := frontmatter.ParseBytes([]byte(payload))
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

	_, _, err := frontmatter.ParseBytes([]byte(payload))
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

	_, _, err := frontmatter.ParseBytes([]byte(payload), frontmatter.WithLineLimit(0))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// Contract: parser should trim leading blank lines when configured.
func Test_FrontmatterParser_ReturnsTail_When_DefaultTrimEnabled(t *testing.T) {
	t.Parallel()

	payload := wrapFrontmatter("status: open", "\n\nBody\n")

	_, tail, err := frontmatter.ParseBytes([]byte(payload))
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

	_, tail, err := frontmatter.ParseBytes([]byte(payload), frontmatter.WithTrimLeadingBlankTail(false))
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

	fm, tail, err := frontmatter.ParseBytes([]byte(payload), frontmatter.WithRequireDelimiter(false))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if len(tail) != 0 {
		t.Fatalf("expected empty tail, got %q", string(tail))
	}

	requireScalarString(t, fm, []byte("status"), "open")
	requireScalarInt(t, fm, []byte("priority"), 2)
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

			_, _, err := frontmatter.ParseBytes([]byte(tc.src))
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

	fm, tail, err := frontmatter.ParseBytes([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if len(tail) != 0 {
		t.Fatalf("expected empty tail, got %q", string(tail))
	}

	requireScalarString(t, fm, []byte("status"), "open")
}

// Contract: parser should stop at the first closing delimiter.
func Test_FrontmatterParser_ReturnsTail_When_MultipleDelimiters(t *testing.T) {
	t.Parallel()

	payload := "---\nstatus: open\n---\n---\nbody\n"

	fm, tail, err := frontmatter.ParseBytes([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if string(tail) != "---\nbody\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}

	requireScalarString(t, fm, []byte("status"), "open")
}

// Contract: byte parser should handle CRLF and preserve tail.
func Test_FrontmatterParser_ReturnsTail_When_CRLFInput(t *testing.T) {
	t.Parallel()

	payload := "---\r\nstatus: open\r\n---\r\n---\r\nbody\r\n"

	_, tail, err := frontmatter.ParseBytes([]byte(payload))
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

	fm, tail, err := frontmatter.ParseBytes([]byte(payload))
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	if fm.Len() != 0 {
		t.Fatal("expected empty frontmatter")
	}

	if string(tail) != "body\n" {
		t.Fatalf("tail mismatch: got %q", string(tail))
	}
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
			_, _, _ = frontmatter.ParseBytes(payload)
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
			_, _, _ = frontmatter.ParseBytes(typical)
		}
	})
}

func requireScalarString(t *testing.T, fm frontmatter.Frontmatter, key []byte, want string) {
	t.Helper()

	got, ok := fm.GetString(key)
	if !ok {
		t.Fatalf("%s: expected string scalar, key missing or wrong type", string(key))
	}

	if got != want {
		t.Fatalf("%s: expected %q, got %q", string(key), want, got)
	}
}

func requireScalarInt(t *testing.T, fm frontmatter.Frontmatter, key []byte, want int64) {
	t.Helper()

	got, ok := fm.GetInt(key)
	if !ok {
		t.Fatalf("%s: expected int scalar, key missing or wrong type", string(key))
	}

	if got != want {
		t.Fatalf("%s: expected %d, got %d", string(key), want, got)
	}
}

func requireScalarBool(t *testing.T, fm frontmatter.Frontmatter, key []byte, want bool) {
	t.Helper()

	got, ok := fm.GetBool(key)
	if !ok {
		t.Fatalf("%s: expected bool scalar, key missing or wrong type", string(key))
	}

	if got != want {
		t.Fatalf("%s: expected %v, got %v", string(key), want, got)
	}
}

func requireList(t *testing.T, fm frontmatter.Frontmatter, key []byte, want []string) {
	t.Helper()

	got, ok := fm.GetList(key)
	if !ok {
		t.Fatalf("%s: expected list, key missing or wrong type", string(key))
	}

	if len(got) != len(want) {
		t.Fatalf("%s: list length mismatch: got %d want %d", string(key), len(got), len(want))
	}

	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s[%d]: got %q want %q", string(key), i, got[i], want[i])
		}
	}
}

func requireObject(t *testing.T, fm frontmatter.Frontmatter, key []byte, want map[string]any) {
	t.Helper()

	entries, ok := fm.GetObject(key)
	if !ok {
		t.Fatalf("%s: expected object, key missing or wrong type", string(key))
	}

	if len(entries) != len(want) {
		t.Fatalf("%s: object size mismatch: got %d want %d", string(key), len(entries), len(want))
	}

	for _, entry := range entries {
		entryKey := string(entry.Key)

		wantVal, exists := want[entryKey]
		if !exists {
			t.Fatalf("%s: unexpected key %q", string(key), entryKey)
		}

		switch wv := wantVal.(type) {
		case string:
			if entry.Value.Kind != frontmatter.ScalarString || string(entry.Value.Bytes) != wv {
				t.Fatalf("%s.%s: expected string %q", string(key), entryKey, wv)
			}
		case int64:
			if entry.Value.Kind != frontmatter.ScalarInt || entry.Value.Int != wv {
				t.Fatalf("%s.%s: expected int %d", string(key), entryKey, wv)
			}
		case bool:
			if entry.Value.Kind != frontmatter.ScalarBool || entry.Value.Bool != wv {
				t.Fatalf("%s.%s: expected bool %v", string(key), entryKey, wv)
			}
		default:
			t.Fatalf("%s.%s: unsupported want type %T", string(key), entryKey, wantVal)
		}
	}
}
