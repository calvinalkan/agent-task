package testutil

// ByteStream is a tiny deterministic byte reader used by fuzz helpers that
// need to consume bytes before options/decoders exist.
//
// It is intentionally very small (no randomness) so Go fuzzing can minimize
// failing inputs.
//
// Missing bytes are treated as 0.
type ByteStream struct {
	b []byte
	i int
}

// NewByteStream creates a new ByteStream reading from b.
func NewByteStream(b []byte) *ByteStream {
	return &ByteStream{b: b}
}

// HasMore reports whether unread bytes remain.
func (s *ByteStream) HasMore() bool {
	return s.i < len(s.b)
}

// NextByte returns the next byte (or 0 if exhausted).
func (s *ByteStream) NextByte() byte {
	if s.i >= len(s.b) {
		return 0
	}

	v := s.b[s.i]
	s.i++

	return v
}

// NextUint32 reads 4 bytes little-endian as a uint32.
func (s *ByteStream) NextUint32() uint32 {
	var v uint32

	v |= uint32(s.NextByte())
	v |= uint32(s.NextByte()) << 8
	v |= uint32(s.NextByte()) << 16
	v |= uint32(s.NextByte()) << 24

	return v
}

// Rest returns the remaining unread bytes.
func (s *ByteStream) Rest() []byte {
	if s.i >= len(s.b) {
		return nil
	}

	return s.b[s.i:]
}
