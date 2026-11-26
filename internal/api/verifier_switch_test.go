package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The runtime review switch (POST /api/verifier/disable, /api/verifier/enable)
// is not the temporary pause: closing it cancels the live reviews, refuses new
// ones with the machine code resume already uses, and opening it makes reviews
// possible again without a restart. Closing twice is a no-op, not an error.
func TestVerifierDisableCancelsLiveReviewsAndEnableReopens(t *testing.T) {
	block := make(chan struct{})
	ex := &batchExec{block: block}
	s, st, mux := newBatchServer(t, ex)
	s.Verifier.Track = s.TrackReviewRun
	f := addVerifyFact(t, st, "vuln", "unverified")

	if status := getVerifierStatus(t, mux); status["enabled"] != true || status["disabled"] != false {
		t.Fatalf("verifier status = %v, want enabled=true disabled=false", status)
	}
	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })
	waitReviewTracked(t, s)

	code, out := doPost(t, mux, "/api/verifier/disable")
	if code != http.StatusOK || out["disabled"] != true || out["canceled"] != float64(1) {
		t.Fatalf("disable = %d %v, want 200 disabled=true canceled=1", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
	run := lastVerifierRun(t, s)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseReviewPause {
		t.Fatalf("review run = status %q cause %q, want canceled/%s", run.Status, run.CancelCause, model.CancelCauseReviewPause)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("canceled review changed the fact to %q", got.Status)
	}
	if status := getVerifierStatus(t, mux); status["enabled"] != false || status["disabled"] != true {
		t.Fatalf("status after disable = %v, want enabled=false disabled=true", status)
	}

	// Resume cannot reopen a disabled review: only enable can. The reason is the
	// same machine code the UI switches its error on.
	code, out = doPost(t, mux, "/api/verifier/resume")
	if code != http.StatusConflict || out["code"] != "verify_disabled" {
		t.Fatalf("resume while disabled = %d %v, want 409 verify_disabled", code, out)
	}
	code, out = doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	if code != http.StatusConflict || out["code"] != "verify_disabled" {
		t.Fatalf("verify while disabled = %d %v, want 409 verify_disabled", code, out)
	}
	if code, out = doPost(t, mux, "/api/facts/verify-pending"); code != http.StatusConflict || out["code"] != "verify_disabled" {
		t.Fatalf("batch while disabled = %d %v, want 409 verify_disabled", code, out)
	}

	// Idempotent: closing a closed switch still answers 200 and cancels nothing.
	if code, out = doPost(t, mux, "/api/verifier/disable"); code != http.StatusOK || out["disabled"] != true || out["canceled"] != float64(0) {
		t.Fatalf("second disable = %d %v, want 200 disabled=true canceled=0", code, out)
	}

	ex.block = nil
	ex.body = `{"verdict":"pass","notes":"重跑一致","proof":"p"}`
	code, out = doPost(t, mux, "/api/verifier/enable")
	if code != http.StatusOK || out["disabled"] != false {
		t.Fatalf("enable = %d %v, want 200 disabled=false", code, out)
	}
	if status := getVerifierStatus(t, mux); status["enabled"] != true || status["disabled"] != false {
		t.Fatalf("status after enable = %v, want enabled=true disabled=false", status)
	}
	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify after enable = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.FactByID(f.ID)
		return got.Status == "verified"
	})
}

// Enabling is idempotent too, and it must not clear a temporary pause the
// operator set while reviews were open: the two switches are separate.
func TestVerifierEnableLeavesAnOperatorPauseAlone(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	s, _, mux := newBatchServer(t, ex)

	if code, out := doPost(t, mux, "/api/verifier/pause"); code != http.StatusOK {
		t.Fatalf("pause = %d %v, want 200", code, out)
	}
	if code, out := doPost(t, mux, "/api/verifier/enable"); code != http.StatusOK || out["disabled"] != false {
		t.Fatalf("enable while already open = %d %v, want 200 disabled=false", code, out)
	}
	if !s.ReviewsPaused() {
		t.Fatal("enable cleared the operator's temporary pause")
	}
}

// The review switch forks the premise gate: with reviews open an unverified
// premise stops the dispatch (verified_only admits nothing else); with reviews
// closed the same fact is usable and the run records that it was not verified.
// Readiness projects the premise as unusable in both cases — it describes the
// claim, the gate decides what to do about it.
func TestVerifierDisableForksThePremiseGate(t *testing.T) {
	s, p, f, _ := proofPolicyFixture(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "consumer", FromFactIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true).Ready {
		t.Fatal("unverified premise projected as usable while reviews are open")
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusRunning, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy(), store.DispatchGate{ReviewsOff: s.reviewsOff()}); err == nil {
		t.Fatal("open reviews dispatched an unverified premise")
	}

	s.SetReviewsDisabled(true)
	t.Cleanup(func() { s.SetReviewsDisabled(false) })
	if got := s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true); !got.Ready {
		t.Fatalf("disabled reviews still projected the premise as unusable: %+v", got)
	}
	db, gaps, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy(), store.DispatchGate{ReviewsOff: s.reviewsOff()})
	if err != nil {
		t.Fatalf("disabled reviews still refused the dispatch: %v", err)
	}
	if db.Facts == nil {
		t.Fatal("input snapshot is empty")
	}
	if !strings.Contains(strings.Join(gaps, "；"), "复核关闭") {
		t.Fatalf("the gap does not say reviews were closed: %v", gaps)
	}
	if frozen, ok := p.Store.GetRun(run.ID); !ok || frozen.PremiseClaimVersions[f.ID] == 0 {
		t.Fatalf("input freeze lost the premise the relaxed dispatch used: %+v", frozen.PremiseClaimVersions)
	}

	s.SetReviewsDisabled(false)
	if s.readiness(p.Store.Snapshot(), in, s.premisePolicy(), true).Ready {
		t.Fatal("re-enabled reviews kept premise admission relaxed")
	}
}

// A closed switch is not a verdict: the fixed settlement phase must not claim a
// finished direction (it would sit in reviewing behind a switch that no resume
// can open), and it must spend no review run.
func TestReviewAfterRunSkipsFixedPhaseWhenReviewsDisabled(t *testing.T) {
	s, in, run, fact, ex := newSettlementFixture(t)
	s.SetReviewsDisabled(true)
	t.Cleanup(func() { s.SetReviewsDisabled(false) })

	if s.reviewAfterRun(in.ID, run.ID) {
		t.Fatal("a disabled review claimed the settlement phase")
	}
	if ex.runs() != 0 {
		t.Fatalf("disabled reviews spent %d review runs, want 0", ex.runs())
	}
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusDone {
		t.Fatalf("direction = %s, want done (a disabled review is not a verdict)", cur.Status)
	}
	got, _ := s.Store.FactByID(fact.ID)
	if got.Status != "unverified" {
		t.Fatalf("conclusion = %s, want unverified", got.Status)
	}
}

// getVerifierStatus is GET /api/verifier for the switch tests.
func getVerifierStatus(t *testing.T, mux *http.ServeMux) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/verifier", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("verifier status = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("verifier status body: %v", err)
	}
	return out
}
