package store

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func gapsText(gaps []string) string { return strings.Join(gaps, "；") }

func TestDispatchInputAndRecordedVersionsUseSameObservation(t *testing.T) {
	s := open(t)
	e, _ := s.AddEvidence(model.Evidence{URI: "proof.txt", Digest: testDigest})
	f, _ := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", Summary: "first", EvidenceIDs: []string{e.ID}})
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-1"}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	db, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("a verified, digested premise produced gaps: %v", gaps)
	}
	_, version, _ := s.FactVersion(f.ID)
	saved, _ := s.GetRun(run.ID)
	if saved.PremiseVersions[f.ID] != version || db.Facts[0].Summary != "first" {
		t.Fatal("input and provenance differ")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "second" })
	if db.Facts[0].Summary != "first" {
		t.Fatal("frozen input changed")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "rejected" })
	if _, _, err = s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{}); err == nil {
		t.Fatal("a rejected premise dispatched")
	} else if !strings.Contains(err.Error(), "否定") {
		t.Fatalf("refusal does not name the rejected claim: %v", err)
	}
}

func TestDispatchPreparationRefusesAnIntentOwnedByAnotherRun(t *testing.T) {
	s := open(t)
	in, _ := s.CreateIntent(model.Intent{Title: "task"})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	_, _ = s.UpdateIntent(in.ID, func(in *model.Intent) { in.RunID = "replacement" })
	if _, _, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{}); err == nil {
		t.Fatal("superseded run prepared an input")
	}
}

func TestVerifiedOnlyPreparationRefusesAnUnreviewedClaim(t *testing.T) {
	s := open(t)
	e, err := s.AddEvidence(model.Evidence{URI: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", Summary: "first", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-1"}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if _, err := s.UpdateFact(f.ID, func(f *model.Fact) { f.Summary = "unreviewed replacement" }); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{})
	if err == nil {
		t.Fatal("an unreviewed replacement dispatched while reviews are open")
	}
	if !strings.Contains(err.Error(), "独立核验") {
		t.Fatalf("refusal does not name the missing verdict: %v", err)
	}
}

func TestGoalReceiptPinsClaimsAndRejectsRefutedSupport(t *testing.T) {
	s := open(t)
	e, _ := s.AddEvidence(model.Evidence{URI: "proof.txt", Digest: testDigest})
	f := verifiedSupport(t, s, e.ID, "support")
	g, _ := s.AddGoal(model.Goal{Statement: "goal"})
	pinned, err := s.PinGoal(g.ID, e.ID, "observed evidence satisfies the goal", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.FromClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("claim receipt missing")
	}
	pinned.FromClaimVersions[f.ID] = 999
	if s.Goals()[0].FromClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("returned receipt aliases store")
	}
	if _, err = s.ReopenGoal(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "rejected" }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PinGoal(g.ID, e.ID, "refuted support", []string{f.ID}); err == nil {
		t.Fatal("refuted support accepted")
	}
}

// Freezing input with review disabled is recorded on the run: the policy field
// alone cannot tell "verified_only was enforced" from "the gate was skipped".
// A rejected premise is still recorded as a gap rather than a refusal.
func TestPrepareRunInputRecordsSkippedReviewAndRetractedClaims(t *testing.T) {
	s := open(t)
	e, _ := s.AddEvidence(model.Evidence{URI: "proof.txt", Digest: testDigest})
	f, _ := s.AddFact(model.Fact{Kind: "vuln", Title: "premise", Summary: "first", EvidenceIDs: []string{e.ID}})
	in, _ := s.CreateIntent(model.Intent{Title: "task", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	_, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err != nil {
		t.Fatalf("reviews-off dispatch refused an unverified premise: %v", err)
	}
	if !strings.Contains(gapsText(gaps), "复核关闭") {
		t.Fatalf("the skipped verdict is not described as closed reviews: %v", gaps)
	}
	saved, _ := s.GetRun(run.ID)
	if !saved.ReviewSkipped {
		t.Fatal("run does not record that review was skipped")
	}
	_, _ = s.UpdateFact(f.ID, func(f *model.Fact) { f.Status = "rejected" })
	in2, _ := s.CreateIntent(model.Intent{Title: "task2", FromFactIDs: []string{f.ID}})
	_, run2, _ := s.StartIntentRun(in2.ID, "local", "", model.Run{})
	if _, _, err = s.PrepareRunInput(run2.ID, in2, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true}); err == nil {
		t.Fatal("a rejected premise dispatched with reviews off")
	} else if !strings.Contains(err.Error(), "否定") {
		t.Fatalf("refusal does not name the rejected claim: %v", err)
	}
}

func TestPrepareRunInputRecordsANotePremiseAsReferenceOnly(t *testing.T) {
	s := open(t)
	bare, _ := s.AddFact(model.Fact{Kind: "vuln", Title: "no evidence", Summary: "x"})
	in, _ := s.CreateIntent(model.Intent{Title: "a", FromFactIDs: []string{bare.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if _, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true}); err != nil || len(gaps) == 0 {
		t.Fatalf("an unverified premise should be recorded, not refused: err=%v gaps=%v", err, gaps)
	}
	note, _ := s.AddFact(model.Fact{Kind: "note", Title: "memo", Summary: "y"})
	in2, _ := s.CreateIntent(model.Intent{Title: "b", FromFactIDs: []string{note.ID}})
	_, run2, _ := s.StartIntentRun(in2.ID, "local", "", model.Run{})
	_, _, err := s.PrepareRunInput(run2.ID, in2, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err == nil {
		t.Fatal("a note premise dispatched")
	}
	if !strings.Contains(err.Error(), "仅作参考") {
		t.Fatalf("refusal does not say the kind can never be a premise: %v", err)
	}
}

// A registered original that is not in the ledger is recorded as a gap; the
// dispatch still runs. An empty evidence list (nothing ever registered) produces
// no gap at all, because there is nothing to be missing.
func TestPrepareRunInputRecordsPremiseWithBrokenOriginal(t *testing.T) {
	s := open(t)
	f, _ := s.AddFact(model.Fact{Kind: "vuln", Title: "ghost original", Summary: "x", EvidenceIDs: []string{"e_ghost"}})
	in, _ := s.CreateIntent(model.Intent{Title: "a", FromFactIDs: []string{f.ID}})
	_, run, _ := s.StartIntentRun(in.ID, "local", "", model.Run{})
	_, gaps, err := s.PrepareRunInput(run.ID, in, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err != nil {
		t.Fatalf("a missing original stopped the dispatch: %v", err)
	}
	if !strings.Contains(gapsText(gaps), "不在账本里") {
		t.Fatalf("the missing original was not recorded: %v", gaps)
	}
}

// A multi-round direction's settled first run does not make its facts citable
// while that direction is running a second round: the assertion can still be
// corrected, and the downstream would be pinned to a version that moves.
func TestPremiseRefusesFactOwnedByARunningDirection(t *testing.T) {
	s := open(t)
	a, _ := s.CreateIntent(model.Intent{Title: "A"})
	first, _ := s.AddRun(model.Run{IntentID: a.ID, Node: "local", Status: model.RunDone})
	f, _ := s.AddFact(model.Fact{Kind: "service", Title: "obs", Summary: "s", IntentID: a.ID, SourceRunID: first.ID})
	// A starts its second round: the run is live again, the fact is from run one.
	if _, _, err := s.StartIntentRun(a.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateIntent(model.Intent{Title: "B", FromFactIDs: []string{f.ID}})
	_, runB, _ := s.StartIntentRun(b.ID, "local", "", model.Run{})
	_, _, err := s.PrepareRunInput(runB.ID, b, model.PremiseVerifiedOnly, DispatchGate{ReviewsOff: true})
	if err == nil {
		t.Fatal("a premise from a direction that is running its next round was admitted")
	}
	if !strings.Contains(err.Error(), "还没收口") {
		t.Fatalf("refusal does not name the unsettled owner: %v", err)
	}
}

// A direction may cite what its own earlier run produced: the retry/continue
// path would otherwise refuse every downstream of its own work.
func TestPremiseAllowsOwnEarlierRunFact(t *testing.T) {
	s := open(t)
	a, _ := s.CreateIntent(model.Intent{Title: "A"})
	first, _ := s.AddRun(model.Run{IntentID: a.ID, Node: "local", Status: model.RunDone})
	f, _ := s.AddFact(model.Fact{Kind: "service", Title: "own", Summary: "s", IntentID: a.ID, SourceRunID: first.ID})
	in, _ := s.CreateIntent(model.Intent{Title: "A2", FromFactIDs: []string{f.ID}, ParentID: ""})
	// Point the citing direction at the same direction id as the fact's owner.
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.ID = a.ID }); err != nil {
		t.Fatal(err)
	}
	self, ok := s.GetIntent(a.ID)
	if !ok {
		t.Fatal("intent lost")
	}
	if owner := s.PremiseOwnerStillLive(f, self.ID); owner != "" {
		t.Fatalf("own fact treated as foreign: %s", owner)
	}
}
