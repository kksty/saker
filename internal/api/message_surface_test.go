package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A receipt before delivery would remove a still-queued message from the inbox
// while the sender is told it was read.
func TestAckOfQueuedMessageIsConflict(t *testing.T) {
	f := newAgentFixture(t)
	m, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: f.srcID.ParticipantID, ToParticipantID: f.tgtID.ParticipantID, Text: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.tgtID, "POST", "/api/messages/"+m.ID+"/ack", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("ack of a queued message = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got, _ := f.p.Store.MessageByID(m.ID); got.Status != model.MessageQueued {
		t.Fatalf("refused ack changed the row: %+v", got)
	}
	if pending := f.p.Store.PendingFor(f.tgtID.ParticipantID); len(pending) != 1 {
		t.Fatalf("refused ack took the message out of the inbox: %+v", pending)
	}
}

// box used to match neither branch and therefore return both boxes; a caller
// that asked for one box silently received the other too.
func TestMessageListRejectsUnknownBox(t *testing.T) {
	f := newAgentFixture(t)
	if rec := f.call(t, f.srcID, "GET", "/api/messages?box=bogus", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("participant list box=bogus = %d %s, want 400", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	f.srv.listMessages(rec, httptest.NewRequest(http.MethodGet, "/api/messages?box=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("console list box=bogus = %d %s, want 400", rec.Code, rec.Body.String())
	}
	// The valid values keep working.
	rec = httptest.NewRecorder()
	f.srv.listMessages(rec, httptest.NewRequest(http.MethodGet, "/api/messages?box=sent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("console list box=sent = %d %s", rec.Code, rec.Body.String())
	}
}

// A truncated console list must say so, otherwise 3,000 messages look like 500.
func TestConsoleMessageListReportsTotalAndTruncation(t *testing.T) {
	f := newAgentFixture(t)
	for i := 0; i < 3; i++ {
		if _, _, err := f.p.Store.AddMessage(model.Message{
			FromParticipantID: f.srcID.ParticipantID, ToParticipantID: f.tgtID.ParticipantID, Text: "hello",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	f.srv.listMessages(rec, httptest.NewRequest(http.MethodGet, "/api/messages?limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages  []model.Message `json:"messages"`
		Total     int             `json:"total"`
		Truncated bool            `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Total != 3 || !got.Truncated {
		t.Fatalf("page=%d total=%d truncated=%v, want 2/3/true", len(got.Messages), got.Total, got.Truncated)
	}
}

// The [重投] marker is what keeps an at-least-once redelivery from reading as a
// brand-new instruction. Prove it reaches the text the executor's prompt
// actually carries (inboxSection claims the row and renders it), not just that
// the store bumped a counter.
func TestRestartRedeliveryTextCarriesTheMarker(t *testing.T) {
	f := newAgentFixture(t)
	m, _, err := f.p.Store.AddMessage(model.Message{
		FromParticipantID: f.srcID.ParticipantID, ToParticipantID: f.tgtID.ParticipantID,
		Kind: model.MessageQuestion, Text: "检查这台主机",
	})
	if err != nil {
		t.Fatal(err)
	}
	// One turn claimed the row; the process then died before the transport
	// outcome was known. Recovery puts the row back on the queue.
	if _, ok, err := f.p.Store.BeginMessageDelivery(m.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if err := f.p.Store.RecoverMessageDeliveries(); err != nil {
		t.Fatal(err)
	}
	section := f.srv.inboxSection(f.tgtID.ParticipantID, "delivery-restart", "r_after_restart", "", 0)
	for _, want := range []string{"[重投]", "message_id=" + m.ID, "检查这台主机"} {
		if !strings.Contains(section, want) {
			t.Fatalf("redelivered inbox section lacks %q:\n%s", want, section)
		}
	}
	// The claim moved the row forward: it is at-least-once, not every-turn.
	if pending := f.p.Store.PendingFor(f.tgtID.ParticipantID); len(pending) != 0 {
		t.Fatalf("redelivered row stayed queued after the next prompt: %+v", pending)
	}
	// A first delivery must not carry the marker, or every message would look
	// like a duplicate.
	if first := formatInboxMessage(m); strings.Contains(first, "[重投]") {
		t.Fatalf("first delivery already marked as a redelivery: %q", first)
	}
}

// Filtering must happen while paging, not on the newest rows afterwards: A,B,A
// with from=A&limit=2 used to show one A (the older one fell below the cut) and
// a total of 3 that the returned rows could never reach.
func TestConsoleMessageListFiltersBeforeTruncation(t *testing.T) {
	f := newAgentFixture(t)
	a, b := f.srcID.ParticipantID, f.tgtID.ParticipantID
	for _, from := range []string{a, b, a} {
		to := b
		if from == b {
			to = a
		}
		if _, _, err := f.p.Store.AddMessage(model.Message{FromParticipantID: from, ToParticipantID: to, Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	f.srv.listMessages(rec, httptest.NewRequest(http.MethodGet, "/api/messages?from="+a+"&limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages  []model.Message `json:"messages"`
		Total     int             `json:"total"`
		Truncated bool            `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Total != 2 || got.Truncated {
		t.Fatalf("filtered page=%d total=%d truncated=%v, want 2/2/false", len(got.Messages), got.Total, got.Truncated)
	}
	for _, m := range got.Messages {
		if m.FromParticipantID != a {
			t.Fatalf("filtered page carries another sender: %+v", m)
		}
	}
}

// The sender's retry after a transport timeout must put the message back on the
// queue; created=false alone left it as a dead letter nobody would deliver.
func TestRetryAfterTransportErrorRequeuesViaAPI(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	body := sendBody(f.tgtID.ParticipantID, "retry-1", "check the host")
	if rec := f.call(t, f.srcID, "POST", "/api/messages", body); rec.Code != http.StatusAccepted {
		t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
	}
	m := f.p.Store.Messages(10)[0]
	if _, err := f.p.Store.UpdateMessage(m.ID, func(x *model.Message) {
		x.Status = model.MessageUnknown
		x.Error = "宿主超时，结果未知"
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, "POST", "/api/messages", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Message  model.Message `json:"message"`
		Created  bool          `json:"created"`
		Requeued bool          `json:"requeued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Created {
		t.Fatal("retry created a second message")
	}
	if !reply.Requeued || reply.Message.Status != model.MessageQueued || reply.Message.Redeliveries != 1 {
		t.Fatalf("retry reply = %+v", reply)
	}
	if pending := f.p.Store.PendingFor(f.tgtID.ParticipantID); len(pending) != 1 {
		t.Fatalf("retry left the message out of the queue: %+v", pending)
	}
}
