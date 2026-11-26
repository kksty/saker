package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/state"
	"github.com/kksty/saker/internal/store"
)

func TestCommanderStateGatewayPreservesRoundReceipt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.AddRun(model.Run{ID: "r_session", Purpose: "commander", Status: model.RunRunning, CommanderRound: 1})
	if err != nil {
		t.Fatal(err)
	}
	registry := collab.NewRegistry(0)
	commander, err := registry.Issue(collab.Identity{ProjectID: "p1", ParticipantID: collab.CommanderID, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := registry.Issue(collab.Identity{ProjectID: "p1", ParticipantID: "pt_in_worker", IntentID: "in_worker", RunID: "r_worker"})
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Store: st, State: state.NewLedger(filepath.Join(t.TempDir(), "state.md"), st)}
	mux := http.NewServeMux()
	s.Register(mux)
	h := withConsoleAuth(http.StripPrefix("/p/p1", mux), "operator-token", registry)
	call := func(token, method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/p/p1/api/state", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	read := call(commander.Token, "GET", "")
	if read.Code != 200 {
		t.Fatalf("read: %d %s", read.Code, read.Body.String())
	}
	var v struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &v); err != nil || v.RunID != run.ID {
		t.Fatalf("identity: %s %v", read.Body.String(), err)
	}
	saved := call(commander.Token, "PUT", fmt.Sprintf(`{"version":0,"run_id":%q,"state":"judgement"}`, run.ID))
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ValidNoteHandoff(run.ID, 0); !ok {
		t.Fatal("gateway write did not produce valid handoff")
	}
	for _, method := range []string{"GET", "PUT"} {
		if rec := call(worker.Token, method, `{"version":1}`); rec.Code != 404 {
			t.Fatalf("worker %s: %d", method, rec.Code)
		}
	}
	if rec := call("operator-token", "PUT", `{"version":1,"state":"operator edit"}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if _, ok := st.ValidNoteHandoff(run.ID, 0); ok {
		t.Fatal("operator edit counted as handoff")
	}
}
