package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/saker/internal/collab"
)

// The three project documents are operator-owned (AGENTS.md L41). The route lives
// on the operator mux, but this pins the handler's own check: a participant
// identity or a commander-actor claim must not be able to write, and the store
// must not record the rejected write under the operator's name.
func TestDocumentWritesAreOperatorOnly(t *testing.T) {
	s, _ := newBindingServer(t)
	mux := http.NewServeMux()
	s.Register(mux)

	put := func(id *collab.Identity, actor, rev string) int {
		req := httptest.NewRequest(http.MethodPut, "/api/documents/scope", bytes.NewBufferString(`{"text":"被改写的起点","revision":`+rev+`}`))
		req.Header.Set("Content-Type", "application/json")
		if id != nil {
			req = req.WithContext(WithIdentity(req.Context(), *id))
		}
		if actor != "" {
			req.Header.Set("X-Saker-Actor", actor)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	if code := put(nil, "", "0"); code != http.StatusOK {
		t.Fatalf("operator write = %d, want 200", code)
	}
	// Use the current revision so the only thing that can stop these is the
	// identity gate, not the optimistic-concurrency check.
	if code := put(&collab.Identity{ParticipantID: "pt_exec", RunID: "r_x"}, "", "1"); code != http.StatusNotFound {
		t.Fatalf("participant write = %d, want 404", code)
	}
	if code := put(nil, collab.CommanderID, "1"); code != http.StatusNotFound {
		t.Fatalf("commander-actor write = %d, want 404", code)
	}
	if got := s.scopeText(); got != "被改写的起点" {
		t.Fatalf("a rejected write changed the document: %q", got)
	}
}
