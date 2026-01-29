package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

const (
	frontmatterDelimiter = "---"
	defaultMaxLines      = 200 // Default line limit; override with WithLineLimit.
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
	// Default: 200
	LineLimit int

	// RequireDelimiter enforces the opening/closing '---' delimiters when true.
	// Default: true
	RequireDelimiter bool

	// TrimLeadingBlankTail removes leading newline(s) from the tail after the
	// closing delimiter. Default: true
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

// ParseBytes parses frontmatter from a full ticket payload, returning the
// remaining body bytes (tail) without extra copies. An empty frontmatter block
// ("---\n---\n") is valid and returns an empty Frontmatter. The tail starts
// immediately after the closing delimiter line; by default leading blank lines
// are trimmed.
//
// See the package doc for the supported grammar and strict formatting rules.
//
// All parsed data (keys, string values, list items) points into the input slice.
// The returned Frontmatter is only valid while the input slice is alive.
//
// Defaults: RequireDelimiter=true, LineLimit=200, TrimLeadingBlankTail=true.
func ParseBytes(src []byte, opts ...ParseOption) (Frontmatter, []byte, error) {
	options := applyParseOptions(opts)

	source := sliceLineSource(src)
	if options.RequireDelimiter {
		first, ok, err := source.next()
		if err != nil {
			return Frontmatter{}, nil, err
		}

		if !ok || !bytes.Equal(first.data, frontmatterDelimiterBytes) {
			return Frontmatter{}, nil, errors.New("missing opening delimiter")
		}
	}

	parser := newFrontmatterParser(source, options.RequireDelimiter, options.LineLimit)

	fm, sawDelimiter, err := parser.parse()
	if err != nil {
		return Frontmatter{}, nil, err
	}

	if options.RequireDelimiter && !sawDelimiter {
		return Frontmatter{}, nil, errors.New("missing closing delimiter")
	}

	if source.hasPending {
		return Frontmatter{}, nil, fmt.Errorf("impossible internal parse state: input: %q", src)
	}

	tail := source.remainder()
	if options.RequireDelimiter && options.TrimLeadingBlankTail {
		tail = trimLeadingBlankLinesBytes(tail)
	}

	return fm, tail, nil
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
	capEntries := 8
	if p.lineLimit > 0 && p.lineLimit < capEntries {
		capEntries = p.lineLimit
	}

	entries := make([]Entry, 0, capEntries)

	for {
		tok, ok, err := p.source.next()
		if err != nil {
			return Frontmatter{}, false, err
		}

		if !ok {
			return Frontmatter{entries: entries}, false, nil
		}

		if p.stopAtDelimiter && bytes.Equal(tok.data, frontmatterDelimiterBytes) {
			return Frontmatter{entries: entries}, true, nil
		}

		err = p.bumpLineCount()
		if err != nil {
			return Frontmatter{}, false, err
		}

		if isCommentLineASCII(tok.data) {
			return Frontmatter{}, false, parseErr(tok.num, "comments are not supported; quote '#' if literal")
		}

		if isBlankLineASCII(tok.data) {
			continue
		}

		if tok.data[0] == ' ' || tok.data[0] == '\t' {
			return Frontmatter{}, false, parseErr(tok.num, "unexpected indentation")
		}

		keyRaw, restRaw, ok := bytes.Cut(tok.data, []byte{':'})
		if !ok {
			return Frontmatter{}, false, parseErr(tok.num, "missing ':'")
		}

		keyBytes := keyRaw
		if len(keyBytes) == 0 {
			return Frontmatter{}, false, parseErr(tok.num, "empty key")
		}

		if bytes.IndexByte(keyBytes, ' ') != -1 || bytes.IndexByte(keyBytes, '\t') != -1 {
			return Frontmatter{}, false, parseErr(tok.num, "whitespace in key")
		}

		// Check for duplicate key
		for i := range entries {
			if bytes.Equal(entries[i].Key, keyBytes) {
				return Frontmatter{}, false, parseErr(tok.num, "duplicate key")
			}
		}

		if len(restRaw) != 0 {
			// Strict format avoids TrimSpace on the hot path.
			if restRaw[0] != ' ' {
				return Frontmatter{}, false, parseErr(tok.num, "expected single space after ':'")
			}

			if len(restRaw) == 1 {
				return Frontmatter{}, false, parseErr(tok.num, "empty scalar")
			}

			if restRaw[1] == ' ' || restRaw[1] == '\t' {
				return Frontmatter{}, false, parseErr(tok.num, "unexpected whitespace after ':'")
			}

			valueBytes := restRaw[1:]

			if valueBytes[len(valueBytes)-1] == ' ' || valueBytes[len(valueBytes)-1] == '\t' {
				return Frontmatter{}, false, parseErr(tok.num, "trailing whitespace")
			}

			if valueBytes[0] == '[' {
				if valueBytes[len(valueBytes)-1] != ']' {
					return Frontmatter{}, false, parseErr(tok.num, "unterminated list")
				}

				var list [][]byte

				list, err = parseInlineList(valueBytes)
				if err != nil {
					return Frontmatter{}, false, parseErr(tok.num, err.Error())
				}

				entries = append(entries, Entry{Key: keyBytes, Value: Value{Kind: ValueList, List: list}})

				continue
			}

			var scalar Scalar

			scalar, err = parseScalar(valueBytes)
			if err != nil {
				return Frontmatter{}, false, parseErr(tok.num, err.Error())
			}

			entries = append(entries, Entry{Key: keyBytes, Value: Value{Kind: ValueScalar, Scalar: scalar}})

			continue
		}

		var blockLine lineToken

		blockLine, ok, err = p.nextNonEmpty()
		if err != nil {
			return Frontmatter{}, false, err
		}

		if !ok {
			return Frontmatter{}, false, parseErr(tok.num, "missing block value")
		}

		if p.stopAtDelimiter && bytes.Equal(blockLine.data, frontmatterDelimiterBytes) {
			return Frontmatter{}, false, parseErr(tok.num, "missing block value")
		}

		indent, hasTab := leadingSpacesBytes(blockLine.data)
		if hasTab || indent == 0 {
			return Frontmatter{}, false, parseErr(blockLine.num, "expected indented block")
		}

		trimmed := blockLine.data[indent:]
		if len(trimmed) >= 2 && trimmed[0] == '-' && trimmed[1] == ' ' {
			var list [][]byte

			list, err = p.parseBlockList(blockLine, indent)
			if err != nil {
				return Frontmatter{}, false, err
			}

			entries = append(entries, Entry{Key: keyBytes, Value: Value{Kind: ValueList, List: list}})

			continue
		}

		var obj []ObjectEntry

		obj, err = p.parseBlockObject(blockLine, indent)
		if err != nil {
			return Frontmatter{}, false, err
		}

		entries = append(entries, Entry{Key: keyBytes, Value: Value{Kind: ValueObject, Object: obj}})
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

		if isCommentLineASCII(tok.data) {
			return lineToken{}, false, parseErr(tok.num, "comments are not supported; quote '#' if literal")
		}

		if isBlankLineASCII(tok.data) {
			continue
		}

		return tok, true, nil
	}
}

func (p *frontmatterParser) parseBlockList(first lineToken, indent int) ([][]byte, error) {
	items := make([][]byte, 0, 4)

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

			if isCommentLineASCII(next.data) {
				err = p.bumpLineCount()
				if err != nil {
					return nil, err
				}

				return nil, parseErr(next.num, "comments are not supported; quote '#' if literal")
			}

			if isBlankLineASCII(next.data) {
				err = p.bumpLineCount()
				if err != nil {
					return nil, err
				}

				continue
			}

			lineIndent, hasTab := leadingSpacesBytes(next.data)
			if hasTab {
				return nil, parseErr(next.num, "tabs are not allowed")
			}

			if lineIndent < indent {
				p.source.unread(next)

				return items, nil
			}

			if lineIndent != indent {
				return nil, parseErr(next.num, "inconsistent indentation")
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

func (p *frontmatterParser) parseBlockObject(first lineToken, indent int) ([]ObjectEntry, error) {
	entries := make([]ObjectEntry, 0, 4)

	current := first

	for {
		key, scalar, err := parseObjectEntry(current, indent)
		if err != nil {
			return nil, err
		}

		// Check for duplicate key
		for i := range entries {
			if bytes.Equal(entries[i].Key, key) {
				return nil, parseErr(current.num, "duplicate object key")
			}
		}

		entries = append(entries, ObjectEntry{Key: key, Value: scalar})

		next, ok, err := p.source.next()
		if err != nil {
			return nil, err
		}

		if !ok {
			return entries, nil
		}

		if p.stopAtDelimiter && bytes.Equal(next.data, frontmatterDelimiterBytes) {
			p.source.unread(next)

			return entries, nil
		}

		if isCommentLineASCII(next.data) {
			err = p.bumpLineCount()
			if err != nil {
				return nil, err
			}

			return nil, parseErr(next.num, "comments are not supported; quote '#' if literal")
		}

		if isBlankLineASCII(next.data) {
			err = p.bumpLineCount()
			if err != nil {
				return nil, err
			}

			continue
		}

		lineIndent, hasTab := leadingSpacesBytes(next.data)
		if hasTab {
			return nil, parseErr(next.num, "tabs are not allowed")
		}

		if lineIndent < indent {
			p.source.unread(next)

			return entries, nil
		}

		if lineIndent != indent {
			return nil, parseErr(next.num, "inconsistent indentation")
		}

		err = p.bumpLineCount()
		if err != nil {
			return nil, err
		}

		current = next
	}
}

func parseInlineList(value []byte) ([][]byte, error) {
	inner := value[1 : len(value)-1]
	if len(inner) == 0 {
		return [][]byte{}, nil
	}

	// Strict format avoids trimming per item.
	if inner[0] == ' ' || inner[0] == '\t' {
		return nil, errors.New("unexpected leading whitespace in list")
	}

	capItems := 1

	for _, b := range inner {
		if b == ',' {
			capItems++
		}
	}

	items := make([][]byte, 0, capItems)
	start := 0

	for i := 0; i <= len(inner); i++ {
		if i < len(inner) && inner[i] != ',' {
			continue
		}

		item := inner[start:i]
		if len(item) == 0 {
			return nil, errors.New("empty list item")
		}

		if item[0] == ' ' || item[0] == '\t' || item[len(item)-1] == ' ' || item[len(item)-1] == '\t' {
			return nil, errors.New("unexpected whitespace in list item")
		}

		parsed := item
		if item[0] == '"' || item[0] == '\'' {
			// Avoid parseStringBytes call for unquoted items in hot path.
			var err error

			parsed, err = parseStringBytes(item)
			if err != nil {
				return nil, err
			}
		} else if bytes.IndexByte(item, '#') != -1 {
			// Fast reject for unquoted items; quoted strings can contain '#'.
			return nil, errors.New("comments are not supported; quote '#' if literal")
		}

		if len(parsed) == 0 {
			return nil, errors.New("empty list item")
		}

		items = append(items, parsed)

		if i < len(inner) {
			if i+1 >= len(inner) {
				return nil, errors.New("empty list item")
			}

			// Strict format avoids trimming between items.
			if inner[i+1] != ' ' {
				return nil, errors.New("expected space after ','")
			}

			if i+2 < len(inner) && inner[i+2] == ' ' {
				return nil, errors.New("unexpected whitespace after ','")
			}

			start = i + 2

			continue
		}

		start = i + 1
	}

	return items, nil
}

func parseListItem(tok lineToken, indent int) ([]byte, error) {
	lineIndent, hasTab := leadingSpacesBytes(tok.data)
	if hasTab {
		return nil, parseErr(tok.num, "tabs are not allowed")
	}

	if lineIndent != indent {
		return nil, parseErr(tok.num, "inconsistent indentation")
	}

	trimmed := tok.data[indent:]
	if len(trimmed) < 2 || trimmed[0] != '-' || trimmed[1] != ' ' {
		return nil, parseErr(tok.num, "expected list item")
	}

	item := trimmed[2:]
	if len(item) == 0 {
		return nil, parseErr(tok.num, "empty list item")
	}

	// Strict format avoids trimming list items.

	if item[0] == ' ' || item[0] == '\t' || item[len(item)-1] == ' ' || item[len(item)-1] == '\t' {
		return nil, parseErr(tok.num, "unexpected whitespace in list item")
	}

	parsed := item
	if item[0] == '"' || item[0] == '\'' {
		// Avoid parseStringBytes call for unquoted items in hot path.
		var err error

		parsed, err = parseStringBytes(item)
		if err != nil {
			return nil, parseErr(tok.num, err.Error())
		}
	} else if bytes.IndexByte(item, '#') != -1 {
		// Fast reject for unquoted items; quoted strings can contain '#'.
		return nil, parseErr(tok.num, "comments are not supported; quote '#' if literal")
	}

	if len(parsed) == 0 {
		return nil, parseErr(tok.num, "empty list item")
	}

	return parsed, nil
}

func parseObjectEntry(tok lineToken, indent int) ([]byte, Scalar, error) {
	lineIndent, hasTab := leadingSpacesBytes(tok.data)
	if hasTab {
		return nil, Scalar{}, parseErr(tok.num, "tabs are not allowed")
	}

	if lineIndent != indent {
		return nil, Scalar{}, parseErr(tok.num, "inconsistent indentation")
	}

	trimmed := tok.data[indent:]

	keyRaw, restRaw, ok := bytes.Cut(trimmed, []byte{':'})
	if !ok {
		return nil, Scalar{}, parseErr(tok.num, "missing ':' in object entry")
	}

	keyBytes := keyRaw
	if len(keyBytes) == 0 {
		return nil, Scalar{}, parseErr(tok.num, "empty object key")
	}

	if bytes.IndexByte(keyBytes, ' ') != -1 || bytes.IndexByte(keyBytes, '\t') != -1 {
		return nil, Scalar{}, parseErr(tok.num, "whitespace in object key")
	}

	if len(restRaw) == 0 {
		return nil, Scalar{}, parseErr(tok.num, "empty object value")
	}

	// Strict format avoids TrimSpace on the hot path.
	if restRaw[0] != ' ' {
		return nil, Scalar{}, parseErr(tok.num, "expected single space after ':' in object")
	}

	if len(restRaw) == 1 {
		return nil, Scalar{}, parseErr(tok.num, "empty object value")
	}

	if restRaw[1] == ' ' || restRaw[1] == '\t' {
		return nil, Scalar{}, parseErr(tok.num, "unexpected whitespace after ':' in object")
	}

	value := restRaw[1:]

	if value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return nil, Scalar{}, parseErr(tok.num, "trailing whitespace in object value")
	}

	scalar, err := parseScalar(value)
	if err != nil {
		return nil, Scalar{}, parseErr(tok.num, err.Error())
	}

	return keyBytes, scalar, nil
}

func parseScalar(value []byte) (Scalar, error) {
	if len(value) == 0 {
		return Scalar{}, errors.New("empty scalar")
	}

	if value[0] == '"' || value[0] == '\'' {
		parsed, err := parseStringBytes(value)
		if err != nil {
			return Scalar{}, err
		}

		return Scalar{Kind: ScalarString, Bytes: parsed}, nil
	}

	if bytes.IndexByte(value, '#') != -1 {
		// Fast reject for unquoted scalars; quoted strings can contain '#'.
		return Scalar{}, errors.New("comments are not supported; quote '#' if literal")
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

	parsed, err := parseStringBytes(value)
	if err != nil {
		return Scalar{}, err
	}

	return Scalar{Kind: ScalarString, Bytes: parsed}, nil
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

// parseStringBytes returns the string content as []byte.
// For quoted strings, this allocates because we need to unescape.
// For unquoted strings, returns a subslice of value (zero-copy).
func parseStringBytes(value []byte) ([]byte, error) {
	if len(value) > 0 && value[0] == '"' {
		if len(value) < 2 || value[len(value)-1] != '"' {
			return nil, errors.New("unterminated quoted string")
		}

		parsed, err := strconv.Unquote(string(value))
		if err != nil {
			return nil, errors.New("invalid quoted string")
		}

		return []byte(parsed), nil
	}

	if len(value) > 0 && value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return nil, errors.New("unterminated quoted string")
		}

		return value[1 : len(value)-1], nil
	}

	return value, nil
}

func (p *frontmatterParser) bumpLineCount() error {
	p.linesSeen++
	if p.lineLimit == 0 {
		return nil
	}

	if p.linesSeen > p.lineLimit {
		return parseErr(p.linesSeen, fmt.Sprintf("exceeds line limit %d", p.lineLimit))
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

// isBlankLineASCII checks for all-space/tab lines without TrimSpace overhead.
func isBlankLineASCII(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}

	return true
}

// isCommentLineASCII reports whether the line starts with a '#' after optional indentation.
func isCommentLineASCII(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\t' {
			continue
		}

		return c == '#'
	}

	return false
}

func applyParseOptions(opts []ParseOption) ParseOptions {
	options := ParseOptions{
		LineLimit:            defaultMaxLines,
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
	return fmt.Sprintf("line %d: %s", e.line, e.msg)
}

func parseErr(line int, msg string) error {
	return &frontmatterError{line: line, msg: msg}
}

type sliceLineReader struct {
	data    []byte
	idx     int
	lineNum int
	// Store pending token inline to avoid per-unread heap allocations.
	pending    lineToken
	hasPending bool
}

func sliceLineSource(data []byte) *sliceLineReader {
	return &sliceLineReader{data: data}
}

func (s *sliceLineReader) next() (lineToken, bool, error) {
	if s.hasPending {
		out := s.pending
		s.hasPending = false

		return out, true, nil
	}

	if s.idx >= len(s.data) {
		return lineToken{}, false, nil
	}

	start := s.idx
	// Use IndexByte to avoid byte-by-byte scans in the hot path.
	if offset := bytes.IndexByte(s.data[s.idx:], '\n'); offset >= 0 {
		end := s.idx + offset
		s.idx = end + 1
		s.lineNum++
		line := trimCRBytes(s.data[start:end])

		return lineToken{data: line, num: s.lineNum}, true, nil
	}

	end := len(s.data)
	s.idx = end
	s.lineNum++
	line := trimCRBytes(s.data[start:end])

	return lineToken{data: line, num: s.lineNum}, true, nil
}

func (s *sliceLineReader) unread(tok lineToken) {
	s.pending = lineToken{data: tok.data, num: tok.num}
	s.hasPending = true
}

func (s *sliceLineReader) remainder() []byte {
	if s.idx >= len(s.data) {
		return nil
	}

	return s.data[s.idx:]
}
