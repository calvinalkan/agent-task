package fmcache

import (
	"encoding/binary"
)

// Field defines a single field in an index.
type Field struct {
	name string
	size int
}

// Uint8Field defines a 1-byte unsigned integer field.
func Uint8Field(name string) Field {
	return Field{name: name, size: 1}
}

// Uint16Field defines a 2-byte unsigned integer field.
func Uint16Field(name string) Field {
	return Field{name: name, size: 2}
}

// BoolField defines a 1-byte boolean field.
func BoolField(name string) Field {
	return Field{name: name, size: 1}
}

// StringField defines a fixed-size null-padded string field.
func StringField(name string, maxLen int) Field {
	return Field{name: name, size: maxLen}
}

// Fields defines the layout of an index.
type Fields []Field

// Size returns the total size in bytes.
func (f Fields) Size() int {
	size := 0
	for _, field := range f {
		size += field.size
	}

	return size
}

// Offset returns the byte offset of a field by name.
// Panics if field not found.
func (f Fields) Offset(name string) int {
	offset := 0

	for _, field := range f {
		if field.name == name {
			return offset
		}

		offset += field.size
	}

	panic("fmcache: field not found: " + name)
}

// FieldWriter builds index bytes.
type FieldWriter struct {
	fields Fields
	buf    []byte
}

// NewWriter creates a writer for the given field layout.
func (f Fields) NewWriter() *FieldWriter {
	return &FieldWriter{
		fields: f,
		buf:    make([]byte, f.Size()),
	}
}

// SetUint8 sets a uint8 field.
func (w *FieldWriter) SetUint8(name string, v uint8) {
	offset := w.fields.Offset(name)
	w.buf[offset] = v
}

// SetUint16 sets a uint16 field.
func (w *FieldWriter) SetUint16(name string, v uint16) {
	offset := w.fields.Offset(name)
	binary.LittleEndian.PutUint16(w.buf[offset:], v)
}

// SetBool sets a bool field.
func (w *FieldWriter) SetBool(name string, v bool) {
	offset := w.fields.Offset(name)
	if v {
		w.buf[offset] = 1
	} else {
		w.buf[offset] = 0
	}
}

// SetString sets a string field (null-padded to field size).
func (w *FieldWriter) SetString(name string, value string) {
	offset := w.fields.Offset(name)

	var size int

	for i, field := range w.fields {
		if field.name == name {
			size = w.fields[i].size

			break
		}
	}

	copy(w.buf[offset:offset+size], value)
}

// Bytes returns the built index bytes.
func (w *FieldWriter) Bytes() []byte {
	return w.buf
}

// FieldReader reads index bytes.
type FieldReader struct {
	fields Fields
	buf    []byte
}

// NewReader creates a reader for the given index bytes.
func (f Fields) NewReader(buf []byte) FieldReader {
	return FieldReader{
		fields: f,
		buf:    buf,
	}
}

// Uint8 reads a uint8 field.
func (r FieldReader) Uint8(name string) uint8 {
	offset := r.fields.Offset(name)

	return r.buf[offset]
}

// Uint16 reads a uint16 field.
func (r FieldReader) Uint16(name string) uint16 {
	offset := r.fields.Offset(name)

	return binary.LittleEndian.Uint16(r.buf[offset:])
}

// Bool reads a bool field.
func (r FieldReader) Bool(name string) bool {
	offset := r.fields.Offset(name)

	return r.buf[offset] != 0
}

// String reads a string field (stops at null or field size).
func (r FieldReader) String(name string) string {
	offset := r.fields.Offset(name)

	var size int

	for i, field := range r.fields {
		if field.name == name {
			size = r.fields[i].size

			break
		}
	}

	data := r.buf[offset : offset+size]
	// Find null terminator
	for i, c := range data {
		if c == 0 {
			return string(data[:i])
		}
	}

	return string(data)
}

// Raw returns the underlying bytes.
func (r FieldReader) Raw() []byte {
	return r.buf
}
