package api

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// drainExec keeps a canceled run in the ledger's "running" state until the test
// releases it. A real executor spends exactly this window tearing its turn
// down, and the operator can press "继续项目" inside it.
type drainExec struct {
	mu      sync.Mutex
	reqs    []executor.Request
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newDrainExec() *drainExec {
	return &drainExec{started: make(chan struct{}), release: make(chan struct{})}
}

func (d *drainExec) Name() string { return "drain" }

func (d *drainExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.once.Do(func() { close(d.started) })
	d.mu.Unlock()
	<-ctx.Done()
	<-d.release
	return executor.Result{ExitCode: -1, StopReason: "canceled"}, ctx.Err()
}

func (d *drainExec) requests() []executor.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]executor.Request{}, d.reqs...)
}

func (d *drainExec) finishDrain() { close(d.release) }

// newPauseServer is a bare ledger server (no manifest): the pause switch and the
// scheduler are the whole subject here.
func newPauseServer(t *testing.T) (*Server, *drainExec, model.Intent) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := newDrainExec()
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "暂停时在跑的顶层意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ex.started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor never started")
	}
	return s, ex, in
}

// A pending top-level direction belongs to the commander. The scheduler must not
// start it just because it is pending: the commander may have deliberately left
// it open, and auto-starting would turn every open direction into work again.
func TestSchedulerDoesNotStartCommanderLeftPendingDirection(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := newDrainExec()
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	if _, err := st.CreateIntent(model.Intent{Title: "指挥官留下待办的意图", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	s.pump()
	time.Sleep(150 * time.Millisecond)
	if n := len(ex.requests()); n != 0 {
		t.Fatalf("the scheduler started %d commander-dispatched directions", n)
	}
}

// The project pause cancels runs, not intents: settlement leaves the direction
// pending, so resuming must continue it. docs/01-architecture.md §8 states the
// rule ("恢复因 pause/restart 中断的意图时优先续接原会话"); a top-level
// direction has no parent to wait on, and nothing else would start it.
func TestResumeContinuesTopLevelDirectionStoppedByPause(t *testing.T) {
	s, ex, in := newPauseServer(t)
	s.SetPaused(true)
	if n := s.CancelProjectWorkWithCause(model.CancelCausePause, "项目暂停"); n != 1 {
		t.Fatalf("pause canceled %d runs, want 1", n)
	}
	ex.finishDrain()
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusPending
	})
	if n := len(runsOf(t, s.Store, in.ID)); n != 1 {
		t.Fatalf("runs while paused = %d, want 1", n)
	}

	s.SetPaused(false)
	s.pump()
	waitFor(t, 5*time.Second, func() bool { return len(runsOf(t, s.Store, in.ID)) == 2 })
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
	stopRunning(t, s)
}

// The same rule when the operator resumes before the cancellation finished
// settling: the run is still "running" at the resume kick, so the direction only
// becomes pending afterwards. A resume-time sweep alone would miss it; the
// settlement path has to pick it up too.
func TestResumeBeforePauseSettlesStillContinues(t *testing.T) {
	s, ex, in := newPauseServer(t)
	s.SetPaused(true)
	if n := s.CancelProjectWorkWithCause(model.CancelCausePause, "项目暂停"); n != 1 {
		t.Fatalf("pause canceled %d runs, want 1", n)
	}
	// The run drains asynchronously; the direction is still running here.
	if got, _ := s.Store.GetIntent(in.ID); got.Status != model.StatusRunning {
		t.Fatalf("intent status during drain = %s, want running", got.Status)
	}
	s.SetPaused(false)
	s.pump()
	if n := len(runsOf(t, s.Store, in.ID)); n != 1 {
		t.Fatalf("a still-running direction started twice: %d runs", n)
	}
	ex.finishDrain()
	waitFor(t, 5*time.Second, func() bool { return len(runsOf(t, s.Store, in.ID)) == 2 })
	waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 2 })
	stopRunning(t, s)
}
