package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MarshalOptions configures frontmatter serialization.
type MarshalOptions struct {
	IncludeDelimiters bool     // IncludeDelimiters writes --- fence lines before and after.
	KeyOrder          [][]byte // KeyOrder specifies the output key order; keys not listed are omitted.
	PriorityKeys      [][]byte // PriorityKeys are placed first (in order), then remaining keys sorted alphabetically.
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
// Keys are compared by value and not retained or modified.
func WithKeyOrder(keys ...[]byte) MarshalOption {
	return func(opts *MarshalOptions) {
		opts.KeyOrder = keys
	}
}

// WithKeyPriority specifies keys that should appear first (in order given),
// with remaining keys sorted alphabetically after.
// Keys are compared by value and not retained or modified.
func WithKeyPriority(keys ...[]byte) MarshalOption {
	return func(opts *MarshalOptions) {
		opts.PriorityKeys = keys
	}
}

// MarshalYAML serializes frontmatter in a deterministic YAML subset.
// By default, it sorts keys alphabetically.
// Use WithKeyOrder to specify a custom key order.
func (fm *Frontmatter) MarshalYAML(opts ...MarshalOption) (string, error) {
	options := MarshalOptions{IncludeDelimiters: true}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(&options)
	}

	if fm == nil {
		return "", errors.New("nil value")
	}

	if err := validateUniqueKeys(options.KeyOrder, "key order"); err != nil {
		return "", err
	}

	if err := validateUniqueKeys(options.PriorityKeys, "priority keys"); err != nil {
		return "", err
	}

	var ordered [][]byte
	if options.KeyOrder != nil {
		ordered = options.KeyOrder
	} else {
		// Collect all keys
		keys := make([][]byte, 0, len(fm.entries))
		for i := range fm.entries {
			keys = append(keys, fm.entries[i].Key)
		}

		slices.SortFunc(keys, bytes.Compare)

		// If priority keys specified, put them first
		if len(options.PriorityKeys) > 0 {
			ordered = make([][]byte, 0, len(keys))
			ordered = append(ordered, options.PriorityKeys...)

			for _, k := range keys {
				if !containsKeyBytes(options.PriorityKeys, k) {
					ordered = append(ordered, k)
				}
			}
		} else {
			ordered = keys
		}
	}

	var builder strings.Builder
	if options.IncludeDelimiters {
		builder.WriteString("---\n")
	}

	for _, key := range ordered {
		value, ok := fm.Get(key)
		if !ok {
			return "", fmt.Errorf("missing %s", string(key))
		}

		builder.Write(key)
		builder.WriteString(":")

		switch value.Kind {
		case ValueScalar:
			builder.WriteString(" ")

			switch value.Scalar.Kind {
			case ScalarString:
				builder.WriteString(marshalYAMLString(string(value.Scalar.Bytes)))
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
				itemStr := string(item)
				if itemStr == "" {
					return "", fmt.Errorf("%s: empty list item", string(key))
				}

				builder.WriteString("  - ")
				builder.WriteString(marshalYAMLString(itemStr))
				builder.WriteString("\n")
			}
		case ValueObject:
			if len(value.Object) == 0 {
				return "", fmt.Errorf("%s: empty object", string(key))
			}

			builder.WriteString("\n")

			objEntries := make([]ObjectEntry, len(value.Object))
			copy(objEntries, value.Object)

			slices.SortFunc(objEntries, func(a, b ObjectEntry) int {
				return bytes.Compare(a.Key, b.Key)
			})

			for i, entry := range objEntries {
				if i > 0 && bytes.Equal(objEntries[i-1].Key, entry.Key) {
					return "", fmt.Errorf("%s: duplicate object key %s", string(key), string(entry.Key))
				}

				builder.WriteString("  ")
				builder.Write(entry.Key)
				builder.WriteString(": ")

				switch entry.Value.Kind {
				case ScalarString:
					builder.WriteString(marshalYAMLString(string(entry.Value.Bytes)))
				case ScalarInt:
					builder.WriteString(strconv.FormatInt(entry.Value.Int, 10))
				case ScalarBool:
					if entry.Value.Bool {
						builder.WriteString("true")
					} else {
						builder.WriteString("false")
					}
				default:
					return "", fmt.Errorf("%s.%s: unsupported scalar kind %d", string(key), string(entry.Key), entry.Value.Kind)
				}

				builder.WriteString("\n")
			}
		default:
			return "", fmt.Errorf("%s: unsupported value kind %d", string(key), value.Kind)
		}
	}

	if options.IncludeDelimiters {
		builder.WriteString("---\n")
	}

	return builder.String(), nil
}

func containsKeyBytes(keys [][]byte, key []byte) bool {
	for _, existing := range keys {
		if bytes.Equal(existing, key) {
			return true
		}
	}

	return false
}

func validateUniqueKeys(keys [][]byte, label string) error {
	for i := range keys {
		if err := validateKey(keys[i]); err != nil {
			return fmt.Errorf("%s contains invalid key %q: %w", label, string(keys[i]), err)
		}

		for j := i + 1; j < len(keys); j++ {
			if bytes.Equal(keys[i], keys[j]) {
				return fmt.Errorf("%s contains duplicate key %q", label, string(keys[i]))
			}
		}
	}

	return nil
}

func marshalYAMLString(value string) string {
	if shouldQuoteYAMLString(value) {
		return strconv.Quote(value)
	}

	return value
}

func shouldQuoteYAMLString(value string) bool {
	if value == "" {
		return true
	}

	if strings.TrimSpace(value) != value {
		return true
	}

	if strings.ContainsAny(value, "\n\r\t") {
		return true
	}

	scalar, err := parseScalar([]byte(value))
	if err != nil {
		return true
	}

	if scalar.Kind != ScalarString {
		return true
	}

	return string(scalar.Bytes) != value
}
