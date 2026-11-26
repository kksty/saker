package api

import (
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A registered original without a digest is a gap in the record, not an
// admission verdict. The dispatch gate records it as dispatch_gap and still
// dispatches (internal/store/input_digest_test.go), so readiness must not flip
// Usable/Ready on it either: Integrity=missing is the hint, and the premise stays
// usable. The bug this pins: the page refused a direction that dispatch admitted.
func TestReadinessTreatsRegisteredEvidenceWithoutDigestAsMissing(t *testing.T) {
	s, p, _, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	legacy, err := p.Store.AddEvidence(model.Evidence{URI: "legacy/proof.html"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Digest != "" {
		t.Fatalf("fixture: digest = %q, want an empty legacy row", legacy.Digest)
	}
	fact, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{legacy.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), false)
	if !got.Ready {
		t.Fatalf("a recorded gap flipped readiness: %+v", got.Premises)
	}
	if len(got.Premises) != 1 || got.Premises[0].Integrity != "missing" || !got.Premises[0].Usable {
		t.Fatalf("premise = %+v, want integrity=missing and still usable", got.Premises)
	}
	if got.Premises[0].Reason != "" {
		t.Fatalf("premise reason = %q, want empty: a gap is not a verdict", got.Premises[0].Reason)
	}
}

// Same rule for the other half of the branch: the id is registered on the fact
// but the evidence row is not in the ledger. The page still shows the gap
// (Integrity=missing) and the direction stays usable, because dispatch only
// records it (internal/store/input.go:115-121).
func TestReadinessTreatsEvidenceMissingFromLedgerAsHint(t *testing.T) {
	s, p, _, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	fact, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{"e_gone"}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), false)
	if !got.Ready || len(got.Premises) != 1 {
		t.Fatalf("premise = %+v, want one usable row", got.Premises)
	}
	if row := got.Premises[0]; row.Integrity != "missing" || !row.Usable || row.Reason != "" {
		t.Fatalf("premise = %+v, want integrity=missing, usable, empty reason", row)
	}
}
