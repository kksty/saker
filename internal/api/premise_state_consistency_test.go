package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The premise catalog (`.saker/context/premises-*.json`) and the admission judge
// (`readiness`) answer the same question — "may this direction rely on this
// premise?" — from two different implementations. Twice already the catalog
// said "usable" and dispatch refused the direction, and both times it was found
// by a human reading two files side by side: `unbacked` (verified, no original)
// and `unreceipted` (verified, original, pass bound to an older assertion).
//
// This test is the machine version of that reading. It enumerates the axes the
// two sides classify on — status × kind × evidence presence × receipt binding —
// builds the smallest ledger that expresses each combination, then asserts the
// one invariant both are supposed to share:
//
//	state == "recorded"  ⟺  readiness says the premise is usable
//
// Nothing else is compared: the two sides use different words for the same
// refusal (`unbacked` vs "缺少可打开的原始证据"), and a wrong word is a copy
// bug, not a contract breach. A combination that is unusable on both sides is
// equivalent even if the words differ.

type premiseCase struct {
	name string
	// fact is the premise row. Its ID is what the intent names.
	fact model.Fact
	// evidence and reviews are the rest of the ledger.
	evidence []model.Evidence
	reviews  []model.Review
}

const premiseCaseIntentID = "in_premise_case"

func passReview(factID string, version uint64, hash, verifier string) model.Review {
	return model.Review{
		FactID:       factID,
		ClaimVersion: version,
		ClaimHash:    hash,
		Verdict:      "pass",
		VerifierRun:  verifier,
		CreatedAt:    time.Now(),
	}
}

// premiseCases enumerates the axes. Each entry is one combination; the table is
// deliberately exhaustive on the four axes rather than pretty.
func premiseCases() []premiseCase {
	// A usable original carries the SHA-256 shape store.EvidenceVerifiable accepts;
	// these cases are about review state, not about digest hygiene.
	proof := model.Evidence{ID: "e_ok", URI: "case/f.txt", Digest: strings.Repeat("a", 64)}
	fact := func(status, kind string, evidenceIDs ...string) model.Fact {
		return model.Fact{
			ID:           "f_premise",
			Kind:         kind,
			Title:        "前提",
			Status:       status,
			ClaimVersion: 2,
			ClaimHash:    "h2",
			EvidenceIDs:  evidenceIDs,
			CreatedAt:    time.Now(),
		}
	}
	return []premiseCase{
		{
			name:     "verified vuln, original with digest, current pass",
			fact:     fact("verified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			name:     "verified vuln, original with digest, pass for an older assertion",
			fact:     fact("verified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 1, "h1", "run-stale")},
		},
		{
			name:     "verified vuln, original with digest, pass with no verifier run",
			fact:     fact("verified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "")},
		},
		{
			name:     "verified vuln, no original at all",
			fact:     fact("verified", "vuln"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "verified vuln, original row not in the ledger",
			fact:     fact("verified", "vuln", "e_gone"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			name:     "verified vuln, original row without a digest",
			fact:     fact("verified", "vuln", "e_nodigest"),
			evidence: []model.Evidence{{ID: "e_nodigest", URI: "case/f.txt"}},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			// A digest that no reader could compare (hand-edited ledger, import) is a
			// broken original on all three surfaces. The catalog used to ask only
			// "digest is not empty" and called this row usable.
			name:     "verified vuln, original row with a digest that is not SHA-256 hex",
			fact:     fact("verified", "vuln", "e_nothex"),
			evidence: []model.Evidence{{ID: "e_nothex", URI: "case/f.txt", Digest: "preserved-elsewhere"}},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			name:     "unverified vuln (reviewable, still pending)",
			fact:     fact("unverified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "unverified note (no review can ever pass)",
			fact:     fact("unverified", "note"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "verified note with an original and a current pass",
			fact:     fact("verified", "note", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			name:     "withdrawn vuln",
			fact:     fact("withdrawn", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "rejected vuln",
			fact:     fact("rejected", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "premise id not in the ledger",
			fact:     model.Fact{ID: "f_other", Kind: "vuln", Status: "verified"},
			evidence: []model.Evidence{proof},
		},
		{
			name:     "verified host, original with digest, current pass",
			fact:     fact("verified", "host", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			// The catalog must open *every* original it names, not just the first:
			// a second missing row is a refusal at dispatch.
			name:     "verified vuln, two originals, the second is not in the ledger",
			fact:     fact("verified", "vuln", "e_ok", "e_gone"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			name:     "verified service, original with digest, current pass",
			fact:     fact("verified", "service", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h2", "run-proof")},
		},
		{
			// Same claim version, different assertion text: the proof is history,
			// not a licence for the row as it stands now.
			name:     "verified vuln, pass for the same version but another claim hash",
			fact:     fact("verified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews:  []model.Review{passReview("f_premise", 2, "h1", "run-other-claim")},
		},
		{
			name:     "verified vuln, original with digest, verdict is needs_evidence",
			fact:     fact("verified", "vuln", "e_ok"),
			evidence: []model.Evidence{proof},
			reviews: []model.Review{func() model.Review {
				r := passReview("f_premise", 2, "h2", "run-needs")
				r.Verdict = "needs_evidence"
				return r
			}()},
		},
		{
			name:     "unverified vuln, no original at all",
			fact:     fact("unverified", "vuln"),
			evidence: []model.Evidence{proof},
		},
		{
			name:     "withdrawn note",
			fact:     fact("withdrawn", "note"),
			evidence: []model.Evidence{proof},
		},
	}
}

// knownCatalogGaps are combinations where the catalog and the judge disagree
// today. Each entry names the catalog state the catalog returns and the reason
// the judge gives; the test accepts the pair only while it still reproduces, so
// closing the gap (or drifting further) turns this test red and forces the
// table to be updated.
var knownCatalogGaps = map[string]string{
	// Empty: the catalog now opens the evidence rows itself (missing row, no
	// digest) and answers the kind axis before the receipt, so every enumerated
	// combination agrees with the judge. A new entry here means a new lie.
}

func TestPremiseCatalogStateAgreesWithReadiness(t *testing.T) {
	// The judge asks the store for the premise's review history while building
	// its hint. The hint is a third axis this test does not compare, so the store
	// is real but empty: what matters here is Usable and its reason.
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{CaseRoot: t.TempDir(), Store: st}
	cases := premiseCases()
	seen := map[string]bool{}

	for _, c := range cases {
		if seen[c.name] {
			t.Fatalf("duplicate case name %q: the known-gap table keys on it", c.name)
		}
		seen[c.name] = true

		premiseID := "f_premise"
		if c.fact.ID != "" {
			c.fact.ID = "f_premise"
		}
		db := store.DB{
			Facts:    []model.Fact{c.fact},
			Evidence: c.evidence,
			Reviews:  c.reviews,
			Intents: []model.Intent{{
				ID:          premiseCaseIntentID,
				Title:       "case",
				Status:      model.StatusPending,
				FromFactIDs: []string{premiseID},
			}},
		}
		if c.name == "premise id not in the ledger" {
			// The intent names an id no fact carries: the row above must not
			// accidentally provide it.
			db.Intents[0].FromFactIDs = []string{"f_absent"}
		}

		work := t.TempDir()
		if err := writeTaskManifest(work, "", db, premiseCaseIntentID); err != nil {
			t.Fatalf("%s: write manifest: %v", c.name, err)
		}
		state := readCatalogState(t, work)
		judge := s.readiness(db, db.Intents[0], model.PremiseVerifiedOnly, false)
		if len(judge.Premises) != 1 {
			t.Fatalf("%s: judge returned %d premise rows, want 1", c.name, len(judge.Premises))
		}
		row := judge.Premises[0]

		catalogUsable := state == "recorded"
		judgeUsable := row.Usable
		if catalogUsable == judgeUsable {
			if _, listed := knownCatalogGaps[c.name]; listed {
				t.Errorf("%s: the catalog and the judge now agree (%s / usable=%v); remove it from knownCatalogGaps",
					c.name, state, judgeUsable)
			}
			continue
		}
		gapReason, listed := knownCatalogGaps[c.name]
		if !listed {
			t.Errorf("%s: catalog state %q (usable=%v) disagrees with readiness (usable=%v, reason=%q): %s",
				c.name, state, catalogUsable, judgeUsable, row.Reason, gapReason)
			continue
		}
		if row.Reason != gapReason {
			t.Errorf("%s: known gap changed shape: judge reason %q, recorded as %q", c.name, row.Reason, gapReason)
		}
	}
}

// readCatalogState returns the state the run would actually read for its single
// premise: the manifest pages are the catalog's only public surface.
func readCatalogState(t *testing.T, workDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workDir, ".saker", "context", "premises-0001.json"))
	if err != nil {
		t.Fatalf("read premise page: %v", err)
	}
	var refs []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatalf("decode premise page: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("premise page holds %d refs, want 1", len(refs))
	}
	return refs[0].State
}
