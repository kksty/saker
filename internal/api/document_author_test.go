package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

// documentHistoryRow is the wire shape the console reads for a history row: the
// same field names as the document card, because the console formats both with
// revisionLine.
type documentHistoryRow struct {
	Revision  uint64 `json:"revision"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
	Text      string `json:"text"`
}

// An audit column has to tell a person's write from a model's. The console
// credential is shared, so the ledger records the operator for a console save and
// the run id for a commander round that acted on an operator instruction — and
// the history endpoint must expose it under the names the console reads.
func TestDocumentHistorySignsOperatorAndCommanderWrites(t *testing.T) {
	s, p := newBindingServer(t)

	if rec := call(t, s, http.MethodPut, "/api/documents/scope", map[string]any{
		"text": "10.0.0.0/24", "revision": 0,
	}); rec.Code != http.StatusOK {
		t.Fatalf("operator save = %d %s", rec.Code, rec.Body.String())
	}
	rows := documentHistory(t, s, "scope")
	if len(rows) != 1 || rows[0].UpdatedBy != collab.OperatorID {
		t.Fatalf("operator save signed as %+v, want operator", rows)
	}
	if strings.TrimSpace(rows[0].UpdatedAt) == "" || strings.HasPrefix(rows[0].UpdatedAt, "0001-01-01") {
		t.Fatalf("operator save has no recorded time: %+v", rows[0])
	}

	// A commander round may edit the goal statement only while citing a live
	// operator instruction, and the ledger must name that run, not the operator.
	run, _ := s.Store.AddRun(model.Run{Purpose: "commander"})
	source, _, _ := s.Store.AddMessage(model.Message{
		FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID,
		Text: "把终点改短", Durable: true,
	})
	g, err := p.Store.AddGoal(model.Goal{Statement: "旧终点", UpdatedBy: collab.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"statement": "新终点", "operator_message_id": source.ID, "revision": s.Store.GoalRevision(g.ID),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/update", bytes.NewReader(body))
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ParticipantID: collab.CommanderID, RunID: run.ID}))
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commander goal edit = %d %s", rec.Code, rec.Body.String())
	}
	if got := s.goalByID(g.ID); got.UpdatedBy != run.ID {
		t.Fatalf("commander edit signed as %q, want the run id %q", got.UpdatedBy, run.ID)
	}

	goalRows := documentHistory(t, s, "goal")
	if len(goalRows) < 2 {
		t.Fatalf("goal history rows = %d, want the seed revision and the edit", len(goalRows))
	}
	// Newest first: the commander's edit, then the operator's original statement.
	if goalRows[0].UpdatedBy != run.ID || goalRows[0].Text != "新终点" {
		t.Fatalf("commander revision = %+v, want run %s", goalRows[0], run.ID)
	}
	if goalRows[1].UpdatedBy != collab.OperatorID || goalRows[1].Text != "旧终点" {
		t.Fatalf("operator revision = %+v, want operator", goalRows[1])
	}

	// The operator editing the same goal afterwards is signed as the operator
	// again: the column follows the writer, not the goal's history.
	rev := s.Store.GoalRevision(g.ID)
	if rec := call(t, s, http.MethodPost, "/api/goals/"+g.ID+"/update", map[string]any{
		"statement": "操作员改回", "revision": rev,
	}); rec.Code != http.StatusOK {
		t.Fatalf("operator goal edit = %d %s", rec.Code, rec.Body.String())
	}
	if got := s.goalByID(g.ID); got.UpdatedBy != collab.OperatorID {
		t.Fatalf("operator edit signed as %q, want operator", got.UpdatedBy)
	}
}

// A write that cannot be attributed is refused, not recorded as the operator's:
// a session identity without a run, or a bare commander claim, has no name to
// put in the column.
func TestDocumentAuthorRefusesUnattributableWriters(t *testing.T) {
	req := func(id *collab.Identity, actor string) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/api/documents/scope", nil)
		if id != nil {
			r = r.WithContext(WithIdentity(r.Context(), *id))
		}
		if actor != "" {
			r.Header.Set("X-Tw-Actor", actor)
		}
		return r
	}
	if got := documentAuthor(req(nil, "")); got != collab.OperatorID {
		t.Fatalf("operator request signed as %q", got)
	}
	if got := documentAuthor(req(&collab.Identity{ParticipantID: "pt_exec"}, "")); got != "" {
		t.Fatalf("a session without a run was signed as %q", got)
	}
	if got := documentAuthor(req(nil, collab.CommanderID)); got != "" {
		t.Fatalf("an unattributed commander claim was signed as %q", got)
	}
	if got := documentAuthor(req(&collab.Identity{ParticipantID: collab.CommanderID, RunID: "r_cmd"}, "")); got != "r_cmd" {
		t.Fatalf("a commander round signed as %q", got)
	}
}

func documentHistory(t *testing.T, s *Server, kind string) []documentHistoryRow {
	t.Helper()
	rec := call(t, s, http.MethodGet, "/api/documents/"+kind+"/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history %s = %d %s", kind, rec.Code, rec.Body.String())
	}
	var out struct {
		Revisions []documentHistoryRow `json:"revisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Revisions
}
