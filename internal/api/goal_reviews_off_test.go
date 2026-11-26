package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// reviewsOffGoalFixture is the deadlock case from 2026-11-25: one unverified
// vuln fact with an evidence file and one open goal, reviews switched off. No
// verdict can ever arrive, so completion must not demand one — otherwise the
// project can never be closed and the operator's manual stamp is blocked by the
// same gate.
func reviewsOffGoalFixture(t *testing.T, reviewsOff bool, factStatus string) (*Server, *http.ServeMux, *model.Goal) {
	t.Helper()
	s, _, st := newCommanderServer(t)
	s.Project = nil
	s.SetReviewsDisabled(reviewsOff)
	mux := http.NewServeMux()
	s.Register(mux)

	ev := addEvidenceFile(t, st, s.CaseRoot, "in_1/p.txt", "p.txt", "text/plain", []byte("proof"))
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "未复核结论", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if factStatus != "" {
		if _, err := st.UpdateFact(f.ID, func(x *model.Fact) { x.Status = factStatus }); err != nil {
			t.Fatal(err)
		}
	}
	g, err := st.AddGoal(model.Goal{Statement: "未复核模式"})
	if err != nil {
		t.Fatal(err)
	}
	return s, mux, &g
}

// The commander's evidence-gated complete must work when reviews are off: the
// cited facts cannot have proofs, and requiring one would make the goal
// un-stampable for as long as reviews stay disabled.
func TestCommanderCompletesGoalWhenReviewsAreDisabled(t *testing.T) {
	s, mux, g := reviewsOffGoalFixture(t, true, "")
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"证据覆盖目标，复核关闭"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander complete with reviews off = %d %s", rec.Code, rec.Body.String())
	}
	stamped := goalByIDFromStore(t, s, g.ID)
	if !stamped.Met || stamped.MetByRun != run.ID {
		t.Fatalf("stamp provenance = %+v", stamped)
	}
}

// With reviews on the verdict gate still applies: an unverified finding cannot
// close the project.
func TestCommanderCompleteStillRefusesUnverifiedWhenReviewsAreOn(t *testing.T) {
	s, mux, g := reviewsOffGoalFixture(t, false, "")
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"未复核也想盖章"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("commander complete with reviews on = %d %s", rec.Code, rec.Body.String())
	}
	if goalByIDFromStore(t, s, g.ID).Met {
		t.Fatal("unverified fact closed the goal while reviews are on")
	}
}

// Dropping the verdict gate must not drop the other premises: a withdrawn fact
// stays unusable even with reviews off.
func TestWithdrawnFactStillBlocksCompletionWhenReviewsAreOff(t *testing.T) {
	s, mux, g := reviewsOffGoalFixture(t, true, "withdrawn")
	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"from":["` + goalFactID(t, s) + `"],"note":"撤回后仍想盖章"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete", body, run.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("withdrawn fact complete = %d %s", rec.Code, rec.Body.String())
	}
	if goalByIDFromStore(t, s, g.ID).Met {
		t.Fatal("withdrawn fact closed the goal")
	}
}

// The operator's manual pin is the documented way out when the session cannot
// cite a proof. The console sends only the evidence row, so an empty from must
// be accepted while reviews are off.
func TestOperatorManualMetWorksWhenReviewsAreDisabled(t *testing.T) {
	s, mux, g := reviewsOffGoalFixture(t, true, "")
	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"操作员核对原始响应后确认"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator met with reviews off = %d %s", rec.Code, rec.Body.String())
	}
	stamped := goalByIDFromStore(t, s, g.ID)
	if !stamped.Met || stamped.MetByRun != "" {
		t.Fatalf("operator stamp provenance = %+v", stamped)
	}
}

// With reviews on the operator's pin still has to cite facts: the manual stamp
// is not a way around the verdict gate.
func TestOperatorManualMetStillRequiresFactsWhenReviewsAreOn(t *testing.T) {
	s, mux, g := reviewsOffGoalFixture(t, false, "")
	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"复核开着，但仍不引用事实"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("operator met with reviews on = %d %s", rec.Code, rec.Body.String())
	}
	if goalByIDFromStore(t, s, g.ID).Met {
		t.Fatal("operator closed the goal without citing facts while reviews are on")
	}
}
