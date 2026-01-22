package testutil

import "encoding/binary"

// ByteStream reads bytes sequentially from a byte slice.
//
// Used by fuzz tests to deterministically derive values from fuzz input.
// When the stream is exhausted, all reads return zero values. This ensures
// determinism: the same input always produces the same sequence of values,
// which is required for Go's fuzzer to minimize failing inputs.
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

// NextInt64 reads 8 bytes as a little-endian int64.
func (s *ByteStream) NextInt64() int64 {
	var raw [8]byte
	for i := range raw {
		raw[i] = s.NextByte()
	}

	return getInt64LE(raw[:])
}

// NextUint32 reads 4 bytes as a little-endian uint32.
func (s *ByteStream) NextUint32() uint32 {
	var v uint32

	v |= uint32(s.NextByte())
	v |= uint32(s.NextByte()) << 8
	v |= uint32(s.NextByte()) << 16
	v |= uint32(s.NextByte()) << 24

	return v
}

// NextUint64 reads 8 bytes as a little-endian uint64.
func (s *ByteStream) NextUint64() uint64 {
	var raw [8]byte
	for i := range raw {
		raw[i] = s.NextByte()
	}

	return binary.LittleEndian.Uint64(raw[:])
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

// Rest returns the remaining unread bytes (nil if exhausted).
func (s *ByteStream) Rest() []byte {
	if s.pos >= len(s.bytes) {
		return nil
	}

	return s.bytes[s.pos:]
}

func getInt64LE(buf []byte) int64 {
	_ = buf[7] // BCE hint

	return int64(buf[0]) |
		int64(buf[1])<<8 |
		int64(buf[2])<<16 |
		int64(buf[3])<<24 |
		int64(buf[4])<<32 |
		int64(buf[5])<<40 |
		int64(buf[6])<<48 |
		int64(buf[7])<<56
}
