package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A writer never hands out "verified" through UpdateFact either. Status is not
// part of the claim hash, so the demotion that follows a changed assertion
// cannot see a status-only write; without the guard a caller could mark
// unchanged content verified and every reader that trusts the status (report
// validation, the decision premise list) would take it at face value.
func TestUpdateFactCannotIntroduceVerified(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	f, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: "host", Title: "observed condition", Summary: "one observed condition"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.UpdateFact(f.ID, func(row *model.Fact) { row.Status = "verified" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unverified" {
		t.Fatalf("UpdateFact returned status %q, want unverified", got.Status)
	}
	stored, ok := st.FactByID(f.ID)
	if !ok {
		t.Fatal("fact vanished after a status-only write")
	}
	if stored.Status != "unverified" {
		t.Fatalf("stored status = %q, want unverified", stored.Status)
	}
	if stored.ClaimVersion != f.ClaimVersion {
		t.Fatalf("status-only write bumped the claim version: %d -> %d", f.ClaimVersion, stored.ClaimVersion)
	}
}

// The guard only stops a verdict being introduced. A bookkeeping write on an
// already reviewed fact keeps the verdict and the assertion version it proved.
func TestUpdateFactKeepsVerifiedOnBookkeepingWrite(t *testing.T) {
	st := openReviewQueueStore(t, filepath.Join(t.TempDir(), "ledger.json"))
	f := addReviewFact(t, st, "in_1", "host", "verified", "reviewed condition")
	if f.Status != "verified" {
		t.Fatalf("fixture status = %q, want verified", f.Status)
	}
	got, err := st.UpdateFact(f.ID, func(row *model.Fact) { row.Title = "renamed for display" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "verified" || got.ClaimVersion != f.ClaimVersion {
		t.Fatalf("bookkeeping write dropped the verdict: status %q version %d, want verified/v%d",
			got.Status, got.ClaimVersion, f.ClaimVersion)
	}
}
