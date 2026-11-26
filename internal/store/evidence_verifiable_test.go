package store

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// The predicate answers "can a reader check these bytes against the recorded
// digest?", and both the screen and the dispatch gate believe it. So it may only
// say yes for a digest the reader can compare: the same SHA-256 shape the write
// path enforces (normalizeEvidence) and preserve.go's compareDigest accepts.
//
// Rows written around the write path exist in principle — a hand-edited ledger or
// an import — and a non-empty-but-unusable digest is exactly the state that used to
// read as "verifiable" on both surfaces. The mutation is dropping the hex check
// back to `strings.TrimSpace(e.Digest) != ""`: every row below that starts with a
// shape mismatch then flips to true and this test goes red.
func TestEvidenceVerifiableNeedsASHA256Digest(t *testing.T) {
	hex64 := strings.Repeat("a1", 32)
	cases := []struct {
		name   string
		digest string
		want   bool
	}{
		{"lower-case hex", hex64, true},
		{"surrounding whitespace", "  " + hex64 + "\n", true},
		// The reader lower-cases before comparing (preserve.go compareDigest), so an
		// upper-case digest really is checkable; refusing it here would refuse a row
		// that verifies fine.
		{"upper-case hex", strings.ToUpper(hex64), true},

		{"empty (legacy row)", "", false},
		{"whitespace only", "   \t", false},
		{"not a digest at all", "preserved-elsewhere", false},
		{"one byte short", hex64[:63], false},
		{"one byte long", hex64 + "0", false},
		{"non-hex character", "g" + hex64[1:], false},
		// A digest with an internal space cannot be compared even though it is
		// non-empty and 64 characters wide.
		{"internal space", hex64[:32] + " " + hex64[32:], false},
	}
	for _, c := range cases {
		if got := EvidenceVerifiable(model.Evidence{ID: "e_1", Digest: c.digest}); got != c.want {
			t.Errorf("%s: EvidenceVerifiable(%q) = %v, want %v", c.name, c.digest, got, c.want)
		}
	}
}
