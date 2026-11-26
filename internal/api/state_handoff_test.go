package api

// P0-3 negative tests for the handoff receipt and note-writer attribution.
//
// The receipt is the note row the ledger commits for one commander round
// (writer=commander, run=that round, version, SHA-256). These tests pin the
// failure modes the old real-world data showed: an operator or another identity
// writing the note must never satisfy a commander handoff, another round's
// receipt must not be borrowed, and an operator write that happens while a
// round is running must not be reported as that round's action.
//
// Positive counterparts live in notebook_test.go (round provenance), and the
// two-attempt release contract lives in decider/note_receipt_test.go; this file
// adds the API-level path and the operator-writer negative cases.

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
	"github.com/kksty/saker/internal/store"
)

type noteEnv struct {
	s   *Server
	ex  *commanderExec
	st  *store.Store
	mux *http.ServeMux
}

func newNoteEnv(t *testing.T) *noteEnv {
	t.Helper()
	s, ex, st := newCommanderServer(t)
	s.Collab = collab.NewRegistry(0)
	mux := http.NewServeMux()
	s.Register(mux)
	return &noteEnv{s: s, ex: ex, st: st, mux: mux}
}

// callNote issues one /api/state request with an optional caller identity.
func (e *noteEnv) callNote(method, body string, id *collab.Identity) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/state", strings.NewReader(body))
	if id != nil {
		r = r.WithContext(WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	return rec
}

// callNoteAsHarness is the session surface without a resolvable credential: the
// gate marks it and the note endpoint must answer 404, not fall back to the
// operator writer.
func (e *noteEnv) callNoteAsHarness(body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/state", strings.NewReader(body))
	r = r.WithContext(WithAgentSurface(r.Context()))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	return rec
}

// An operator write during a live commander round is recorded as operator
// provenance: the round gets no receipt, and the write is not reported as the
// round's action. This is the residual "operator write counted as a round
// action" defect: action attribution is by writer/run, never by the action's
// text or its presence in the round's time window.
func TestOperatorNoteWriteDuringRoundIsNotACommanderAction(t *testing.T) {
	env := newNoteEnv(t)
	var currentRun string
	env.ex.during = func() {
		ident := *env.s.commanderIdent
		currentRun = ident.RunID
		// The operator token cannot name the live round as its own writer.
		body := fmt.Sprintf(`{"state":"operator claim","version":0,"run_id":%q}`, ident.RunID)
		if rec := env.callNote(http.MethodPut, body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("operator receipt with a run id = %d %s", rec.Code, rec.Body.String())
		}
		if rec := env.callNote(http.MethodPut, `{"state":"operator note","version":0}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("operator note = %d %s", rec.Code, rec.Body.String())
		}
	}
	res, actions, err := env.s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	runID := res.LedgerRunID
	if runID == "" || currentRun != runID {
		t.Fatalf("run = %q, current = %q", runID, currentRun)
	}
	for _, action := range actions {
		if action.Kind == "state" {
			t.Fatalf("operator write was reported as the commander round's action: %+v", actions)
		}
	}
	note, ok := env.st.Notebook()
	if !ok || note.Writer != "operator" || note.RunID != "" {
		t.Fatalf("note provenance = %+v", note)
	}
	run, ok := env.st.GetRun(runID)
	if !ok || run.Status != model.RunDone || len(run.NoteWrites) != 0 {
		t.Fatalf("round receipt = %+v", run)
	}
	if _, ok := env.st.ValidNoteHandoff(runID, 0); ok {
		t.Fatal("operator note satisfied the commander's handoff")
	}
	if !strings.Contains(strings.Join(run.Infos, "\n"), "操作员凭据改写笔记") {
		t.Fatalf("operator write during the round left no audit: %+v", run.Infos)
	}
}

// The command round's own write is the receipt: it is an action of that round
// and the only thing that can prove the handoff afterwards.
func TestCommanderNoteWriteIsTheRoundsReceipt(t *testing.T) {
	env := newNoteEnv(t)
	var currentRun string
	env.ex.during = func() {
		ident := *env.s.commanderIdent
		currentRun = ident.RunID
		body := fmt.Sprintf(`{"state":"本轮判断","version":0,"run_id":%q}`, ident.RunID)
		if rec := env.callNote(http.MethodPut, body, &ident); rec.Code != http.StatusOK {
			t.Fatalf("commander note = %d %s", rec.Code, rec.Body.String())
		}
	}
	res, actions, err := env.s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	if currentRun != res.LedgerRunID {
		t.Fatalf("run = %q, current = %q", res.LedgerRunID, currentRun)
	}
	if len(actions) != 1 || actions[0].Kind != "state" {
		t.Fatalf("commander write missing from the round's actions: %+v", actions)
	}
	run, _ := env.st.GetRun(res.LedgerRunID)
	if len(run.NoteWrites) != 1 || run.NoteWrites[0].RunID != res.LedgerRunID {
		t.Fatalf("round receipt = %+v", run.NoteWrites)
	}
	receipt, ok := env.st.ValidNoteHandoff(res.LedgerRunID, 0)
	if !ok || receipt.RunID != res.LedgerRunID || receipt.Writer != "commander" {
		t.Fatalf("valid receipt = %+v ok=%v", receipt, ok)
	}
}

// An operator overwrite after the commander's write changes the current note
// provenance, so the round's earlier receipt no longer proves the handoff. The
// receipt row stays in run.NoteWrites for audit, but the current version is not
// the commander's.
func TestOperatorOverwriteInvalidatesCommanderHandoff(t *testing.T) {
	env := newNoteEnv(t)
	env.ex.during = func() {
		ident := *env.s.commanderIdent
		body := fmt.Sprintf(`{"state":"commander note","version":0,"run_id":%q}`, ident.RunID)
		if rec := env.callNote(http.MethodPut, body, &ident); rec.Code != http.StatusOK {
			t.Fatalf("commander note = %d %s", rec.Code, rec.Body.String())
		}
		if rec := env.callNote(http.MethodPut, `{"state":"operator correction","version":1}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("operator correction = %d %s", rec.Code, rec.Body.String())
		}
	}
	res, _, err := env.s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := env.st.GetRun(res.LedgerRunID)
	if len(run.NoteWrites) != 1 {
		t.Fatalf("receipt audit = %+v", run.NoteWrites)
	}
	note, _ := env.st.Notebook()
	if note.Writer != "operator" || note.Version != 2 {
		t.Fatalf("current note = %+v", note)
	}
	if _, ok := env.st.ValidNoteHandoff(res.LedgerRunID, 0); ok {
		t.Fatal("operator overwrite still satisfied the commander's handoff")
	}
}

// Writer/identity forgery is rejected before any ledger write: executors get
// 404 on the note, a session surface without a credential gets 404, and the
// store refuses a commander writer that is not a live commander run.
func TestForgedNoteWriterIsRejected(t *testing.T) {
	env := newNoteEnv(t)

	worker := collab.Identity{ParticipantID: "pt_worker", IntentID: "in_1", RunID: "r_worker"}
	body := `{"state":"伪造","version":0,"run_id":"r_worker"}`
	if rec := env.callNote(http.MethodPut, body, &worker); rec.Code != http.StatusNotFound {
		t.Fatalf("executor note write = %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.callNoteAsHarness(body); rec.Code != http.StatusNotFound {
		t.Fatalf("credential-less session note write = %d %s", rec.Code, rec.Body.String())
	}

	// Store-level forgery: not every handler path goes through the gate, so the
	// writer/run contract must hold on its own.
	execRun, err := env.st.AddRun(model.Run{Purpose: "executor", Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.SaveNotebook(0, "forged", "commander", execRun.ID, nil); err == nil {
		t.Fatal("executor run accepted as the commander writer")
	}
	commanderRun, err := env.st.AddRun(model.Run{Purpose: "commander", CommanderRound: 1, Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.SaveNotebook(0, "forged", "operator", commanderRun.ID, nil); err == nil {
		t.Fatal("operator writer accepted a run binding")
	}
	if _, err := env.st.SaveNotebook(0, "forged", "intruder", "", nil); err == nil {
		t.Fatal("unknown writer accepted")
	}
	if _, err := env.st.SaveNotebook(0, "commander note", "commander", commanderRun.ID, nil); err != nil {
		t.Fatalf("legitimate commander writer rejected: %v", err)
	}
}

// A round cannot borrow another round's receipt, and a finished round's
// credential cannot write a new version: the version CAS and the live-run check
// both fail.
func TestOldRoundReceiptAndCredentialAreRejected(t *testing.T) {
	env := newNoteEnv(t)
	var firstIdent collab.Identity
	env.ex.during = func() {
		firstIdent = *env.s.commanderIdent
		body := fmt.Sprintf(`{"state":"first round","version":0,"run_id":%q}`, firstIdent.RunID)
		if rec := env.callNote(http.MethodPut, body, &firstIdent); rec.Code != http.StatusOK {
			t.Fatalf("first round note = %d %s", rec.Code, rec.Body.String())
		}
	}
	first, _, err := env.s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env.st.ValidNoteHandoff(first.LedgerRunID, 0); !ok {
		t.Fatal("first round's own receipt was not valid")
	}

	// Replaying the finished round's credential with the current version fails:
	// the run is no longer live, so the store's writer check rejects it.
	replay := fmt.Sprintf(`{"state":"replayed","version":1,"run_id":%q}`, firstIdent.RunID)
	if rec := env.callNote(http.MethodPut, replay, &firstIdent); rec.Code != http.StatusConflict {
		t.Fatalf("finished round wrote the note = %d %s", rec.Code, rec.Body.String())
	}

	// A second round writes its own version; the first round's receipt must not
	// be accepted as the second round's (different run, and its version is not
	// newer than what the second round read).
	var secondRun string
	env.ex.during = func() {
		secondIdent := *env.s.commanderIdent
		secondRun = secondIdent.RunID
		// A stale version (0, already superseded) must be rejected even for the
		// live round's own credential.
		stale := fmt.Sprintf(`{"state":"stale","version":0,"run_id":%q}`, secondIdent.RunID)
		if rec := env.callNote(http.MethodPut, stale, &secondIdent); rec.Code != http.StatusConflict {
			t.Fatalf("stale version = %d %s", rec.Code, rec.Body.String())
		}
		var read struct {
			RunID   string `json:"run_id"`
			Version uint64 `json:"version"`
		}
		rec := env.callNote(http.MethodGet, "", &secondIdent)
		if rec.Code != http.StatusOK {
			t.Fatalf("read note = %d %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
			t.Fatal(err)
		}
		if read.RunID != secondIdent.RunID || read.Version != 1 {
			t.Fatalf("read binding = %+v", read)
		}
		body := fmt.Sprintf(`{"state":"second round","version":%d,"run_id":%q}`, read.Version, read.RunID)
		if rec := env.callNote(http.MethodPut, body, &secondIdent); rec.Code != http.StatusOK {
			t.Fatalf("second round note = %d %s", rec.Code, rec.Body.String())
		}
	}
	second, _, err := env.s.StartCommander(context.Background(), withBoardAnchor("## 下一轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.LedgerRunID == first.LedgerRunID || secondRun != second.LedgerRunID {
		t.Fatalf("rounds = %q, %q", first.LedgerRunID, second.LedgerRunID)
	}
	if _, ok := env.st.ValidNoteHandoff(second.LedgerRunID, 1); !ok {
		t.Fatal("second round's own receipt was not valid")
	}
	if _, ok := env.st.ValidNoteHandoff(first.LedgerRunID, 0); ok {
		t.Fatal("an old round's receipt still proved a handoff after the next round wrote")
	}
}

// The two-attempt cap is the same contract at the API/runner boundary: a round
// that is supposed to hand off without a valid receipt counts a failure, the
// second failure releases the pressure, and no round ever fakes a receipt.
func TestHandoffAttemptCapReleasesWithoutFakeReceipt(t *testing.T) {
	env := newNoteEnv(t)
	r := decider.NewRunner(env.st, nil, "")
	r.ModelName, r.ContextWindow = "fixture", 128000
	var prompts []string
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		prompts = append(prompts, prompt)
		res, actions, err := env.s.StartCommander(ctx, withBoardAnchor(prompt), resume)
		// The harness context measurement is what arms the 85% line; the note
		// write itself stays real (none here: this test is the missing-receipt
		// path).
		return decider.RoundResult{RunID: res.LedgerRunID, Tokens: res.Tokens, Actions: actions,
			ContextTokens: 110000, ContextWindow: 128000}, err
	}
	first := r.Run(context.Background(), "first", true)
	second := r.Run(context.Background(), "handoff-1", true)
	third := r.Run(context.Background(), "handoff-2", true)
	if first == nil || second == nil || third == nil {
		t.Fatal("rounds did not run")
	}
	if first.Handoff {
		t.Fatalf("first round had no measured pressure yet: %+v", first)
	}
	if !second.Handoff || second.HandoffMissing != 1 || second.HandoffReceipt != nil || second.HandoffReleased {
		t.Fatalf("first failed handoff = %+v", second)
	}
	if !strings.Contains(prompts[1], "操作员明确请求的动作优先") {
		t.Fatal("handoff round lost operator-instruction priority")
	}
	if !third.Handoff || third.HandoffMissing != 2 || !third.HandoffReleased || third.HandoffReceipt != nil {
		t.Fatalf("second failed handoff = %+v", third)
	}
	if !strings.Contains(strings.Join(third.Notes, ","), "交接连续 2 次无有效回执") {
		t.Fatalf("release was not reported: %+v", third.Notes)
	}
	fourth := r.Run(context.Background(), "after", true)
	if fourth.Handoff || !fourth.HandoffReleased || fourth.HandoffReceipt != nil {
		t.Fatalf("pressure was not released truthfully: %+v", fourth)
	}
	if !strings.Contains(prompts[3], "不代表交接成功") || !strings.Contains(prompts[3], "不得改用操作员令牌") {
		t.Fatal("released round did not state the truthful recovery")
	}
	// No receipt may appear on any round or run.
	for _, run := range env.st.Runs() {
		if run.Purpose != "commander" {
			continue
		}
		if len(run.NoteWrites) != 0 {
			t.Fatalf("a missing-receipt round recorded a note write: %+v", run.NoteWrites)
		}
	}
	if note, ok := env.st.Notebook(); ok && note.Writer == "commander" {
		t.Fatalf("no round wrote a note, but the notebook claims a commander writer: %+v", note)
	}
}
