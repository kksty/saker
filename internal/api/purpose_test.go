package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/report"
	"github.com/kksty/tianwu/internal/store"
	"github.com/kksty/tianwu/internal/verifier"
)

// newPurposeServer builds the smallest project that can start a report or a
// review: one verified-able fact with one real evidence file inside the case.
func newPurposeServer(t *testing.T) (*Server, *store.Store, *model.Fact, string) {
	t.Helper()
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases")
	if err := os.MkdirAll(filepath.Join(cases, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cases, "in_1", "proof.txt"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/proof.txt", Name: "proof.txt", Digest: "c1cda26362828b69266512052b97cb3729e3b052e4ade47c0a1e3383defe73c7"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "越权",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, cases)
	s.Timeout = 0
	return s, st, &f, cases
}

// A report run is started by the generator, not dispatch. Pausing the project
// used to leave it running and writing under the project being stopped; it must
// be canceled, settled as canceled (not failed), and leave no half report.
func TestProjectPauseCancelsAReportRun(t *testing.T) {
	s, st, _, cases := newPurposeServer(t)
	ex := s.Exec.(*blockingExec)
	s.Reports = &report.Generator{
		Store: st, Exec: ex, CaseRoot: cases, Track: s.TrackPurposeRun,
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Reports.Generate(s.Ctx)
		errCh <- err
	}()
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	waitFor(t, 5*time.Second, func() bool {
		for _, r := range st.Runs() {
			if r.Purpose == "report" && r.Status == model.RunRunning {
				return true
			}
		}
		return false
	})

	s.projectPause(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/project/pause", nil))

	var run model.Run
	waitFor(t, 5*time.Second, func() bool {
		for _, r := range st.Runs() {
			if r.Purpose == "report" {
				run = r
				return r.Status == model.RunCanceled
			}
		}
		return false
	})
	if run.CancelCause != model.CancelCausePause {
		t.Fatalf("cancel cause = %q, want %q (the control plane stopped it, the executor did not fail)",
			run.CancelCause, model.CancelCausePause)
	}
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "取消") {
			t.Fatalf("Generate() = %v, want a cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after the cancel")
	}
	if entries, err := os.ReadDir(filepath.Join(cases, report.Dir)); err == nil && len(entries) > 0 {
		t.Fatalf("canceled report left a half-written directory: %v", entries)
	}
	if !s.WaitIdle(5 * time.Second) {
		t.Fatal("purpose run registry did not drain after the cancel")
	}
}

// A review is a purpose run too. Canceling it must settle the run as canceled
// and must not write a verdict: "we stopped it" is not "the finding could not
// be reproduced".
func TestProjectPauseCancelsAVerifierRun(t *testing.T) {
	s, st, f, cases := newPurposeServer(t)
	ex := s.Exec.(*blockingExec)
	vf := verifier.New(st, ex, cases)
	vf.Track = s.TrackPurposeRun
	s.Verifier = vf

	if err := s.Verifier.Start(s.Ctx, f.ID, func() { t.Error("onDone fired for a canceled review") }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })

	s.projectPause(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/project/pause", nil))

	var run model.Run
	waitFor(t, 5*time.Second, func() bool {
		for _, r := range st.Runs() {
			if r.Purpose == "verifier" {
				run = r
				return r.Status == model.RunCanceled
			}
		}
		return false
	})
	if run.CancelCause != model.CancelCausePause {
		t.Fatalf("cancel cause = %q, want %q", run.CancelCause, model.CancelCausePause)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("a canceled review changed the fact status to %q", got.Status)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("a canceled review wrote a verdict: %+v", reviews)
	}
	if !s.WaitIdle(5 * time.Second) {
		t.Fatal("verifier run registry did not drain after the cancel")
	}
}

// Configuration failures must be visible before acceptance, and concurrent submissions share one reservation.
func TestReportSubmissionReturnsOneDurableRunReceipt(t *testing.T) {
	s, st, _, cases := newPurposeServer(t)
	s.Reports = &report.Generator{Store: st, Exec: s.Exec, CaseRoot: cases, Node: "removed-test-node"}
	if rec := call(t, s, http.MethodPost, "/api/reports", nil); rec.Code != http.StatusConflict {
		t.Fatalf("unavailable writer = %d %s", rec.Code, rec.Body.String())
	}
	if len(st.Runs()) != 0 {
		t.Fatal("an invalid submission created a run")
	}

	s.Reports.Node = ""
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	first := call(t, s, http.MethodPost, "/api/reports", nil)
	var receipt struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || first.Code != http.StatusAccepted || receipt.RunID == "" {
		t.Fatalf("receipt = %d %s, %v", first.Code, first.Body.String(), err)
	}
	if run, ok := st.GetRun(receipt.RunID); !ok || run.Purpose != "report" {
		t.Fatalf("receipt names no report run: %+v", run)
	}
	if second := call(t, s, http.MethodPost, "/api/reports", nil); second.Code != http.StatusConflict {
		t.Fatalf("second submission = %d %s", second.Code, second.Body.String())
	}
	cancel()
	waitFor(t, 5*time.Second, func() bool { return !s.Reports.Busy() })
}
