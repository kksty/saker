package api

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A stop gives the live executor one bounded evidence window (the shared
// cleanup notice through the native inbox), then cancels it with the restart
// cause. The direction goes back to retryable without being replayed, and a
// second drain has nothing left to do.
func TestDrainRunningDeliversBoundedNoticeThenCancelsWithRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	// The drain window, not the per-run timeout, bounds this test: a run with
	// Timeout=0 has no timeout cleanup window of its own.
	s.Timeout = 0

	var mu sync.Mutex
	var texts []string
	var deliveryID string
	var statusAtNotice string
	s.SayEvent = func(_ context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool, eventID string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, text)
		deliveryID = eventID
		// The notice must arrive while the run is still live: the window is for
		// writing down what the executor already has, not a report after death.
		if run, ok := st.GetRun(strings.TrimPrefix(eventID, "cleanup:")); ok {
			statusAtNotice = run.Status
		}
		if !interrupt {
			t.Error("cleanup notice must interrupt the turn")
		}
		if sess.Session != "sess-1" {
			t.Errorf("session = %q", sess.Session)
		}
		_ = in
		return "receipt", nil
	}

	in, err := st.CreateIntent(model.Intent{Title: "停机收尾"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	var runID string
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		if !ok || got.Status != model.StatusRunning || got.RunID == "" {
			return false
		}
		runID = got.RunID
		return true
	})
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	// The native delivery path needs the run's AOP session pointer; dispatch
	// creates the case directory, the harness publishes the marker.
	writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
	})

	window := 700 * time.Millisecond
	start := time.Now()
	n, err := s.DrainRunning(context.Background(), window)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 1 {
		t.Fatalf("drained = %d, want 1", n)
	}
	// The window is a ceiling: one getRun snapshot and one cancel must not turn
	// a shutdown into an unbounded wait.
	if elapsed > window+time.Second {
		t.Fatalf("drain took %s, bounded window is %s", elapsed, window)
	}

	mu.Lock()
	gotTexts := append([]string{}, texts...)
	gotID := deliveryID
	gotStatus := statusAtNotice
	mu.Unlock()
	if len(gotTexts) != 1 {
		t.Fatalf("cleanup notices delivered = %d, want 1", len(gotTexts))
	}
	if !strings.Contains(gotTexts[0], "事实交接契约") {
		t.Fatalf("cleanup notice lost the handoff contract: %q", gotTexts[0])
	}
	if gotID != "cleanup:"+runID {
		t.Fatalf("cleanup event id = %q, want cleanup:%s", gotID, runID)
	}
	if gotStatus != model.RunRunning {
		t.Fatalf("cleanup notice arrived after the run settled: status %q", gotStatus)
	}

	// The cancellation is our decision: canceled, with the restart cause, and
	// the harness's own stop verdict (blockingExec reports "canceled") retained.
	waitFor(t, 5*time.Second, func() bool {
		run, ok := st.GetRun(runID)
		return ok && run.Status == model.RunCanceled
	})
	run, _ := st.GetRun(runID)
	if run.CancelCause != model.CancelCauseRestart {
		t.Fatalf("cancel cause = %q, want %q", run.CancelCause, model.CancelCauseRestart)
	}
	if run.StopReason != "canceled" {
		t.Fatalf("stop_reason = %q; the drain invented a verdict instead of preserving the harness's", run.StopReason)
	}
	if run.CleanupPhase != "accepted" {
		t.Fatalf("cleanup phase = %q, want accepted", run.CleanupPhase)
	}

	// Retryable, not done, and the interruption did not spend the attempt.
	gotIn, ok := st.GetIntent(in.ID)
	if !ok {
		t.Fatal("intent disappeared")
	}
	if gotIn.Status != model.StatusPending {
		t.Fatalf("intent status = %q, want pending (retryable)", gotIn.Status)
	}
	if gotIn.Attempts != 0 {
		t.Fatalf("intent attempts = %d, want 0 (a drain is not a failure)", gotIn.Attempts)
	}
	if gotIn.Hold {
		t.Fatal("a drain held the direction instead of making it retryable")
	}

	// Idempotent: nothing live is left, so nothing is delivered or canceled.
	n, err = s.DrainRunning(context.Background(), window)
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if n != 0 {
		t.Fatalf("second drain canceled %d runs, want 0", n)
	}
	mu.Lock()
	deliveries := len(texts)
	mu.Unlock()
	if deliveries != 1 {
		t.Fatalf("second drain re-delivered the cleanup notice (%d deliveries)", deliveries)
	}
}

// A restart leaves rows recorded as running. The sweep converges them to
// canceled/restart, preserves the stop_reason the dead process had already
// recorded, and returns the intents to retryable — without a single executor
// call, and without a second pass changing anything.
func TestReconcileAfterRestartConvergesLeftoversWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "上个进程没结束的意图"})
	if err != nil {
		t.Fatal(err)
	}
	// Build the same linked identity a real dispatch leaves behind: intent
	// running, one attempt spent, one running run row.
	claimed, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claim attempts = %d", claimed.Attempts)
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.StopReason = "completed" }); err != nil {
		t.Fatal(err)
	}
	// A commander round is a purpose run with no intent: the same sweep must
	// converge it too, or a dead round keeps the board looking busy.
	cmdRun, err := st.AddRun(model.Run{Purpose: "commander", Node: "local", StopReason: "completed"})
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.ReconcileAfterRestart(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("reconciled = %d runs, want 2", n)
	}

	gotRun, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if gotRun.Status != model.RunCanceled || gotRun.CancelCause != model.CancelCauseRestart {
		t.Fatalf("run = %s/%s, want canceled/%s", gotRun.Status, gotRun.CancelCause, model.CancelCauseRestart)
	}
	if gotRun.EndedAt == nil {
		t.Fatal("reconciled run has no end timestamp")
	}
	if gotRun.StopReason != "completed" {
		t.Fatalf("stop_reason = %q, want the dead process's original value", gotRun.StopReason)
	}
	if !strings.Contains(gotRun.ExitReason, "进程重启对账") {
		t.Fatalf("exit_reason = %q, want it to say 进程重启对账", gotRun.ExitReason)
	}

	gotCmd, ok := st.GetRun(cmdRun.ID)
	if !ok {
		t.Fatal("commander run disappeared")
	}
	if gotCmd.Status != model.RunCanceled || gotCmd.CancelCause != model.CancelCauseRestart {
		t.Fatalf("commander run = %s/%s, want canceled/%s", gotCmd.Status, gotCmd.CancelCause, model.CancelCauseRestart)
	}
	if gotCmd.StopReason != "completed" {
		t.Fatalf("commander stop_reason = %q, want preserved", gotCmd.StopReason)
	}

	// The direction is retryable: pending, attempt returned, not done, no hold.
	gotIn, ok := st.GetIntent(in.ID)
	if !ok {
		t.Fatal("intent disappeared")
	}
	if gotIn.Status != model.StatusPending || gotIn.Attempts != 0 || gotIn.Hold {
		t.Fatalf("reconciled intent = status %q attempts %d hold %v, want pending/0/false", gotIn.Status, gotIn.Attempts, gotIn.Hold)
	}
	if gotIn.RunID != run.ID {
		t.Fatalf("reconciled intent points at run %q, want %q", gotIn.RunID, run.ID)
	}

	// No replay, no external action: the executor was never invoked.
	if reqs := ex.requests(); len(reqs) != 0 {
		t.Fatalf("reconcile replayed %d executor request(s)", len(reqs))
	}

	// Idempotent: a second sweep finds nothing running and changes nothing.
	before := st.ContentHash()
	n, err = s.ReconcileAfterRestart(context.Background())
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if n != 0 {
		t.Fatalf("second reconcile repaired %d rows, want 0", n)
	}
	if st.ContentHash() != before {
		t.Fatal("second reconcile changed the ledger")
	}
}

// A canceled context must not wait the full window: the stop is bounded by
// whichever comes first, and the cancel still happens.
func TestDrainRunningStopsWaitingWhenContextIsCanceled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "被提前打断的停机"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	var runID string
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		if !ok || got.Status != model.StatusRunning || got.RunID == "" {
			return false
		}
		runID = got.RunID
		return true
	})
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	n, err := s.DrainRunning(ctx, 30*time.Second)
	elapsed := time.Since(start)
	if n != 1 {
		t.Fatalf("drained = %d, want 1", n)
	}
	if err == nil {
		t.Fatal("drain did not report that its window was cut short")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("drain ignored the canceled context: %s", elapsed)
	}
	waitFor(t, 5*time.Second, func() bool {
		run, ok := st.GetRun(runID)
		return ok && run.Status == model.RunCanceled && run.CancelCause == model.CancelCauseRestart
	})
}
