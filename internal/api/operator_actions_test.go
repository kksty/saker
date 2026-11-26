package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// The operator's stamp is the final judgement and must leave a durable line:
// the console has no round to write into decisions.jsonl, and a reopen clears
// the goal row that would otherwise carry who stamped it and why.
func TestOperatorMetLeavesADurableAuditLine(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"操作员核对原始响应后确认"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator met = %d %s", rec.Code, rec.Body.String())
	}

	lines := readOperatorActions(t, s)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1: %+v", len(lines), lines)
	}
	got := lines[0]
	if got.Action != "goal_met" || got.GoalID != g.ID || got.By != "operator" {
		t.Fatalf("audit line = %+v", got)
	}
	if got.Note != "操作员核对原始响应后确认" || got.EvidenceID == "" || got.At.IsZero() {
		t.Fatalf("audit line lost the decision: %+v", got)
	}
	if got.MetByRun != "" {
		t.Fatalf("operator stamp recorded a commander run: %+v", got)
	}
}

// Withdrawing a commander's stamp is the operator's override (D98): the line
// must name the withdrawn stamp, because after the reopen the goal row does not.
func TestOperatorReopenAuditNamesTheWithdrawnStamp(t *testing.T) {
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

	lines := readOperatorActions(t, s)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1: %+v", len(lines), lines)
	}
	got := lines[0]
	if got.Action != "goal_reopen" || got.By != "operator" || got.StampedBy != run.ID {
		t.Fatalf("audit line = %+v", got)
	}
	if got.StampedAt == "" || got.StampedNote != "证据覆盖目标" || got.MetByRun != run.ID {
		t.Fatalf("audit line lost the withdrawn stamp: %+v", got)
	}
}

// A commander withdrawing its own stamp is recorded with the run as the actor,
// so the trace still answers "who" for the self-correction path.
func TestCommanderReopenAuditNamesTheRun(t *testing.T) {
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
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", "", run.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander reopen = %d %s", rec.Code, rec.Body.String())
	}
	lines := readOperatorActions(t, s)
	if len(lines) != 1 || lines[0].By != run.ID || lines[0].StampedBy != run.ID {
		t.Fatalf("audit lines = %+v", lines)
	}
}

// A refused reopen changes nothing, so it must not claim a decision either.
func TestRefusedReopenWritesNoAuditLine(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"操作员核对原始响应后确认"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator met = %d %s", rec.Code, rec.Body.String())
	}
	before := len(readOperatorActions(t, s))

	run, err := s.Store.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, commanderStampRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", "", run.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session reopen of an operator stamp = %d %s", rec.Code, rec.Body.String())
	}
	if after := len(readOperatorActions(t, s)); after != before {
		t.Fatalf("refused reopen wrote %d audit line(s): %+v", after-before, readOperatorActions(t, s)[before:])
	}
}

// An audit that cannot be written must be reported, not swallowed: the response
// says the stamp landed but the record did not, so the operator can see the
// asymmetry instead of trusting a silent success.
func TestAuditWriteFailureIsReportedNotSwallowed(t *testing.T) {
	s, mux, g := goalStampFixture(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.DataDir = blocked

	body := `{"evidence_id":"` + goalEvidenceID(t, s, g.ID) + `","note":"操作员核对原始响应后确认"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "操作员审计写入失败") {
		t.Fatalf("met with an unwritable audit = %d %s", rec.Code, rec.Body.String())
	}
	if goal := goalByIDFromStore(t, s, g.ID); !goal.Met {
		t.Fatal("the stamp should still be applied: the audit failed after the ledger write")
	}
}

func readOperatorActions(t *testing.T, s *Server) []operatorAction {	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.DataDir, operatorActionsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read operator actions: %v", err)
	}
	var out []operatorAction
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var a operatorAction
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("decode operator action %q: %v", line, err)
		}
		out = append(out, a)
	}
	return out
}
