// Package ids generates and validates type-prefixed ULIDs, e.g. "out_01HX...".
//
// IDs are k-sortable ULIDs (lowercase Crockford base32) prefixed by an
// underscore-joined type tag so they are self-describing across services.
package ids

import (
	"crypto/rand"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// New returns a fresh ULID prefixed with prefix, e.g. New("out") -> "out_01HX...".
// A ULID is exactly 26 base32 characters; this implementation emits them lowercase.
func New(prefix string) string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.Monotonic(rand.Reader, 0))
	return prefix + "_" + strings.ToLower(id.String())
}

// Valid reports whether id has the given prefix and a parseable ULID suffix.
func Valid(prefix, id string) bool {
	want := prefix + "_"
	if !strings.HasPrefix(id, want) {
		return false
	}
	suffix := id[len(want):]
	if len(suffix) != ulid.EncodedSize {
		return false
	}
	// ULID parsing is case-insensitive; New emits lowercase.
	if _, err := ulid.ParseStrict(strings.ToUpper(suffix)); err != nil {
		return false
	}
	return true
}
