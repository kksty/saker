package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A message sent straight to one direction is not an operator instruction to the
// commander: it becomes a ledger escalation row that the commander adjudicates.
// The row is the record — "did this get escalated" must be answerable from the
// ledger, not inferred from a receipt string.
//
// Falsification: delete the escalateDirectionDirective call and both the receipt
// field and the ledger row disappear.
func TestDirectionDirectiveEscalatesToCommanderInTheLedger(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "订阅判定", CreatedBy: collab.OperatorID})
	if err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "可以测真实 topic，不删改数据", "target": "intent", "intent_id": in.ID, "idempotency_key": "esc-1",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("directive = %d %s", rec.Code, rec.Body.String())
	}
	var out directiveResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Message == nil {
		t.Fatalf("no message in receipt: %+v", out)
	}
	if out.Escalation == nil || out.Escalation.IntentID != in.ID {
		t.Fatalf("receipt carries no escalation for %s: %+v", in.ID, out)
	}

	// Re-open the ledger rather than reading the running store's snapshot: the
	// claim is that the record was written, not that it is still in memory.
	reopened, rerr := store.OpenSQLite(p.Store.Path())
	if rerr != nil {
		t.Fatal(rerr)
	}
	defer reopened.Close()
	stored := reopened.Snapshot().Messages
	var esc *model.Message
	for i, m := range stored {
		if m.Kind == "escalation" {
			esc = &stored[i]
			break
		}
	}
	if esc == nil {
		t.Fatalf("no escalation row in the reopened ledger: %+v", stored)
	}
	if esc.ID != out.Escalation.ID {
		t.Fatalf("receipt escalation %s does not match the ledger row %s", out.Escalation.ID, esc.ID)
	}
	if esc.ToParticipantID != collab.CommanderID || !esc.Durable || esc.NoticeIntentID != in.ID {
		t.Fatalf("escalation row is not addressed to the commander: %+v", esc)
	}
	if esc.InReplyTo != out.Message.ID {
		t.Fatalf("escalation does not point at the original message: %+v", esc)
	}

	// The same directive key does not escalate twice.
	again := call(t, s, http.MethodPost, "/api/directives", map[string]any{
		"text": "可以测真实 topic，不删改数据", "target": "intent", "intent_id": in.ID, "idempotency_key": "esc-1",
	})
	if again.Code != http.StatusAccepted {
		t.Fatalf("idempotent retry = %d %s", again.Code, again.Body.String())
	}
	n := 0
	for _, m := range p.Store.Snapshot().Messages {
		if m.Kind == "escalation" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("escalation rows = %d, want 1", n)
	}
}
