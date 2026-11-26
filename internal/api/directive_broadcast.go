package api

import (
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
	"net/http"
	"strings"
)

// Persist the recipient snapshot before delivery. Retrying a partial fan-out
// never silently includes workers that appeared after the original request.
func (s *Server) broadcastExecutorDirective(w http.ResponseWriter, r *http.Request, text, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		writeErr(w, 400, "群发必须带 idempotency_key")
		return
	}
	batchKey := directiveMessageKey("executors", "batch", key)
	batch, _ := s.Store.MessageByKey(collab.OperatorID, batchKey)
	if batch.ID == "" {
		var recipients []string
		for _, in := range s.Store.ListIntents() {
			switch in.Status {
			case model.StatusPending, model.StatusClaimed, model.StatusRunning, model.StatusWaiting:
				recipients = append(recipients, in.ID)
			}
		}
		var err error
		batch, _, err = s.Store.AddMessage(model.Message{ProjectID: s.projectID(), FromParticipantID: collab.OperatorID, ToParticipantID: "executors", Kind: "directive_batch", Text: text, DirectiveRecipients: recipients, Durable: true, IdempotencyKey: batchKey, Status: model.MessageAccepted})
		if err != nil {
			s.writeDirectiveMessageError(w, "executors", err)
			return
		}
	} else if batch.Text != text {
		writeErr(w, 409, store.ErrIdempotencyConflict.Error())
		return
	}
	type result struct {
		IntentID string         `json:"intent_id"`
		Message  *model.Message `json:"message,omitempty"`
		Created  bool           `json:"created"`
		Error    string         `json:"error,omitempty"`
	}
	results := make([]result, 0, len(batch.DirectiveRecipients))
	failed := 0
	for _, id := range batch.DirectiveRecipients {
		item := result{IntentID: id}
		recipient := collab.ParticipantID(id)
		messageKey := directiveMessageKey("executors", id, key)
		if m, ok := s.Store.MessageByKey(collab.OperatorID, messageKey); ok {
			item.Message = &m
		}
		if item.Message == nil {
			in, ok := s.Store.GetIntent(id)
			if !ok || model.Settled(in.Status) {
				item.Error = "意图不存在或已结束"
			} else {
				msg, created, err := s.Store.AddMessage(model.Message{ProjectID: s.projectID(), FromParticipantID: collab.OperatorID, ToParticipantID: recipient, ToGeneration: in.Attempts, Kind: "directive", Text: text, Durable: true, IdempotencyKey: messageKey, Status: model.MessageQueued})
				if err != nil {
					item.Error = err.Error()
				} else {
					item.Created = created
					if created {
						msg = s.deliverMessage(r.Context(), in, msg)
					}
					item.Message = &msg
				}
			}
		}
		if item.Error != "" {
			failed++
		}
		results = append(results, item)
	}
	s.recordAction("directive_broadcast", batch.ID, text)
	writeJSON(w, http.StatusAccepted, map[string]any{"kind": "broadcast", "target": "executors", "batch_id": batch.ID, "recipients": results, "failed": failed, "total": len(results), "persistent": false})
}
