// Package frontmatter parses and serializes a restricted YAML subset for
// markdown frontmatter blocks.
//
// The supported grammar is intentionally minimal to keep parsing deterministic
// and avoid the complexity of full YAML. Only the following constructs are
// allowed:
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
// Scalar values may be unquoted strings, integers, or booleans (true/false).
// Lists contain only strings. Objects (nested maps) contain only scalar values.
// Quoted strings using single or double quotes are supported for values
// containing special characters.
//
// Features explicitly not supported: multi-line strings, anchors, aliases,
// tags, flow mappings, null values, floats, and nested lists/objects.
package frontmatter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
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
type Scalar struct {
	Kind   ScalarKind // Kind describes which scalar value is populated.
	String string     // String holds the scalar string value when Kind == ScalarString.
	Int    int64      // Int holds the scalar integer value when Kind == ScalarInt.
	Bool   bool       // Bool holds the scalar boolean value when Kind == ScalarBool.
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
type Value struct {
	Kind   ValueKind         // Kind describes which Value shape is populated.
	Scalar Scalar            // Scalar holds the value when Kind == ValueScalar.
	List   []string          // List holds the value when Kind == ValueList.
	Object map[string]Scalar // Object holds the value when Kind == ValueObject.
}

// StringValue creates a Value with a string scalar.
func StringValue(s string) Value {
	return Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarString, String: s}}
}

// IntValue creates a Value with an integer scalar.
func IntValue(i int64) Value {
	return Value{Kind: ValueScalar, Scalar: Scalar{Kind: ScalarInt, Int: i}}
}

// ListValue creates a Value with a string list.
func ListValue(items []string) Value {
	return Value{Kind: ValueList, List: items}
}

// UnmarshalJSON parses a JSON scalar, list, or object into a frontmatter value.
func (v *Value) UnmarshalJSON(data []byte) error {
	if v == nil {
		return errors.New("unmarshal frontmatter value: nil receiver")
	}

	var raw any

	err := json.Unmarshal(data, &raw)
	if err != nil {
		return fmt.Errorf("unmarshal frontmatter value: %w", err)
	}

	switch typed := raw.(type) {
	case string:
		v.Kind = ValueScalar
		v.Scalar = Scalar{Kind: ScalarString, String: typed}
	case bool:
		v.Kind = ValueScalar
		v.Scalar = Scalar{Kind: ScalarBool, Bool: typed}
	case float64:
		if typed != math.Trunc(typed) {
			return errors.New("unmarshal frontmatter value: numeric value must be integer")
		}

		v.Kind = ValueScalar
		v.Scalar = Scalar{Kind: ScalarInt, Int: int64(typed)}
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return errors.New("unmarshal frontmatter value: list items must be strings")
			}

			list = append(list, str)
		}

		v.Kind = ValueList
		v.List = list
		v.Object = nil
	case map[string]any:
		obj := make(map[string]Scalar, len(typed))
		for key, value := range typed {
			switch scalar := value.(type) {
			case string:
				obj[key] = Scalar{Kind: ScalarString, String: scalar}
			case bool:
				obj[key] = Scalar{Kind: ScalarBool, Bool: scalar}
			case float64:
				if scalar != math.Trunc(scalar) {
					return fmt.Errorf("unmarshal frontmatter value: object %s must be integer", key)
				}

				obj[key] = Scalar{Kind: ScalarInt, Int: int64(scalar)}
			default:
				return fmt.Errorf("unmarshal frontmatter value: object %s must be scalar", key)
			}
		}

		v.Kind = ValueObject
		v.Object = obj
		v.List = nil
	default:
		return fmt.Errorf("unmarshal frontmatter value: unsupported type %T", raw)
	}

	return nil
}

// Frontmatter maps top-level keys to validated values.
type Frontmatter map[string]Value

// GetString returns the string value for key.
// Returns ("", false) if key is missing or not a string scalar.
func (fm Frontmatter) GetString(key string) (string, bool) {
	v, ok := fm[key]
	if !ok || v.Kind != ValueScalar || v.Scalar.Kind != ScalarString {
		return "", false
	}

	return v.Scalar.String, true
}

// GetInt returns the int64 value for key.
// Returns (0, false) if key is missing or not an int scalar.
func (fm Frontmatter) GetInt(key string) (int64, bool) {
	v, ok := fm[key]
	if !ok || v.Kind != ValueScalar || v.Scalar.Kind != ScalarInt {
		return 0, false
	}

	return v.Scalar.Int, true
}

// GetBool returns the bool value for key.
// Returns (false, false) if key is missing or not a bool scalar.
func (fm Frontmatter) GetBool(key string) (bool, bool) {
	v, ok := fm[key]
	if !ok || v.Kind != ValueScalar || v.Scalar.Kind != ScalarBool {
		return false, false
	}

	return v.Scalar.Bool, true
}

// GetList returns the string slice for key.
// Returns (nil, false) if key is missing or not a list.
func (fm Frontmatter) GetList(key string) ([]string, bool) {
	v, ok := fm[key]
	if !ok || v.Kind != ValueList {
		return nil, false
	}

	return v.List, true
}

// MarshalJSON renders the frontmatter map as a JSON object.
func (fm Frontmatter) MarshalJSON() ([]byte, error) {
	obj := make(map[string]any, len(fm))
	for key, value := range fm {
		switch value.Kind {
		case ValueScalar:
			switch value.Scalar.Kind {
			case ScalarString:
				obj[key] = value.Scalar.String
			case ScalarInt:
				obj[key] = value.Scalar.Int
			case ScalarBool:
				obj[key] = value.Scalar.Bool
			default:
				return nil, fmt.Errorf("%s: unsupported scalar kind %d", key, value.Scalar.Kind)
			}
		case ValueList:
			obj[key] = value.List
		case ValueObject:
			inner := make(map[string]any, len(value.Object))
			for objKey, scalar := range value.Object {
				switch scalar.Kind {
				case ScalarString:
					inner[objKey] = scalar.String
				case ScalarInt:
					inner[objKey] = scalar.Int
				case ScalarBool:
					inner[objKey] = scalar.Bool
				default:
					return nil, fmt.Errorf("%s.%s: unsupported scalar kind %d", key, objKey, scalar.Kind)
				}
			}

			obj[key] = inner
		default:
			return nil, fmt.Errorf("%s: unsupported value kind %d", key, value.Kind)
		}
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}

	return data, nil
}

// MarshalOptions configures frontmatter serialization.
type MarshalOptions struct {
	IncludeDelimiters bool     // IncludeDelimiters writes --- fence lines before and after.
	KeyOrder          []string // KeyOrder specifies the output key order; keys not listed are omitted.
}

// MarshalOption mutates MarshalOptions.
type MarshalOption func(*MarshalOptions)

// WithYAMLDelimiters toggles whether MarshalYAML includes --- delimiters.
// The default is true to match the on-disk ticket format.
func WithYAMLDelimiters(include bool) MarshalOption {
	return func(opts *MarshalOptions) {
		opts.IncludeDelimiters = include
	}
}

// WithKeyOrder specifies the exact key order for YAML output.
// Keys not in this list are omitted from output.
// If nil (default), keys are sorted alphabetically with id and schema_version first.
func WithKeyOrder(keys []string) MarshalOption {
	return func(opts *MarshalOptions) {
		opts.KeyOrder = keys
	}
}

// MarshalYAML serializes frontmatter in a deterministic YAML subset.
// By default, it sorts keys alphabetically with "id" and "schema_version" first.
// Use WithKeyOrder to specify a custom key order.
// Returns an error if id or schema_version is missing.
func (fm Frontmatter) MarshalYAML(opts ...MarshalOption) (string, error) {
	options := MarshalOptions{IncludeDelimiters: true}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(&options)
	}

	if fm == nil {
		return "", errors.New("nil map")
	}

	if _, ok := fm["id"]; !ok {
		return "", errors.New("missing id")
	}

	if _, ok := fm["schema_version"]; !ok {
		return "", errors.New("missing schema_version")
	}

	var ordered []string
	if options.KeyOrder != nil {
		ordered = options.KeyOrder
	} else {
		keys := make([]string, 0, len(fm))
		for key := range fm {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		ordered = make([]string, 0, len(keys))
		ordered = append(ordered, "id", "schema_version")

		for _, key := range keys {
			if key == "id" || key == "schema_version" {
				continue
			}

			ordered = append(ordered, key)
		}
	}

	var builder strings.Builder
	if options.IncludeDelimiters {
		builder.WriteString("---\n")
	}

	for _, key := range ordered {
		value, ok := fm[key]
		if !ok {
			return "", fmt.Errorf("missing %s", key)
		}

		builder.WriteString(key)
		builder.WriteString(":")

		switch value.Kind {
		case ValueScalar:
			builder.WriteString(" ")

			switch value.Scalar.Kind {
			case ScalarString:
				builder.WriteString(value.Scalar.String)
			case ScalarInt:
				builder.WriteString(strconv.FormatInt(value.Scalar.Int, 10))
			case ScalarBool:
				if value.Scalar.Bool {
					builder.WriteString("true")
				} else {
					builder.WriteString("false")
				}
			default:
				return "", fmt.Errorf("%s: unsupported scalar kind %d", key, value.Scalar.Kind)
			}

			builder.WriteString("\n")
		case ValueList:
			if len(value.List) == 0 {
				builder.WriteString(" []\n")

				break
			}

			builder.WriteString("\n")

			for _, item := range value.List {
				if item == "" {
					return "", fmt.Errorf("%s: empty list item", key)
				}

				builder.WriteString("  - ")
				builder.WriteString(item)
				builder.WriteString("\n")
			}
		case ValueObject:
			if len(value.Object) == 0 {
				return "", fmt.Errorf("%s: empty object", key)
			}

			builder.WriteString("\n")

			objKeys := make([]string, 0, len(value.Object))
			for objKey := range value.Object {
				objKeys = append(objKeys, objKey)
			}

			slices.Sort(objKeys)

			for _, objKey := range objKeys {
				scalar := value.Object[objKey]

				builder.WriteString("  ")
				builder.WriteString(objKey)
				builder.WriteString(": ")

				switch scalar.Kind {
				case ScalarString:
					builder.WriteString(scalar.String)
				case ScalarInt:
					builder.WriteString(strconv.FormatInt(scalar.Int, 10))
				case ScalarBool:
					if scalar.Bool {
						builder.WriteString("true")
					} else {
						builder.WriteString("false")
					}
				default:
					return "", fmt.Errorf("%s.%s: unsupported scalar kind %d", key, objKey, scalar.Kind)
				}

				builder.WriteString("\n")
			}
		default:
			return "", fmt.Errorf("%s: unsupported value kind %d", key, value.Kind)
		}
	}

	if options.IncludeDelimiters {
		builder.WriteString("---\n")
	}

	return builder.String(), nil
}

const (
	frontmatterDelimiter = "---"
	maxFrontmatterLines  = 200 // Default line limit; override with WithLineLimit.
)

var (
	frontmatterDelimiterBytes = []byte(frontmatterDelimiter)
	trueBytes                 = []byte("true")
	falseBytes                = []byte("false")
)

// ParseOptions configures frontmatter parsing behavior.
type ParseOptions struct {
	// LineLimit is the maximum number of frontmatter lines allowed. A value of 0
	// disables the line limit.
	LineLimit int
	// RequireDelimiter enforces the opening/closing '---' delimiters when true.
	RequireDelimiter bool
	// TrimLeadingBlankTail removes leading newline(s) from the tail after the
	// closing delimiter.
	TrimLeadingBlankTail bool
}

// ParseOption mutates ParseOptions.
type ParseOption func(*ParseOptions)

// WithLineLimit sets the maximum number of frontmatter lines. Use 0 to disable
// the limit entirely.
func WithLineLimit(limit int) ParseOption {
	return func(opts *ParseOptions) {
		if limit < 0 {
			limit = 0
		}

		opts.LineLimit = limit
	}
}

// WithRequireDelimiter toggles whether the opening/closing '---' delimiters are
// required. When false, the input is treated as frontmatter-only and the tail
// is empty.
func WithRequireDelimiter(required bool) ParseOption {
	return func(opts *ParseOptions) {
		opts.RequireDelimiter = required
	}
}

// WithTrimLeadingBlankTail removes leading newline(s) from the tail after the
// closing delimiter.
func WithTrimLeadingBlankTail(trim bool) ParseOption {
	return func(opts *ParseOptions) {
		opts.TrimLeadingBlankTail = trim
	}
}

// ParseFrontmatter parses frontmatter from a full ticket payload, returning the
// remaining body bytes (tail) without extra copies. An empty frontmatter block
// ("---\n---\n") is valid and returns an empty map. The tail starts immediately
// after the closing delimiter line; by default leading blank lines are trimmed.
//
// Defaults: RequireDelimiter=true, LineLimit=200, TrimLeadingBlankTail=true.
//
// Example:
//
//	payload := []byte("---\nstatus: open\n---\n# Title\nBody\n")
//	fm, tail, err := store.ParseFrontmatter(payload)
//	if err != nil {
//		return err
//	}
//	_ = fm["status"]
//	_ = tail // "# Title\nBody\n"
func ParseFrontmatter(src []byte, opts ...ParseOption) (Frontmatter, []byte, error) {
	options := applyParseOptions(opts)

	source := sliceLineSource(src)
	if options.RequireDelimiter {
		first, ok, err := source.next()
		if err != nil {
			return nil, nil, err
		}

		if !ok || !bytes.Equal(first.data, frontmatterDelimiterBytes) {
			return nil, nil, errors.New("parse frontmatter: missing opening delimiter")
		}
	}

	parser := newFrontmatterParser(source, options.RequireDelimiter, options.LineLimit)

	fm, sawDelimiter, err := parser.parse()
	if err != nil {
		return nil, nil, err
	}

	if options.RequireDelimiter && !sawDelimiter {
		return nil, nil, errors.New("parse frontmatter: missing closing delimiter")
	}

	if source.pending != nil {
		return nil, nil, errors.New("parse frontmatter: internal parse state")
	}

	tail := source.remainder()
	if options.RequireDelimiter && options.TrimLeadingBlankTail {
		tail = trimLeadingBlankLinesBytes(tail)
	}

	return fm, tail, nil
}

// ParseFrontmatterReader parses frontmatter from a reader containing a full
// ticket file, returning a reader positioned at the body (tail). An empty
// frontmatter block ("---\n---\n") is valid and returns an empty map. The tail
// starts immediately after the closing delimiter line; by default leading blank
// lines are trimmed.
//
// Defaults: RequireDelimiter=true, LineLimit=200, TrimLeadingBlankTail=true.
//
// Example:
//
//	f, _ := os.Open("ticket.md")
//	defer f.Close()
//	fm, tail, err := store.ParseFrontmatterReader(f)
//	if err != nil {
//		return err
//	}
//	body, _ := io.ReadAll(tail)
//	_ = fm["status"]
//	_ = body
func ParseFrontmatterReader(r io.Reader, opts ...ParseOption) (Frontmatter, io.Reader, error) {
	options := applyParseOptions(opts)

	var br *bufio.Reader
	if existing, ok := r.(*bufio.Reader); ok {
		br = existing
	} else {
		br = bufio.NewReader(r)
	}

	source := readerLineSource(br)
	if options.RequireDelimiter {
		first, ok, err := source.next()
		if err != nil {
			return nil, nil, err
		}

		if !ok || !bytes.Equal(first.data, frontmatterDelimiterBytes) {
			return nil, nil, errors.New("parse frontmatter: missing opening delimiter")
		}
	}

	parser := newFrontmatterParser(source, options.RequireDelimiter, options.LineLimit)

	fm, sawDelimiter, err := parser.parse()
	if err != nil {
		return nil, nil, err
	}

	if options.RequireDelimiter && !sawDelimiter {
		return nil, nil, errors.New("parse frontmatter: missing closing delimiter")
	}

	if source.pending != nil {
		return nil, nil, errors.New("parse frontmatter: internal parse state")
	}

	if options.RequireDelimiter && options.TrimLeadingBlankTail {
		err = trimLeadingBlankLinesReader(source.r)
		if err != nil {
			return nil, nil, err
		}
	}

	return fm, source.r, nil
}

type lineToken struct {
	data []byte
	num  int
}

type lineSource interface {
	next() (lineToken, bool, error)
	unread(tok lineToken)
}

type frontmatterParser struct {
	source          lineSource
	stopAtDelimiter bool
	linesSeen       int
	lineLimit       int
}

func newFrontmatterParser(source lineSource, stopAtDelimiter bool, lineLimit int) *frontmatterParser {
	return &frontmatterParser{source: source, stopAtDelimiter: stopAtDelimiter, lineLimit: lineLimit}
}

func (p *frontmatterParser) parse() (Frontmatter, bool, error) {
	out := make(Frontmatter)

	for {
		tok, ok, err := p.source.next()
		if err != nil {
			return nil, false, err
		}

		if !ok {
			return out, false, nil
		}

		if p.stopAtDelimiter && bytes.Equal(tok.data, frontmatterDelimiterBytes) {
			return out, true, nil
		}

		err = p.bumpLineCount()
		if err != nil {
			return nil, false, err
		}

		if len(bytes.TrimSpace(tok.data)) == 0 {
			continue
		}

		if tok.data[0] == ' ' || tok.data[0] == '\t' {
			return nil, false, frontmatterErr(tok.num, "unexpected indentation")
		}

		keyRaw, restRaw, ok := bytes.Cut(tok.data, []byte{':'})
		if !ok {
			return nil, false, frontmatterErr(tok.num, "missing ':'")
		}

		keyBytes := bytes.TrimSpace(keyRaw)
		if len(keyBytes) == 0 {
			return nil, false, frontmatterErr(tok.num, "empty key")
		}

		if bytes.IndexByte(keyBytes, ' ') != -1 || bytes.IndexByte(keyBytes, '\t') != -1 {
			return nil, false, frontmatterErr(tok.num, "whitespace in key")
		}

		key := string(keyBytes)

		if _, exists := out[key]; exists {
			return nil, false, frontmatterErr(tok.num, "duplicate key")
		}

		value := bytes.TrimSpace(restRaw)
		if len(value) != 0 {
			if value[0] == '[' {
				if value[len(value)-1] != ']' {
					return nil, false, frontmatterErr(tok.num, "unterminated list")
				}

				var list []string

				list, err = parseInlineList(value)
				if err != nil {
					return nil, false, frontmatterErr(tok.num, err.Error())
				}

				out[key] = Value{Kind: ValueList, List: list}

				continue
			}

			var scalar Scalar

			scalar, err = parseScalar(value)
			if err != nil {
				return nil, false, frontmatterErr(tok.num, err.Error())
			}

			out[key] = Value{Kind: ValueScalar, Scalar: scalar}

			continue
		}

		var blockLine lineToken

		blockLine, ok, err = p.nextNonEmpty()
		if err != nil {
			return nil, false, err
		}

		if !ok {
			return nil, false, frontmatterErr(tok.num, "missing block value")
		}

		if p.stopAtDelimiter && bytes.Equal(blockLine.data, frontmatterDelimiterBytes) {
			return nil, false, frontmatterErr(tok.num, "missing block value")
		}

		indent, hasTab := leadingSpacesBytes(blockLine.data)
		if hasTab || indent == 0 {
			return nil, false, frontmatterErr(blockLine.num, "expected indented block")
		}

		trimmed := blockLine.data[indent:]
		if len(trimmed) >= 2 && trimmed[0] == '-' && trimmed[1] == ' ' {
			var list []string

			list, err = p.parseBlockList(blockLine, indent)
			if err != nil {
				return nil, false, err
			}

			out[key] = Value{Kind: ValueList, List: list}

			continue
		}

		var obj map[string]Scalar

		obj, err = p.parseBlockObject(blockLine, indent)
		if err != nil {
			return nil, false, err
		}

		out[key] = Value{Kind: ValueObject, Object: obj}
	}
}

func (p *frontmatterParser) nextNonEmpty() (lineToken, bool, error) {
	for {
		tok, ok, err := p.source.next()
		if err != nil {
			return lineToken{}, false, err
		}

		if !ok {
			return lineToken{}, false, nil
		}

		if p.stopAtDelimiter && bytes.Equal(tok.data, frontmatterDelimiterBytes) {
			p.source.unread(tok)

			return lineToken{}, false, nil
		}

		err = p.bumpLineCount()
		if err != nil {
			return lineToken{}, false, err
		}

		if len(bytes.TrimSpace(tok.data)) == 0 {
			continue
		}

		return tok, true, nil
	}
}

func (p *frontmatterParser) parseBlockList(first lineToken, indent int) ([]string, error) {
	items := []string{}
	current := first

	for {
		item, err := parseListItem(current, indent)
		if err != nil {
			return nil, err
		}

		items = append(items, item)

		for {
			next, ok, err := p.source.next()
			if err != nil {
				return nil, err
			}

			if !ok {
				return items, nil
			}

			if p.stopAtDelimiter && bytes.Equal(next.data, frontmatterDelimiterBytes) {
				p.source.unread(next)

				return items, nil
			}

			if len(bytes.TrimSpace(next.data)) == 0 {
				err = p.bumpLineCount()
				if err != nil {
					return nil, err
				}

				continue
			}

			lineIndent, hasTab := leadingSpacesBytes(next.data)
			if hasTab {
				return nil, frontmatterErr(next.num, "tabs are not allowed")
			}

			if lineIndent < indent {
				p.source.unread(next)

				return items, nil
			}

			if lineIndent != indent {
				return nil, frontmatterErr(next.num, "inconsistent indentation")
			}

			err = p.bumpLineCount()
			if err != nil {
				return nil, err
			}

			current = next

			break
		}
	}
}

func (p *frontmatterParser) parseBlockObject(first lineToken, indent int) (map[string]Scalar, error) {
	obj := make(map[string]Scalar)
	current := first

	for {
		key, scalar, err := parseObjectEntry(current, indent)
		if err != nil {
			return nil, err
		}

		if _, exists := obj[key]; exists {
			return nil, frontmatterErr(current.num, "duplicate object key")
		}

		obj[key] = scalar

		next, ok, err := p.source.next()
		if err != nil {
			return nil, err
		}

		if !ok {
			return obj, nil
		}

		if p.stopAtDelimiter && bytes.Equal(next.data, frontmatterDelimiterBytes) {
			p.source.unread(next)

			return obj, nil
		}

		if len(bytes.TrimSpace(next.data)) == 0 {
			err = p.bumpLineCount()
			if err != nil {
				return nil, err
			}

			continue
		}

		lineIndent, hasTab := leadingSpacesBytes(next.data)
		if hasTab {
			return nil, frontmatterErr(next.num, "tabs are not allowed")
		}

		if lineIndent < indent {
			p.source.unread(next)

			return obj, nil
		}

		if lineIndent != indent {
			return nil, frontmatterErr(next.num, "inconsistent indentation")
		}

		err = p.bumpLineCount()
		if err != nil {
			return nil, err
		}

		current = next
	}
}

func parseInlineList(value []byte) ([]string, error) {
	inner := bytes.TrimSpace(value[1 : len(value)-1])
	if len(inner) == 0 {
		return []string{}, nil
	}

	parts := bytes.Split(inner, []byte{','})

	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := bytes.TrimSpace(part)
		if len(item) == 0 {
			return nil, errors.New("empty list item")
		}

		parsed, err := parseString(item)
		if err != nil {
			return nil, err
		}

		items = append(items, parsed)
	}

	return items, nil
}

func parseListItem(tok lineToken, indent int) (string, error) {
	lineIndent, hasTab := leadingSpacesBytes(tok.data)
	if hasTab {
		return "", frontmatterErr(tok.num, "tabs are not allowed")
	}

	if lineIndent != indent {
		return "", frontmatterErr(tok.num, "inconsistent indentation")
	}

	trimmed := tok.data[indent:]
	if len(trimmed) < 2 || trimmed[0] != '-' || trimmed[1] != ' ' {
		return "", frontmatterErr(tok.num, "expected list item")
	}

	item := bytes.TrimSpace(trimmed[2:])
	if len(item) == 0 {
		return "", frontmatterErr(tok.num, "empty list item")
	}

	parsed, err := parseString(item)
	if err != nil {
		return "", frontmatterErr(tok.num, err.Error())
	}

	return parsed, nil
}

func parseObjectEntry(tok lineToken, indent int) (string, Scalar, error) {
	lineIndent, hasTab := leadingSpacesBytes(tok.data)
	if hasTab {
		return "", Scalar{}, frontmatterErr(tok.num, "tabs are not allowed")
	}

	if lineIndent != indent {
		return "", Scalar{}, frontmatterErr(tok.num, "inconsistent indentation")
	}

	trimmed := tok.data[indent:]

	keyRaw, restRaw, ok := bytes.Cut(trimmed, []byte{':'})
	if !ok {
		return "", Scalar{}, frontmatterErr(tok.num, "missing ':' in object entry")
	}

	keyBytes := bytes.TrimSpace(keyRaw)
	if len(keyBytes) == 0 {
		return "", Scalar{}, frontmatterErr(tok.num, "empty object key")
	}

	if bytes.IndexByte(keyBytes, ' ') != -1 || bytes.IndexByte(keyBytes, '\t') != -1 {
		return "", Scalar{}, frontmatterErr(tok.num, "whitespace in object key")
	}

	key := string(keyBytes)

	value := bytes.TrimSpace(restRaw)
	if len(value) == 0 {
		return "", Scalar{}, frontmatterErr(tok.num, "empty object value")
	}

	scalar, err := parseScalar(value)
	if err != nil {
		return "", Scalar{}, frontmatterErr(tok.num, err.Error())
	}

	return key, scalar, nil
}

func parseScalar(value []byte) (Scalar, error) {
	if len(value) == 0 {
		return Scalar{}, errors.New("empty scalar")
	}

	if valueHasUnsupportedPrefix(value) {
		return Scalar{}, errors.New("unsupported value")
	}

	if bytes.Equal(value, trueBytes) || bytes.Equal(value, falseBytes) {
		return Scalar{Kind: ScalarBool, Bool: value[0] == 't'}, nil
	}

	if parsed, ok := parseInt(value); ok {
		return Scalar{Kind: ScalarInt, Int: parsed}, nil
	}

	parsed, err := parseString(value)
	if err != nil {
		return Scalar{}, err
	}

	return Scalar{Kind: ScalarString, String: parsed}, nil
}

func valueHasUnsupportedPrefix(value []byte) bool {
	if len(value) == 0 {
		return false
	}

	switch value[0] {
	case '[', '{', '}', ']', '|', '>', '&', '*', '!', '%', '@', '`':
		return true
	}

	return len(value) >= 2 && value[0] == '-' && value[1] == ' '
}

func parseInt(value []byte) (int64, bool) {
	if len(value) == 0 {
		return 0, false
	}

	neg := false
	idx := 0

	if value[0] == '-' {
		neg = true

		idx++
		if idx == len(value) {
			return 0, false
		}
	}

	var n int64

	for ; idx < len(value); idx++ {
		r := value[idx]
		if r < '0' || r > '9' {
			return 0, false
		}

		digit := int64(r - '0')
		if n > (int64(^uint64(0)>>1)-digit)/10 {
			return 0, false
		}

		n = n*10 + digit
	}

	if neg {
		n = -n
	}

	return n, true
}

func parseString(value []byte) (string, error) {
	if len(value) > 0 && value[0] == '"' {
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("unterminated quoted string")
		}

		parsed, err := strconv.Unquote(string(value))
		if err != nil {
			return "", errors.New("invalid quoted string")
		}

		return parsed, nil
	}

	if len(value) > 0 && value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated quoted string")
		}

		return string(value[1 : len(value)-1]), nil
	}

	return string(value), nil
}

func (p *frontmatterParser) bumpLineCount() error {
	p.linesSeen++
	if p.lineLimit == 0 {
		return nil
	}

	if p.linesSeen > p.lineLimit {
		return errors.New("parse frontmatter: exceeds maximum line limit")
	}

	return nil
}

func leadingSpacesBytes(line []byte) (int, bool) {
	count := 0

	for _, r := range line {
		if r == ' ' {
			count++

			continue
		}

		if r == '\t' {
			return 0, true
		}

		break
	}

	return count, false
}

func trimCRBytes(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}

	return line
}

func trimLeadingBlankLinesBytes(tail []byte) []byte {
	for len(tail) > 0 {
		if tail[0] == '\n' {
			tail = tail[1:]

			continue
		}

		if tail[0] == '\r' {
			if len(tail) >= 2 && tail[1] == '\n' {
				tail = tail[2:]

				continue
			}
		}

		break
	}

	return tail
}

func trimLeadingBlankLinesReader(r *bufio.Reader) error {
	for {
		peek, err := r.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return fmt.Errorf("peek tail: %w", err)
		}

		switch peek[0] {
		case '\n':
			_, _ = r.ReadByte()

			continue
		case '\r':
			peek2, err := r.Peek(2)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}

				return fmt.Errorf("peek tail: %w", err)
			}

			if len(peek2) >= 2 && peek2[1] == '\n' {
				_, _ = r.ReadByte()
				_, _ = r.ReadByte()

				continue
			}
		}

		return nil
	}
}

func applyParseOptions(opts []ParseOption) ParseOptions {
	options := ParseOptions{
		LineLimit:            maxFrontmatterLines,
		RequireDelimiter:     true,
		TrimLeadingBlankTail: true,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	return options
}

type frontmatterError struct {
	line int
	msg  string
}

func (e *frontmatterError) Error() string {
	return fmt.Sprintf("parse frontmatter line %d: %s", e.line, e.msg)
}

func frontmatterErr(line int, msg string) error {
	return &frontmatterError{line: line, msg: msg}
}

type sliceLineReader struct {
	data    []byte
	idx     int
	lineNum int
	pending *lineToken
}

func sliceLineSource(data []byte) *sliceLineReader {
	return &sliceLineReader{data: data}
}

func (s *sliceLineReader) next() (lineToken, bool, error) {
	if s.pending != nil {
		out := *s.pending
		s.pending = nil

		return out, true, nil
	}

	if s.idx >= len(s.data) {
		return lineToken{}, false, nil
	}

	start := s.idx
	for s.idx < len(s.data) && s.data[s.idx] != '\n' {
		s.idx++
	}

	end := s.idx
	if s.idx < len(s.data) && s.data[s.idx] == '\n' {
		s.idx++
	}

	s.lineNum++
	line := trimCRBytes(s.data[start:end])

	return lineToken{data: line, num: s.lineNum}, true, nil
}

func (s *sliceLineReader) unread(tok lineToken) {
	s.pending = &lineToken{data: tok.data, num: tok.num}
}

func (s *sliceLineReader) remainder() []byte {
	if s.idx >= len(s.data) {
		return nil
	}

	return s.data[s.idx:]
}

type bufferedLineReader struct {
	r       *bufio.Reader
	lineNum int
	pending *lineToken
}

func readerLineSource(r *bufio.Reader) *bufferedLineReader {
	return &bufferedLineReader{r: r}
}

func (b *bufferedLineReader) next() (lineToken, bool, error) {
	if b.pending != nil {
		out := *b.pending
		b.pending = nil

		return out, true, nil
	}

	chunk, err := b.r.ReadSlice('\n')
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
		return lineToken{}, false, fmt.Errorf("read line: %w", err)
	}

	if len(chunk) == 0 && errors.Is(err, io.EOF) {
		return lineToken{}, false, nil
	}

	if errors.Is(err, bufio.ErrBufferFull) {
		buf := append([]byte{}, chunk...)
		for errors.Is(err, bufio.ErrBufferFull) {
			chunk, err = b.r.ReadSlice('\n')
			if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
				return lineToken{}, false, fmt.Errorf("read line: %w", err)
			}

			buf = append(buf, chunk...)
		}

		chunk = buf
	}

	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		chunk = chunk[:len(chunk)-1]
	}

	line := trimCRBytes(chunk)
	b.lineNum++

	return lineToken{data: line, num: b.lineNum}, true, nil
}

func (b *bufferedLineReader) unread(tok lineToken) {
	b.pending = &lineToken{data: tok.data, num: tok.num}
}
