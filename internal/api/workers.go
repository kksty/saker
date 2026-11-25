package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// workerCaller identifies a roster or control request.
type workerCaller struct {
	participant string
	intentID    string
	operator    bool
}

// workerView is one row of the participant-side roster: enough to decide
// whether to message, peek at or kill this worker, without the full ledger row
// (notes, handoff state, doc) that belongs to the console.
type workerView struct {
	IntentID      string `json:"intent_id"`
	ParticipantID string `json:"participant_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	CreatedBy     string `json:"created_by_participant_id,omitempty"`
	Node          string `json:"node,omitempty"`
	// Profile/ResolvedProfile are read-only history from the retired profile
	// layer; new workers never carry one (the gateway owns model routing).
	Profile         string `json:"profile,omitempty"`
	ResolvedProfile string `json:"resolved_profile,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	RunStatus       string `json:"run_status,omitempty"`
	Model           string `json:"model,omitempty"`
	Running         bool   `json:"running"`
	Hold            bool   `json:"hold,omitempty"`
	// HeldBy/HoldReason let an owner see whether it may restart a held worker:
	// the operator's or commander's stop is not the parent's to release.
	HeldBy     string `json:"held_by,omitempty"`
	HoldReason string `json:"hold_reason,omitempty"`
	// ParkReason/ParkedAt are the direction's recorded "set aside" decision. The
	// roster carries them because a parked row otherwise reads like an
	// unfinished task: the badge says 停放 but not why, and the reason is the
	// whole difference between "blocked, may resume" and "answered, do not
	// reopen".
	ParkReason    string     `json:"park_reason,omitempty"`
	ParkedAt      *time.Time `json:"parked_at,omitempty"`
	LastEventKind string     `json:"last_event_kind,omitempty"`
	LastEventText string     `json:"last_event_text,omitempty"`
	LastEventAt   *time.Time `json:"last_event_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CanKill       bool       `json:"can_kill"`
}

// agentWorkers lists the project's workers with enough live state to decide what
// to do next. authorized visibility is not restricted to one's own subtree: the
// roster is the "who is here" half of the language ability (peek needs target
// run ids), while kill is where ownership is enforced.
func (s *Server) agentWorkers(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.workersRoster(w, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID,
	})
}

// workersRoster is the one roster implementation for the operator and the agent.
func (s *Server) workersRoster(w http.ResponseWriter, caller workerCaller) {
	latest := s.latestRuns()
	intents := s.Store.ListIntents()
	out := make([]workerView, 0, len(intents))
	for _, in := range intents {
		out = append(out, s.workerViewOf(in, latest[in.ID], caller))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": out, "count": len(out)})
}

func (s *Server) workerViewOf(in model.Intent, run model.Run, caller workerCaller) workerView {
	runID := in.RunID
	if runID == "" && run.ID != "" {
		runID = run.ID
	}
	resolved := run.ResolvedProfile
	profile := in.Profile
	if profile == "" {
		profile = resolved
	}
	v := workerView{
		IntentID: in.ID, ParticipantID: collab.ParticipantID(in.ID),
		Title: in.Title, Status: in.Status,
		CreatedBy: in.CreatedBy,
		Node:      in.Node, Profile: profile, ResolvedProfile: resolved,
		RunID: runID,
		Hold:  in.Hold, CreatedAt: in.CreatedAt,
		HeldBy: in.HeldBy, HoldReason: in.HoldReason,
		ParkReason: in.ParkReason, ParkedAt: in.ParkedAt,
		CanKill: s.canKillWorker(caller, in),
	}
	if run.ID != "" {
		v.RunStatus = run.Status
		v.Model = run.Model
		v.LastEventKind = run.LastEventKind
		v.LastEventText = run.LastEventText
		v.LastEventAt = run.LastEventAt
		v.Running = in.Status == model.StatusRunning && run.Status == model.RunRunning
	}
	return v
}

// canKillWorker answers the roster's can_kill: may this caller kill the target
// right now. A finished worker is false for everyone (kill returns 409); the
// operator may kill any still-active worker; an executor is limited to what it
// created or its own descendants.
func (s *Server) canKillWorker(caller workerCaller, target model.Intent) bool {
	if model.Settled(target.Status) {
		return false
	}
	if caller.operator {
		return true
	}
	return false
}

// mayReleaseWorkerHold decides whether this caller may clear a held worker:
// only the actor that set the stop can release it, plus automatic liveness
// stops (watchdog). An operator/commander stop and an unknown legacy stop are
// not a parent's to override.
func (s *Server) mayReleaseWorkerHold(target model.Intent, caller workerCaller) bool {
	if caller.operator {
		return true
	}
	switch target.HeldBy {
	case model.HoldActorOperator, model.HoldActorCommander, "":
		return false
	case model.HoldActorWatchdog, model.HoldActorWorker:
		return true
	default:
		return target.HeldBy == caller.participant
	}
}

// heldWorkerText names the holder in operator language, so a refused restart
// says who must act instead of a bare 409.
func (s *Server) heldWorkerText(target model.Intent) string {
	who := target.HeldBy
	switch who {
	case model.HoldActorOperator:
		who = "操作员"
	case model.HoldActorCommander:
		who = "指挥官"
	case model.HoldActorWatchdog:
		who = "看门狗"
	case model.HoldActorWorker:
		who = "执行者"
	case model.HoldActorUnknown, "":
		who = "未记录"
	}
	reason := strings.TrimSpace(target.HoldReason)
	if reason != "" {
		reason = "：" + reason
	}
	if strings.HasPrefix(who, "pt_") {
		who = "发起执行者"
	}
	return "worker 由「" + who + "」停止" + reason + "；要重启请让停止者或操作员处理"
}

// killWorker is the one kill implementation for the operator and the agent. The
// operator bypasses the ownership rule (but not the terminal-state check); an
// executor is limited to what it created or its own descendants.
func (s *Server) killWorker(w http.ResponseWriter, r *http.Request, caller workerCaller) {
	raw := strings.TrimSpace(r.PathValue("id"))
	if intentID, ok := collab.IntentIDFromParticipant(raw); ok {
		raw = intentID
	}
	target, found := s.Store.GetIntent(raw)
	if !found {
		writeErr(w, http.StatusNotFound, "没有这个 worker："+raw)
		return
	}
	if model.Settled(target.Status) {
		writeErr(w, http.StatusConflict, "worker 已结束（"+target.Status+"）：不用再 kill")
		return
	}
	// Killing is not a way to seize a hold: a worker already stopped by the
	// operator/commander stays theirs. Otherwise a parent could kill it (which
	// re-stamps HeldBy to itself) and then restart, silently overriding the
	// operator's decision. The guard runs inside the store lock together with
	// the write: checking the snapshot first would let a stop that lands in
	// between be overwritten by this kill.
	actor := holdActorOf(caller)
	cur, applied, err := s.Store.UpdateIntentWhen(target.ID,
		func(i model.Intent) bool {
			return !model.Settled(i.Status) && (!i.Hold || s.mayReleaseWorkerHold(i, caller))
		},
		func(i *model.Intent) {
			i.Hold = true
			i.HeldBy = actor
			i.HoldReason = "worker kill：" + actor
			i.Aborted = true
			i.Wait = nil
		})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !applied {
		if now, ok := s.Store.GetIntent(target.ID); ok {
			if model.Settled(now.Status) {
				writeErr(w, http.StatusConflict, "worker 已结束（"+now.Status+"）：不用再 kill")
				return
			}
			target = now
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": s.heldWorkerText(target),
			"code":  "worker_held",
		})
		return
	}
	runID := cur.RunID
	canceled := false
	if runID != "" {
		if run, ok := s.Store.GetRun(runID); ok && run.Status == model.RunRunning {
			if err := s.CancelRunWithCause(runID, model.CancelCauseWorkerKill,
				"worker kill："+caller.participant); err != nil {
				// A running row with no live dispatcher (hand-edited ledger,
				// crash between AddRun and registration): settle it here so the
				// direction cannot stay running forever.
				s.settleKilledRun(runID, target.ID, actor)
			} else {
				canceled = true
			}
		}
	}
	if !canceled {
		// Nothing was live: make the stop final now.
		if _, err := s.Store.UpdateIntent(target.ID, func(i *model.Intent) {
			if !model.Settled(i.Status) {
				i.Status = model.StatusFailed
				i.Hold = true
				i.HeldBy = actor
				i.HoldReason = "worker kill：" + actor
				i.Aborted = true
				i.Wait = nil
				i.ClaimedBy = ""
			}
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	cur, _ = s.Store.GetIntent(target.ID)
	latest := s.latestRuns()
	writeJSON(w, http.StatusOK, map[string]any{
		"killed": true, "intent_id": target.ID, "run_id": runID,
		"cancel_cause": model.CancelCauseWorkerKill, "canceled": canceled,
		"worker": s.workerViewOf(cur, latest[cur.ID], caller),
	})
}

// holdActorOf records who is setting the stop: the operator, or the participant
// id of the executor that killed its own worker.
func holdActorOf(caller workerCaller) string {
	if caller.operator {
		return model.HoldActorOperator
	}
	if caller.participant != "" {
		return caller.participant
	}
	return model.HoldActorUnknown
}

// settleKilledRun is the fallback for a run row whose dispatcher is gone: it
// writes the terminal row directly with the worker_kill cause. Normal kills go
// through CancelRunWithCause and the dispatch finish path, not this.
func (s *Server) settleKilledRun(runID, intentID, actor string) {
	now := time.Now()
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		if r.Status != model.RunRunning {
			return
		}
		r.Status = model.RunCanceled
		r.CancelCause = model.CancelCauseWorkerKill
		r.ExitReason = "worker kill"
		r.EndedAt = &now
	})
	if _, err := s.Store.UpdateIntent(intentID, func(i *model.Intent) {
		if !model.Settled(i.Status) {
			i.Status = model.StatusFailed
			i.Hold = true
			i.HeldBy = actor
			i.HoldReason = "worker kill：" + actor
			i.Aborted = true
			i.Wait = nil
			i.ClaimedBy = ""
		}
	}); err != nil {
		// Best effort: the run row is already terminal, the intent keeps its
		// current status and the next settle/watchdog sweep reconciles it.
		return
	}
}
