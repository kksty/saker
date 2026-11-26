package retrieval

import (
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A session read re-checks the live row on every call, so an assertion that
// changed after it was delivered must stop showing its text. The withdrawn case
// is covered elsewhere; this pins the other way a delivered claim goes stale —
// the row is still there, still readable by the operator, but its claim version
// moved and its verification no longer covers what it now says.
func TestSessionReadMasksFactWhoseClaimDrifted(t *testing.T) {
	s := ledger(t)
	premise, err := s.AddFact(model.Fact{Kind: "host", Title: "10.0.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "意图", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := s.AddEvidence(model.Evidence{IntentID: in.ID, URI: in.ID + "/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.AddFact(model.Fact{
		IntentID: in.ID, Kind: "vuln", Title: "SQLi in /search", Summary: "boolean based",
		EvidenceIDs: []string{ev.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: out.ID, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}

	page, err := Facts(s, FactQuery{Session: true, Intent: in.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := findFact(page.Facts, out.ID); got == nil || got.Title != "SQLi in /search" {
		t.Fatalf("verified fact not readable in session view: %+v", page.Facts)
	}

	// Rewrite the assertion text: the claim version moves and the review no
	// longer proves the new sentence. (The display title is excluded from the
	// claim hash on purpose, so editing only the title would stay verified.)
	if _, err := s.UpdateFact(out.ID, func(f *model.Fact) { f.Summary = "boolean based, revised" }); err != nil {
		t.Fatal(err)
	}

	page, err = Facts(s, FactQuery{Session: true, Intent: in.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := findFact(page.Facts, out.ID)
	if got == nil {
		t.Fatalf("fact vanished instead of being masked: %+v", page.Facts)
	}
	if got.Title != "" || got.Summary != "" {
		t.Fatalf("drifted fact still carries text: %+v", got)
	}
	if got.Status != "unverified" {
		t.Fatalf("drifted fact status = %q, want unverified", got.Status)
	}
}

func findFact(facts []model.Fact, id string) *model.Fact {
	for i := range facts {
		if facts[i].ID == id {
			return &facts[i]
		}
	}
	return nil
}
