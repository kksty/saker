package api

import (
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// verifiedFixture creates a fact and records the passing review production would
// record. A fact cannot be born verified: the store refuses that status from a
// writer, so a fixture that needs one has to ask for the verdict.
func verifiedFixture(t *testing.T, st *store.Store, f model.Fact) model.Fact {
	t.Helper()
	f.Status = ""
	created, err := st.AddFact(f)
	if err != nil {
		t.Fatal(err)
	}
	// Production always records the run that produced the verdict; a pass with no
	// run is not a current proof, so a fixture that leaves it out would build the
	// legacy shape instead of the normal one.
	if _, err := st.ReviewFact(model.Review{FactID: created.ID, Verdict: "pass", VerifierRun: "run-fixture"}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.FactByID(created.ID)
	if !ok {
		t.Fatalf("fact %s vanished after review", created.ID)
	}
	return got
}
