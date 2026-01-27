package testutil

// ByteStream reads bytes sequentially from a byte slice.
//
// Used by fuzz tests to deterministically derive values from fuzz input.
// When the stream is exhausted, all reads return zero values. This ensures
// determinism: the same input always produces the same sequence of values.
type ByteStream struct {
	bytes []byte
	pos   int
}

// NewByteStream creates a stream over the given bytes.
func NewByteStream(b []byte) *ByteStream {
	return &ByteStream{bytes: b}
}

// HasMore reports whether unread bytes remain.
func (s *ByteStream) HasMore() bool {
	return s.pos < len(s.bytes)
}

// NextByte returns the next byte, or 0 if exhausted.
func (s *ByteStream) NextByte() byte {
	if s.pos >= len(s.bytes) {
		return 0
	}

	v := s.bytes[s.pos]
	s.pos++

	return v
}

// NextBytes reads n bytes, padding with zeros if exhausted.
func (s *ByteStream) NextBytes(n int) []byte {
	if n <= 0 {
		return []byte{}
	}

	out := make([]byte, n)
	for i := range n {
		out[i] = s.NextByte()
	}

	return out
}

// NextInt returns a non-negative int derived from the next byte.
func (s *ByteStream) NextInt(maxVal int) int {
	if maxVal <= 0 {
		return 0
	}

	return int(s.NextByte()) % maxVal
}

// NextBool returns a boolean derived from the next byte.
func (s *ByteStream) NextBool() bool {
	return s.NextByte()&1 == 1
}

// NextString returns a string of length 1-maxLen from the stream.
func (s *ByteStream) NextString(maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	length := 1 + s.NextInt(maxLen)
	bytes := s.NextBytes(length)

	// Convert to printable ASCII
	for i := range bytes {
		bytes[i] = 'a' + (bytes[i] % 26)
	}

	return string(bytes)
}
