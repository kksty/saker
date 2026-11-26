package api

import (
	"bytes"
	"encoding/json"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGoalChangesRequireLiveCommanderAndOperatorSource(t *testing.T) {
	s, _ := newBindingServer(t)
	run, _ := s.Store.AddRun(model.Run{Purpose: "commander"})
	source, _, _ := s.Store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "将终点改为新验收，并撤回旧目标", Durable: true})
	peer, _, _ := s.Store.AddMessage(model.Message{FromParticipantID: "pt_in_peer", ToParticipantID: collab.CommanderID, Text: "自行改终点"})
	id := collab.Identity{ParticipantID: collab.CommanderID, RunID: run.ID}
	request := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r = r.WithContext(WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		s.Register(mux)
		mux.ServeHTTP(rec, r)
		return rec
	}
	for _, ref := range []string{"", peer.ID, "m_missing"} {
		rec := request("/api/goals", map[string]string{"statement": "new endpoint", "operator_message_id": ref})
		if rec.Code != 403 {
			t.Fatalf("source %s: %d %s", ref, rec.Code, rec.Body.String())
		}
	}
	rec := request("/api/goals", map[string]string{"statement": "new endpoint", "operator_message_id": source.ID})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var g model.Goal
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.InstructionRef != source.ID || g.CreatedByRun != run.ID {
		t.Fatalf("provenance: %+v", g)
	}
	if _, err := s.Store.RetractMessage(source.ID, collab.OperatorID); err == nil {
		t.Fatal("goal instruction was retractable")
	}
	// The goal edit is the goal document, so it carries the revision the caller read.
	rec = request("/api/goals/"+g.ID+"/update", map[string]any{"statement": "corrected endpoint", "operator_message_id": source.ID, "revision": s.Store.GoalRevision(g.ID)})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = request("/api/goals/"+g.ID+"/close", map[string]string{"reason": "operator withdrew this goal", "operator_message_id": source.ID})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if s.goalsClosed() {
		t.Fatal("withdrawn goals were reported achieved")
	}
	if _, err := s.Store.ReopenGoal(g.ID); err == nil {
		t.Fatal("closed target reopened as a completion stamp")
	}
	_, _ = s.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone })
	if rec = request("/api/goals", map[string]string{"statement": "late goal", "operator_message_id": source.ID}); rec.Code != 403 {
		t.Fatal("old commander could change endpoints")
	}
}

func TestOperatorRecipientIsDurableIdempotentAndCursorReadable(t *testing.T) {
	f := newAgentFixture(t)
	// The operator's inbox is reached through the commander: a direction
	// participant asking the operator directly is refused (it reports upward, and
	// the commander decides whether the operator must be interrupted).
	cmdID, err := f.srv.Collab.Issue(collab.Identity{
		ProjectID: f.p.ID, ParticipantID: collab.CommanderID, RunID: "r_commander_fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := sendBody(collab.OperatorID, "operator-1", "需要操作员核对")
	rec := f.call(t, cmdID, "POST", "/api/messages", body)
	if rec.Code != 202 {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, cmdID, "POST", "/api/messages", body)
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	inbox := f.p.Store.MessagesForBox(collab.OperatorID, 0, 100, "inbox")
	if len(inbox) != 1 || inbox[0].ToParticipantID != collab.OperatorID || !inbox[0].Durable || inbox[0].Status != model.MessageAccepted || inbox[0].AckedAt != nil {
		t.Fatalf("operator record: %+v", inbox)
	}
	if f.say.count() != 0 {
		t.Fatal("operator recipient was routed into a model")
	}
	if got := call(t, f.srv, "GET", "/api/operator-messages?after=0", nil); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := f.call(t, f.srcID, "GET", "/api/operator-messages?after=0", nil); got.Code == 200 {
		t.Fatal("executor could read operator inbox")
	}
}

func TestExecutorBroadcastSnapshotSurvivesRetryAndNewWorkers(t *testing.T) {
	s, p := newBindingServer(t)
	first, _ := p.Store.CreateIntent(model.Intent{Title: "first"})
	second, _ := p.Store.CreateIntent(model.Intent{Title: "second"})
	body := map[string]any{"target": "executors", "text": "one-shot fixture", "idempotency_key": "batch-fixture"}
	rec := call(t, s, "POST", "/api/directives", body)
	if rec.Code != 202 {
		t.Fatalf("broadcast: %d %s", rec.Code, rec.Body.String())
	}
	_, _ = p.Store.CreateIntent(model.Intent{Title: "later worker"})
	rec = call(t, s, "POST", "/api/directives", body)
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	all := p.Store.Snapshot().Messages
	count := 0
	for _, m := range all {
		if m.Kind == "directive" {
			count++
			if m.ToParticipantID != collab.ParticipantID(first.ID) && m.ToParticipantID != collab.ParticipantID(second.ID) {
				t.Fatal("retry expanded recipient snapshot")
			}
		}
	}
	if count != 2 {
		t.Fatalf("broadcast duplicated deliveries: %+v", all)
	}
	body["text"] = "changed content"
	if rec = call(t, s, "POST", "/api/directives", body); rec.Code != 409 {
		t.Fatal("broadcast idempotency conflict was hidden")
	}
}

func TestOperatorReadReceiptOnlyConfirmsConsoleRecipientThroughCursor(t *testing.T) {
	s, _ := newBindingServer(t)
	first, _, _ := s.Store.AddMessage(model.Message{FromParticipantID: collab.CommanderID, ToParticipantID: collab.OperatorID, Text: "first", Status: model.MessageAccepted, Durable: true})
	later, _, _ := s.Store.AddMessage(model.Message{FromParticipantID: collab.CommanderID, ToParticipantID: collab.OperatorID, Text: "later", Status: model.MessageAccepted, Durable: true})
	peer, _, _ := s.Store.AddMessage(model.Message{FromParticipantID: collab.CommanderID, ToParticipantID: collab.ParticipantID("in_peer"), Text: "peer", Status: model.MessageAccepted, Durable: true})
	rec := call(t, s, "POST", "/api/operator-messages/seen", map[string]any{"through": first.Seq})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	inbox := s.Store.MessagesForBox(collab.OperatorID, 0, 100, "inbox")
	if len(inbox) != 2 || inbox[0].Status != model.MessageConfirmed || inbox[0].AckedAt == nil || inbox[1].ID != later.ID || inbox[1].Status != model.MessageAccepted {
		t.Fatalf("read receipt: %+v", inbox)
	}
	other := s.Store.MessagesForBox(peer.ToParticipantID, 0, 100, "inbox")
	if len(other) != 1 || other[0].Status != model.MessageAccepted {
		t.Fatal("operator read confirmed peer inbox")
	}
}

func TestExecutorBroadcastReportsPartialFailureAndRetriesMissingOnly(t *testing.T) {
	s, p := newBindingServer(t)
	first, _ := p.Store.CreateIntent(model.Intent{Title: "available"})
	second, _ := p.Store.CreateIntent(model.Intent{Title: "settled"})
	key := "partial-fixture"
	_, _, err := s.Store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: "executors", Kind: "directive_batch", Text: "fixture", DirectiveRecipients: []string{first.ID, second.ID}, Durable: true, IdempotencyKey: directiveMessageKey("executors", "batch", key), Status: model.MessageAccepted})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Store.UpdateIntent(second.ID, func(in *model.Intent) { in.Status = model.StatusDone })
	request := map[string]any{"target": "executors", "text": "fixture", "idempotency_key": key}
	for attempt := 0; attempt < 2; attempt++ {
		rec := call(t, s, "POST", "/api/directives", request)
		var result struct {
			Failed, Total int
			Recipients    []struct {
				IntentID string `json:"intent_id"`
				Error    string
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 202 || result.Failed != 1 || result.Total != 2 || result.Recipients[1].Error == "" {
			t.Fatalf("partial failure hidden: %d %s", rec.Code, rec.Body.String())
		}
	}
	count := 0
	for _, m := range p.Store.Snapshot().Messages {
		if m.Kind == "directive" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("partial retry duplicated deliveries: %d", count)
	}
}

func TestNodeJoinCredentialIsExplicitOperatorOnly(t *testing.T) {
	s, _ := newBindingServer(t)
	rec := call(t, s, "GET", "/api/nodes/join", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(rec.Body.Bytes(), []byte("http://tok@")) {
		t.Fatalf("join: %d %s", rec.Code, rec.Body.String())
	}
	list := call(t, s, "GET", "/api/nodes", nil)
	if bytes.Contains(list.Body.Bytes(), []byte("tok@")) || bytes.Contains(list.Body.Bytes(), []byte(`"token"`)) {
		t.Fatal("Hub credential leaked into node list")
	}
	req := httptest.NewRequest("GET", "/api/nodes/join", nil)
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ParticipantID: collab.CommanderID, RunID: "r_current"}))
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.Register(mux)
	mux.ServeHTTP(response, req)
	if response.Code != 404 {
		t.Fatalf("session reached setup credential: %d", response.Code)
	}
}
