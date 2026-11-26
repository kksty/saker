package decider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/state"
)

// A verified handoff round is a step, not the end of the pressure cycle: it
// deliberately dispatches nothing, so the note's "next step" has no in-flight
// run that could wake it. The receipt must schedule exactly one forced
// follow-up round; otherwise the project idles after every handoff with the
// deferred work unexecuted.
func TestVerifiedHandoffSchedulesOneFollowUpRound(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), true)
	tr.run(t, r, resumedSession())            // climb to the 85% line
	handoff := tr.run(t, r, resumedSession()) // the handoff round, valid receipt
	if handoff.HandoffReceipt == nil {
		t.Fatalf("fixture handoff has no valid receipt: %+v", handoff)
	}
	select {
	case reason := <-r.wake:
		if reason != wakeHandoffContinue {
			t.Fatalf("follow-up wake = %q, want %q", reason, wakeHandoffContinue)
		}
	default:
		t.Fatal("a verified handoff scheduled no follow-up round")
	}
	if !isForced(wakeHandoffContinue) {
		t.Fatal("the handoff continuation must be a forced wake")
	}
	// The continuation is a normal round that reads the note, not a second
	// handoff: the cycle already closed.
	next := r.Run(context.Background(), wakeHandoffContinue, isForced(wakeHandoffContinue))
	if next == nil {
		t.Fatal("the follow-up round did not run")
	}
	// The continuation is a machine wake, not the operator asking for a round:
	// the round's control attention must say so. Recording it as explicit_wake
	// would attribute automatic spending to the operator in the decision log.
	if len(next.Attention) != 1 || next.Attention[0].Code != "handoff_continue" {
		t.Fatalf("continuation attention = %+v, want one handoff_continue reason", next.Attention)
	}
	if next.Handoff || strings.Contains(tr.prompts[len(tr.prompts)-1], "## 交接（本轮必做）") {
		t.Fatalf("the follow-up round handed off again:\n%s", tr.prompts[len(tr.prompts)-1])
	}
	// Exactly one round: an ordinary round that did not hand off schedules
	// nothing on its own.
	select {
	case reason := <-r.wake:
		t.Fatalf("an ordinary round scheduled another wake: %q", reason)
	default:
	}
}

// Pause is the one stop the automatic continuation must respect. The handoff
// round already ran and its receipt stands (the note is written), but the
// follow-up round it scheduled is machine spending, and a paused project must
// not spend. Two independent gates have to hold: Wake() refuses to queue while
// paused, and Run() refuses again at its entry for a wake the loop already took
// out of the channel (the API pauses dispatch and the decider in the same step).
// "Dropped" is the contract, not "it ran and produced nothing": no round count,
// no run row, no decision-log line, and Resume must not resurrect it.
func TestPausedProjectDropsTheHandoffContinuation(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), true)
	r.LogPath = filepath.Join(t.TempDir(), "decisions.jsonl")
	tr.run(t, r, resumedSession())            // climb to the 85% line
	handoff := tr.run(t, r, resumedSession()) // the handoff round, valid receipt
	if handoff.HandoffReceipt == nil {
		t.Fatalf("fixture handoff has no valid receipt: %+v", handoff)
	}
	if got := len(r.wake); got != 1 {
		t.Fatalf("fixture queued %d continuation wakes, want 1", got)
	}
	// The loop already dequeued the forced continuation; the pause lands before
	// the round starts. This is the window in which only Run's entry check can
	// stop the spend.
	select {
	case <-r.wake:
	default:
		t.Fatal("fixture continuation vanished before the pause")
	}
	rounds, runs, lines := r.Status().Rounds, len(st.Runs()), decisionLines(t, r.LogPath)
	r.PauseFor("操作员暂停了指挥官：等你点「继续」")
	if d := r.Run(context.Background(), wakeHandoffContinue, true); d != nil {
		t.Fatalf("a paused project ran the handoff continuation: %+v", d)
	}
	// A wake that arrives after the pause is refused at the door.
	r.Wake(wakeHandoffContinue)
	if got := len(r.wake); got != 0 {
		t.Fatalf("a paused project queued the handoff continuation (%q)", wakeHandoffContinue)
	}
	if d := r.Run(context.Background(), wakeHandoffContinue, true); d != nil {
		t.Fatalf("a paused project ran the handoff continuation on a later wake: %+v", d)
	}
	// Dropped is not "a successful empty continuation".
	if got := r.Status().Rounds; got != rounds {
		t.Fatalf("the dropped continuation still counted a round: %d -> %d", rounds, got)
	}
	if got := len(st.Runs()); got != runs {
		t.Fatalf("the dropped continuation still recorded a run: %d -> %d", runs, got)
	}
	if got := decisionLines(t, r.LogPath); got != lines {
		t.Fatalf("the dropped continuation was logged as a decision: %d -> %d lines", lines, got)
	}
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	if last != handoff {
		t.Fatalf("the last decision is not the handoff round any more: %+v", last)
	}
	// The drop is final: resuming is the operator's explicit act and must not
	// bring the machine's dropped wake back with it.
	r.Resume()
	if got := len(r.wake); got != 0 {
		t.Fatalf("Resume resurrected the dropped handoff continuation")
	}
}

// decisionLines counts the decisions appended to the runner's decision log. A
// dropped round must add none; reading the file is the only way to see "nothing
// was recorded", because the in-memory last decision would be overwritten.
func decisionLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// The cycle-state row is the only thing that stops a restart from reopening a
// closed pressure cycle, so a failed write must be visible: the round itself did
// its work, the decision carries the error, and the live activity stays blocked
// instead of falling back to the ordinary idle line.
func TestCycleStateWriteFailureIsVisible(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName, r.ContextWindow = "fixture", 1000000
	r.lastContext = 100000 // below the handoff band: an ordinary round
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		// The harness reports a run row the ledger does not have, so the cycle
		// state cannot be recorded on it.
		return RoundResult{ContextTokens: 100000, RunID: "run_missing"}, nil
	}
	d := r.Run(context.Background(), "manual", true)
	if d == nil {
		t.Fatal("the round did not run")
	}
	if !strings.Contains(strings.Join(d.Notes, "\n"), "记录交接周期状态失败") {
		t.Fatalf("a failed cycle-state write left no trace on the decision: %v", d.Notes)
	}
	if got := r.Status().Activity; got.Phase != "blocked" || !strings.Contains(got.Detail, "记录交接周期状态失败") {
		t.Fatalf("a failed cycle-state write did not block the activity: %+v", got)
	}
}

// The pressure-cycle state rides the round row, so restart recovery does not
// depend on the in-memory flag surviving the process.
func TestVerifiedHandoffPersistsTheCycleState(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), true)
	tr.run(t, r, resumedSession())
	handoff := tr.run(t, r, resumedSession())
	if handoff.RunID == "" {
		t.Fatal("fixture handoff has no run id")
	}
	run, ok := st.GetRun(handoff.RunID)
	if !ok {
		t.Fatalf("handoff run %s was not recorded", handoff.RunID)
	}
	if !run.HandoffDone {
		t.Fatal("the verified handoff did not persist the closed pressure cycle on its run")
	}
}

// Without a valid receipt the cycle never closed: no follow-up round is owed,
// the next wake must be the retry/first ordinary round instead.
func TestHandoffWithoutReceiptSchedulesNoFollowUp(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), false)
	tr.run(t, r, resumedSession())
	d := tr.run(t, r, resumedSession())
	if d.HandoffReceipt != nil {
		t.Fatalf("fixture unexpectedly wrote a receipt: %+v", d)
	}
	select {
	case reason := <-r.wake:
		t.Fatalf("a missing receipt scheduled a round: %q", reason)
	default:
	}
}

// The wake must go through the real loop, not only sit in the channel: the
// handoff round ends, the loop debounces, and the forced continuation round
// runs by itself. Exactly two rounds, never a third.
func TestLoopRunsTheHandoffContinuationAutomatically(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName, r.ContextWindow, r.lastContext = "fixture", 1000000, 900000
	r.Debounce = time.Millisecond
	r.Heartbeat = time.Hour
	calls := make(chan string, 8)
	rounds := 0
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, prompt string, _ bool) (RoundResult, error) {
		rounds++
		calls <- prompt
		if rounds != 1 {
			return RoundResult{ContextTokens: 900000}, nil
		}
		run, err := st.AddRun(model.Run{Purpose: "commander", CommanderRound: CommanderRoundInput(ctx).Round})
		if err != nil {
			return RoundResult{}, err
		}
		note, _ := st.Notebook()
		if _, err := st.SaveNotebook(note.Version, "交接笔记：下一步派意图", "commander", run.ID, nil); err != nil {
			return RoundResult{}, err
		}
		if _, err := st.UpdateRun(run.ID, func(x *model.Run) { x.Status = model.RunDone }); err != nil {
			return RoundResult{}, err
		}
		return RoundResult{RunID: run.ID, ContextTokens: 900000}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Wake("manual")
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("the handoff round did not start")
	}
	select {
	case prompt := <-calls:
		if strings.Contains(prompt, "## 交接（本轮必做）") {
			t.Fatalf("the automatic continuation handed off again:\n%s", prompt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not run the handoff continuation by itself")
	}
	select {
	case <-calls:
		t.Fatal("the continuation round scheduled a third round")
	case <-time.After(200 * time.Millisecond):
	}
}

// A restart after a cycle that closed with a verified handoff must not reopen
// it: the resumed session is still near the handoff line, but the note is
// already current, so the first round has to work instead of handing off.
func TestSeedContextKeepsAClosedPressureCycle(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	prev, err := st.AddRun(model.Run{
		Purpose: "commander", ContextTokens: 900000, FinalContextTokens: 900000,
		ContextWindow: 1000000, HandoffDone: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(prev.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true, Restarted: true} }
	var prompt string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompt = p
		return RoundResult{ContextTokens: 900000, ContextWindow: 1000000}, nil
	}
	r.seedContext()
	if !r.handoffDone {
		t.Fatal("seedContext dropped the closed pressure cycle recorded on the last run")
	}
	d := r.Run(context.Background(), "operator", true)
	if d == nil {
		t.Fatal("the operator's forced round did not run")
	}
	if d.Handoff || strings.Contains(prompt, "## 交接（本轮必做）") {
		t.Fatalf("the restart reopened a closed pressure cycle:\n%s", prompt)
	}
}

// The counterpart: an open cycle still gets its handoff round after a restart.
func TestSeedContextKeepsAnOpenPressureCycle(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	prev, err := st.AddRun(model.Run{
		Purpose: "commander", ContextTokens: 900000, FinalContextTokens: 900000,
		ContextWindow: 1000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(prev.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true, Restarted: true} }
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		return RoundResult{ContextTokens: 900000, ContextWindow: 1000000}, nil
	}
	r.seedContext()
	if r.handoffDone {
		t.Fatal("seedContext invented a closed cycle for an open one")
	}
	if d := r.Run(context.Background(), "operator", true); d == nil || !d.Handoff {
		t.Fatalf("an open cycle must still hand off after a restart: %+v", d)
	}
}
