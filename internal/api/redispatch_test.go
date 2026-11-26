package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// runsOf returns the run rows of one direction, oldest first.
func runsOf(t *testing.T, st *store.Store, intentID string) []model.Run {
	t.Helper()
	out := []model.Run{}
	for _, r := range st.Runs() {
		if r.IntentID == intentID {
			out = append(out, r)
		}
	}
	return out
}

// An interrupted run restarts the same direction on its own. This is the whole
// point of P1: coverage must not depend on the commander re-creating the intent.
func TestWatchdogCancelRedispatchesSameIntent(t *testing.T) {
	s, ex, in := newSteerServer(t)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRunWithCause(live.RunID, model.CancelCauseWatchdog, "静默 15m"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(runsOf(t, s.Store, in.ID)) == 2 })
	// The restarted run must actually reach the executor, not just exist as a row.
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusRunning {
		t.Fatalf("intent after redispatch = %s, want running", got.Status)
	}
	stopRunning(t, s)
}

// The operator's cancel is a decision. CancelIntent holds the direction before
// stopping the run, and this path must never undo that hold.
func TestOperatorCancelDoesNotRedispatch(t *testing.T) {
	s, ex, in := newSteerServer(t)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRun(live.RunID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	time.Sleep(200 * time.Millisecond)
	if n := len(runsOf(t, s.Store, in.ID)); n != 1 {
		t.Fatalf("runs after an operator cancel = %d, want 1", n)
	}
	if n := len(ex.requests()); n != 1 {
		t.Fatalf("executor calls after an operator cancel = %d, want 1", n)
	}
}

// Three restarts is the stop-loss: the fourth interruption is left to the
// commander instead of spending another run on the same wall.
func TestRedispatchStopsAfterThree(t *testing.T) {
	s, _, in := newSteerServer(t)
	for i := 0; i < maxRedispatches; i++ {
		want := i + 2
		live, _ := s.Store.GetIntent(in.ID)
		if err := s.CancelRunWithCause(live.RunID, model.CancelCauseWatchdog, "静默 15m"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool { return len(runsOf(t, s.Store, in.ID)) == want })
	}
	// The fourth cancel has no budget left.
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRunWithCause(live.RunID, model.CancelCauseWatchdog, "静默 15m"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status != model.StatusRunning
	})
	time.Sleep(200 * time.Millisecond)
	if n := len(runsOf(t, s.Store, in.ID)); n != maxRedispatches+1 {
		t.Fatalf("runs = %d, want %d (original + %d restarts)", n, maxRedispatches+1, maxRedispatches)
	}
}

// A control-plane restart is not a failure of the direction. Counting
// restart-canceled runs as interruptions spent the budget on routine stop/resume
// cycles, so a few deployment restarts silently stopped continuing a direction
// that was interrupted for real later. Restart cycles must not spend a slot.
func TestRedispatchBudgetIgnoresRestartCycles(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "重启不花预算", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	// One cycle more than the budget: if a restart counted, that last resume
	// would refuse to continue.
	for cycle := 1; cycle <= maxRedispatches+1; cycle++ {
		s.paused.Store(true)
		live, _ := s.Store.GetIntent(in.ID)
		if err := s.CancelRunWithCause(live.RunID, model.CancelCauseRestart, "换 exe 停项目"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			got, ok := s.Store.GetIntent(in.ID)
			return ok && got.Status == model.StatusPending
		})
		s.paused.Store(false)
		s.redispatchInterrupted()
		want := cycle + 1
		waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == want })
	}
	if got := len(runsOf(t, s.Store, in.ID)); got != maxRedispatches+2 {
		t.Fatalf("runs = %d, want %d (original + %d resume cycles)", got, maxRedispatches+2, maxRedispatches+1)
	}
	stopRunning(t, s)
}

// A paused project must not restart anything: the pause is what the operator
// asked for, and resuming is when the interrupted work may continue.
func TestPausedProjectDoesNotRedispatch(t *testing.T) {
	s, _, in := newSteerServer(t)
	s.paused.Store(true)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRunWithCause(live.RunID, model.CancelCauseWatchdog, "静默 15m"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status != model.StatusRunning
	})
	time.Sleep(200 * time.Millisecond)
	if n := len(runsOf(t, s.Store, in.ID)); n != 1 {
		t.Fatalf("runs while paused = %d, want 1", n)
	}
}

// A restart-interrupted direction is restarted on explicit resume, not at boot.
func TestResumeRedispatchesRestartCanceled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "重启中断", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
	s.paused.Store(true)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRunWithCause(live.RunID, model.CancelCauseRestart, "进程重启对账"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusPending
	})
	time.Sleep(150 * time.Millisecond)
	if n := len(runsOf(t, s.Store, in.ID)); n != 1 {
		t.Fatalf("runs while paused = %d, want 1", n)
	}
	s.paused.Store(false)
	s.redispatchInterrupted()
	waitFor(t, 5*time.Second, func() bool { return len(runsOf(t, s.Store, in.ID)) == 2 })
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
	stopRunning(t, s)
}

// stopRunning settles every live run before the test ends. t.TempDir's cleanup
// runs after the test, and a journal writer that is still open makes Windows
// refuse to delete the file (the same leak class as project_transfer_test.go).
func stopRunning(t *testing.T, s *Server) {
	t.Helper()
	for _, r := range s.Store.Runs() {
		if r.Status == model.RunRunning {
			_ = s.CancelRun(r.ID)
		}
	}
	waitFor(t, 5*time.Second, func() bool { return s.RunningIntentCount() == 0 })
}
