package store

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// testDigest is a well-formed SHA-256 for fixtures that need a preserved
// original.
const testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// A registered original without a digest cannot be re-proven. The dispatch runs
// and records it: an incomplete evidence row is an observation about the record,
// not a reason to keep the engagement from being tested. The verdict gate is a
// separate rule (reviews open ⇒ unverified premises refuse), which is why this
// fixture closes reviews and asserts on the digest line only.
func TestPrepareRunInputRecordsDigestGapAndDispatches(t *testing.T) {
	s := open(t)
	empty, err := s.AddEvidence(model.Evidence{URI: "legacy/proof.html"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Digest != "" {
		t.Fatalf("fixture: digest = %q, want an empty legacy row", empty.Digest)
	}
	fact, err := s.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{empty.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	_, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err != nil {
		t.Fatalf("dispatch refused a premise whose original has no digest: %v", err)
	}
	if !strings.Contains(strings.Join(gaps, "；"), "保全摘要") {
		t.Fatalf("gap does not name the digest rule: %v", gaps)
	}
}

// The control: the same shape with a digest produces no digest gap, so the rule
// reports the empty digest and not merely "any evidence-backed premise".
func TestPrepareRunInputAdmitsRegisteredEvidenceWithDigest(t *testing.T) {
	s := open(t)
	kept, err := s.AddEvidence(model.Evidence{
		URI:    "legacy/proof.html",
		Digest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{kept.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	_, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err != nil {
		t.Fatalf("a premise with a digest was refused: %v", err)
	}
	if strings.Contains(strings.Join(gaps, "；"), "保全摘要") {
		t.Fatalf("a complete original produced a digest gap: %v", gaps)
	}
}

// The gap is judged by shape, not by presence: a digest that is not a SHA-256 hex
// string cannot be re-proven either, and only the predicate-level table covered
// those shapes. The rows are written straight into the in-memory ledger on
// purpose — the write path (normalizeEvidence) would refuse them, so the only
// rows like these come from a hand edit or an import that bypassed it.
//
// Case is folded, so an upper-case or padded digest is admitted: preserve.go's
// compareDigest and verifyFileDigest both fold case, and reporting a gap here
// would misdescribe a row the reader can verify perfectly well.
func TestPrepareRunInputJudgesDigestShape(t *testing.T) {
	cases := []struct {
		name   string
		digest string
		admit  bool
	}{
		{"lower-case sha256", testDigest, true},
		{"upper-case sha256", strings.ToUpper(testDigest), true},
		{"padded sha256", "  " + testDigest + "\n", true},
		{"non-hex character", "g" + strings.Repeat("a", 63), false},
		{"one byte short", strings.Repeat("a", 62), false},
		{"one byte long", strings.Repeat("a", 65), false},
		{"internal space", strings.Repeat("a", 32) + " " + strings.Repeat("a", 31), false},
		{"prefix instead of hex", "sha256:ok", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			kept, err := s.AddEvidence(model.Evidence{URI: "legacy/proof.html", Digest: testDigest})
			if err != nil {
				t.Fatal(err)
			}
			edited := false
			for i := range s.db.Evidence {
				if s.db.Evidence[i].ID == kept.ID {
					s.db.Evidence[i].Digest = tc.digest
					edited = true
				}
			}
			if !edited {
				t.Fatalf("fixture: evidence %s not found in the in-memory ledger", kept.ID)
			}
			fact, err := s.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{kept.ID}})
			if err != nil {
				t.Fatal(err)
			}
			in, err := s.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
			if err != nil {
				t.Fatal(err)
			}
			_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
			if err != nil {
				t.Fatal(err)
			}
			_, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
			if err != nil {
				t.Fatalf("dispatch refused the run itself: %v", err)
			}
			joined := strings.Join(gaps, "；")
			hasDigestGap := strings.Contains(joined, "保全摘要")
			if tc.admit {
				if hasDigestGap {
					t.Fatalf("a re-provable digest %q produced a digest gap: %v", tc.digest, gaps)
				}
				return
			}
			if !hasDigestGap {
				t.Fatalf("a digest that cannot be re-proven was recorded as clean: %q", tc.digest)
			}
		})
	}
}
