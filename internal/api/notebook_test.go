package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/model"
)

func TestNotebookAuthenticatesWriterAndFencesOldRound(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	s.Collab = collab.NewRegistry(0)
	mux := http.NewServeMux()
	s.Register(mux)
	request := func(method, path, body string, identity *collab.Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if identity != nil {
			r = r.WithContext(WithIdentity(r.Context(), *identity))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	if rec := request("PUT", "/api/state", `{"state":"missing version"}`, nil); rec.Code != 400 {
		t.Fatalf("missing version: %d", rec.Code)
	}
	if rec := request("PUT", "/api/state", `{"state":"fake round","version":0,"run_id":"r_fake"}`, nil); rec.Code != 400 {
		t.Fatalf("spoofed operator run: %d", rec.Code)
	}
	worker := collab.Identity{ParticipantID: "pt_in_worker", RunID: "r_worker"}
	if rec := request("GET", "/api/state?version=1", "", &worker); rec.Code != 404 {
		t.Fatalf("worker read note: %d", rec.Code)
	}
	var firstRun string
	ex.resumable = true
	ex.during = func() {
		ident := *s.commanderIdent
		if firstRun == "" {
			firstRun = ident.RunID
		} else {
			body := fmt.Sprintf(`{"state":"late write","version":1,"run_id":%q}`, firstRun)
			if rec := request("PUT", "/api/state", body, &ident); rec.Code != 409 {
				t.Fatalf("old round write: %d %s", rec.Code, rec.Body.String())
			}
			return
		}
		var read struct {
			Version uint64 `json:"version"`
			RunID   string `json:"run_id"`
		}
		rec := request("GET", "/api/state", "", &ident)
		if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
			t.Fatal(err)
		}
		if read.RunID != ident.RunID || read.Version != 0 {
			t.Fatalf("wrong read binding: %+v", read)
		}
		body := fmt.Sprintf(`{"state":"本轮判断","version":0,"run_id":%q}`, read.RunID)
		rec = request("PUT", "/api/state", body, &ident)
		if rec.Code != 200 {
			t.Fatalf("write: %d %s", rec.Code, rec.Body.String())
		}
		if rec = request("PUT", "/api/state", body, &ident); rec.Code != 409 {
			t.Fatalf("repeated stale write: %d", rec.Code)
		}
	}
	res, _, err := s.StartCommander(context.Background(), withBoardAnchor("round"), false)
	if err != nil || res.LedgerRunID != firstRun {
		t.Fatalf("round: %+v %v", res, err)
	}
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("next"), true); err != nil {
		t.Fatal(err)
	}
	note, ok := st.Notebook()
	if !ok || note.Writer != "commander" || note.RunID != firstRun || note.Version != 1 {
		t.Fatalf("note provenance: %+v", note)
	}
	if rec := request("PUT", "/api/state", `{"state":"人工修正","version":1}`, nil); rec.Code != 200 {
		t.Fatalf("operator write: %s", rec.Body.String())
	}
	if rec := request("GET", "/api/state?version=1", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), firstRun) {
		t.Fatalf("historical receipt: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.ValidNoteHandoff(firstRun, 0); ok {
		t.Fatal("operator note counted as commander's handoff")
	}
}

func TestCommanderCannotWriteAnEmptyHandoffNote(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	s.Collab = collab.NewRegistry(0)
	mux := http.NewServeMux()
	s.Register(mux)
	var roundRun string
	call := func(body string, ident collab.Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/state", strings.NewReader(body))
		r = r.WithContext(WithIdentity(r.Context(), ident))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	ex.during = func() {
		ident := *s.commanderIdent
		roundRun = ident.RunID
		// A blank body is not a handoff: it carries no conclusion, lead or next
		// step and would erase the memory the round is supposed to preserve.
		for _, blank := range []string{`""`, `"  \n "`} {
			body := fmt.Sprintf(`{"state":%s,"version":0,"run_id":%q}`, blank, ident.RunID)
			if rec := call(body, ident); rec.Code != http.StatusBadRequest {
				t.Fatalf("blank handoff accepted (%s): %d %s", blank, rec.Code, rec.Body.String())
			}
		}
		if note, ok := st.Notebook(); ok {
			t.Fatalf("blank write created a note: %+v", note)
		}
		body := fmt.Sprintf(`{"state":"本轮判断","version":0,"run_id":%q}`, ident.RunID)
		if rec := call(body, ident); rec.Code != 200 {
			t.Fatalf("real write: %d %s", rec.Code, rec.Body.String())
		}
	}
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("round"), false); err != nil {
		t.Fatal(err)
	}
	note, ok := st.Notebook()
	if !ok || note.Version != 1 {
		t.Fatalf("note after the round: %+v", note)
	}
	if _, ok := st.ValidNoteHandoff(roundRun, 0); !ok {
		t.Fatal("a written body did not satisfy the handoff receipt")
	}
}

func TestCommanderHandoffUsesPersistedReceiptAndInputVersion(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	s.Collab = collab.NewRegistry(0)
	r := decider.NewRunner(st, s.State, "")
	r.ModelName, r.ContextWindow = "fixture", 128000
	s.Decider = r
	mux := http.NewServeMux()
	s.Register(mux)
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		res, actions, err := s.StartCommander(ctx, withBoardAnchor(prompt), resume)
		return decider.RoundResult{RunID: res.LedgerRunID, Tokens: res.Tokens, Actions: actions, ContextTokens: 110000}, err
	}
	ex.during = func() {
		if ex.runs != 2 {
			return
		}
		ident := *s.commanderIdent
		body := fmt.Sprintf(`{"state":"交接判断","version":0,"run_id":%q}`, ident.RunID)
		req := httptest.NewRequest("PUT", "/api/state", strings.NewReader(body))
		req = req.WithContext(WithIdentity(req.Context(), ident))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("write: %d %s", rec.Code, rec.Body.String())
		}
	}
	r.Run(context.Background(), "first", true)
	d := r.Run(context.Background(), "handoff", true)
	if d == nil || d.HandoffReceipt == nil || d.HandoffReceipt.Round != 2 || d.NoteRead.Version != 0 {
		t.Fatalf("handoff: %+v", d)
	}
	run, ok := st.GetRun(d.RunID)
	if !ok || run.NoteRead == nil || run.NoteRead.Version != 0 || len(run.NoteWrites) != 1 || run.Status != model.RunDone {
		t.Fatalf("run provenance: %+v", run)
	}
	d = r.Run(context.Background(), "after", true)
	if d.NoteRead.Version != 1 || !strings.Contains(ex.req.Prompt, "交接判断") {
		t.Fatal("following round did not consume receipt version")
	}
}
