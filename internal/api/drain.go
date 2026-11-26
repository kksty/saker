package api

import (
	"context"
	"path/filepath"
	"time"

	"github.com/kksty/saker/internal/model"
)

// drainWindowCeiling is the longest a stop may wait for evidence wrap-up. A
// stop is a bounded operation: the caller may ask for less, never for more.
const drainWindowCeiling = 90 * time.Second

// drainPollInterval is how often the drain re-reads the live set while the
// window runs. It is short enough that a run finishing early ends the stop
// promptly, and long enough not to spin the ledger read.
const drainPollInterval = 50 * time.Millisecond

// drainNoticeTimeout bounds one cleanup-notice delivery. The notice is a
// courtesy inside an already-bounded window: a transport that stops answering
// must not consume the window (and, on the delete path, must not block the
// delete forever).
const drainNoticeTimeout = 10 * time.Second

// drainReason is the structured reason recorded on every run a stop cancels. It
// says exactly what the control plane did (bounded window, then restart stop)
// and what it did not do (replay an external action). The harness's own
// stop_reason is left to the settlement path.
const drainReason = "停机收尾：有界证据窗口结束，取消运行（保留原始 stop_reason，未重放外部动作）"

// DrainDeleteReason is the delete-path wording. A project delete is an
// administrative stop, not a deployment restart; keeping the two distinct is
// what lets Phase 1 classify the two cancels differently.
const DrainDeleteReason = "删除项目：有界证据窗口结束，取消运行（保留原始 stop_reason，未重放外部动作）"

// BeginDrain seals this project's admission for a bounded stop without
// canceling the live runs. Process shutdown uses it before DrainRunning so no
// new round, child or review can start during the evidence window; a project
// delete already closes the same door with SetDeleting.
//
// It is the sealing half of BeginShutdown (which is BeginDrain + CancelAll);
// both write the same transient lifetime flag and pause the decider.
func (s *Server) BeginDrain() {
	if s.Lifecycle != nil {
		s.Lifecycle.BeginDrain()
	} else {
		s.closing.Store(true)
	}
	s.reviewPumpMu.Lock()
	if s.reviewPumpCancel != nil {
		s.reviewPumpCancel()
	}
	s.reviewPumpMu.Unlock()
}

// DrainRunning stops every run this process is still executing, after one
// bounded evidence window.
//
// Semantics, in order:
//
//  1. Window: min(timeout, 90s). timeout <= 0 means the 90s ceiling. The timer
//     starts before the notices, so a slow transport cannot extend the stop.
//     Only runs currently owned by this process are drained; ledger rows left
//     running by an earlier process are ReconcileAfterRestart's business.
//  2. Notice: each live intent run gets the shared cleanupNotice through its
//     native inbox (the same text/delivery path as startCleanupWindow), once.
//     Each delivery has its own short deadline inside the window. Purpose runs
//     (commander/report/verifier) have no intent evidence handoff, so they only
//     get the window.
//  3. Cancel: whatever is still running when the window ends — or when ctx is
//     canceled — is stopped with the caller's cause/reason. DrainRunning uses
//     restart; a project delete uses project_stop. The run's stop_reason is
//     never synthesized here: the harness/executor settlement path owns it.
//  4. Caller: it is a stop primitive, not a payment. The caller must already
//     have sealed admission (BeginDrain / SetDeleting) and should follow with
//     WaitIdle so canceled runs settle before hosts are torn down.
//
// Idempotent: already canceled/ended runs are not in the live set, a run whose
// cleanup notice was already delivered is not messaged again, and a second call
// with nothing live returns 0 immediately.
//
// The count is the number of runs this call canceled; a run that finished on its
// own inside the window is not counted. The error is ctx.Err() (the window was
// cut short; the cancel above still ran), or nil.
//
// Nothing external is replayed: the drain only writes the ledger and the run
// journal, and it never promotes a guess (finished, found, not found) to fact.
func (s *Server) DrainRunning(ctx context.Context, timeout time.Duration) (int, error) {
	return s.DrainRunningWithCause(ctx, timeout, model.CancelCauseRestart, drainReason)
}

// DrainRunningWithCause is DrainRunning with the ledger cause/reason chosen by
// the caller. A deployment stop is "restart"; deleting a project is
// "project_stop". The two share the same bounded evidence window, but Phase 1
// must not report a delete as a process shutdown.
func (s *Server) DrainRunningWithCause(ctx context.Context, timeout time.Duration, cause, reason string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Store == nil || s.Lifecycle == nil {
		return 0, nil
	}
	window := timeout
	if window <= 0 || window > drainWindowCeiling {
		window = drainWindowCeiling
	}
	live := s.drainableRuns()
	if len(live) == 0 {
		return 0, nil
	}
	// The window starts now, before the notices: a slow or hung transport must
	// not extend the stop. Each notice gets its own short deadline inside the
	// window, so N notices cannot turn one bounded stop into N×10s.
	deadline := time.Now().Add(window)
	timer := time.NewTimer(window)
	defer timer.Stop()
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for _, r := range live {
		if r.Purpose != "" || r.IntentID == "" {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		workDir := filepath.Join(s.CaseRoot, r.IntentID)
		noticeCtx, cancel := context.WithTimeout(ctx, min(drainNoticeTimeout, remaining))
		_, _ = s.deliverCleanupNotice(noticeCtx, r.ID, r.IntentID, workDir, remaining)
		cancel()
	}
wait:
	for {
		// Runs that ended during the window need no cancel: the stop is only
		// about what is still alive. Also keeps a stop short when work wraps up.
		if len(s.drainableRuns()) == 0 {
			return 0, ctx.Err()
		}
		select {
		case <-ctx.Done():
			break wait
		case <-timer.C:
			break wait
		case <-ticker.C:
		}
	}
	canceled := 0
	for _, r := range s.drainableRuns() {
		if err := s.CancelRunWithCause(r.ID, cause, reason); err != nil {
			// The run settled between the read and the cancel: the goal state is
			// its terminal row, not an error.
			continue
		}
		canceled++
	}
	return canceled, ctx.Err()
}

// drainableRuns is the live set: ledger rows still running that this process
// actually owns a cancel handle for. A running row without a handle is not being
// executed by anyone here (a previous process left it, or its dispatcher died)
// and is settled by ReconcileAfterRestart, not drained.
func (s *Server) drainableRuns() []model.Run {
	out := []model.Run{}
	for _, r := range s.Store.Runs() {
		if r.Status == model.RunRunning && s.Lifecycle.RunActive(r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// ReconcileAfterRestart repairs the stop that never happened: runs a previous
// process left recorded as running are converged to canceled with the restart
// cause, their original stop_reason is preserved (this process did not observe a
// new verdict), and their intents are returned to a retryable state.
//
// The intent is made retryable, not retried: no executor run is started, no
// external action is replayed, and nothing is marked done. A root direction goes
// back to pending without spending its attempt (our interruption is not its
// failure); a waiting direction keeps its durable wait; a delegated helper
// becomes failed so its coordinator decides, instead of being silently
// re-dispatched.
//
// Idempotent: a second call finds no running rows and returns 0 without
// touching the ledger.
func (s *Server) ReconcileAfterRestart(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.Store == nil || s.Lifecycle == nil {
		return 0, nil
	}
	// Reconcile settles rows outside cancelWithCause, so record the same Phase 1
	// cancel rows here. Snapshot first: after the sweep the rows are terminal and
	// a second call must not double-count them.
	var live []model.Run
	for _, r := range s.Store.Runs() {
		if r.Status == model.RunRunning {
			live = append(live, r)
		}
	}
	n := s.Lifecycle.ReconcileAfterRestart()
	if s.Metrics != nil {
		for _, before := range live {
			cur, ok := s.Store.GetRun(before.ID)
			if !ok || cur.Status != model.RunCanceled {
				continue
			}
			s.Metrics.Cancel(before.ID, model.CancelCauseRestart, cur.ExitReason, before.StopReason)
		}
	}
	return n, nil
}
