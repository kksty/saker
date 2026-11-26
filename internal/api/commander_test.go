package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/store"
)

type commanderExec struct {
	req       executor.Request
	runs      int
	resumable bool
	window    int
	during    func()
}

func (e *commanderExec) Name() string { return "commander-stub" }

func (e *commanderExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.runs++
	e.req = req
	if e.during != nil {
		e.during()
	}
	return executor.Result{ExitCode: 0, ContextWindow: e.window}, nil
}

func (e *commanderExec) CanResume(node, workDir string) bool { return e.resumable }

func newCommanderServer(t *testing.T) (*Server, *commanderExec, *store.Store) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	st, err := store.Open(filepath.Join(projectDir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &commanderExec{}
	s := New(st, ex, projectDir, filepath.Join(projectDir, "cases"))
	s.Project = &project.Project{Dir: projectDir, Manifest: project.Manifest{ID: "default"}}
	s.APIBase = "http://127.0.0.1:9000"
	return s, ex, st
}

// The operator's "continue current goal" resume attaches one open goal to the
// next commander round. It is consumed by that round, not stored as a second
// persistent instruction.
func TestStartCommanderInjectsOneShotResumeScope(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	s.setResumeScope(resumeScope{GoalID: "g_open", Statement: "继续打剩余候选"})
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.req.Prompt, "## 本轮继续的目标") || !strings.Contains(ex.req.Prompt, "g_open") {
		t.Fatalf("resume scope missing from the round prompt: %q", ex.req.Prompt)
	}
	// The resume note is bookkeeping, not a second policy channel. Lifecycle
	// rules live in the Skill/instructions; repeating them here is how two
	// copies drifted and blocked a legitimate blocked-standby.
	if !strings.Contains(ex.req.Prompt, "按当前 board、state 与 instructions 继续") {
		t.Fatalf("resume scope lost its pointer to the current inputs: %q", ex.req.Prompt)
	}
	if strings.Contains(ex.req.Prompt, "project/standby") || strings.Contains(ex.req.Prompt, "goal_achieved") || strings.Contains(ex.req.Prompt, "不要调用") {
		t.Fatalf("resume scope reintroduced lifecycle policy: %q", ex.req.Prompt)
	}
	if s.consumeResumeScope() != nil {
		t.Fatal("resume scope was not consumed by the round")
	}
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ex.req.Prompt, "## 本轮继续的目标") {
		t.Fatalf("resume scope leaked into the next round: %q", ex.req.Prompt)
	}
}

// A first commander turn must load the skill through harness and carry the
// control-plane coordinates; saker never pastes the skill body itself (D78).
func TestStartCommanderLoadsSkillOnFirstTurn(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ex.req.Prompt, "/skill:commander ") {
		t.Fatalf("prompt = %q", ex.req.Prompt)
	}
	for _, needle := range []string{"http://127.0.0.1:9000", "$SAKER_AUTH", "$SAKER_BATTLE", ".tw/auth", "## 本轮"} {
		if !strings.Contains(ex.req.Prompt, needle) {
			t.Fatalf("prompt missing %q: %q", needle, ex.req.Prompt)
		}
	}
	if ex.req.Resume {
		t.Fatal("a new session must not be marked resume")
	}
	if want := filepath.Join(filepath.Dir(s.CaseRoot), "commander"); ex.req.WorkDir != want {
		t.Fatalf("workdir = %q, want %q", ex.req.WorkDir, want)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].Purpose != "commander" || runs[0].Status != model.RunDone {
		t.Fatalf("runs = %+v", runs)
	}
	// The console compares this run's context against the same window the
	// handoff line is measured against (D80).
	if runs[0].ContextWindow != s.ModelContext() {
		t.Fatalf("context window = %d, want %d", runs[0].ContextWindow, s.ModelContext())
	}
}

// The harness resolves the model's real window and reports it with usage; the
// run records that value, not the profile fallback (D80).
func TestStartCommanderRecordsHarnessWindow(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	ex.window = 1000000
	s.SetContextWindow(128000)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false); err != nil {
		t.Fatal(err)
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].ContextWindow != 1000000 {
		t.Fatalf("runs = %+v, want the harness-reported 1000000", runs)
	}
}

// A session that still exists gets only the new round; the skill body is
// already in its context.
func TestStartCommanderResumesWithoutSkillPrefix(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	ex.resumable = true
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), true); err != nil {
		t.Fatal(err)
	}
	if !ex.req.Resume {
		t.Fatal("resume = false, want true")
	}
	if strings.HasPrefix(ex.req.Prompt, "/skill:commander") {
		t.Fatalf("resumed prompt re-loads the skill: %q", ex.req.Prompt)
	}
}

// The commander's skill is expanded into the session's first turn. Compaction is
// lossy, so the round right after a compacted turn re-loads it once — otherwise
// a long campaign runs on a skill nobody can prove is still in context.
func TestStartCommanderRelearnsSkillAfterCompaction(t *testing.T) {
	s, ex, st := newCommanderServer(t)
	ex.resumable = true
	if _, err := st.AddRun(model.Run{Purpose: "commander", Node: "local", Compacted: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ex.req.Prompt, "/skill:commander ") {
		t.Fatalf("round after compaction did not re-load the skill: %q", ex.req.Prompt)
	}
	// Once is enough: the next resumed round sees an uncompacted previous run and
	// does not pay for the skill body again.
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), true); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(ex.req.Prompt, "/skill:commander") {
		t.Fatalf("skill re-loaded twice: %q", ex.req.Prompt)
	}
}

// The commander's write endpoints go through the same rules as the decider's
// actions: a goal without evidence, a reason or a playbook without provenance
// are all refused (D55, D70, D78, D96).
func TestCommanderActionEndpointsShareTheRules(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mux := http.NewServeMux()
	s.Register(mux)

	g, err := st.AddGoal(model.Goal{Statement: "拿到 shell"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{"from":["f_missing"],"note":"范围已覆盖"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "目标依据") {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须写 note") {
		t.Fatalf("complete without a reason = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/playbooks/nominate",
		strings.NewReader(`{"title":"x","when":"y"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "已有事实") {
		t.Fatalf("nominate = %d %s", rec.Code, rec.Body.String())
	}
}

// The round's decision log is where the console reads back "why did it close":
// recording the goal statement there said what was closed, never why (D96).
func TestCompleteActionRecordsTheReason(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev := addEvidenceFile(t, st, s.CaseRoot, "in_1/p.txt", "p.txt", "text/plain", []byte("proof"))
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "已复核", Status: "unverified", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "拿到 shell"})
	if err != nil {
		t.Fatal(err)
	}
	mark := s.ActionMark()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/complete",
		strings.NewReader(`{"from":["`+f.ID+`"],"note":"复核通过：验证码可跳过，实测可反复试口令"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(mark)
	if len(acts) != 1 || acts[0].Kind != "complete" || acts[0].ID != g.ID {
		t.Fatalf("actions = %+v", acts)
	}
	if acts[0].Text != "复核通过：验证码可跳过，实测可反复试口令" {
		t.Fatalf("action text = %q, want the reason, not the statement", acts[0].Text)
	}
}

// The operator's manual pin carries the same receipt: a met goal the page
// cannot explain is exactly the state D96 forbids.
func TestOperatorPinRequiresReason(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev := addEvidenceFile(t, st, s.CaseRoot, "in_1/p.txt", "p.txt", "text/plain", []byte("proof"))
	support, err := st.AddFact(model.Fact{Kind: "vuln", Title: "支持", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: support.ID, Verdict: "pass", VerifierRun: "run-support"}); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "人工盖章"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须写理由") {
		t.Fatalf("met without a reason = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`","note":"操作员核对原始响应后确认"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("met = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Goal
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Met || got.Note != "操作员核对原始响应后确认" || got.CompletedAt == nil {
		t.Fatalf("goal = %+v", got)
	}
}

// Withdrawing a stamp is the operator's correction path: the goal leaves
// "已达成" and re-enters the open list with its receipt cleared; a goal that is
// already open is refused rather than reported as reopened (D98).
func TestOperatorReopenWithdrawsTheStamp(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Project = nil
	mux := http.NewServeMux()
	s.Register(mux)

	ev := addEvidenceFile(t, st, s.CaseRoot, "in_1/p.txt", "p.txt", "text/plain", []byte("proof"))
	support, err := st.AddFact(model.Fact{Kind: "vuln", Title: "支持", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: support.ID, Verdict: "pass", VerifierRun: "run-support"}); err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "人工盖章"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/met",
		strings.NewReader(`{"evidence_id":"`+ev.ID+`","note":"操作员核对原始响应后确认"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("met = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen = %d %s", rec.Code, rec.Body.String())
	}
	var got model.Goal
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Met || got.EvidenceID != "" || got.Note != "" || got.CompletedAt != nil {
		t.Fatalf("reopened goal = %+v", got)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/goals/"+g.ID+"/reopen", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "没有盖章") {
		t.Fatalf("second reopen = %d %s", rec.Code, rec.Body.String())
	}
}

// A round reports the mutations performed through the API while it ran, and
// takes them once so the next round does not inherit them (D78).
func TestStartCommanderReportsRoundActions(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	ex.during = func() { s.recordAction("intent", "in_1", "验证 /admin") }
	_, acts, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Kind != "intent" || acts[0].ID != "in_1" {
		t.Fatalf("actions = %+v", acts)
	}
	if got := s.ActionsSince(0); len(got) != 0 {
		t.Fatalf("actions replayed: %+v", got)
	}
}

func TestActionsSinceRoundTrip(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	mark := s.ActionMark()
	s.recordAction("intent", "in_1", "x")
	got := s.ActionsSince(mark)
	if len(got) != 1 {
		t.Fatalf("actions = %+v", got)
	}
}

// The handlers that mutate the ledger record what they did, so a round can
// report real actions instead of the model's own account (D78).
func TestMutatingEndpointsRecordActions(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	s.Project = nil // no node binding required for this path
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/intents",
		strings.NewReader(`{"title":"t","description":"d"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(0)
	if len(acts) != 1 || acts[0].Kind != "intent" || acts[0].ID == "" {
		t.Fatalf("actions = %+v", acts)
	}
}

// A handoff round writes state.md through the API. If that write is not in the
// round's actions, "did the 85% round actually hand off?" is unanswerable from
// decisions.jsonl (D80).
func TestStateWriteIsRecordedAsAnAction(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	mux := http.NewServeMux()
	s.Register(mux)
	mark := s.ActionMark()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/state",
		strings.NewReader(`{"state":"# 交接\n- 下一步：验 /admin","version":0}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put state = %d %s", rec.Code, rec.Body.String())
	}
	acts := s.ActionsSince(mark)
	if len(acts) != 1 || acts[0].Kind != "state" || !strings.Contains(acts[0].Text, "交接") {
		t.Fatalf("actions = %+v", acts)
	}
}

// The commander's credential is per session, not per round: the shell init
// exports SAKER_AUTH once, so a per-round rotation would leave a long-lived shell
// on a stale token. A replacement session revokes the old credential.
func TestCommanderSessionKeepsItsNoteIdentity(t *testing.T) {
	s, ex, _ := newCommanderServer(t)
	s.Collab = collab.NewRegistry(time.Hour)
	ex.resumable = true
	var tokens []string
	ex.during = func() {
		raw, err := os.ReadFile(identityPath(s.CommanderDir()))
		if err != nil {
			t.Errorf("commander identity file missing during the round: %v", err)
			return
		}
		var ident collab.Identity
		if err := json.Unmarshal(raw, &ident); err != nil {
			t.Errorf("identity file: %v", err)
			return
		}
		if ident.ParticipantID != collab.CommanderID || ident.RunID == "" {
			t.Errorf("identity = %+v", ident)
		}
		// The token is reused, but its run binding follows the live round: a
		// message or audit event sent through $SAKER_AGENT must name this round,
		// not the first one that ever received the session token.
		if runs := s.Store.Runs(); len(runs) == 0 || ident.RunID != runs[len(runs)-1].ID {
			t.Errorf("identity run = %q, live round = %+v", ident.RunID, runs)
		}
		if _, ok := s.Collab.Resolve(ident.Token); !ok {
			t.Errorf("commander token does not resolve: %+v", ident)
		}
		// `.tw/auth` is the shell-friendly form of the same token, and it must
		// match the identity file (a stale header would be worse than none).
		auth, err := os.ReadFile(authPath(s.CommanderDir()))
		if err != nil || strings.TrimSpace(string(auth)) != "Authorization: Bearer "+ident.Token {
			t.Errorf("auth file = %q, %v", auth, err)
		}
		tokens = append(tokens, ident.Token)
	}
	run := func(resume bool) {
		t.Helper()
		if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮\nboard"), resume); err != nil {
			t.Fatal(err)
		}
	}

	run(false) // new session
	run(true)  // same session: the credential is reused
	if len(tokens) != 2 || tokens[0] != tokens[1] {
		t.Fatalf("session token rotated between rounds: %v", tokens)
	}

	run(false) // replacement session: new credential, old one revoked
	if len(tokens) != 3 || tokens[2] == tokens[0] {
		t.Fatalf("replacement session did not get a new token: %v", tokens)
	}
	if _, ok := s.Collab.Resolve(tokens[0]); ok {
		t.Fatal("the replaced session's token survived")
	}
	if _, ok := s.Collab.Resolve(tokens[2]); !ok {
		t.Fatal("the replacement session's token does not resolve")
	}
	if !strings.Contains(ex.req.Prompt, "$SAKER_BATTLE") {
		t.Fatalf("prompt has no battlefield/note base: %q", ex.req.Prompt)
	}
}

// A commander round that cannot open its journal must not stay "running": the
// purpose-run registration and the row would both survive, so WaitIdle would
// never return and project deletion would answer 409 forever.
func TestCommanderRoundJournalFailureSettlesTheRun(t *testing.T) {
	s, _, st := newCommanderServer(t)
	// A file where the log directory belongs makes MkdirAll fail deterministically.
	if err := os.WriteFile(filepath.Join(s.DataDir, "runs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false); err == nil {
		t.Fatal("StartCommander succeeded without a journal")
	}
	runs := st.Runs()
	if len(runs) != 1 || runs[0].Status != model.RunFailed || runs[0].EndedAt == nil {
		t.Fatalf("failed commander round was not settled: %+v", runs)
	}
	if !s.WaitIdle(200 * time.Millisecond) {
		t.Fatal("the failed round still holds the project busy")
	}
}

// The collab registry is process-wide and outlives the project. A commander
// token that still resolves after this runtime stops is a second, fully
// privileged credential if the same project id is ever re-attached.
func TestBeginShutdownRevokesTheCommanderIdentity(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	s.Collab = collab.NewRegistry(time.Hour)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false); err != nil {
		t.Fatal(err)
	}
	s.commanderMu.Lock()
	ident := s.commanderIdent
	s.commanderMu.Unlock()
	if ident == nil {
		t.Fatal("commander identity was not issued")
	}
	if _, ok := s.Collab.Resolve(ident.Token); !ok {
		t.Fatal("commander identity does not resolve before shutdown")
	}
	s.BeginShutdown()
	if _, ok := s.Collab.Resolve(ident.Token); ok {
		t.Fatal("commander token survived project shutdown")
	}
	s.commanderMu.Lock()
	still := s.commanderIdent
	s.commanderMu.Unlock()
	if still != nil {
		t.Fatal("cached commander identity was not cleared")
	}
}
