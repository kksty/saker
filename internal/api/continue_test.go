package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// finishedServer builds a direction that already ran once. The ledger rows are
// written directly, so the continue path can be tested without a model or a
// host; withMarker writes the AOP session pointer that makes continuation
// possible at all.
func finishedServer(t *testing.T, status, runStatus string, withMarker bool) (*Server, *blockingExec, model.Intent, model.Run) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	s.Scope = "10.0.0.0/24"

	in, err := st.CreateIntent(model.Intent{Title: "查 /admin 越权", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-time.Minute)
	run, err = st.UpdateRun(run.ID, func(r *model.Run) {
		r.Status = runStatus
		r.StopReason = "completed"
		r.LastState = "context 37%"
		r.Generation = 1
		r.ContextTokens = 32000
		r.ContextWindow = 128000
		r.EndedAt = &ended
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = status
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	if withMarker {
		writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))
	}
	t.Cleanup(func() {
		s.BeginShutdown()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	return s, ex, in, run
}

// continueHTTP posts to the console route, so the test also proves the endpoint
// is registered on the api mux.
func continueHTTP(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, "/api/intents/"+id+"/continue", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/intents/"+id+"/continue", strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func updateRunForTest(t *testing.T, s *Server, runID string, fn func(*model.Run)) {
	t.Helper()
	if _, err := s.Store.UpdateRun(runID, fn); err != nil {
		t.Fatal(err)
	}
}

func updateIntentForTest(t *testing.T, s *Server, id string, fn func(*model.Intent)) {
	t.Helper()
	if _, err := s.Store.UpdateIntent(id, fn); err != nil {
		t.Fatal(err)
	}
}

// A done direction can continue its own harness session: same trajectory, new
// generation, an incremental prompt, and no archived brief.
func TestContinueDoneIntentResumesTheSameSession(t *testing.T) {
	s, ex, in, prev := finishedServer(t, model.StatusDone, model.RunDone, true)

	runID, continuedFrom, err := s.continueIntentRun(in.ID, "把越权链补完整")
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if continuedFrom != prev.ID {
		t.Fatalf("continued_from = %q, want %q", continuedFrom, prev.ID)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	req := ex.requests()[0]
	if !req.Resume {
		t.Fatal("continue dispatched a fresh session")
	}
	if !strings.Contains(req.Prompt, "## 上一轮") || !strings.Contains(req.Prompt, "## 继续的方式") {
		t.Fatalf("continue prompt lacks the structured handover:\n%s", req.Prompt)
	}
	if !strings.Contains(req.Prompt, "操作员备注：把越权链补完整") {
		t.Fatalf("continue prompt lost the operator note:\n%s", req.Prompt)
	}
	if strings.Contains(req.Prompt, "完整任务书不再重发") {
		t.Fatalf("continue still tells the model the brief was not re-sent:\n%s", req.Prompt)
	}
	if !strings.HasPrefix(req.Prompt, continuationBriefHeader) {
		t.Fatalf("continue did not lead with the authoritative brief:\n%s", req.Prompt)
	}
	if !strings.Contains(req.Prompt, "## 交接（必做）") || !strings.Contains(req.Prompt, "## 简报") {
		t.Fatalf("continue did not carry the current brief:\n%s", req.Prompt)
	}

	run, ok := s.Store.GetRun(runID)
	if !ok {
		t.Fatal("continued run missing")
	}
	if run.ContinuedFrom != prev.ID || run.Generation != 2 {
		t.Fatalf("continued run = %+v", run)
	}
	if run.BriefPath == "" {
		t.Fatal("continue did not archive the brief it carried")
	}
	if _, err := os.Stat(filepath.Join(s.CaseRoot, filepath.FromSlash(run.BriefPath))); err != nil {
		t.Fatalf("continued run's archived brief missing: %v", err)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusRunning && got.Status != model.StatusClaimed {
		t.Fatalf("intent after continue = %+v", got)
	}
	if got.Hold {
		t.Fatalf("continue left the direction held: %+v", got)
	}
}

func TestStartAfterPauseOrRestartPrefersPreservedSession(t *testing.T) {
	for _, cause := range []string{model.CancelCausePause, model.CancelCauseRestart} {
		t.Run(cause, func(t *testing.T) {
			s, ex, in, prev := finishedServer(t, model.StatusPending, model.RunCanceled, true)
			s.Exec = nativeMarkerBlockingExec{ex}
			updateRunForTest(t, s, prev.ID, func(r *model.Run) { r.CancelCause = cause })
			runID, err := s.StartRun(in.ID)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
			if !ex.requests()[0].Resume {
				t.Fatal("administrative resume discarded session")
			}
			run, _ := s.Store.GetRun(runID)
			if run.ContinuedFrom != prev.ID {
				t.Fatal("resume ancestry missing")
			}
		})
	}
}

type nativeMarkerBlockingExec struct{ *blockingExec }

func (nativeMarkerBlockingExec) CanResume(_ string, workDir string) bool {
	return aopexec.CanResume(workDir)
}

// The console route answers 202 with the resume contract the dialog shows.
func TestContinueEndpointAcceptsFinishedDirection(t *testing.T) {
	s, ex, in, prev := finishedServer(t, model.StatusDone, model.RunDone, true)

	rec := continueHTTP(t, s, in.ID, `{"note":"补上被夹住的验证"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("continue: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		IntentID      string `json:"intent_id"`
		RunID         string `json:"run_id"`
		Resumed       bool   `json:"resumed"`
		ContinuedFrom string `json:"continued_from"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.IntentID != in.ID || !out.Resumed || out.ContinuedFrom != prev.ID || out.RunID == "" {
		t.Fatalf("continue response = %+v", out)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if !ex.requests()[0].Resume {
		t.Fatal("endpoint dispatched a fresh session")
	}
}

// A direction with no session pointer is refused explicitly: the caller must
// choose "retry on a new session", and nothing is started here.
func TestContinueWithoutSessionIsExplicit(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusDone, model.RunDone, false)

	_, _, err := s.continueIntentRun(in.ID, "")
	var ce *continueError
	if !errors.As(err, &ce) || ce.code != "continue_unavailable" {
		t.Fatalf("err = %v, want continue_unavailable", err)
	}
	if !strings.Contains(ce.Error(), "retry") {
		t.Fatalf("refusal does not name the alternative: %v", ce)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusDone || got.Hold {
		t.Fatalf("refused continue changed the direction: %+v", got)
	}
	if len(ex.requests()) != 0 {
		t.Fatal("refused continue dispatched a run")
	}

	rec := continueHTTP(t, s, in.ID, `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP continue = %d, want 409", rec.Code)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "continue_unavailable" {
		t.Fatalf("response code = %q", out.Code)
	}
}

// Live or queued directions have no finished turn to continue.
func TestContinueRefusesUnfinishedDirection(t *testing.T) {
	for _, status := range []string{model.StatusRunning, model.StatusPending, model.StatusWaiting} {
		t.Run(status, func(t *testing.T) {
			s, ex, in, _ := finishedServer(t, status, model.RunRunning, true)
			_, _, err := s.continueIntentRun(in.ID, "")
			var ce *continueError
			if !errors.As(err, &ce) || ce.code != "continue_conflict" {
				t.Fatalf("err = %v, want continue_conflict", err)
			}
			if len(ex.requests()) != 0 {
				t.Fatal("refused continue dispatched a run")
			}
		})
	}
}

// A failed direction that was held (cancel/kill) is continued only because the
// operator asked explicitly; the hold is cleared by that request.
func TestContinueFailedDirectionClearsHold(t *testing.T) {
	s, ex, in, prev := finishedServer(t, model.StatusFailed, model.RunFailed, true)
	updateIntentForTest(t, s, in.ID, func(i *model.Intent) { i.Hold = true })

	runID, _, err := s.continueIntentRun(in.ID, "")
	if err != nil {
		t.Fatalf("continue held direction: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	if !ex.requests()[0].Resume {
		t.Fatal("continue dispatched a fresh session")
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Hold {
		t.Fatalf("hold survived continue: %+v", got)
	}
	run, _ := s.Store.GetRun(runID)
	if run.ContinuedFrom != prev.ID || run.Generation != 2 {
		t.Fatalf("continued run = %+v", run)
	}
}

// Continuing a parked direction is the explicit un-park; the park reason goes
// away with the state that recorded it.
func TestContinueParkedDirectionClearsParkReason(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusParked, model.RunDone, true)
	updateIntentForTest(t, s, in.ID, func(i *model.Intent) {
		i.ParkReason = "需要内网出口"
		i.Hold = true
	})

	if _, _, err := s.continueIntentRun(in.ID, ""); err != nil {
		t.Fatalf("continue parked direction: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	got, _ := s.Store.GetIntent(in.ID)
	if got.ParkReason != "" || got.Hold {
		t.Fatalf("park state survived continue: %+v", got)
	}
}

// The note budget mirrors retry: a remark longer than 500 runes is a bad
// request, not a reason to start a run.
func TestContinueNoteTooLongIsRejected(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusDone, model.RunDone, true)

	_, _, err := s.continueIntentRun(in.ID, strings.Repeat("长", 501))
	var ce *continueError
	if !errors.As(err, &ce) || ce.code != "continue_invalid" {
		t.Fatalf("err = %v, want continue_invalid", err)
	}
	rec := continueHTTP(t, s, in.ID, `{"note":"`+strings.Repeat("长", 501)+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP continue = %d, want 400", rec.Code)
	}
	if len(ex.requests()) != 0 {
		t.Fatal("rejected continue dispatched a run")
	}
}

// After a harness compaction the session's early details are gone, so the
// continue prompt says so and injects the worker's own note whole.
func TestContinueAfterCompactionInjectsWorkerNote(t *testing.T) {
	s, ex, in, prev := finishedServer(t, model.StatusDone, model.RunDone, true)
	updateRunForTest(t, s, prev.ID, func(r *model.Run) {
		r.Compacted = true
		r.ContextTokens = 110000
		r.ContextWindow = 128000
	})
	note := "已确认：/admin 对普通用户返回 200，证据 raw/admin.json\n下一步：拿订单导出验证越权范围"
	if err := os.WriteFile(filepath.Join(s.CaseRoot, in.ID, ".tw", "worker-note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.continueIntentRun(in.ID, ""); err != nil {
		t.Fatalf("continue after compaction: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	prompt := ex.requests()[0].Prompt
	if strings.Contains(prompt, note) {
		t.Fatal("continued prompt contains free-text note")
	}
	for _, want := range []string{
		"上一轮峰值 110000/128000（85%）",
		"压缩过",
		"## 你的笔记（worker-note.md，来自上一次运行）",
		"正文由操作员审阅",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("compacted continue prompt missing %q:\n%s", want, prompt)
		}
	}
	// The continue prompt states the measured peak (above) but must not turn it
	// into a fixed policy: an executor cannot observe its own usage, so
	// "80%/85% → stop" is unactionable and was misread as a window limit. The
	// brief section repeats the harness's own compaction wording, so the check is
	// scoped to the continuation text the dispatcher appends.
	_, tail, cut := strings.Cut(prompt, "## 本轮续跑")
	if !cut {
		t.Fatalf("continue prompt has no continuation section:\n%s", prompt)
	}
	for _, banned := range []string{"上下文到 80%", "85% 之前", "压缩有损"} {
		if strings.Contains(tail, banned) {
			t.Fatalf("continue prompt hardcodes a context policy %q:\n%s", banned, tail)
		}
	}
	// A continue follows the direction's own previous turn; it must not claim
	// the run was recycled (that wording belongs to a fresh dispatch).
	if strings.Contains(prompt, "被回收") {
		t.Fatalf("continue prompt claimed a recycled run:\n%s", prompt)
	}
	// The current brief rides in front of the continuation: the session's copy is
	// history and must not outrank the ledger.
	if !strings.HasPrefix(prompt, continuationBriefHeader) || !strings.Contains(prompt, "## 交接（必做）") {
		t.Fatalf("compacted continue did not carry the current brief:\n%s", prompt)
	}
}

// The run row is the ledger's memory of compaction: a later continue reads it
// instead of guessing from the last_state prose.
func TestFinishRecordsHarnessCompaction(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &compactingExec{}, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	in, err := st.CreateIntent(model.Intent{Title: "长会话意图"})
	if err != nil {
		t.Fatal(err)
	}

	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		r, ok := s.Store.GetRun(runID)
		return ok && r.Status == model.RunDone
	})
	run, ok := s.Store.GetRun(runID)
	if !ok || !run.Compacted {
		t.Fatalf("run.compacted = %+v, want true", run)
	}
	if run.ContextTokens != 90000 || run.ContextWindow != 128000 {
		t.Fatalf("compaction settle lost the context pressure: %+v", run)
	}
}

// compactingExec stands in for a harness that compressed the session's context
// mid-turn and then finished normally.
type compactingExec struct{}

func (e *compactingExec) Name() string { return "compacting" }

func (e *compactingExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{
		ExitCode: 0, StopReason: "completed", Compacted: true,
		ContextTokens: 90000, ContextWindow: 128000,
	}, nil
}

// The console actor is recorded on the direction. It is provenance, not a
// permission boundary: both actors share the console credential.
func TestCreateIntentRecordsConsoleActor(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	mux := http.NewServeMux()
	s.Register(mux)

	post := func(actor string) model.Intent {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/intents",
			strings.NewReader(`{"title":"查 /admin","description":"只读"}`))
		if actor != "" {
			req.Header.Set("X-Tw-Actor", actor)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create intent: %d %s", rec.Code, rec.Body.String())
		}
		var in model.Intent
		if err := json.Unmarshal(rec.Body.Bytes(), &in); err != nil {
			t.Fatal(err)
		}
		return in
	}

	if in := post("commander"); in.CreatedBy != collab.CommanderID {
		t.Fatalf("commander-created intent created_by = %q", in.CreatedBy)
	}
	if in := post(""); in.CreatedBy != collab.OperatorID {
		t.Fatalf("operator-created intent created_by = %q", in.CreatedBy)
	}
}

// A new dispatch after a recycled run starts a session with no conversation
// memory: the note the worker left in the case is injected into the fresh brief.
func TestFreshDispatchInjectsRecoveredWorkerNote(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	in, err := st.CreateIntent(model.Intent{Title: "接着做被回收的意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	note := "已确认：目标只开了 443\n下一步：从 TLS 证书找内网域名"
	if err := os.MkdirAll(filepath.Join(s.CaseRoot, in.ID, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.CaseRoot, in.ID, ".tw", "worker-note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	prompt := ex.requests()[0].Prompt
	for _, want := range []string{
		"## 你的笔记（worker-note.md，来自上一次运行）",
		"正文由操作员审阅",
		"## 上下文与回收",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fresh dispatch prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The note reader is the boundary between the worker's file and the prompt: it
// tolerates a missing file and redacts known secrets. It must not cap the note:
// a byte budget silently cuts the tail — where the latest judgement and the next
// step live — and the worker is never told the budget while writing. Length is
// the worker's judgement, the same contract the commander note has (D106).
func TestReadWorkerNoteRedactsAndKeepsTheWholeNote(t *testing.T) {
	s, _, in, _ := finishedServer(t, model.StatusDone, model.RunDone, false)
	dir := filepath.Join(s.CaseRoot, in.ID)

	if got := s.readWorkerNote(filepath.Join(dir, "missing-case")); got != "" {
		t.Fatalf("missing note = %q, want empty", got)
	}
	s.Secrets = func() []string { return []string{"sk-live-0123456789"} }
	big := "secret=sk-live-0123456789\n" + strings.Repeat("甲", 20000) + "\n下一步：先验证越权范围" // gitleaks:allow -- deliberately fictional redaction fixture
	if err := os.MkdirAll(filepath.Join(dir, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tw", "worker-note.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	note := s.readWorkerNote(dir)
	if strings.Contains(note, "sk-live-0123456789") {
		t.Fatalf("note leaked a known secret:\n%s", note)
	}
	if !strings.Contains(note, "下一步：先验证越权范围") {
		t.Fatalf("the note tail was dropped: %d bytes", len(note))
	}
	if strings.Contains(note, "已截断") {
		t.Fatalf("a long note was clipped instead of injected whole: %d bytes", len(note))
	}
}

// Two continues arriving together must produce one run and leave the direction
// running. The loser's failure compensation used to write failed+Hold over the
// winner's live run; a failed intent is claimable, so a later retry could start
// a second harness session on the same case.
func TestConcurrentContinuesStartOneRunAndLeaveItRunning(t *testing.T) {
	s, ex, in, prev := finishedServer(t, model.StatusDone, model.RunDone, true)

	const attempts = 2
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			_, _, err := s.continueIntentRun(in.ID, "同时点两次续跑")
			errs <- err
		}()
	}
	wins := 0
	for i := 0; i < attempts; i++ {
		if err := <-errs; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful continues = %d, want exactly 1", wins)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	time.Sleep(100 * time.Millisecond) // let any second dispatch surface
	if got := len(ex.requests()); got != 1 {
		t.Fatalf("dispatched runs = %d, want 1", got)
	}
	cur, _ := s.Store.GetIntent(in.ID)
	if cur.Status != model.StatusRunning && cur.Status != model.StatusClaimed {
		t.Fatalf("continued direction = %s hold=%v, want running", cur.Status, cur.Hold)
	}
	if cur.Hold {
		t.Fatal("a losing continue held the direction under a live run")
	}
	if cur.RunID == prev.ID {
		t.Fatal("the direction still points at the finished run")
	}
}
