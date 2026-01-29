// Package frontmatter provides a fast, strict frontmatter parser/serializer for
// a tiny YAML subset. It is designed for zero-copy, zero-allocation hot paths
// where you parse many documents and only need a few keys.
//
// The parser accepts only a minimal, deterministic grammar. Supported forms:
//
//	---
//	id: ABC-123
//	schema_version: 1
//	enabled: true
//	tags:
//	  - bug
//	  - urgent
//	inline_list: [a, b, c]
//	metadata:
//	  author: alice
//	  priority: 2
//	---
//
// Scalars may be unquoted strings, integers, or booleans (true/false).
// Lists contain only strings. Objects (nested maps) contain only scalar values.
// Single- and double-quoted strings are supported for values containing
// special characters (including '#').
//
// Strict formatting rules (for speed and determinism):
//   - Inline values require a single space after ':' (e.g., "key: value").
//   - Inline lists use ", " separators (comma + single space).
//   - List items and object values may not contain extra leading/trailing
//     whitespace outside of quotes.
//   - Comments are not supported. Quote '#' if it is part of a string literal.
//
// Explicitly not supported: multi-line strings, anchors, aliases, tags, flow
// mappings, null values, floats, nested lists/objects, or inline objects.
//
// # Borrowed vs Owned Data
//
// When parsing with [ParseBytes], all string data (keys, string values,
// list items) point directly into the input []byte slice. This is zero-copy
// and zero-allocation for the hot path.
//
// Use [Frontmatter.GetBytes] for zero-alloc access when the data lifetime
// is short (e.g., bulk indexing).
//
// Use [Frontmatter.GetString] when you need owned strings that outlive
// the input buffer.
//
// # Key Lookups
//
// The Get* methods take []byte keys to avoid allocations when comparing against
// parsed keys. Avoid per-call conversions like []byte("id") in hot paths.
// Reuse shared []byte keys instead (for example mddb.FrontmatterKeyID).
package frontmatter

import (
	"bytes"
	"errors"
)

// ScalarKind distinguishes scalar YAML values inside ticket frontmatter.
type ScalarKind uint8

// ScalarKind values enumerate the YAML scalar subset we accept.
const (
	ScalarString ScalarKind = iota
	ScalarInt
	ScalarBool
)

// Scalar keeps the restricted YAML scalar types explicit for downstream validation.
// For string scalars, Bytes points into the original input (borrowed).
type Scalar struct {
	Kind  ScalarKind
	Bytes []byte // For ScalarString: points into input data (borrowed)
	Int   int64  // For ScalarInt
	Bool  bool   // For ScalarBool
}

// String returns the string value, allocating a new string.
// Returns empty string if not a string scalar.
func (s Scalar) String() string {
	if s.Kind != ScalarString {
		return ""
	}

	return string(s.Bytes)
}

// ValueKind describes the supported frontmatter shapes.
type ValueKind uint8

// ValueKind values enumerate the supported top-level YAML shapes.
const (
	ValueScalar ValueKind = iota
	ValueList
	ValueObject
)

// Value represents a validated frontmatter value in the supported YAML subset.
// All []byte fields point into the original input data (borrowed).
type Value struct {
	Kind   ValueKind
	Scalar Scalar
	List   [][]byte // Each item points into input data
	Object []ObjectEntry
}

// ObjectEntry is a key-value pair in an object value.
type ObjectEntry struct {
	Key   []byte // Points into input data
	Value Scalar
}

// Entry is a top-level frontmatter key-value pair.
type Entry struct {
	Key   []byte // Points into input data
	Value Value
}

// Frontmatter holds parsed frontmatter entries.
// All data is borrowed from the input buffer and valid only while the input lives.
type Frontmatter struct {
	entries []Entry
}

// Len returns the number of entries.
func (fm *Frontmatter) Len() int {
	return len(fm.entries)
}

// EntriesView returns the underlying entries slice for iteration.
// The returned slice is borrowed - do not modify or retain beyond the
// lifetime of the Frontmatter (or the input buffer it was parsed from).
func (fm *Frontmatter) EntriesView() []Entry {
	return fm.entries
}

// GetBytes returns the raw bytes for a string scalar.
// Returns (nil, false) if key is missing or not a string scalar.
// Zero allocations - returns a subslice of the input.
func (fm *Frontmatter) GetBytes(key []byte) ([]byte, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			v := &fm.entries[i].Value
			if v.Kind != ValueScalar || v.Scalar.Kind != ScalarString {
				return nil, false
			}

			return v.Scalar.Bytes, true
		}
	}

	return nil, false
}

// GetString returns the string value for key, allocating a new string.
// Returns ("", false) if key is missing or not a string scalar.
func (fm *Frontmatter) GetString(key []byte) (string, bool) {
	b, ok := fm.GetBytes(key)
	if !ok {
		return "", false
	}

	return string(b), true
}

// GetInt returns the int64 value for key.
// Returns (0, false) if key is missing or not an int scalar.
// Zero allocations.
func (fm *Frontmatter) GetInt(key []byte) (int64, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			v := &fm.entries[i].Value
			if v.Kind != ValueScalar || v.Scalar.Kind != ScalarInt {
				return 0, false
			}

			return v.Scalar.Int, true
		}
	}

	return 0, false
}

// GetBool returns the bool value for key.
// Returns (false, false) if key is missing or not a bool scalar.
// Zero allocations.
func (fm *Frontmatter) GetBool(key []byte) (bool, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			v := &fm.entries[i].Value
			if v.Kind != ValueScalar || v.Scalar.Kind != ScalarBool {
				return false, false
			}

			return v.Scalar.Bool, true
		}
	}

	return false, false
}

// GetListBytes returns the list items as borrowed []byte slices.
// Returns (nil, false) if key is missing or not a list.
// Zero allocations - returns a view into borrowed data.
func (fm *Frontmatter) GetListBytes(key []byte) ([][]byte, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			v := &fm.entries[i].Value
			if v.Kind != ValueList {
				return nil, false
			}

			return v.List, true
		}
	}

	return nil, false
}

// GetList returns the string slice for key, allocating new strings.
// Returns (nil, false) if key is missing or not a list.
func (fm *Frontmatter) GetList(key []byte) ([]string, bool) {
	items, ok := fm.GetListBytes(key)
	if !ok {
		return nil, false
	}

	result := make([]string, len(items))
	for i, item := range items {
		result[i] = string(item)
	}

	return result, true
}

// GetObject returns the object entries for key.
// Returns (nil, false) if key is missing or not an object.
// Zero allocations - returns a view into borrowed data.
func (fm *Frontmatter) GetObject(key []byte) ([]ObjectEntry, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			v := &fm.entries[i].Value
			if v.Kind != ValueObject {
				return nil, false
			}

			return v.Object, true
		}
	}

	return nil, false
}

// Has returns true if the key exists.
// Zero allocations.
func (fm *Frontmatter) Has(key []byte) bool {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			return true
		}
	}

	return false
}

// Get returns the Value for key.
// Returns (Value{}, false) if key is missing.
// Zero allocations.
func (fm *Frontmatter) Get(key []byte) (Value, bool) {
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, key) {
			return fm.entries[i].Value, true
		}
	}

	return Value{}, false
}

var (
	errEmptyKey      = errors.New("empty key")
	errKeyWhitespace = errors.New("key contains whitespace")
	errKeyInvalid    = errors.New("key contains invalid character")
	errNilValue      = errors.New("nil value")
)

func validateKey(key []byte) error {
	if len(key) == 0 {
		return errEmptyKey
	}

	if bytes.IndexByte(key, ' ') != -1 || bytes.IndexByte(key, '\t') != -1 {
		return errKeyWhitespace
	}

	if bytes.IndexByte(key, ':') != -1 || bytes.IndexByte(key, '\n') != -1 || bytes.IndexByte(key, '\r') != -1 {
		return errKeyInvalid
	}

	return nil
}

// Set adds or updates an entry. The key is copied, value is dereferenced and stored.
// Returns an error for empty/whitespace keys or nil values.
// This is used for marshaling where we construct owned values.
func (fm *Frontmatter) Set(key []byte, value *Value) error {
	if err := validateKey(key); err != nil {
		return err
	}

	if value == nil {
		return errNilValue
	}

	keyBytes := append([]byte(nil), key...)
	for i := range fm.entries {
		if bytes.Equal(fm.entries[i].Key, keyBytes) {
			fm.entries[i].Value = *value

			return nil
		}
	}

	fm.entries = append(fm.entries, Entry{Key: keyBytes, Value: *value})

	return nil
}

// MustSet is like Set but panics on error.
func (fm *Frontmatter) MustSet(key []byte, value *Value) {
	if err := fm.Set(key, value); err != nil {
		panic(err)
	}
}

// StringValue creates a Value with a string scalar (owned copy).
func StringValue(s string) *Value {
	return &Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarString, Bytes: []byte(s)}}
}

// IntValue creates a Value with an integer scalar.
func IntValue(i int64) *Value {
	return &Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarInt, Int: i}}
}

// BoolValue returns a Value with a bool scalar.
func BoolValue(b bool) *Value {
	return &Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarBool, Bool: b}}
}

// StringListValue creates a Value with a string list (owned copies).
func StringListValue(items []string) *Value {
	list := make([][]byte, len(items))
	for i, item := range items {
		list[i] = []byte(item)
	}

	return &Value{Kind: ValueList, List: list}
}
