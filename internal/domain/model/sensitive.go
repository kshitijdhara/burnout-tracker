// Package model holds the domain's value types. It has no dependencies
// beyond the standard library.
package model

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Sensitive wraps a value that must never reach logs, errors or output.
// Every formatting path returns "[redacted]"; code that genuinely needs the
// value calls Reveal, which is easy to grep for in review.
type Sensitive[T any] struct{ v T }

// NewSensitive wraps v.
func NewSensitive[T any](v T) Sensitive[T] { return Sensitive[T]{v: v} }

// Reveal returns the wrapped value.
func (s Sensitive[T]) Reveal() T { return s.v }

// String implements fmt.Stringer.
func (Sensitive[T]) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (Sensitive[T]) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (Sensitive[T]) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, so JSON and other encoders
// also see "[redacted]".
func (Sensitive[T]) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Format makes %v, %+v, %#v, %s, %q and friends all print "[redacted]".
func (Sensitive[T]) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }
