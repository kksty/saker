package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// postFirstTouch creates a no-premise direction from a harness session and
// returns its HTTP status plus the created intent.
func postFirstTouch(t *testing.T, s *Server, body string) (int, model.Intent) {
	t.Helper()
	req := harnessRequest(http.MethodPost, "/api/intents")
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := serveMux(t, s, req)
	var created model.Intent
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return rec.Code, created
}

// A no-premise direction is a genuine first touch: it is created unheld and runs,
// anchored to the project node in the graph. Holding it for operator
// confirmation used to be the patch for unverified facts reaching a decision;
// the decision surface now carries only verified facts, so there is nothing for
// the hold to protect — and it blocked authorized targets for no review reason.
func TestNoPremiseDirectionIsNotHeld(t *testing.T) {
	s, _ := newBindingServer(t)
	gate, err := s.Store.AddFact(model.Fact{Kind: "host", Title: "10.0.0.7", EntityRef: "host:10.0.0.7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.CreateIntent(model.Intent{Title: "waits on review", FromFactIDs: []string{gate.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"title":"root direction"}`,
		`{"title":"root direction","interest_entity_refs":["host:10.0.0.7"]}`,
		`{"title":"root direction","interest_entity_refs":["host:10.0.0.99"]}`,
	} {
		code, in := postFirstTouch(t, s, body)
		if code != http.StatusCreated || in.Hold || in.Bootstrap {
			t.Fatalf("first touch %s = %d %+v, want created and unheld", body, code, in)
		}
	}
}
