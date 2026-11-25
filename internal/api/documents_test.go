package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type documentsBody struct {
	Documents map[string]documentView `json:"documents"`
}

func getDocuments(t *testing.T, s *Server) documentsBody {
	t.Helper()
	rec := call(t, s, http.MethodGet, "/api/documents", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET documents = %d %s", rec.Code, rec.Body.String())
	}
	var out documentsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The three documents are read, saved and versioned through one entry point, and
// a stale revision is a conflict rather than an overwrite.
func TestDocumentsSaveConflictsAndHistory(t *testing.T) {
	s, p := newBindingServer(t)
	s.SetProjectSettings("10.0.0.0/24", "不要删除或修改真实数据。")

	docs := getDocuments(t, s)
	if docs.Documents[store.DocumentScope].Text != "10.0.0.0/24" {
		t.Fatalf("scope document = %+v", docs.Documents[store.DocumentScope])
	}
	if docs.Documents[store.DocumentConstraints].Revision != 0 {
		t.Fatalf("a never-saved document must report revision 0: %+v", docs.Documents[store.DocumentConstraints])
	}

	rec := call(t, s, http.MethodPut, "/api/documents/scope", map[string]any{
		"text": "10.0.0.0/24, 192.168.1.0/24", "revision": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil || saved.Revision != 1 {
		t.Fatalf("save receipt = %+v, %v", saved, err)
	}
	if got := getDocuments(t, s).Documents[store.DocumentScope]; got.Text != "10.0.0.0/24, 192.168.1.0/24" || got.Revision != 1 {
		t.Fatalf("scope after save = %+v", got)
	}
	// The in-memory snapshot the prompts read followed the save.
	if s.scopeText() != "10.0.0.0/24, 192.168.1.0/24" {
		t.Fatalf("prompt snapshot did not follow the document: %q", s.scopeText())
	}

	// A second save with the old revision must not overwrite.
	stale := call(t, s, http.MethodPut, "/api/documents/scope", map[string]any{
		"text": "stale", "revision": 0,
	})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"document_conflict"`) {
		t.Fatalf("stale save = %d %s", stale.Code, stale.Body.String())
	}
	if !strings.Contains(stale.Body.String(), `"revision":1`) {
		t.Fatalf("conflict receipt lost the current revision: %s", stale.Body.String())
	}
	if got := getDocuments(t, s).Documents[store.DocumentScope]; got.Text == "stale" {
		t.Fatal("stale save overwrote the newer text")
	}

	// History is newest-first and carries both revisions.
	hist := call(t, s, http.MethodGet, "/api/documents/scope/history", nil)
	if hist.Code != http.StatusOK {
		t.Fatalf("history = %d %s", hist.Code, hist.Body.String())
	}
	var h struct {
		Revisions []documentView `json:"revisions"`
	}
	if err := json.Unmarshal(hist.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 1 || h.Revisions[0].Revision != 1 {
		t.Fatalf("history = %+v", h.Revisions)
	}
	// Unknown kinds are refused, not silently created.
	if rec := call(t, s, http.MethodPut, "/api/documents/whatever", map[string]any{"text": "x", "revision": 0}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind = %d", rec.Code)
	}
	_ = p
}

// The goal document is the goal object itself: saving it edits the statement of
// the current (earliest open) goal, and the goal editor must pass the same
// revision or it is refused.
func TestGoalDocumentIsTheGoalAndGoalEditorNeedsRevision(t *testing.T) {
	s, p := newBindingServer(t)
	if _, err := p.Store.AddGoal(model.Goal{Statement: "拿到管理员权限"}); err != nil {
		t.Fatal(err)
	}
	docs := getDocuments(t, s)
	goalDoc, ok := docs.Documents[store.DocumentGoal]
	if !ok || goalDoc.GoalID == "" {
		t.Fatalf("no editable goal document: %+v", docs.Documents)
	}
	if goalDoc.Revision == 0 {
		t.Fatalf("goal document revision must be the goal row's version, got %+v", goalDoc)
	}

	rec := call(t, s, http.MethodPut, "/api/documents/goal", map[string]any{
		"text": "拿到数据库读写权限", "revision": goalDoc.Revision, "goal_id": goalDoc.GoalID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save goal = %d %s", rec.Code, rec.Body.String())
	}
	if cur := s.goalByID(goalDoc.GoalID); cur.Statement != "拿到数据库读写权限" {
		t.Fatalf("goal statement = %q", cur.Statement)
	}
	rev := goalDoc.Revision + 1

	// The goal editor path requires a revision now.
	noRev := call(t, s, http.MethodPost, "/api/goals/"+goalDoc.GoalID+"/update", map[string]any{"statement": "另一个目标"})
	if noRev.Code != http.StatusConflict || !strings.Contains(noRev.Body.String(), `"code":"document_conflict"`) {
		t.Fatalf("goal update without revision = %d %s", noRev.Code, noRev.Body.String())
	}
	okRev := call(t, s, http.MethodPost, "/api/goals/"+goalDoc.GoalID+"/update", map[string]any{
		"statement": "换成这个目标", "revision": rev,
	})
	if okRev.Code != http.StatusOK {
		t.Fatalf("goal update with revision = %d %s", okRev.Code, okRev.Body.String())
	}
	if got := s.goalByID(goalDoc.GoalID).Statement; got != "换成这个目标" {
		t.Fatalf("goal statement after edit = %q", got)
	}
	stale := call(t, s, http.MethodPost, "/api/goals/"+goalDoc.GoalID+"/update", map[string]any{
		"statement": "过期写入", "revision": rev,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale goal edit = %d %s", stale.Code, stale.Body.String())
	}

	// The goals list carries the same revision, so the goal dialog can submit it.
	list := call(t, s, http.MethodGet, "/api/goals", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"revision":`) {
		t.Fatalf("GET goals lost the revision: %d %s", list.Code, list.Body.String())
	}

	// The goal document's history is the goal row's version journal: after two
	// edits it must list at least the versions those edits produced.
	hist := call(t, s, http.MethodGet, "/api/documents/goal/history", nil)
	if hist.Code != http.StatusOK {
		t.Fatalf("goal history = %d %s", hist.Code, hist.Body.String())
	}
	var revisions struct {
		Revisions []struct {
			Revision uint64 `json:"revision"`
			Text     string `json:"text"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(hist.Body.Bytes(), &revisions); err != nil {
		t.Fatal(err)
	}
	if len(revisions.Revisions) < 2 {
		t.Fatalf("goal history lists %d revisions, want at least 2: %s", len(revisions.Revisions), hist.Body.String())
	}
	if revisions.Revisions[0].Revision <= revisions.Revisions[len(revisions.Revisions)-1].Revision {
		t.Fatalf("goal history is not newest-first: %+v", revisions.Revisions)
	}

	// Saving the same text again writes nothing and reports the stored revision,
	// not a phantom current+1.
	before := s.Store.GoalRevision(goalDoc.GoalID)
	same := call(t, s, http.MethodPut, "/api/documents/goal", map[string]any{
		"text": "换成这个目标", "revision": before, "goal_id": goalDoc.GoalID,
	})
	if same.Code != http.StatusOK || !strings.Contains(same.Body.String(), `"changed":false`) {
		t.Fatalf("same-text save = %d %s", same.Code, same.Body.String())
	}
	if after := s.Store.GoalRevision(goalDoc.GoalID); after != before {
		t.Fatalf("same-text save moved the revision: %d -> %d", before, after)
	}
	_ = p
}

// The commander round carries all three documents, each exactly once, in the
// order 约束 → 起点 → 目标. Mutation: change a heading or drop a section and this
// fails.
func TestCommanderRoundCarriesThreeDocumentsInOrder(t *testing.T) {
	s, p := newBindingServer(t)
	s.SetProjectSettings("10.0.0.0/24", "不要删除或修改真实数据。")
	if _, err := p.Store.AddGoal(model.Goal{Statement: "拿到管理员权限"}); err != nil {
		t.Fatal(err)
	}
	docs := s.CommanderDocs()
	for _, want := range []string{"## 约束\n不要删除或修改真实数据。", "## 起点\n10.0.0.0/24", "## 目标\n拿到管理员权限"} {
		if strings.Count(docs, want) != 1 {
			t.Fatalf("commander documents missing or duplicating %q:\n%s", want, docs)
		}
	}
	order := []string{"## 约束", "## 起点", "## 目标"}
	at := -1
	for _, h := range order {
		next := strings.Index(docs, h)
		if next <= at {
			t.Fatalf("section %q out of order:\n%s", h, docs)
		}
		at = next
	}
	prompt := decider.CommanderRoundPrompt(board.BuildFromDB(p.Store.Snapshot(), 0), "", docs, decider.ContextStatus{}, decider.RoundContext{})
	if strings.Count(prompt, docs) != 1 {
		t.Fatalf("commander round does not render the documents verbatim:\n%s", prompt)
	}
}

// Two goals can share a revision. A draft for the closed goal must not edit its replacement.
func TestGoalDocumentSaveIsBoundToTheDisplayedGoal(t *testing.T) {
	s, p := newBindingServer(t)
	first, err := p.Store.AddGoal(model.Goal{Statement: "First", CreatedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Store.AddGoal(model.Goal{Statement: "Second", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	doc := getDocuments(t, s).Documents[store.DocumentGoal]
	if doc.GoalID != first.ID || doc.Revision != p.Store.GoalRevision(second.ID) {
		t.Fatalf("unexpected fixture: %+v", doc)
	}
	if _, err := p.Store.CloseGoal(first.ID, "superseded", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPut, "/api/documents/goal", map[string]any{
		"text": "Draft for first", "revision": doc.Revision, "goal_id": doc.GoalID,
	})
	got := getDocuments(t, s).Documents[store.DocumentGoal]
	if rec.Code != http.StatusConflict || got.GoalID != second.ID || got.Text != second.Statement {
		t.Fatalf("stale save = %d, current goal = %+v", rec.Code, got)
	}
}
