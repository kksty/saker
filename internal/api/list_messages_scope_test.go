package api

// The project-mux conversation route is reachable by a harness executor with its
// per-run credential (cmd/saker admits project reads for sessions and attaches the
// executor identity). It must narrow to that executor's own box, exactly like
// the participant surface (/agent/.../api/messages) does; the operator view is
// the only caller that may see every direction's conversation.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

func TestProjectMessagesRouteNarrowsToTheCaller(t *testing.T) {
	s, p := newBindingServer(t)
	first, err := p.Store.CreateIntent(model.Intent{Title: "甲方意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Store.CreateIntent(model.Intent{Title: "乙方意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := collab.ParticipantID(first.ID)
	secondID := collab.ParticipantID(second.ID)
	for _, m := range []model.Message{
		{FromParticipantID: collab.OperatorID, ToParticipantID: firstID, Text: "只发给甲方的提示"},
		{FromParticipantID: secondID, ToParticipantID: "pt_peer", Text: "乙方的私有消息"},
	} {
		if _, _, err := p.Store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	s.Register(mux)
	read := func(id *collab.Identity) []model.Message {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/messages?limit=50", nil)
		if id != nil {
			req = req.WithContext(WithIdentity(req.Context(), *id))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Messages
	}

	// An executor sees its own box, not the operator/other-direction conversation.
	executor := read(&collab.Identity{ProjectID: p.ID, ParticipantID: firstID, IntentID: first.ID, RunID: "r_first"})
	if len(executor) != 1 || executor[0].ToParticipantID != firstID {
		t.Fatalf("executor view = %+v", executor)
	}
	for _, m := range executor {
		if strings.Contains(m.Text, "乙方的私有消息") || m.FromParticipantID == secondID {
			t.Fatalf("executor read another direction's message: %+v", m)
		}
	}

	// The operator (no identity) keeps the whole-project view.
	if all := read(nil); len(all) != 2 {
		t.Fatalf("operator view = %+v", all)
	}
}
