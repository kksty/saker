package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/verifier"
)

// The review state on readiness is the session-visible half of the verifier:
// it says what the session should do (wait / ask the operator) without exposing
// the review run, its output or the queue. It exists because a blocked
// verified_only direction otherwise invites the commander to invent a review
// workaround.
// A direction that shares an entity with its own unverified premise is the
// re-probe circularity: the review probes the entity, then the direction probes
// it again. The hint is advisory because the same shape is valid for a deeper
// direction that merely operates on that host.
func TestReadinessWarnsAboutSelfReferentialPremise(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	if _, err := p.Store.UpdateFact(f.ID, func(f *model.Fact) {
		f.EntityRef = "host:36.103.223.42"
	}); err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{
		Title:              "复探 36.103.223.42 可达性",
		FromFactIDs:        []string{f.ID},
		InterestEntityRefs: []string{"host:36.103.223.42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
	if got.SelfReferenceHint == "" || !strings.Contains(got.SelfReferenceHint, "重复") {
		t.Fatalf("self-reference hint = %q", got.SelfReferenceHint)
	}
}

func TestSessionReadinessWithholdsUnverifiedProse(t *testing.T) {
	s, st := newFocusTestServer(t)
	f, err := st.AddFact(model.Fact{Kind: "host", Title: "SYNTHETIC-TITLE", EntityRef: "host:synthetic.example"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{
		Title: "fixture", FromFactIDs: []string{f.ID}, InterestEntityRefs: []string{f.EntityRef},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/intents/{id}/readiness", s.getIntentReadiness)
	for _, agent := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/api/intents/"+in.ID+"/readiness", nil)
		if agent {
			req = req.WithContext(WithAgentSurface(req.Context()))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, f.ID) || !strings.Contains(body, "unverified") {
			t.Fatalf("readiness = %d %s", rec.Code, body)
		}
		if strings.Contains(body, f.Title) == agent || strings.Contains(body, f.EntityRef) == agent {
			t.Fatalf("unexpected readiness visibility (agent=%t): %s", agent, body)
		}
	}
}

func TestReadinessReportsBlockedReviewState(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateQueued {
			t.Fatalf("queued premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "会话不能触发复核") {
			t.Fatalf("queued hint = %q", got.ReviewHint)
		}
	})

	t.Run("running", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		ex := &reviewProbeExec{
			blockFirst: true,
			entered:    make(chan struct{}, 1),
			release:    make(chan struct{}),
		}
		s.Verifier = verifier.New(p.Store, ex, s.CaseRoot)
		s.Verifier.Policy = s.premisePolicy
		if err := s.Verifier.Start(context.Background(), f.ID, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			select {
			case <-ex.entered:
				return true
			default:
				return false
			}
		})
		t.Cleanup(func() {
			close(ex.release)
			waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
		})
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateRunning {
			t.Fatalf("running premise = %+v", got.Premises)
		}
	})

	t.Run("cooling", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.noteReviewFailure(f, time.Now())
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateCooling {
			t.Fatalf("cooling premise = %+v", got.Premises)
		}
	})

	t.Run("paused", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.SetReviewsPaused(true)
		t.Cleanup(func() { s.SetReviewsPaused(false) })
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStatePaused {
			t.Fatalf("paused premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "请操作员继续复核") {
			t.Fatalf("paused hint = %q", got.ReviewHint)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		s, p, f, _ := proofPolicyFixture(t)
		s.Verifier = nil
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateDisabled {
			t.Fatalf("disabled premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "请操作员处理") {
			t.Fatalf("disabled hint = %q", got.ReviewHint)
		}
	})

	t.Run("failed", func(t *testing.T) {
		s, p, source, _ := proofPolicyFixture(t)
		fact, err := p.Store.AddFact(model.Fact{
			IntentID: source.IntentID, Kind: "vuln", Title: "failed premise",
			Origin: model.OriginDeclared, EvidenceIDs: source.EvidenceIDs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "needs_evidence", Notes: "无法复现"}); err != nil {
			t.Fatal(err)
		}
		in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{fact.ID}})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateFailed {
			t.Fatalf("failed premise = %+v", got.Premises)
		}
		if !strings.Contains(got.ReviewHint, "复核未通过") {
			t.Fatalf("failed hint = %q", got.ReviewHint)
		}
	})

	t.Run("ineligible", func(t *testing.T) {
		s, p, _, _ := proofPolicyFixture(t)
		note, err := p.Store.AddFact(model.Fact{Kind: "note", Title: "historical suggestion"})
		if err != nil {
			t.Fatal(err)
		}
		got := s.readiness(p.Store.Snapshot(), model.Intent{FromFactIDs: []string{note.ID}}, model.PremiseVerifiedOnly, true)
		if len(got.Premises) != 1 || got.Premises[0].Review == nil || got.Premises[0].Review.State != reviewStateIneligible {
			t.Fatalf("ineligible premise = %+v", got.Premises)
		}
	})

}

// A verified row can still lack a usable receipt: a ledger imported from an
// older build, or a review that proved an earlier assertion, leaves the status
// alone while no pass is bound to the current version. Admission has to name
// that reason instead of letting the row read as proven.
func TestReadinessNamesTheMissingReceipt(t *testing.T) {
	s, st := newFocusTestServer(t)
	db := st.Snapshot()
	db.Facts = append(db.Facts, model.Fact{ID: "f_legacy", Kind: "host", Title: "导入的历史结论", Status: "verified", ClaimVersion: 2, ClaimHash: "hash-v2", EvidenceIDs: []string{"ev_legacy"}})
	// The digest has to be the SHA-256 shape the gate accepts (store.EvidenceVerifiable):
	// this fixture is about a stale receipt, so the original itself must be sound.
	db.Evidence = append(db.Evidence, model.Evidence{ID: "ev_legacy", URI: "in_legacy/raw/legacy.txt",
		Digest: strings.Repeat("a", 64)})
	db.Reviews = append(db.Reviews, model.Review{ID: "rv_old", FactID: "f_legacy", ClaimVersion: 1, ClaimHash: "hash-v1", Verdict: "pass", VerifierRun: "run-old"})
	in := model.Intent{ID: "in_legacy", Title: "导入后的意图", FromFactIDs: []string{"f_legacy"}}
	got := s.readiness(db, in, model.PremiseVerifiedOnly, false)
	if got.Ready || !strings.Contains(got.Premises[0].Reason, "收据") {
		t.Fatalf("stale receipt admitted: %+v", got)
	}
	// The same row with a pass that recorded a run against the current version
	// is a real receipt and has to be admitted.
	db.Reviews = []model.Review{{ID: "rv_new", FactID: "f_legacy", ClaimVersion: 2, ClaimHash: "hash-v2", Verdict: "pass", VerifierRun: "run-new"}}
	if got := s.readiness(db, in, model.PremiseVerifiedOnly, false); !got.Ready {
		t.Fatalf("bound receipt refused: %+v", got)
	}
}
