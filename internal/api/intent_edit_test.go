package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

func editBody(version any, fields map[string]any) map[string]any {
	body := map[string]any{"version": version}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// The operator can fix the text of a direction that has never been dispatched.
// The response and the stored row carry the new version and editor; stale
// editors get a conflict with the current row, not a silent overwrite.
func TestEditIntentPlanAPIRejectsStaleAndInvalidEdits(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "旧标题", Description: "旧说明", Eval: "旧验收"})
	if err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodPatch, "/api/intents/"+in.ID, editBody(0, map[string]any{
		"title": "新标题", "eval": "新验收",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Intent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "新标题" || got.Eval != "新验收" || got.Description != "旧说明" {
		t.Fatalf("edited fields = %+v", got)
	}
	if got.EditVersion != 1 || got.EditedBy != collab.OperatorID {
		t.Fatalf("edit provenance = v%d by %q", got.EditVersion, got.EditedBy)
	}

	rec = call(t, s, http.MethodPatch, "/api/intents/"+in.ID, editBody(0, map[string]any{"eval": "覆盖"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale edit = %d %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Code   string       `json:"code"`
		Intent model.Intent `json:"intent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Code != "intent_edit_conflict" || conflict.Intent.EditVersion != 1 || conflict.Intent.Eval != "新验收" {
		t.Fatalf("conflict body = %+v", conflict)
	}

	if rec := call(t, s, http.MethodPatch, "/api/intents/"+in.ID, map[string]any{"title": "没有版本"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing version = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPatch, "/api/intents/"+in.ID, map[string]any{"version": 1}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no fields = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPatch, "/api/intents/"+in.ID, editBody(1, map[string]any{"title": "  "})); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty title = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPatch, "/api/intents/in_missing", editBody(0, map[string]any{"eval": "x"})); rec.Code != http.StatusNotFound {
		t.Fatalf("missing intent = %d %s", rec.Code, rec.Body.String())
	}
}

// Once a run holds the direction, the text is history: the API says so with a
// stable code and the current row, and points nowhere near a silent rewrite.
func TestEditIntentPlanAPIRejectsARunningDirection(t *testing.T) {
	s, _, in := newSteerServer(t)

	rec := call(t, s, http.MethodPatch, "/api/intents/"+in.ID, editBody(0, map[string]any{"eval": "运行中改写"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("running edit = %d %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Code != "intent_not_editable" {
		t.Fatalf("running edit code = %q, want intent_not_editable", conflict.Code)
	}
}

// The console attributes a command to the commander through the gate-stamped
// header; the edit records the same identity, not a blanket "operator".
func TestEditIntentPlanRecordsTheCommanderAsEditor(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "旧标题"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/intents/"+in.ID, strings.NewReader(`{"version":0,"eval":"指挥官改"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Saker-Actor", collab.CommanderID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commander edit = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Intent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.EditedBy != collab.CommanderID || got.EditVersion != 1 {
		t.Fatalf("commander edit provenance = v%d by %q", got.EditVersion, got.EditedBy)
	}
}
