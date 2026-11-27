package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

// decodeActions reads the action contract out of a board response.
func decodeActions(t *testing.T, rec *httptest.ResponseRecorder) ProjectActions {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Actions *ProjectActions `json:"project_actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode board: %v", err)
	}
	if body.Actions == nil {
		t.Fatalf("operator board lost project_actions: %s", rec.Body.String())
	}
	return *body.Actions
}

func actionByID(t *testing.T, acts ProjectActions, id string) ProjectAction {
	t.Helper()
	for _, a := range acts.Actions {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("action %q missing from %+v", id, acts.Actions)
	return ProjectAction{}
}

// metGoal adds one goal that already carries a current completion receipt. The
// ledger only accepts a stamp that names an existing verified fact, so the
// fixture builds evidence → fact → review → pin instead of setting Met by hand.
func metGoal(t *testing.T, s *Server, statement string) model.Goal {
	t.Helper()
	e, err := s.Store.AddEvidence(model.Evidence{URI: "in_fixture/proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Store.AddFact(model.Fact{Kind: "vuln", Title: statement + " 的依据", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-fixture"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Store.AddGoal(model.Goal{Statement: statement, QuantifiedTarget: "至少一条已核验事实"})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.Store.PinGoal(g.ID, e.ID, "已核验", []string{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}

// A completion stand-by with a direction still open has to say both things:
// every goal is met, and there is work left to resume for. The old console
// derivation (paused + goal count) could only say one of them.
func TestProjectActionsMetGoalWithPendingWork(t *testing.T) {
	s, _ := newBindingServer(t)
	metGoal(t, s, "拿到管理员权限")
	if _, err := s.Store.CreateIntent(model.Intent{Title: "继续验证影响面"}); err != nil {
		t.Fatal(err)
	}
	s.AutoPause("目标已完成：项目自动暂停")

	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	if acts.State.Kind != pauseKindCompletionStandby || !acts.State.Paused {
		t.Fatalf("state = %+v, want a completion stand-by", acts.State)
	}
	if acts.Facts.GoalsTotal != 1 || acts.Facts.GoalsOpen != 0 || acts.Facts.Pending != 1 {
		t.Fatalf("facts = %+v, want 1 goal met and 1 pending direction", acts.Facts)
	}
	resume := actionByID(t, acts, actionResume)
	if !resume.Enabled || resume.ReasonCode != reasonPausedCompletion {
		t.Fatalf("resume = %+v, want an enabled completion resume", resume)
	}
	found := false
	for _, n := range acts.Notes {
		if n.Code == reasonGoalMetPendingWork && strings.Contains(n.Text, "待办意图") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %+v, want the two-sided goal-met-with-work note", acts.Notes)
	}
}

// The end of the engagement: nothing chartered is open and nothing is waiting.
// The button must be disabled with the same code the handler refuses with.
func TestProjectActionsClosedProjectRefusesResume(t *testing.T) {
	s, _ := newBindingServer(t)
	metGoal(t, s, "拿到管理员权限")
	s.AutoPause("目标已完成：项目自动暂停")

	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	resume := actionByID(t, acts, actionResume)
	if resume.Enabled || resume.ReasonCode != reasonNothingToContinue {
		t.Fatalf("resume = %+v, want a disabled nothing_to_continue", resume)
	}
	if acts.Facts.GoalsMet != 1 || acts.Facts.Pending != 0 || acts.Facts.Unread != 0 {
		t.Fatalf("facts = %+v, want a closed project with no work", acts.Facts)
	}

	rec := call(t, s, http.MethodPost, "/api/project/resume", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Paused  bool   `json:"paused"`
		Resumed bool   `json:"resumed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != resume.ReasonCode || body.Error != resume.Reason {
		t.Fatalf("handler code/error = %q/%q, button says %q/%q", body.Code, body.Error, resume.ReasonCode, resume.Reason)
	}
	if !body.Paused || body.Resumed {
		t.Fatalf("refusal body = %+v, want paused and not resumed", body)
	}
}

// A manual pause is resumable and pausable-ness is the mirror of the switch.
func TestProjectActionsManualPause(t *testing.T) {
	s, _ := newBindingServer(t)
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	if pause := actionByID(t, acts, actionPause); pause.Enabled || pause.ReasonCode != reasonAlreadyPaused {
		t.Fatalf("pause = %+v, want already_paused", pause)
	}
	resume := actionByID(t, acts, actionResume)
	if !resume.Enabled || resume.ReasonCode != reasonPausedManual {
		t.Fatalf("resume = %+v, want an enabled manual resume", resume)
	}
}

// Closeout is not a pause: resume is refused while the commander is finishing,
// and the code is the same one the console greys the button with.
func TestProjectActionsCloseoutInProgress(t *testing.T) {
	s, _ := newBindingServer(t)
	s.closeout.Store(true)
	t.Cleanup(func() { s.closeout.Store(false) })

	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	resume := actionByID(t, acts, actionResume)
	if resume.Enabled || resume.ReasonCode != reasonCloseoutInProgress {
		t.Fatalf("resume = %+v, want closeout_in_progress", resume)
	}
	rec := call(t, s, http.MethodPost, "/api/project/resume", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), reasonCloseoutInProgress) {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
}

// A provider fault is the pause whose recovery IS 继续: the action must stay
// available even though the project is parked with no work settled.
func TestProjectActionsProviderFaultStaysResumable(t *testing.T) {
	s, p := newBindingServer(t)
	// Runtime paused while the manifest still says running is the fault signal
	// projectStateLocked classifies (the credential path pauses without
	// persisting the flag).
	s.SetPaused(true)
	if p.IsPaused() {
		t.Fatal("fixture must not persist the pause")
	}

	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	if acts.State.Kind != pauseKindProviderFault {
		t.Fatalf("state = %+v, want provider_fault", acts.State)
	}
	resume := actionByID(t, acts, actionResume)
	if !resume.Enabled || resume.ReasonCode != reasonProviderFaultResume {
		t.Fatalf("resume = %+v, want the provider-fault recovery", resume)
	}
}

// While the project runs, pause is the available action and resume is not.
func TestProjectActionsRunningProject(t *testing.T) {
	s, _ := newBindingServer(t)
	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	if pause := actionByID(t, acts, actionPause); !pause.Enabled || pause.ReasonCode != reasonOK {
		t.Fatalf("pause = %+v, want enabled", pause)
	}
	if resume := actionByID(t, acts, actionResume); resume.Enabled || resume.ReasonCode != reasonAlreadyRunning {
		t.Fatalf("resume = %+v, want already_running", resume)
	}
	if appendGoal := actionByID(t, acts, actionAppendGoal); !appendGoal.Enabled || appendGoal.ReasonCode != reasonOK {
		t.Fatalf("append_goal = %+v, want enabled", appendGoal)
	}
}

// The rule itself, without a ledger: which counters make an explicit resume
// meaningless. This is the branch table the reason codes sit on. The
// "every goal withdrawn" row is the behaviour goalsClosed expressed by returning
// false for an active count of zero — moving the counters must not turn it into
// a refusal (a goal can only be closed from a live commander round, so the board
// level case is covered here rather than through a store fixture).
func TestProjectFactsContinuableRules(t *testing.T) {
	cases := []struct {
		name   string
		facts  ProjectFacts
		expect bool
	}{
		{"no ledger", ProjectFacts{}, true},
		{"no goals chartered", ProjectFacts{HasLedger: true}, true},
		{"every goal withdrawn", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsClosed: 1}, true},
		{"goal not met", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsActive: 1, GoalsOpen: 1}, true},
		{"stale receipt", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsActive: 1, GoalsOpen: 1, GoalsStale: 1}, true},
		{"met with pending work", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsActive: 1, GoalsMet: 1, Pending: 1}, true},
		{"met with unread message", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsActive: 1, GoalsMet: 1, Unread: 1}, true},
		{"met with nothing left", ProjectFacts{HasLedger: true, GoalsTotal: 1, GoalsActive: 1, GoalsMet: 1}, false},
	}
	for _, c := range cases {
		if got := c.facts.continuable(); got != c.expect {
			t.Errorf("%s: continuable = %v, want %v (%+v)", c.name, got, c.expect, c.facts)
		}
	}
}

// The commander/executor surface keeps its old shape: the action contract is
// operator-side, and a session must not read the dispatch decision.
func TestProjectActionsHiddenFromHarnessSession(t *testing.T) {
	s, _ := newBindingServer(t)
	rec := serveMux(t, s, harnessRequest(http.MethodGet, "/api/board"))
	if rec.Code != http.StatusOK {
		t.Fatalf("harness board = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "project_actions") {
		t.Fatalf("harness board carries the operator action contract: %s", rec.Body.String())
	}
}

// A project created with 只创建 is parked as not_started, not as an operator
// pause and not as "cause unrecorded". The contract and the handler must agree:
// the button is enabled under that code, and 继续 is the one explicit action that
// buys the first round.
func TestProjectActionsCreatedWithoutStartResumesExplicitly(t *testing.T) {
	s, p := newBindingServer(t)
	runner, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = runner
	if _, err := s.Store.AddGoal(model.Goal{Statement: "拿到边界主机"}); err != nil {
		t.Fatal(err)
	}
	// Exactly what the creation path writes before attach: the manifest flag plus
	// the runtime classification of that cause.
	if err := p.SetPausedWithKind(true, project.PauseKindNotStarted); err != nil {
		t.Fatal(err)
	}
	s.SetPaused(true)
	s.RestorePauseKind(project.PauseKindNotStarted)
	runner.PauseFor("新建项目尚未开始：点「继续项目」让指挥官跑第一轮")

	acts := decodeActions(t, call(t, s, http.MethodGet, "/api/board", nil))
	if !acts.State.Paused || acts.State.Kind != pauseKindNotStarted {
		t.Fatalf("state = %+v, want paused/not_started", acts.State)
	}
	resume := actionByID(t, acts, actionResume)
	if !resume.Enabled || resume.ReasonCode != reasonPausedNotStarted {
		t.Fatalf("resume = %+v, want an enabled %s", resume, reasonPausedNotStarted)
	}
	if resume.ReasonCode == reasonPausedUnrecorded || resume.ReasonCode == reasonPausedManual {
		t.Fatalf("resume = %+v, the creation pause was attributed to a cause it does not have", resume)
	}
	// Nothing is due while the project is parked, even with an open endpoint.
	time.Sleep(50 * time.Millisecond)
	if got := commander.count(); got != 0 {
		t.Fatalf("commander rounds = %d while the project is not started, want 0", got)
	}

	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool { return commander.count() == 1 })
	if p.IsPaused() {
		t.Fatal("resume left the manifest paused")
	}
}

// A resume that arrives while the project is already running must be refused with
// the button's own code: before this the handler only consulted the verdict when
// the project was paused, so a double-click (or a retry after a lost response)
// rewrote the manifest and forced a commander round with nothing new to act on.
func TestResumeOnARunningProjectRefusesInsteadOfWakingAgain(t *testing.T) {
	s, p := newBindingServer(t)
	runner, commander := startAppendGoalDecider(t, s.Store)
	s.Decider = runner

	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, http.MethodPost, "/api/project/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("first resume = %d: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool { return commander.count() == 1 })

	rec := call(t, s, http.MethodPost, "/api/project/resume", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second resume = %d: %s, want 409 %s", rec.Code, rec.Body.String(), reasonAlreadyRunning)
	}
	var body struct {
		Code    string   `json:"code"`
		Paused  bool     `json:"paused"`
		Resumed bool     `json:"resumed"`
		Next    []string `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != reasonAlreadyRunning || body.Paused || body.Resumed {
		t.Fatalf("second resume = %+v, want %s with paused=false resumed=false", body, reasonAlreadyRunning)
	}
	// A running project has no "next" to offer: append_goal/message advice is for
	// parked projects only.
	if len(body.Next) != 0 {
		t.Fatalf("second resume next = %v, want none for a running project", body.Next)
	}
	// Nothing was written and nobody was woken: the round count stays at one over
	// a quiet window, and the manifest never flips back to paused.
	time.Sleep(250 * time.Millisecond)
	if got := commander.count(); got != 1 {
		t.Fatalf("commander rounds = %d after a duplicate resume, want 1", got)
	}
	if p.IsPaused() {
		t.Fatal("duplicate resume wrote paused=true into the manifest")
	}
}
