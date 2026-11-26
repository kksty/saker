package api

import (
	"errors"
	"log"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// maxRedispatches bounds how many times one direction may be restarted after an
// unplanned interruption. It is a stop-loss, not a scheduler: three restarts
// with no result means the direction needs a human or the commander, and a
// fourth automatic run would only spend tokens on the same wall.
const maxRedispatches = 3

// interrupted says which cancellation causes are an unplanned failure of the
// direction — the only ones the redispatch budget counts.
//
// A process restart (deployment, crash sweep) is deliberately NOT one: the run
// was healthy and the interruption was the control plane's, not the direction's.
// Counting it spent the budget on routine stop/resume cycles, so after a few
// restarts an interrupted direction silently stopped being continued. The
// restart-canceled run is still restarted (redispatchInterrupted), it just does
// not consume a slot.
//
// Explicit stops (operator, stop-loss, worker kill, model stall) hold the intent
// and are never restarted: a hold is a decision, and CancelIntent sets it before
// cancelling, so the operator's cancel cannot be undone by this path. Pause,
// steer and review pause have their own continuation semantics.
//
// An empty cause is an interruption we could not classify — historically it was
// recorded as "operator", which silently lost the work (22 of 49 cancelled runs
// in the TSecBench 2026-11-22 package were never retried).
func interrupted(cause string) bool {
	switch cause {
	case "", model.CancelCauseWatchdog:
		return true
	}
	return false
}

// redispatchesUsed counts how much of the budget this direction has already
// spent: earlier runs that ended in an unplanned interruption. exceptRunID is
// the run being handled now — it is the interruption, not a restart that was
// paid for — so it never counts against itself. The cap is on interruptions, not
// on runs: a direction that finishes and is later reopened by the commander
// starts fresh.
func (s *Server) redispatchesUsed(intentID, exceptRunID string) int {
	n := 0
	for _, r := range s.Store.Runs() {
		if r.ID == exceptRunID {
			continue
		}
		if r.IntentID == intentID && r.Status == model.RunCanceled && interrupted(r.CancelCause) {
			n++
		}
	}
	return n
}

// redispatchAfterCancel restarts a direction whose run the watchdog killed, so
// the work does not depend on the commander happening to notice it is missing.
//
// Only the watchdog qualifies at runtime. The other cancellation causes are
// decisions (operator, stop-loss, worker kill, model stall hold the intent) or
// have their own continuation (steer, pause, review pause). Restart is handled at
// resume instead: the project-stop path cancels every run with cause=restart, and
// restarting them from inside that loop would fight the shutdown.
func (s *Server) redispatchAfterCancel(runID, intentID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok || run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseWatchdog {
		return
	}
	if s.paused.Load() || s.closing.Load() || s.deleting.Load() {
		s.noteRedispatch(runID, intentID, 0, run.CancelCause, "skipped", "项目已暂停或正在关闭", "")
		return
	}
	s.redispatchOne(runID, intentID, run.CancelCause)
}

// redispatchInterrupted restarts directions that were interrupted by the process
// going away. It runs after the boot watchdog recovery cleared the restart pause,
// or after an explicit project resume: work interrupted by a crash is not a
// decision to abandon it, but it must never replay before the crash sweep has
// converged the orphan rows.
func (s *Server) redispatchInterrupted() {
	if s.paused.Load() || s.closing.Load() || s.deleting.Load() {
		return
	}
	for _, in := range s.Store.ListIntents() {
		if in.Status != model.StatusPending || in.Hold || in.Aborted || in.Wait != nil {
			continue
		}
		last, ok := s.Store.GetRun(in.RunID)
		if !ok || last.Status != model.RunCanceled {
			continue
		}
		if last.CancelCause != model.CancelCauseRestart && last.CancelCause != "" {
			continue
		}
		s.redispatchOne(last.ID, in.ID, last.CancelCause)
	}
}

// redispatchOne is the guarded restart: every check here is a reason a restart
// would be wrong, and the budget is checked before the run is started.
func (s *Server) redispatchOne(runID, intentID, cause string) {
	in, ok := s.Store.GetIntent(intentID)
	skip := ""
	switch {
	case !ok:
		skip = "意图不存在"
	case in.Hold:
		skip = "意图挂起"
		if in.HoldReason != "" {
			skip += "：" + in.HoldReason
		}
	case in.Aborted:
		skip = "意图已停止"
	case in.Wait != nil:
		skip = "意图仍有等待条件"
	case in.Status != model.StatusPending:
		skip = "当前意图状态为 " + in.Status
	}
	if skip != "" {
		s.noteRedispatch(runID, intentID, 0, cause, "skipped", skip, "")
		return
	}
	if in.RunID != "" {
		if live, ok := s.Store.GetRun(in.RunID); ok && live.Status == model.RunRunning {
			s.noteRedispatch(runID, intentID, 0, cause, "skipped", "意图已有在途运行 "+live.ID, "")
			return
		}
	}
	// used is the budget already spent, counted without the run being handled:
	// that run is the interruption itself, not one of the restarts paid for.
	// Restart-canceled runs are not counted at all (see interrupted).
	used := s.redispatchesUsed(intentID, runID)
	if used >= maxRedispatches {
		s.noteRedispatch(runID, intentID, used+1, cause, "skipped", "超过重派上限，留给指挥官决定", "")
		return
	}
	successor, err := s.startRunAfterInterruption(intentID, runID)
	if errors.Is(err, store.ErrAutoRedispatchSkipped) {
		s.noteRedispatch(runID, intentID, used+1, cause, "skipped", err.Error(), "")
		return
	}
	if err != nil {
		log.Printf("重派 %s（第 %d 次）：%v", intentID, used+1, err)
		s.noteRedispatch(runID, intentID, used+1, cause, "failed", err.Error(), "")
		return
	}
	s.noteRedispatch(runID, intentID, used+1, cause, "started", "", successor)
}

// noteRedispatch mirrors the outcome to the old run's journal and retains a
// reason when no successor was admitted.
func (s *Server) noteRedispatch(runID, intentID string, attempt int, cause, state, reason, successor string) {
	line := map[string]any{
		"kind": "run_redispatch", "at": time.Now(), "run_id": runID, "intent_id": intentID,
		"attempt": attempt, "cause": cause, "max": maxRedispatches, "state": state,
	}
	if successor != "" {
		line["successor_run_id"] = successor
	} else {
		reason = s.redactSecrets(reason)
		line["reason"] = reason
		if _, err := s.Store.UpdateRun(runID, func(r *model.Run) {
			r.RedispatchDisposition = &model.RedispatchDisposition{State: state, Reason: reason}
		}); err != nil {
			log.Printf("run %s: 保存重派结果失败：%v", runID, err)
		}
	}
	if err := s.appendJournalOnDisk(runID, line); err != nil {
		log.Printf("run %s: 记录重派失败：%v", runID, err)
	}
}
