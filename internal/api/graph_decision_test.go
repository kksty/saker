package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The decision surfaces are built from decisionDB, and nothing else proves that
// an unverified row is gone rather than merely styled. BuildGraph is also called
// directly elsewhere, so a test that only exercised it would leave this filter
// unguarded.
func TestDecisionDBDropsUnverifiedFacts(t *testing.T) {
	db := store.DB{Facts: []model.Fact{
		{ID: "f_verified", Kind: "vuln", Title: "SQLi", Status: "verified"},
		{ID: "f_unverified", Kind: "vuln", Title: "maybe SQLi", Status: "unverified"},
		{ID: "f_withdrawn", Kind: "note", Title: "retracted", Status: "withdrawn"},
		{ID: "f_rejected", Kind: "note", Title: "not true", Status: "rejected"},
	}}
	kept := decisionDB(db)
	if len(kept.Facts) != 1 || kept.Facts[0].ID != "f_verified" {
		t.Fatalf("decisionDB kept %+v, want only the verified row", kept.Facts)
	}
	// The operator's ledger is not mutated by projecting it.
	if len(db.Facts) != 4 {
		t.Fatalf("decisionDB mutated the input: %d rows left", len(db.Facts))
	}
}

// The premise catalog is a decision surface too: a run reads it to learn which
// of its premises still hold. An unverified premise must not be labelled the
// same as a verified one, or the run cannot tell that its backing is gone.
func TestTaskManifestMarksUnverifiedPremises(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The catalog no longer reads the originals: a missing or digestless row is a
	// recorded gap (dispatch still runs), so a fixture may name an evidence id the
	// ledger does not carry. It stays `recorded` like any other verified premise.
	proof, err := st.AddEvidence(model.Evidence{URI: "in_1/raw/proof.txt", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 12, Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}

	verified := verifiedFixture(t, st, model.Fact{Kind: "service", Title: "10.0.0.7:443", EvidenceIDs: []string{proof.ID}})
	unverified, err := st.AddFact(model.Fact{Kind: "service", Title: "10.0.0.7:8443"})
	if err != nil {
		t.Fatal(err)
	}
	// A note is reference material: no review can ever pass it, so calling it
	// "unverified" would read as "still waiting for review", which is a
	// different (and wrong) answer for the run.
	note, err := st.AddFact(model.Fact{Kind: "note", Title: "抓到的一段流量"})
	if err != nil {
		t.Fatal(err)
	}
	// A withdrawn note is withdrawn, not reference-only: the author's retraction
	// is the more specific answer and must win the ordering.
	retracted, err := st.AddFact(model.Fact{Kind: "note", Title: "撤回的观察"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WithdrawFact(retracted.ID, "operator", "", "判断不成立"); err != nil {
		t.Fatal(err)
	}
	// Verified but with no original evidence: admission refuses it too, so the
	// catalog must not call this one usable either. The store no longer writes
	// this shape (a pass needs material to replay), so it is built on the snapshot
	// the manifest reads — the same way an older build's or an imported ledger's
	// row arrives.
	unbacked, err := st.AddFact(model.Fact{Kind: "vuln", Title: "只有结论没有原件"})
	if err != nil {
		t.Fatal(err)
	}
	// Verified, with an original, but the passing review proves an earlier
	// assertion: only a ledger written by an older build or imported from one
	// carries this shape, and admission refuses it for the missing receipt.
	stale := verifiedFixture(t, st, model.Fact{Kind: "vuln", Title: "复核过的是上一版", EvidenceIDs: []string{proof.ID}})
	in, err := st.CreateIntent(model.Intent{
		Title:       "接着打 443",
		FromFactIDs: []string{verified.ID, unverified.ID, note.ID, retracted.ID, unbacked.ID, stale.ID, "f_missing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A stale receipt only exists in a ledger this build did not write through
	// the API, so the version bump happens on the snapshot the manifest reads.
	db := st.Snapshot()
	for i := range db.Facts {
		switch db.Facts[i].ID {
		case unbacked.ID:
			db.Facts[i].Status = "verified"
		case stale.ID:
			db.Facts[i].ClaimVersion++
			db.Facts[i].ClaimHash = "hash-next"
		}
	}
	db.Reviews = append(db.Reviews, model.Review{
		ID: "rv_imported_pass", FactID: unbacked.ID, Verdict: "pass", VerifierRun: "run_imported",
		ClaimVersion: unbacked.ClaimVersion, ClaimHash: unbacked.ClaimHash,
	})
	work := t.TempDir()
	if err := writeTaskManifest(work, "", db, in.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(work, ".tw", "context", "premises-0001.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The catalog is a decision surface: it may name a premise, never describe
	// it. A future field carrying title or summary would ride straight into the
	// run, so the shape itself is asserted here, not just the values.
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if len(row) != 3 {
			t.Fatalf("premise row carries more than id/version/state: %v", row)
		}
		for _, key := range []string{"id", "version", "state"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("premise row missing %q: %v", key, row)
			}
		}
	}
	states := map[string]string{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		state, _ := row["state"].(string)
		states[id] = state
	}
	if states[verified.ID] != premiseRecorded {
		t.Fatalf("verified premise = %q, want %q", states[verified.ID], premiseRecorded)
	}
	if states[unverified.ID] != premiseUnverified {
		t.Fatalf("unverified premise = %q, want %q", states[unverified.ID], premiseUnverified)
	}
	if states[note.ID] != premiseReferenceOnly {
		t.Fatalf("note premise = %q, want %q", states[note.ID], premiseReferenceOnly)
	}
	// Evidence is advisory: a verified fact without a preserved original is
	// still a recorded (usable) premise; the operator sees an annotation.
	if states[unbacked.ID] != premiseRecorded {
		t.Fatalf("verified-without-evidence premise = %q, want %q", states[unbacked.ID], premiseRecorded)
	}
	if states[stale.ID] != premiseUnreceipted {
		t.Fatalf("verified-with-stale-receipt premise = %q, want %q", states[stale.ID], premiseUnreceipted)
	}
	if states[retracted.ID] != premiseWithdrawn {
		t.Fatalf("withdrawn note = %q, want %q", states[retracted.ID], premiseWithdrawn)
	}
	if states["f_missing"] != premiseMissing {
		t.Fatalf("absent premise = %q, want %q", states["f_missing"], premiseMissing)
	}
}
