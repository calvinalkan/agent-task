// Package testutil provides test utilities for the slotcache package.
// It includes harnesses for comparing the real implementation against
// an in-memory behavioral model, fuzz decoders, and helper functions.
package testutil

import (
	"bytes"
	"fmt"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FilterKind represents the type of filter to apply.
type FilterKind uint8

// Filter kinds for the deterministic filter DSL.
const (
	FilterAll FilterKind = iota
	FilterNone
	FilterRevisionMask
	FilterIndexByteEq
	FilterKeyPrefixEq // byte prefix at offset 0
)

// FilterSpec describes a filter to apply during scans.
type FilterSpec struct {
	Kind FilterKind

	// RevisionMask: (Revision & Mask) == Want
	Mask int64
	Want int64

	// IndexByteEq: Index[Offset] == Byte
	Offset int
	Byte   byte

	// KeyPrefixEq: bytes.HasPrefix(Key, Prefix)
	Prefix []byte
}

func (f FilterSpec) String() string {
	switch f.Kind {
	case FilterAll:
		return "All"
	case FilterNone:
		return "None"
	case FilterRevisionMask:
		return fmt.Sprintf("RevisionMask(mask=0x%X,want=0x%X)", f.Mask, f.Want)
	case FilterIndexByteEq:
		return fmt.Sprintf("IndexByteEq(offset=%d,byte=0x%02X)", f.Offset, f.Byte)
	case FilterKeyPrefixEq:
		return fmt.Sprintf("KeyPrefixEq(%x)", f.Prefix)
	default:
		return fmt.Sprintf("Unknown(kind=%d)", f.Kind)
	}
}

// BuildFilter creates a filter function from a FilterSpec.
func BuildFilter(spec FilterSpec) func(slotcache.Entry) bool {
	switch spec.Kind {
	case FilterNone:
		return func(slotcache.Entry) bool { return false }

	case FilterRevisionMask:
		mask := spec.Mask
		want := spec.Want

		// Keep it non-degenerate + never panic.
		if mask == 0 {
			return func(slotcache.Entry) bool { return true }
		}

		return func(e slotcache.Entry) bool {
			return (e.Revision & mask) == want
		}

	case FilterIndexByteEq:
		offset := spec.Offset
		targetByte := spec.Byte

		return func(e slotcache.Entry) bool {
			if offset < 0 || offset >= len(e.Index) {
				return false
			}

			return e.Index[offset] == targetByte
		}

	case FilterKeyPrefixEq:
		// Copy so the spec is stable even if caller mutates input slice.
		prefix := append([]byte(nil), spec.Prefix...)

		return func(e slotcache.Entry) bool {
			if len(prefix) == 0 || len(prefix) > len(e.Key) {
				return false
			}

			return bytes.HasPrefix(e.Key, prefix)
		}

	default: // FilterAll and unknown kinds
		return func(slotcache.Entry) bool { return true }
	}
}
