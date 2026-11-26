package store

// Dependency propagation is the one inference the ledger makes about
// provenance. These tests pin the four judgements the specification names, in
// both directions, so a later "simplification" cannot quietly drop the
// inheritance or turn a lost premise into a rejection.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func BenchmarkPremiseLossLongChain(b *testing.B) {
	for _, count := range []int{253, 1000, 4000} {
		b.Run(fmt.Sprintf("%d", count), func(b *testing.B) {
			s := &Store{}
			s.db.Facts = make([]model.Fact, count)
			s.db.Intents = make([]model.Intent, count-1)
			for i := range s.db.Facts {
				fact := &s.db.Facts[i]
				fact.ID = fmt.Sprintf("f_%d", i)
				fact.DeclaredConfidence = "observed"
				if i > 0 {
					intentID := fmt.Sprintf("in_%d", i)
					fact.IntentID = intentID
					s.db.Intents[i-1] = model.Intent{ID: intentID, FromFactIDs: []string{s.db.Facts[i-1].ID}}
				}
			}
			root := s.db.Facts[0].ID
			for b.Loop() {
				for i := 1; i < count; i++ {
					s.db.Facts[i].Status = "verified"
				}
				lost := s.refreshDownstreamLocked(root, true)
				if len(lost) != count-1 || s.db.Facts[count-1].Confidence != "inferred" {
					b.Fatal("premise loss did not reach the end of the chain")
				}
			}
		})
	}
}

// observedPremise writes a fact with no direction behind it, so its confidence
// is exactly what the writer declared, and returns it.
func observedPremise(t *testing.T, s *Store, title string) model.Fact {
	t.Helper()
	f, err := s.AddFact(model.Fact{Kind: "host", Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// proof admits one preserved evidence row for a fixture finding. A vuln/cred row
// can only become verified with material a reviewer can replay: the store refuses
// a pass on a finding with no cited evidence (ReviewPassBlocked), so a fixture
// that expects a pass must describe a reviewable finding.
func proof(t *testing.T, s *Store, intentID string) string {
	t.Helper()
	ev, err := s.AddEvidence(model.Evidence{
		IntentID: intentID,
		URI:      fmt.Sprintf("%s/proof-%d.txt", intentID, len(s.db.Evidence)+1),
		Name:     "proof.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev.ID
}

func verifiedFact(t *testing.T, s *Store, title string) model.Fact {
	t.Helper()
	f := observedPremise(t, s, title)
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-test"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func direction(t *testing.T, s *Store, title string, premises ...string) model.Intent {
	t.Helper()
	in, err := s.CreateIntent(model.Intent{Title: title, FromFactIDs: premises})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// S2: a fact cannot look more certain than the direction's premises.
func TestFactConfidenceInheritsUnverifiedPremise(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind the premise", premise.ID)

	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived", Confidence: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Confidence != "inferred" {
		t.Fatalf("confidence = %q, want inferred: an unverified premise cannot produce an observed claim", f.Confidence)
	}
}

// S2 false sample: with every premise verified, the declared value stands.
func TestFactConfidenceKeepsDeclaredValueWhenPremisesVerified(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind verified premise", premise.ID)

	for _, declared := range []string{"observed", "inferred", ""} {
		f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived " + declared, Confidence: declared})
		if err != nil {
			t.Fatal(err)
		}
		want := declared
		if want == "" {
			want = "observed"
		}
		if f.Confidence != want {
			t.Fatalf("declared %q: confidence = %q, want %q", declared, f.Confidence, want)
		}
	}
}

// S2 keeps holding on later writes: without the recompute, the next content
// edit would drift an inferred row back to observed.
func TestFactConfidenceIsRecomputedOnUpdate(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind the premise", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived", Confidence: "observed"})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateFact(f.ID, func(row *model.Fact) {
		row.Summary = "corrected wording"
		row.DeclaredConfidence = "observed"
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Confidence != "inferred" {
		t.Fatalf("confidence = %q after update, want inferred", updated.Confidence)
	}
}

// The ratchet: a label forced down by a premise must come back up when the
// premise is verified again, without touching the claim version.
func TestConfidenceUpgradesWhenPremiseGetsVerified(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind the premise", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Confidence != "inferred" {
		t.Fatalf("confidence = %q before the premise was verified, want inferred", f.Confidence)
	}

	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-premise"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(f.ID)
	if got.Confidence != "observed" {
		t.Fatalf("confidence = %q after the premise was verified, want observed", got.Confidence)
	}
	if got.ClaimVersion != f.ClaimVersion {
		t.Fatalf("label move bumped the claim version %d→%d", f.ClaimVersion, got.ClaimVersion)
	}
}

// The base is the writer's, not the direction's: a lead declared `inferred`
// stays inferred even when every premise is verified. Promoting it would turn
// a guess into an observation, which is the reverse pollution this field exists
// to prevent.
func TestDeclaredInferredIsNeverPromoted(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "lead", DeclaredConfidence: "inferred"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Confidence != "inferred" || f.DeclaredConfidence != "inferred" {
		t.Fatalf("declared lead: confidence=%q base=%q, want inferred/inferred", f.Confidence, f.DeclaredConfidence)
	}

	// A later write (and a downstream recompute) must not be able to lose it.
	updated, err := s.UpdateFact(f.ID, func(row *model.Fact) { row.Summary = "rewritten" })
	if err != nil {
		t.Fatal(err)
	}
	if updated.Confidence != "inferred" {
		t.Fatalf("declared lead was promoted to %q by a rewrite", updated.Confidence)
	}
}

// The label is not part of the assertion: moving it must not change the
// fingerprint a review is bound to.
func TestConfidenceIsNotPartOfTheClaimFingerprint(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind the premise", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived"})
	if err != nil {
		t.Fatal(err)
	}
	before := f.ClaimHash
	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-premise"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(f.ID)
	if got.Confidence == "inferred" {
		t.Fatal("fixture did not exercise the label move")
	}
	if got.ClaimHash != before {
		t.Fatalf("label move changed the claim fingerprint %q→%q", before, got.ClaimHash)
	}
}

// The hash slot holds the declared base, so a row written before the field
// existed hashes exactly as it always did: empty base falls back to the value
// that used to live in that slot.
func TestClaimHashKeepsLegacyRowsStable(t *testing.T) {
	legacy := model.Fact{Kind: "vuln", Title: "t", Summary: "s", Confidence: "inferred"}
	upgraded := legacy
	upgraded.DeclaredConfidence = "inferred"
	if claimHash(legacy) != claimHash(upgraded) {
		t.Fatal("recording the declared base changed the fingerprint of an unchanged row")
	}
}

// The derived label may move in both directions; the declared base is the
// writer's claim and changing it is a new assertion.
func TestClaimHashFollowsTheDeclaredBaseNotTheLabel(t *testing.T) {
	row := model.Fact{Kind: "vuln", Title: "t", Summary: "s", DeclaredConfidence: "observed", Confidence: "inferred"}
	moved := row
	moved.Confidence = "observed"
	if claimHash(row) != claimHash(moved) {
		t.Fatal("derived label move changed the fingerprint")
	}
	redeclared := row
	redeclared.DeclaredConfidence = "inferred"
	if claimHash(redeclared) == claimHash(row) {
		t.Fatal("a changed declaration did not change the fingerprint")
	}
}

// A bookkeeping-only write stays inert: renaming or re-attributing a row must
// not bump its version, drop a review, or move its confidence. Only an actual
// assertion change re-derives inheritance.
func TestBookkeepingWriteDoesNotRederiveInheritance(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "probe behind the premise", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "derived"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateFact(f.ID, func(row *model.Fact) { row.SourceRunID = "run-late-attribution" })
	if err != nil {
		t.Fatal(err)
	}
	if updated.ClaimVersion != f.ClaimVersion || updated.Status != f.Status {
		t.Fatalf("bookkeeping write changed the claim: version %d→%d status %s→%s",
			f.ClaimVersion, updated.ClaimVersion, f.Status, updated.Status)
	}
}

// A premise whose assertion changes under a verified downstream removes that
// downstream's backing just like a withdrawal does: the verdict proved v1, and
// v1 is gone.
func TestPremiseClaimChangeDemotesVerifiedDownstream(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpdateFact(premise.ID, func(row *model.Fact) { row.Summary = "corrected assertion" }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(down.ID)
	if got.Status != "unverified" {
		t.Fatalf("downstream status = %q, want unverified after the premise changed", got.Status)
	}
	var reason string
	for _, c := range s.History("fact", down.ID) {
		if c.Reason != "" {
			reason = c.Reason
		}
	}
	if reason != premiseChangedReason+premise.ID {
		t.Fatalf("journal reason = %q, want %q", reason, premiseChangedReason+premise.ID)
	}
}

// The same recompute must not fire on a bookkeeping write: renaming the premise
// would otherwise drop every review below it.
func TestBookkeepingWriteDoesNotDemoteVerifiedDownstream(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpdateFact(premise.ID, func(row *model.Fact) { row.SourceRunID = "run-late" }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(down.ID); got.Status != "verified" {
		t.Fatalf("downstream status = %q, want verified to survive a bookkeeping write", got.Status)
	}
}

func TestUpdateFactPropagatesLostVerificationWithoutChangingClaim(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "note", IntentID: in.ID, Title: "downstream"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	up, err := s.UpdateFact(premise.ID, func(row *model.Fact) { row.Status = "unverified" })
	if err != nil {
		t.Fatal(err)
	}
	if up.ClaimVersion != premise.ClaimVersion || up.ClaimHash != premise.ClaimHash {
		t.Fatalf("status-only mutation changed the assertion: %d %q", up.ClaimVersion, up.ClaimHash)
	}
	got, _ := s.FactByID(down.ID)
	if got.Status != "unverified" || got.Confidence != "inferred" {
		t.Fatalf("downstream = %s/%s, want unverified/inferred", got.Status, got.Confidence)
	}
	for _, c := range s.History("fact", down.ID) {
		if c.Reason == premiseLostReason+premise.ID {
			return
		}
	}
	t.Fatal("missing premise-loss journal reason")
}

// A downstream row that never carried an endorsement is not touched by a claim
// change either: no version bump, no journal line.
func TestPremiseClaimChangeLeavesUnverifiedDownstreamAlone(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.History("fact", down.ID))

	if _, err := s.UpdateFact(premise.ID, func(row *model.Fact) { row.Summary = "corrected assertion" }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(down.ID); got.Status != "unverified" || got.ClaimVersion != down.ClaimVersion {
		t.Fatalf("row changed: status=%q version=%d", got.Status, got.ClaimVersion)
	}
	// The label follows the premise; the claim does not.
	if after := len(s.History("fact", down.ID)); after < before {
		t.Fatalf("history lost entries: %d→%d", before, after)
	}
}

// S3: withdrawing a verified premise removes its endorsement, not the
// downstream conclusion's authorship.
func TestWithdrawPropagatesToVerifiedDownstream(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "10.0.0.5:8443")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WithdrawFact(premise.ID, "pt_operator", "", "前提不成立"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.FactByID(down.ID)
	if !ok {
		t.Fatal("downstream fact disappeared")
	}
	if got.Status != "unverified" {
		t.Fatalf("downstream status = %q, want unverified", got.Status)
	}

	var reason string
	for _, c := range s.History("fact", down.ID) {
		if c.Reason != "" {
			reason = c.Reason
		}
	}
	if reason != premiseLostReason+premise.ID {
		t.Fatalf("journal reason = %q, want %q", reason, premiseLostReason+premise.ID)
	}
}

// S3 false sample: a row that never carried an endorsement is left exactly as
// it was — no version, no journal line, no status change.
func TestWithdrawLeavesUnverifiedDownstreamAlone(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "10.0.0.5:8443")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.History("fact", down.ID))

	if _, err := s.WithdrawFact(premise.ID, "pt_operator", "", "前提不成立"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(down.ID); got.Status != "unverified" || got.ClaimVersion != down.ClaimVersion {
		t.Fatalf("row changed: status=%q version=%d", got.Status, got.ClaimVersion)
	}
	// The label follows the premise (that is the point of the derived value), so
	// a record-version entry may appear; what must not change is the claim.
	if after := len(s.History("fact", down.ID)); after < before {
		t.Fatalf("history lost entries: %d→%d", before, after)
	}
}

// S3 reaches the whole chain, and never turns "not backed" into "false".
func TestPremiseLossReachesTransitiveDownstreamWithoutRejecting(t *testing.T) {
	s := open(t)
	root := verifiedFact(t, s, "root premise")
	first := direction(t, s, "first hop", root.ID)
	middle, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: first.ID, Title: "middle", EvidenceIDs: []string{proof(t, s, first.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: middle.ID, Verdict: "pass", VerifierRun: "run-middle"}); err != nil {
		t.Fatal(err)
	}
	second := direction(t, s, "second hop", middle.ID)
	leaf, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: second.ID, Title: "leaf", EvidenceIDs: []string{proof(t, s, second.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: leaf.ID, Verdict: "pass", VerifierRun: "run-leaf"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WithdrawFact(root.ID, "pt_operator", "", "前提不成立"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{middle.ID, leaf.ID} {
		got, _ := s.FactByID(id)
		if got.Status != "unverified" {
			t.Fatalf("%s status = %q, want unverified", id, got.Status)
		}
	}
}

// needs_evidence is a verdict too: the verifier could not substantiate the
// claim, so the endorsement it gave the rows below is gone. Without this the
// downstream stays `verified` and keeps entering the decision surface and
// reports on a premise nobody could prove.
func TestNeedsEvidencePropagatesToVerifiedDownstream(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "needs_evidence", Notes: "拿不到原始证据"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(down.ID)
	if got.Status != "unverified" {
		t.Fatalf("downstream status = %q, want unverified", got.Status)
	}
	if got.ClaimVersion != down.ClaimVersion {
		t.Fatalf("demotion bumped the claim version %d→%d", down.ClaimVersion, got.ClaimVersion)
	}
	var reason string
	for _, c := range s.History("fact", down.ID) {
		if c.Reason != "" {
			reason = c.Reason
		}
	}
	if reason != premiseLostReason+premise.ID {
		t.Fatalf("journal reason = %q, want %q", reason, premiseLostReason+premise.ID)
	}
	// The premise itself stays usable as a lead, not as a rejected claim: the
	// verifier asked for evidence, it did not say the claim is false.
	if premiseRow, _ := s.FactByID(premise.ID); premiseRow.Status != "unverified" {
		t.Fatalf("premise status = %q, want unverified", premiseRow.Status)
	}
}

// S3 also hangs off a rejection: the verifier saying "this does not reproduce"
// pulls the backing the same way a withdrawal does.
func TestRejectPropagatesToVerifiedDownstream(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	down, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: down.ID, Verdict: "pass", VerifierRun: "run-down"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "reject", Notes: "重跑不成立"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(down.ID)
	if got.Status != "unverified" {
		t.Fatalf("downstream status = %q, want unverified", got.Status)
	}
	if rejected, _ := s.FactByID(premise.ID); rejected.Status != "rejected" {
		t.Fatalf("premise status = %q, want rejected", rejected.Status)
	}
}

// S4: a pass proves the assertion, and the verdict stands even when the chain
// above is unverified — refusing it would deadlock downstream behind an
// upstream nobody re-reviews. What must not happen is the claim getting more
// certain than its premises, or the gap going unrecorded.
func TestPassKeepsInferredAndRecordsThePremiseGap(t *testing.T) {
	s := open(t)
	premise := observedPremise(t, s, "10.0.0.5:8443")
	in := direction(t, s, "derived direction", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}

	review, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-review"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(f.ID)
	if got.Status != "verified" {
		t.Fatalf("status = %q, want verified: a pass is a verdict, not a permission", got.Status)
	}
	if got.Confidence != "inferred" {
		t.Fatalf("confidence = %q, want inferred", got.Confidence)
	}
	if !strings.Contains(review.Notes, "premise_unverified:"+premise.ID) {
		t.Fatalf("review notes = %q, want the premise gap named", review.Notes)
	}

	if _, err := s.ReviewFact(model.Review{FactID: premise.ID, Verdict: "pass", VerifierRun: "run-premise"}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.FactByID(f.ID)
	if current.Confidence != "observed" || current.ClaimHash != got.ClaimHash || current.ClaimVersion != got.ClaimVersion {
		t.Fatalf("premise proof unexpectedly changed the downstream assertion: %+v", current)
	}
	followup, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-followup"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(followup.Notes, "premise_unverified:") {
		t.Fatalf("new review kept a stale premise warning: %q", followup.Notes)
	}
	for _, historical := range s.Reviews(f.ID) {
		if historical.ID == review.ID && historical.Notes == review.Notes {
			return
		}
	}
	t.Fatal("the original review must retain its premise warning")
}

// S4 must not touch a row whose premises are verified.
func TestPassWithVerifiedPremisesLeavesConfidenceAlone(t *testing.T) {
	s := open(t)
	premise := verifiedFact(t, s, "premise")
	in := direction(t, s, "derived direction", premise.ID)
	f, err := s.AddFact(model.Fact{Kind: "vuln", IntentID: in.ID, Title: "downstream", EvidenceIDs: []string{proof(t, s, in.ID)}})
	if err != nil {
		t.Fatal(err)
	}

	review, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-review"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.FactByID(f.ID)
	if got.Status != "verified" || got.Confidence != "observed" {
		t.Fatalf("status=%q confidence=%q, want verified/observed", got.Status, got.Confidence)
	}
	if strings.Contains(review.Notes, "premise_unverified") {
		t.Fatalf("review notes = %q, want no premise gap", review.Notes)
	}
}
