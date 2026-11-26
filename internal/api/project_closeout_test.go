package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

func closeoutServer(t *testing.T) (*Server, *commanderExec) {
	t.Helper()
	s, ex, _ := newCommanderServer(t)
	s.Decider = decider.NewRunner(s.Store, nil, "")
	s.Decider.ModelName = "fixture"
	s.Decider.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		res, actions, err := s.StartCommander(ctx, withBoardAnchor(prompt), resume)
		return decider.RoundResult{RunID: res.LedgerRunID, Tokens: res.Tokens, Actions: actions}, err
	}
	if _, err := s.Store.AddGoal(model.Goal{Statement: "fixture goal"}); err != nil {
		t.Fatal(err)
	}
	return s, ex
}

func TestCloseoutAfterAccidentalResumeAndManualPause(t *testing.T) {
	s, ex := closeoutServer(t)
	if err := s.Project.SetPausedWithKind(true, project.PauseKindCommanderStandby); err != nil {
		t.Fatal(err)
	}
	s.SetPaused(true)
	s.RestorePauseKind(project.PauseKindCommanderStandby)
	s.Decider.Pause()
	if rec := call(t, s, "POST", "/api/project/resume", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := call(t, s, "POST", "/api/project/pause", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	proof := addEvidenceFile(t, s.Store, s.CaseRoot, "in_1/proof.txt", "proof.txt", "text/plain", []byte("fixture proof"))
	fact, err := s.Store.AddFact(model.Fact{Kind: "artifact", Title: "fixture observed", EvidenceIDs: []string{proof.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "pass", VerifierRun: "fixture-proof"}); err != nil {
		t.Fatal(err)
	}
	goal := s.Store.Goals()[0]
	ex.during = func() {
		if !s.paused.Load() || !s.Project.IsPaused() || !s.Decider.Paused() {
			t.Error("closeout resumed project work")
		}
		for _, path := range []string{"/api/project/resume", "/api/project/closeout"} {
			if rec := call(t, s, "POST", path, nil); rec.Code != 409 {
				t.Errorf("concurrent %s = %d", path, rec.Code)
			}
		}
		rec := call(t, s, "POST", "/api/goals/"+goal.ID+"/complete", map[string]any{"from": []string{fact.ID}, "note": "existing fixture proof"})
		if rec.Code != 200 {
			t.Error(rec.Body.String())
		}
	}
	rec := call(t, s, "POST", "/api/project/closeout", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"completed":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ex.runs != 1 || !strings.Contains(ex.req.Prompt, "操作员请求指挥官收口") {
		t.Fatal("closeout did not run exactly one scoped commander round")
	}
	if !s.paused.Load() || !s.Project.IsPaused() || !s.Decider.Paused() {
		t.Fatal("pause was lost")
	}
	if d := s.Decider.Run(context.Background(), "operator", true); d != nil {
		t.Fatal("ordinary paused round ran after closeout")
	}
	if len(s.Store.Goals()[0].From) != 1 {
		t.Fatal("completion lost its fact receipt")
	}
}

func TestCloseoutWithoutProofKeepsGoalOpen(t *testing.T) {
	s, ex := closeoutServer(t)
	call(t, s, "POST", "/api/project/pause", nil)
	ex.during = func() {
		goal := s.Store.Goals()[0]
		rec := call(t, s, "POST", "/api/goals/"+goal.ID+"/complete", map[string]any{"from": []string{"missing"}, "note": "unsupported"})
		if rec.Code != 400 {
			t.Errorf("unproven completion = %d", rec.Code)
		}
	}
	rec := call(t, s, "POST", "/api/project/closeout", nil)
	var out struct {
		Completed bool
		Paused    bool
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Completed || !out.Paused || s.Store.Goals()[0].Met {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestCloseoutRequiresPausedOperatorRequestAndRemainsCancelable(t *testing.T) {
	s, ex := closeoutServer(t)
	if rec := call(t, s, "POST", "/api/project/closeout", nil); rec.Code != 409 {
		t.Fatal(rec.Body.String())
	}
	req := httptest.NewRequest("POST", "/api/project/closeout", nil)
	req = req.WithContext(WithAgentSurface(req.Context()))
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("agent closeout = %d", rec.Code)
	}
	call(t, s, "POST", "/api/project/pause", nil)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("not authorized"), false); err == nil {
		t.Fatal("pause bypassed without closeout context")
	}
	ex.during = func() { call(t, s, "POST", "/api/project/pause", nil) }
	rec = call(t, s, "POST", "/api/project/closeout", nil)
	if rec.Code != 502 || s.closeout.Load() {
		t.Fatalf("canceled closeout = %d %s", rec.Code, rec.Body.String())
	}
	runs := s.Store.Runs()
	if len(runs) != 1 || runs[0].Status != model.RunCanceled {
		t.Fatalf("runs = %+v", runs)
	}
}
