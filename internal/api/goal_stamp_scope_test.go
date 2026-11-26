package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

// goalStampFixture builds the smallest ledger that can be stamped: one verified
// fact backed by one evidence file, and one open goal.
func goalStampFixture(t *testing.T) (*Server, *http.ServeMux, *model.Goal) {
	t.Helper()
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev := addEvidenceFile(t, st, s.CaseRoot, "in_1/p.txt", "p.txt", "text/plain", []byte("proof"))
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "支持", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-support"}); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "人工盖章"})
	if err != nil {
		t.Fatal(err)
	}
	return s, mux, &g
}

// commanderStampRequest is the commander session's real goal-write shape after
// the auth gate attaches the run identity: participant=commander, run id, no
// console token.
func commanderStampRequest(method, path, body, runID string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ParticipantID: collab.CommanderID,
		RunID:         runID,
	}))
}

func goalByIDFromStore(t *testing.T, s *Server, id string) model.Goal {
	t.Helper()
	for _, g := range s.Store.Goals() {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("goal %s not found", id)
	return model.Goal{}
}

// The operator's manual stamp is the operator's judgement and its one-line
// reason: a session reopening it would erase both with no round log to point
// at. It must be refused, and every field must stay byte-for-byte identical —
// including when the caller presents a live commander run.
func TestCommanderSessionCannotWithdrawOperatorStamp(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	ev := s.Store.Goals()[0].EvidenceID
	_ = ev

	// Operator pins the goal by hand (no harness surface, no run identity).
	var req struct{ EvidenceID, Note string }
	_ = req
	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"操作员核对原始响应后确认"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator met = %d %s", rec.Code, rec.Body.String())
	}
	before := goalByIDFromStore(t, s, g.ID)
	if !before.Met || before.MetByRun != "" {
		t.Fatalf("operator stamp provenance = %+v", before)
	}

	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*http.Request{
		commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", "", run.ID),
		harnessRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen"),
	} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "goal_operator_only") {
			t.Fatalf("session reopen of an operator stamp = %d %s", rec.Code, rec.Body.String())
		}
		if after := goalByIDFromStore(t, s, g.ID); !reflect.DeepEqual(before, after) {
			t.Fatalf("refused reopen changed the operator stamp:\nbefore %+v\nafter  %+v", before, after)
		}
	}
}

// A commander's own evidence-gated completion is a judgement that can be wrong:
// a live commander round may withdraw it (D97/D98), and the withdrawn stamp is
// named in the round's action record before the row forgets it.
func TestCommanderSessionWithdrawsItsOwnStamp(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"证据覆盖目标"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander complete = %d %s", rec.Code, rec.Body.String())
	}
	stamped := goalByIDFromStore(t, s, g.ID)
	if !stamped.Met || stamped.MetByRun != run.ID {
		t.Fatalf("commander stamp provenance = %+v", stamped)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", "", run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander reopen of its own stamp = %d %s", rec.Code, rec.Body.String())
	}
	var reopened model.Goal
	if err := json.Unmarshal(rec.Body.Bytes(), &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Met || reopened.MetByRun != "" || reopened.CompletedAt != nil || reopened.Note != "" {
		t.Fatalf("reopened goal = %+v", reopened)
	}
}

// A dead round cannot withdraw a stamp either: the identity gate is the same one
// goal definitions use, so a stale session credential has no more power here.
func TestDeadCommanderRunCannotWithdrawItsStamp(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"证据覆盖目标"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander complete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	before := goalByIDFromStore(t, s, g.ID)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", "", run.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dead run reopen = %d %s", rec.Code, rec.Body.String())
	}
	if after := goalByIDFromStore(t, s, g.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused reopen changed the commander stamp:\nbefore %+v\nafter  %+v", before, after)
	}
}

// The operator keeps the override on a commander's stamp (D98).
func TestOperatorCanStillWithdrawACommanderStamp(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"证据覆盖目标"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander complete = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator reopen = %d %s", rec.Code, rec.Body.String())
	}
}

// goalFactID returns the fixture's only fact id.
func goalFactID(t *testing.T, s *Server) string {
	t.Helper()
	facts := s.Store.ListFacts("", 0)
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	return facts[0].ID
}

// goalEvidenceID returns the evidence id the fixture attached to that fact.
func goalEvidenceID(t *testing.T, s *Server, _ string) string {
	t.Helper()
	facts := s.Store.ListFacts("", 0)
	if len(facts) != 1 || len(facts[0].EvidenceIDs) != 1 {
		t.Fatalf("facts/evidence = %+v", facts)
	}
	return facts[0].EvidenceIDs[0]
}
