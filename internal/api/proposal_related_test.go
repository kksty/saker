package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func relatedCandidates(t *testing.T, mux *http.ServeMux, proposalID string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proposals/"+proposalID+"/related", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("related = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func candidateByID(t *testing.T, out map[string]any, id string) map[string]any {
	t.Helper()
	rows, _ := out["candidates"].([]any)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if ok && row["intent_id"] == id {
			return row
		}
	}
	t.Fatalf("candidate %s not found in %v", id, rows)
	return nil
}

// A proposal that shares a recorded premise with an existing direction must be
// able to locate it even when the titles differ. Two directions on that premise
// keep their own acceptance criteria, and a parked one is still findable as
// reopenable. Reading the candidates has no effect on the proposal or admission.
func TestRelatedProposalIntentsFindsSharedPremiseAndKeepsCriteriaApart(t *testing.T) {
	_, st, mux := newBatchServer(t, &batchExec{})
	premise, err := st.AddFact(model.Fact{
		IntentID: "in_seed", Kind: "host", Title: "10.0.0.5 可登录",
		EntityRef: "host:10.0.0.5", Status: "verified", Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	parked, err := st.CreateIntent(model.Intent{Title: "拿下边界主机", Eval: "拿到管理员会话"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parked.ID, func(in *model.Intent) {
		in.FromFactIDs = []string{premise.ID}
		in.Status = model.StatusParked
		in.ParkReason = "等复核"
	}); err != nil {
		t.Fatal(err)
	}
	active, err := st.CreateIntent(model.Intent{Title: "读取订单数据", Eval: "导出全部订单"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(active.ID, func(in *model.Intent) {
		in.FromFactIDs = []string{premise.ID}
	}); err != nil {
		t.Fatal(err)
	}
	unrelated, err := st.CreateIntent(model.Intent{Title: "扫描整个 /24", Eval: "列出开放端口"})
	if err != nil {
		t.Fatal(err)
	}
	prop, _, err := st.CreateProposal(model.Proposal{
		ID: "pr_related", Title: "换个键重提同一件事", FromFactIDs: []string{premise.ID},
		FromParticipantID: "pt_proposer", IdempotencyKey: "related-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	out := relatedCandidates(t, mux, prop.ID)
	if out["note"] == "" {
		t.Fatal("the candidate list must state that it does not change admission")
	}
	rows, _ := out["candidates"].([]any)
	if len(rows) != 2 {
		t.Fatalf("candidates = %v, want the two directions sharing the premise", rows)
	}
	if got := candidateByID(t, out, parked.ID); got["eval"] != "拿到管理员会话" || got["parked"] != true || got["reopenable"] != true {
		t.Fatalf("parked candidate = %v; its acceptance and reopenability must stay visible", got)
	}
	if got := candidateByID(t, out, active.ID); got["eval"] != "导出全部订单" {
		t.Fatalf("active candidate = %v", got)
	}
	if rows, _ := out["candidates"].([]any); len(rows) == 3 {
		t.Fatalf("unrelated direction leaked into candidates: %v", rows)
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["intent_id"] == unrelated.ID {
			t.Fatalf("unrelated direction leaked into candidates: %v", row)
		}
	}
	for _, id := range []string{parked.ID, active.ID} {
		got := candidateByID(t, out, id)
		reasons, _ := got["reasons"].([]any)
		found := false
		for _, raw := range reasons {
			reason, _ := raw.(map[string]any)
			if reason["kind"] == "shared_premise" && reason["value"] == premise.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s reasons = %v, want shared premise %s", id, reasons, premise.ID)
		}
	}
	// Candidate lookup is read-only: the proposal is still undecided, and the
	// parked direction did not move.
	if p, _ := st.GetProposal(prop.ID); p.Status != model.ProposalProposed {
		t.Fatalf("related lookup changed the proposal: %+v", p)
	}
	if in, _ := st.GetIntent(parked.ID); in.Status != model.StatusParked {
		t.Fatalf("related lookup changed the parked direction: %+v", in)
	}
}

// Entity identity is the other recorded link: two directions can reference the
// same host without sharing a premise fact, and their different acceptance
// criteria stay next to them so the commander can judge independently.
func TestRelatedProposalIntentsFindsSharedEntity(t *testing.T) {
	_, st, mux := newBatchServer(t, &batchExec{})
	entityFact, err := st.AddFact(model.Fact{
		IntentID: "in_seed", Kind: "host", Title: "10.0.0.9 是边界",
		EntityRef: "host:10.0.0.9", Status: "verified", Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, eval := range []string{"拿到只读账号", "验证提权路径"} {
		in, err := st.CreateIntent(model.Intent{Title: "同一实体的不同验收 " + eval, Eval: eval})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateIntent(in.ID, func(row *model.Intent) {
			row.InterestEntityRefs = []string{"host:10.0.0.9"}
		}); err != nil {
			t.Fatal(err)
		}
	}
	prop, _, err := st.CreateProposal(model.Proposal{
		ID: "pr_entity", Title: "针对同一台主机的新提议", FromFactIDs: []string{entityFact.ID},
		FromParticipantID: "pt_proposer", IdempotencyKey: "related-entity",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := relatedCandidates(t, mux, prop.ID)
	rows, _ := out["candidates"].([]any)
	if len(rows) != 2 {
		t.Fatalf("entity candidates = %v, want two", rows)
	}
	evals := map[any]bool{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		evals[row["eval"]] = true
		reasons, _ := row["reasons"].([]any)
		if len(reasons) != 1 {
			t.Fatalf("reasons = %v", reasons)
		}
		reason, _ := reasons[0].(map[string]any)
		if reason["kind"] != "shared_entity" || reason["value"] != "host:10.0.0.9" {
			t.Fatalf("reason = %v", reason)
		}
	}
	if !evals["拿到只读账号"] || !evals["验证提权路径"] {
		t.Fatalf("acceptance criteria were collapsed: %v", evals)
	}
}
