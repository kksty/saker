package store

// Child delegation and waits.
//
// Both are one atomic state change with the ledger, not a side table: a child
// intent, its parent link and the parent's wait must survive a restart together,
// and the "child finished before the wait was registered" race must be evaluated
// under the same lock that records the wait. Everything here runs under s.mu and
// persists with flush() (a wait/child is a change the commander must see).

import (
	"errors"
	"fmt"
	"time"

	"github.com/kksty/saker/internal/model"
)

var ErrWaitInvalid = errors.New("等待条件不合法")

type SettleOutcome struct {
	Status string
	Wake   bool
	Wait   *model.Wait
	// Stale is true when the intent already moved to a newer run; the caller
	// must not notify a parent or schedule a wake for this turn.
	Stale bool
}

// SettleIntentForRun applies one run's terminal status to its intent under the
// lock. It is the only place that turns "turn ended" into "done / waiting /
// pending / failed", so the child results and the parent state cannot disagree.
//
// A stale finish (the intent has moved to a newer run) changes nothing: a steered
// direction must not be marked done by the turn it already replaced.
func (s *Store) SettleIntentForRun(intentID, runID, runStatus string, cancelCause ...string) (SettleOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok {
		return SettleOutcome{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if in.RunID != "" && in.RunID != runID {
		return SettleOutcome{Status: in.Status, Stale: true}, nil
	}
	now := time.Now()
	in.UpdatedAt = now
	if in.HandoffOperation != "" {
		// Only the replacement owner may consume the old wait. The old run can
		// finish normally or be canceled while BeginHandoff is stopping it.
		in.Status, in.ClaimedBy, in.Hold = model.StatusPending, "", true
		if runStatus == model.RunCanceled && in.Attempts > 0 {
			in.Attempts--
		}
		if err := s.flush("intent"); err != nil {
			return SettleOutcome{}, err
		}
		return SettleOutcome{Status: in.Status, Wait: detachWait(in.Wait)}, nil
	}
	if runStatus == model.RunCanceled && len(cancelCause) > 0 &&
		(cancelCause[0] == model.CancelCauseOperatorWait || cancelCause[0] == model.CancelCauseActionWait) &&
		in.Wait != nil && in.Wait.RegisteredByRun == runID && !in.Hold &&
		((cancelCause[0] == model.CancelCauseOperatorWait) == (in.Wait.Operator != nil)) &&
		((cancelCause[0] == model.CancelCauseActionWait) == (len(in.Wait.Actions) > 0)) {
		// The actual run stays canceled; only its business settlement follows
		// the normal yielded-turn path. A concurrent operator cancel that has
		// cleared the wait cannot accidentally be turned into success.
		runStatus = model.RunDone
	}
	switch runStatus {
	case model.RunDone:
		if in.Wait != nil && in.Wait.RegisteredByRun == runID {
			ready, results, _, reason := s.evaluateWaitLocked(in.Wait, now)
			if ready {
				in.Status = model.StatusPending
				in.Hold = false
				in.Wait.WakePending = true
				in.Wait.WakeReason = reason
				in.Wait.Results = results
				if err := s.flush("intent"); err != nil {
					return SettleOutcome{}, err
				}
				return SettleOutcome{Status: in.Status, Wake: true, Wait: detachWait(in.Wait)}, nil
			}
			in.Status = model.StatusWaiting
			if err := s.flush("intent"); err != nil {
				return SettleOutcome{}, err
			}
			return SettleOutcome{Status: in.Status, Wait: detachWait(in.Wait)}, nil
		}
		in.Status = model.StatusDone
		in.Hold = false
		in.HeldBy, in.HoldReason = "", ""
		in.Wait = nil
	case model.RunCanceled:
		in.Wait = nil
		hold := in.Aborted || len(cancelCause) > 0 && model.CancelHoldsIntent(cancelCause[0])
		cause := ""
		if len(cancelCause) > 0 {
			cause = cancelCause[0]
		}
		// Publish an explicit stop and its Hold together. A separate follow-up
		// write would expose pending work to the scheduler between commits.
		if hold {
			// The actor is normally stamped before the run is cancelled (intent
			// stop / worker kill). A run-level cancel has no such stamp: derive
			// the two causes that can only come from one actor.
			if in.HeldBy == "" {
				switch cause {
				case model.CancelCauseOperator:
					in.HeldBy = model.HoldActorOperator
				case model.CancelCauseModelStall:
					in.HeldBy = model.HoldActorWatchdog
				}
			}
			in.Status = model.StatusFailed
			in.Hold = true
			in.ClaimedBy = ""
		} else {
			// A non-holding cancellation leaves Hold alone: the timeout path
			// sets Hold deliberately before settling (watchdog cause), and only
			// an explicit retry/run may clear it. Clearing here would turn a
			// timed-out direction back into an auto-redispatch loop.
			in.Status = model.StatusPending
			in.ClaimedBy = ""
		}
		if in.Attempts > 0 {
			in.Attempts--
		}
	case model.RunFailed:
		in.Status = model.StatusFailed
		in.Hold = false
		in.HeldBy, in.HoldReason = "", ""
		in.Wait = nil
	default:
		in.Status = model.StatusFailed
		in.Wait = nil
	}
	if err := s.flush("intent"); err != nil {
		return SettleOutcome{}, err
	}
	return SettleOutcome{Status: in.Status}, nil
}

type WaitState struct {
	Ready   bool
	Reason  string
	Results []model.WaitResult
	Pending []string
}

// RegisterWait records a wait on one intent. If the condition is already
// satisfied (a child finished before the parent registered), it returns ready
// immediately and stores no wait: the parent's current turn can continue.
//
// Child results are collected while the same lock is held, so a concurrent
// finish cannot slip between "evaluate" and "record the wait".
func (s *Store) RegisterWait(intentID, runID string, wait model.Wait) (WaitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if wait.Operator != nil {
		return WaitState{}, fmt.Errorf("%w：人工条件必须通过人工交接接口登记", ErrWaitInvalid)
	}
	in, ok := s.intentLocked(intentID)
	if !ok {
		return WaitState{}, fmt.Errorf("意图 %s 不存在", intentID)
	}
	if runID != "" && in.RunID != "" && in.RunID != runID {
		return WaitState{}, fmt.Errorf("%w：执行轮次已更换", ErrWaitInvalid)
	}
	switch in.Status {
	case model.StatusDone, model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
		return WaitState{}, fmt.Errorf("意图 %s 是 %s：不再等待", intentID, in.Status)
	}
	if len(wait.Actions) == 0 {
		return WaitState{}, fmt.Errorf("%w：请指定外部动作", ErrWaitInvalid)
	}
	if in.Wait != nil && in.Wait.Operator != nil {
		return WaitState{}, fmt.Errorf("%w：正在等待人工操作，不能覆盖", ErrWaitInvalid)
	}
	if wait.Mode == "" {
		wait.Mode = model.WaitAll
	}
	if wait.Mode != model.WaitAll && wait.Mode != model.WaitAny {
		return WaitState{}, fmt.Errorf("%w：mode 只能是 all 或 any", ErrWaitInvalid)
	}
	seen := map[string]bool{}
	for _, id := range wait.Actions {
		if id == "" || seen[id] {
			return WaitState{}, fmt.Errorf("%w：动作 id 为空或重复", ErrWaitInvalid)
		}
		seen[id] = true
		if _, ok := s.actionLocked(id); !ok {
			return WaitState{}, fmt.Errorf("%w：外部动作 %s 不存在", ErrWaitInvalid, id)
		}
	}
	now := time.Now()
	wait.RegisteredAt = now
	wait.RegisteredByRun = runID
	wait.WakePending = false
	wait.WakeReason = ""
	ready, results, pending, reason := s.evaluateWaitLocked(&wait, now)
	if ready {
		// The condition is already true. A running turn keeps running with the
		// results in hand; a waiting direction is put back on the queue. Drop a
		// wait from an earlier registration: otherwise the turn's finish would
		// later see it, wake the direction a second time, and duplicate the run.
		changed := in.Wait != nil
		in.Wait = nil
		if in.Status == model.StatusWaiting || in.Status == model.StatusPending {
			in.Status = model.StatusPending
			in.Hold = false
			changed = true
		}
		if changed {
			in.UpdatedAt = now
			if err := s.flush("intent"); err != nil {
				return WaitState{}, err
			}
		}
		return WaitState{Ready: true, Reason: reason, Results: results, Pending: pending}, nil
	}
	wait.Results = results
	in.Wait = detachWait(&wait)
	if in.Status == model.StatusPending {
		in.Status = model.StatusWaiting
	}
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return WaitState{}, err
	}
	return WaitState{Ready: false, Reason: reason, Results: results, Pending: pending}, nil
}

// EvaluateWait recomputes a stored wait without changing it. It is used when a
// child settles and when the watchdog checks deadlines.
func (s *Store) EvaluateWait(intentID string) (WaitState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil {
		return WaitState{}, false
	}
	ready, results, pending, reason := s.evaluateWaitLocked(in.Wait, time.Now())
	return WaitState{Ready: ready, Reason: reason, Results: results, Pending: pending}, true
}

// SatisfyWait marks a stored wait as ready to wake and records the structured
// results. It is called after a child settles.
func (s *Store) SatisfyWait(intentID string) (model.Intent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(intentID)
	if !ok || in.Wait == nil {
		return model.Intent{}, false, nil
	}
	ready, results, _, reason := s.evaluateWaitLocked(in.Wait, time.Now())
	if !ready {
		return detachIntent(*in), false, nil
	}
	wait := *in.Wait
	wait.WakePending = true
	wait.WakeReason = reason
	wait.Results = results
	in.Wait = detachWait(&wait)
	now := time.Now()
	in.UpdatedAt = now
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, false, err
	}
	return detachIntent(*in), true, nil
}

func (s *Store) intentLocked(id string) (*model.Intent, bool) {
	for i := range s.db.Intents {
		if s.db.Intents[i].ID == id {
			return &s.db.Intents[i], true
		}
	}
	return nil, false
}

// evaluateWaitLocked is the single definition of readiness. Caller holds s.mu.
func (s *Store) evaluateWaitLocked(w *model.Wait, now time.Time) (ready bool, results []model.WaitResult, pending []string, reason string) {
	if len(w.Actions) > 0 {
		return s.evaluateActionsLocked(w, now)
	}
	if w.Operator != nil {
		if w.Operator.Resolution == model.OperatorUnavailable {
			return w.Operator.CompletedAt != nil, nil, nil, "operator_unavailable"
		}
		return w.Operator.CompletedAt != nil, nil, nil, "operator_completed"
	}
	return false, nil, nil, ""
}
