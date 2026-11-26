package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

// A temporary pause stops review runs; it must not relax what a direction is
// allowed to dispatch on. Only the instance-level --no-verify switch does that,
// because a paused review can resume and a direction frozen without its promised
// review would be silently downgraded.
func TestReadinessBypassNeedsTheInstanceSwitchNotATemporaryPause(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "无核验前提", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetReviewsPaused(true)
	if got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true); got.Ready {
		t.Fatalf("a paused review relaxed premise admission: %+v", got)
	}
	s.SetReviewsPaused(false)
	s.SetReviewsDisabled(true)
	if got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true); !got.Ready {
		t.Fatalf("reviews disabled still blocked the direction: %+v", got)
	}
}

// With review off the text of an unverified claim is shown, but a withdrawn or
// rejected claim must never become readable because a switch was flipped: those
// statuses are recorded verdicts, not "waiting for review".
func TestRenderFactHidesRetractedClaimsEvenWhenReviewIsOff(t *testing.T) {
	for _, status := range []string{"withdrawn", "rejected", "missing", ""} {
		f := model.Fact{ID: "f_" + status, Kind: "vuln", Status: status, Title: "TITLE-" + status, Summary: "SUMMARY"}
		got := renderFact(f, true)
		if strings.Contains(got, "TITLE-") || strings.Contains(got, "SUMMARY") {
			t.Fatalf("status %q leaked its text with review off: %q", status, got)
		}
	}
	unverified := model.Fact{ID: "f_u", Kind: "vuln", Status: "unverified", Title: "TITLE-U", Summary: "SUMMARY-U"}
	if got := renderFact(unverified, true); !strings.Contains(got, "TITLE-U") {
		t.Fatalf("unverified claim hidden with review off: %q", got)
	}
}

// The commander round used to say "verified_only, wait for the current proof" on
// every round, including rounds where review was off. That describes a queue that
// cannot move: the switch must change the admission wording, not only the gate.
func TestPremisePolicyLineFollowsTheReviewSwitch(t *testing.T) {
	off := premisePolicyLine("verified_only", true)
	if !strings.Contains(off, "复核已关闭") || strings.Contains(off, "本轮前提政策") {
		t.Fatalf("review-off line still describes verified_only: %q", off)
	}
	if !strings.Contains(off, "不要等待复核") {
		t.Fatalf("review-off line does not stop the waiting instruction: %q", off)
	}
	on := premisePolicyLine("verified_only", false)
	if !strings.Contains(on, "本轮前提政策：verified_only") || strings.Contains(on, "复核已关闭") {
		t.Fatalf("review-on line changed: %q", on)
	}
}

// With review off a direction must not be shown as "waiting for review": there is
// no queue, so readiness reports the premise usable and attaches no review state.
func TestReadinessAttachesNoReviewStateWhenReviewIsOff(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "无核验前提", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetReviewsDisabled(true)
	got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
	if !got.Ready {
		t.Fatalf("review-off readiness blocked: %+v", got)
	}
	for _, row := range got.Premises {
		if row.Review != nil {
			t.Fatalf("review-off premise still carries a review state: %+v", row)
		}
	}
	if got.ReviewHint != "" {
		t.Fatalf("review-off readiness still explains a queue: %q", got.ReviewHint)
	}
}

// A registered original that cannot be opened is a recorded gap, not a verdict:
// the page shows Integrity=missing but keeps the premise usable, because the
// dispatch gate only appends a gap for the same row and still dispatches
// (internal/store/input.go:115-121).
func TestReadinessRecordsBrokenOriginalWithReviewsOff(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	in, err := p.Store.CreateIntent(model.Intent{Title: "x", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	db := p.Store.Snapshot()
	db.Evidence = nil // the premise still names its evidence; the rows are gone
	got := s.readiness(db, in, s.premisePolicy(), false)
	if !got.IntegrityChecked {
		t.Fatal("readiness reported integrity not checked although it checked presence")
	}
	if !got.Ready {
		t.Fatalf("a recorded gap blocked the direction: %+v", got.Premises)
	}
	if len(got.Premises) != 1 || got.Premises[0].Integrity != "missing" || !got.Premises[0].Usable {
		t.Fatalf("premise integrity = %+v, want integrity=missing and usable", got.Premises)
	}
}

// An empty evidence list is still dispatchable with review off; the operator sees
// the "missing" annotation only.
func TestReadinessAllowsPremiseWithoutEvidenceWithReviewsOff(t *testing.T) {
	s, p, _, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	f, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "no evidence", Summary: "x"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "x", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), false)
	if !got.Ready {
		t.Fatalf("a premise with no evidence was blocked: %+v", got.Premises)
	}
	if len(got.Premises) != 1 || got.Premises[0].Integrity != "none" || !got.Premises[0].Usable {
		t.Fatalf("premise = %+v", got.Premises)
	}
}

// Every intent that cites premises shares checkPremiseRefs. Closing review must
// not exempt one from the type rule: a note is still not a premise, while an
// unverified reviewable fact may be cited (its dispatch is gated separately).
func TestPremiseRefsStayStrictWithReviewsOff(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	note, err := p.Store.AddFact(model.Fact{Kind: "note", Title: "memo", Summary: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkPremiseRefs([]string{note.ID}); err == nil {
		t.Fatal("a note premise was accepted as a child's from_fact_ids")
	}
	if err := s.checkPremiseRefs([]string{f.ID}); err != nil {
		t.Fatalf("an unverified reviewable premise was refused: %v", err)
	}
}

// A switch the operator sees as successful must survive a restart. When the
// manifest cannot be written the endpoint reports it and leaves the runtime
// value untouched instead of lying.
func TestVerifierDisableReportsPersistenceFailure(t *testing.T) {
	s, p, _, _ := proofPolicyFixture(t)
	if err := os.Mkdir(filepath.Join(p.Dir, project.ManifestFile+".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.verifierDisable(w, httptest.NewRequest("POST", "/api/verifier/disable", nil))
	if w.Code != 500 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if s.ReviewsDisabled() {
		t.Fatal("runtime review switch changed although persistence failed")
	}
}

// Evidence is advisory at the edge too: an evidence-less premise is accepted
// (the operator sees the "no original evidence" annotation), while the type and
// retraction rules still refuse.
func TestCheckPremiseRefsAcceptsPremiseWithoutEvidence(t *testing.T) {
	s, p, evidenced, _ := proofPolicyFixture(t)
	s.SetReviewsDisabled(true)
	if err := s.checkPremiseRefs([]string{evidenced.ID}); err != nil {
		t.Fatalf("an evidenced premise was refused: %v", err)
	}
	bare, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "no evidence", Summary: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkPremiseRefs([]string{bare.ID}); err != nil {
		t.Fatalf("evidence-less premise was refused: %v", err)
	}
}
