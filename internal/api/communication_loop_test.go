package api

// Communication closed-loop tests (audit item 9): a request can be correlated
// with the participant that accepted it and the result that closes it, the
// injected inbox carries the quoted request (or says the quote is gone), and a
// direction that yields on a wait releases its slot and is resumed exactly once.
//
// These use the existing temporary project ledger and fake transports; no
// browser, harness, model or real project data is touched.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestMessageThreadEndpointCorrelatesAcceptanceAndResult(t *testing.T) {
	f := newAgentFixture(t)
	// The target is not live, so messages queue instead of racing a session; the
	// association is in the ledger, not in the transport outcome.
	send := func(id collab.Identity, to, kind, text, replyTo string) model.Message {
		t.Helper()
		body := map[string]any{"to": to, "kind": kind, "text": text}
		if replyTo != "" {
			body["in_reply_to"] = replyTo
		}
		rec := f.call(t, id, http.MethodPost, "/api/messages", body)
		if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
			t.Fatalf("send %s: %d %s", kind, rec.Code, rec.Body.String())
		}
		var resp struct {
			Message model.Message `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Message
	}
	srcTo, tgtTo := collab.ParticipantID(f.src.ID), collab.ParticipantID(f.target.ID)
	request := send(f.srcID, tgtTo, model.MessageQuestion, "请只读复核 /admin 是否越权", "")
	accept := send(f.tgtID, srcTo, "answer", "收到，先只读复核", request.ID)
	result := send(f.tgtID, srcTo, model.MessageResult, "复核完成：普通用户返回 200", accept.ID)

	// The requester reads the closed loop: who accepted, the quoted request, and
	// the result under the acceptance.
	rec := f.call(t, f.srcID, http.MethodGet, "/api/messages/"+request.ID+"/thread", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("thread: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Thread storeThread `json:"thread"`
		Note   string      `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Thread.AcceptedBy) != 1 || body.Thread.AcceptedBy[0] != tgtTo {
		t.Fatalf("accepted_by = %v", body.Thread.AcceptedBy)
	}
	if len(body.Thread.Replies) != 1 || body.Thread.Replies[0].Message.ID != accept.ID || !body.Thread.Replies[0].QuoteValid {
		t.Fatalf("replies = %+v", body.Thread.Replies)
	}
	if len(body.Thread.Results) != 1 || body.Thread.Results[0].Message.ID != result.ID {
		t.Fatalf("results = %+v", body.Thread.Results)
	}
	// Receipt and execution are reported as different facts.
	if body.Thread.Results[0].Read != "unknown" || body.Thread.Results[0].Executed != "unknown" {
		t.Fatalf("read/executed = %+v", body.Thread.Results[0])
	}
	if !strings.Contains(body.Note, "不证明") || !strings.Contains(body.Note, "未知投递不自动重发") {
		t.Fatalf("endpoint note lost the acceptance/execution contract: %q", body.Note)
	}

	// A participant outside the conversation sees 404, not the thread.
	outsider := collab.Identity{ParticipantID: collab.ParticipantID("in_outsider"), IntentID: "in_outsider", RunID: "r_out"}
	rec = f.call(t, outsider, http.MethodGet, "/api/messages/"+request.ID+"/thread", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider thread: %d %s", rec.Code, rec.Body.String())
	}
	// The operator surface may read it (the console has no participant id).
	if rec := call(t, f.srv, http.MethodGet, "/api/messages/"+request.ID+"/thread", nil); rec.Code != http.StatusOK {
		t.Fatalf("operator thread: %d %s", rec.Code, rec.Body.String())
	}
	// An unknown id is absence, not an empty thread.
	if rec := call(t, f.srv, http.MethodGet, "/api/messages/m_missing/thread", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing thread: %d %s", rec.Code, rec.Body.String())
	}
}

// storeThread is the wire shape of store.MessageCorrelation for the assertions
// above. Keeping it local avoids exporting test-only aliases from the package.
type storeThread struct {
	AcceptedBy []string                `json:"accepted_by"`
	Replies    []threadMessageEnvelope `json:"replies"`
	Results    []threadMessageEnvelope `json:"results"`
	Unanswered bool                    `json:"unanswered"`
}

type threadMessageEnvelope struct {
	Message    model.Message `json:"message"`
	Delivery   string        `json:"delivery"`
	Read       string        `json:"read"`
	Executed   string        `json:"executed"`
	QuoteValid bool          `json:"quote_valid"`
}

// A commander request and a subtask result are the same closed loop: the thread
// identifies the accepting direction and the result that closes it, and the
// participants involved (including the subtask) can read it.
func TestMessageThreadCoversCommanderAndSubtaskParticipants(t *testing.T) {
	f := newParentFixture(t)
	child, err := f.p.Store.CreateIntent(model.Intent{Title: "子意图", CreatedBy: f.ident.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	childIdentity, err := f.srv.Collab.Issue(collab.Identity{
		ProjectID: f.p.ID, ParticipantID: collab.ParticipantID(child.ID),
		IntentID: child.ID, RunID: "r_child", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ask, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: collab.CommanderID, ToParticipantID: f.ident.ParticipantID,
		Kind: model.MessageQuestion, Text: "这个意图能不能拆一个只读子意图？",
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: f.ident.ParticipantID, ToParticipantID: collab.CommanderID,
		Kind: "answer", Text: "可以，子意图已派出", InReplyTo: ask.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: collab.ParticipantID(child.ID), ToParticipantID: f.ident.ParticipantID,
		Kind: model.MessageResult, Text: "子意图完成，结论见 fact", InReplyTo: accept.ID,
		FactRefs: []string{"f_subtask"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The subtask itself, a participant in the conversation, may read the loop.
	rec := f.call(t, &childIdentity, http.MethodGet, "/api/messages/"+ask.ID+"/thread", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subtask thread: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Thread storeThread `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Thread.AcceptedBy) != 1 || body.Thread.AcceptedBy[0] != f.ident.ParticipantID {
		t.Fatalf("accepted_by = %v", body.Thread.AcceptedBy)
	}
	if len(body.Thread.Results) != 1 || body.Thread.Results[0].Message.ID != result.ID ||
		len(body.Thread.Results[0].Message.FactRefs) != 1 {
		t.Fatalf("subtask result association = %+v", body.Thread.Results)
	}
	// The operator/commander surface reads the same thread.
	if rec := call(t, f.srv, http.MethodGet, "/api/messages/"+ask.ID+"/thread", nil); rec.Code != http.StatusOK {
		t.Fatalf("operator thread: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInboxQuotesTheRequestAndMarksAMissingQuote(t *testing.T) {
	f := newAgentFixture(t)
	asker := collab.ParticipantID(f.src.ID)
	target := collab.ParticipantID(f.target.ID)
	request, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: asker, ToParticipantID: target,
		Kind: model.MessageQuestion, Text: "请核对 10.0.0.5:8080 的 /admin 是否可越权读订单",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The reply is what the asker's inbox carries: it quotes the request.
	reply, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: target, ToParticipantID: asker,
		Kind: "answer", Text: "收到，先只读复核", InReplyTo: request.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A reply whose quoted message is gone still says so instead of looking
	// anonymous.
	if _, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: target, ToParticipantID: asker,
		Kind: model.MessageUpdate, Text: "在吗", InReplyTo: "m_deleted",
	}); err != nil {
		t.Fatal(err)
	}

	inbox := f.srv.inboxSection(asker, "prompt:q1", "r_q1", "", 0)
	if !strings.Contains(inbox, request.ID) || !strings.Contains(inbox, "引用 "+request.ID) {
		t.Fatalf("inbox does not quote the request a reply answers:\n%s", inbox)
	}
	if !strings.Contains(inbox, "请核对 10.0.0.5:8080") {
		t.Fatalf("inbox quote does not carry the request excerpt:\n%s", inbox)
	}
	if !strings.Contains(inbox, reply.ID) {
		t.Fatalf("inbox lost the reply:\n%s", inbox)
	}
	if !strings.Contains(inbox, "已不存在") || !strings.Contains(inbox, "m_deleted") {
		t.Fatalf("a dangling quote was not marked missing:\n%s", inbox)
	}
}

// countingExec wraps the node registry so the test can observe concurrent runs
// while still answering node resolution like a real host.
type countingExec struct {
	nodeExec
	mu     sync.Mutex
	active int
	max    int
	runs   int
}

func (e *countingExec) Run(_ context.Context, _ executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.active++
	e.runs++
	if e.active > e.max {
		e.max = e.active
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}()
	time.Sleep(25 * time.Millisecond)
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *countingExec) peak() (active, max, runs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active, e.max, e.runs
}
