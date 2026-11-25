package api

import (
	"strconv"
	"strings"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// Operator-facing project state. The console used to derive "the project is
// finished" from "paused plus every goal met", which cannot tell a completion
// stand-by from a manual pause and says nothing about the directions still
// waiting for a decision. Every action the console can offer is therefore
// decided here, once, and the resume handler asks the same function: a button
// that looks available and a request that is refused can no longer disagree.
const (
	actionPause      = "pause"
	actionResume     = "resume"
	actionAppendGoal = "append_goal"
)

// Reason codes are the contract with the console. The prose next to them is a
// sentence, not a rule: copy may be re-worded, the code may not.
const (
	reasonOK                  = "ok"
	reasonAlreadyPaused       = "already_paused"
	reasonAlreadyRunning      = "already_running"
	reasonCloseoutInProgress  = "closeout_in_progress"
	reasonDeleting            = "deleting"
	reasonNoLedger            = "no_ledger"
	reasonNothingToContinue   = "nothing_to_continue"
	reasonPausedManual        = "paused_manual"
	reasonPausedCompletion    = "paused_completion"
	reasonPausedCommander     = "paused_commander"
	reasonPausedRestart       = "paused_restart"
	reasonPausedUnrecorded    = "paused_unrecorded"
	reasonProviderFaultResume = "provider_fault_recovery"
	reasonGoalMetPendingWork  = "goal_met_pending_work"
	reasonGoalMetNothingToDo  = "goal_met_no_work"
)

// ProjectAction is one action plus why it is or is not available.
type ProjectAction struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// ReasonCode is the machine code; Reason is one sentence for the operator.
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
}

// ProjectFacts is what the decisions above were made from, sent along so the
// console can explain a disabled action without re-deriving anything. Counts
// are cheap: the ledger is one project, and the board handler already walks it.
type ProjectFacts struct {
	HasLedger  bool `json:"has_ledger"`
	GoalsTotal int  `json:"goals_total"`
	GoalsMet   int  `json:"goals_met"`
	// GoalsOpen counts goals that are not met yet. A met-but-stale receipt is
	// open work again (the endpoint is met on paper only). A met goal that was
	// never explicitly closed is NOT open work: goalsClosed has always treated
	// it as reached, and treating it as open would disable 继续 on exactly the
	// completion stand-by the button exists for.
	GoalsOpen   int `json:"goals_open"`
	GoalsClosed int `json:"goals_closed,omitempty"`
	// GoalsActive counts goals that were never explicitly closed. Closing a goal
	// withdraws the endpoint (it does not mean reached), so a project with no
	// active goal has nothing to judge and keeps 继续 available — the same rule
	// goalsClosed always expressed by returning false.
	GoalsActive int `json:"goals_active"`
	// GoalsStale says why an apparently finished goal is open again.
	GoalsStale int `json:"goals_stale_receipt,omitempty"`
	// Pending is directions a resume would actually have something to do for.
	Pending  int  `json:"pending_directions"`
	Held     int  `json:"held_directions,omitempty"`
	Parked   int  `json:"parked_directions,omitempty"`
	Running  int  `json:"running_directions,omitempty"`
	Unread   int  `json:"unread_messages,omitempty"`
	Closeout bool `json:"closeout,omitempty"`
	Deleting bool `json:"deleting,omitempty"`
}

// ProjectNote is one fact that needs to be said out loud next to the buttons,
// because two things are true at once ("every goal is met" and "three
// directions still need a decision").
type ProjectNote struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// ProjectActions is the single payload the console renders.
type ProjectActions struct {
	State   projectState    `json:"state"`
	Facts   ProjectFacts    `json:"facts"`
	Actions []ProjectAction `json:"actions"`
	Notes   []ProjectNote   `json:"notes,omitempty"`
}

// projectActions snapshots the operator contract under controlMu. The pause
// cause is guarded by that mutex, so the classification and the counts must be
// read under the same lock as the run handlers that set it.
func (s *Server) projectActions() ProjectActions {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.projectActionsLocked()
}

func (s *Server) projectActionsLocked() ProjectActions {
	st := s.projectStateLocked()
	facts := s.projectFactsLocked()
	return ProjectActions{
		State:   st,
		Facts:   facts,
		Actions: []ProjectAction{s.pauseActionLocked(st, facts), s.resumeActionLocked(st, facts), s.appendGoalActionLocked(facts)},
		Notes:   projectNotes(st, facts),
	}
}

// pauseActionLocked mirrors projectPause: a pause is always available while
// running, and it also cancels a closeout in progress (that is what the handler
// does), so closeout is not a reason to disable it.
func (s *Server) pauseActionLocked(st projectState, facts ProjectFacts) ProjectAction {
	if facts.Deleting {
		return ProjectAction{ID: actionPause, Enabled: false, ReasonCode: reasonDeleting, Reason: "项目正在删除，不能暂停"}
	}
	if st.Paused {
		return ProjectAction{ID: actionPause, Enabled: false, ReasonCode: reasonAlreadyPaused, Reason: "项目已暂停"}
	}
	return ProjectAction{ID: actionPause, Enabled: true, ReasonCode: reasonOK, Reason: "暂停项目：停止指挥官轮次并取消在跑运行（意图回到待派）"}
}

// resumeActionLocked is the one place that decides "does 继续 do anything".
// projectResume refuses on exactly these codes, so the button and the handler
// cannot drift apart.
func (s *Server) resumeActionLocked(st projectState, facts ProjectFacts) ProjectAction {
	switch {
	case facts.Deleting:
		return ProjectAction{ID: actionResume, Enabled: false, ReasonCode: reasonDeleting, Reason: "项目正在删除"}
	case facts.Closeout:
		return ProjectAction{ID: actionResume, Enabled: false, ReasonCode: reasonCloseoutInProgress, Reason: "指挥官正在收口：等本轮结束，或先暂停取消"}
	case !st.Paused:
		return ProjectAction{ID: actionResume, Enabled: false, ReasonCode: reasonAlreadyRunning, Reason: "项目没有暂停"}
	case st.Kind == pauseKindProviderFault:
		// A provider fault is the one pause whose recovery IS 继续: refusing it
		// would leave the operator with no way back after fixing the key.
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonProviderFaultResume, Reason: "模型或凭据故障暂停：修好配置后继续即恢复"}
	case !facts.continuable():
		return ProjectAction{ID: actionResume, Enabled: false, ReasonCode: reasonNothingToContinue, Reason: "项目已收口：没有未达成的目标、待办意图或待读消息；继续只会空跑一轮"}
	}
	switch st.Kind {
	case pauseKindCompletionStandby:
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonPausedCompletion, Reason: "目标已达成后自动暂停：继续会在当前目标下再跑一轮"}
	case pauseKindCommanderStandby:
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonPausedCommander, Reason: "指挥官请求停止：只有操作员能继续"}
	case pauseKindRestart:
		// The watchdog clears this pause on its own once the crash sweep is done;
		// seeing it here means that recovery did not finish (for example the
		// manifest write failed), so it is the operator's fallback.
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonPausedRestart, Reason: "控制面重启后自动恢复未完成：确认现场后继续"}
	case pauseKindUnrecorded:
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonPausedUnrecorded, Reason: "暂停原因未记录：继续会重新派发待办意图"}
	default:
		return ProjectAction{ID: actionResume, Enabled: true, ReasonCode: reasonPausedManual, Reason: "人工暂停：继续会重新派发待办意图"}
	}
}

func (s *Server) appendGoalActionLocked(facts ProjectFacts) ProjectAction {
	switch {
	case facts.Deleting:
		return ProjectAction{ID: actionAppendGoal, Enabled: false, ReasonCode: reasonDeleting, Reason: "项目正在删除"}
	case !facts.HasLedger:
		return ProjectAction{ID: actionAppendGoal, Enabled: false, ReasonCode: reasonNoLedger, Reason: "这个项目没有账本，不能追加目标"}
	}
	return ProjectAction{ID: actionAppendGoal, Enabled: true, ReasonCode: reasonOK, Reason: "追加一个新目标并继续（不改动已达成目标的完成收据）"}
}

// projectNotes says the two-sided truths out loud. "All goals met" and "three
// directions still open" are both true after a completion stand-by with work in
// flight; a single status word would hide one of them.
func projectNotes(st projectState, f ProjectFacts) []ProjectNote {
	var notes []ProjectNote
	closed := f.HasLedger && f.GoalsActive > 0 && f.GoalsOpen == 0 && f.GoalsStale == 0
	switch {
	case closed && (f.Pending > 0 || f.Unread > 0):
		var parts []string
		if f.Pending > 0 {
			parts = append(parts, strconv.Itoa(f.Pending)+" 个待办意图")
		}
		if f.Unread > 0 {
			parts = append(parts, strconv.Itoa(f.Unread)+" 条未读消息")
		}
		notes = append(notes, ProjectNote{
			Code: reasonGoalMetPendingWork,
			Text: "目标已全部达成，但仍有 " + strings.Join(parts, "、") + "：继续会处理它们，不会改写已完成的目标",
		})
	case closed:
		notes = append(notes, ProjectNote{
			Code: reasonGoalMetNothingToDo,
			Text: "目标已全部达成，且没有待办意图或未读消息（项目已收口）",
		})
	}
	if st.Paused && st.Kind == pauseKindProviderFault {
		notes = append(notes, ProjectNote{Code: pauseKindProviderFault, Text: "项目因模型或凭据故障暂停：先修好配置再继续"})
	}
	if f.Closeout {
		notes = append(notes, ProjectNote{Code: reasonCloseoutInProgress, Text: "指挥官正在收口：本轮结束后才会接受新的派发"})
	}
	return notes
}

// continuable is the one rule for "an explicit resume has work to act on".
// It used to live in continueWork as its own switch; the counters above are now
// the single source so a reason code can say *which* part is missing.
func (f ProjectFacts) continuable() bool {
	if !f.HasLedger || f.GoalsTotal == 0 {
		// Nothing chartered yet: a fresh engagement, not a finished one.
		return true
	}
	if f.GoalsActive == 0 {
		// Every goal was withdrawn. There is no endpoint left to judge, so 继续
		// stays available exactly as goalsClosed (active == 0 ⇒ false) had it.
		return true
	}
	if f.GoalsOpen > 0 || f.GoalsStale > 0 {
		return true
	}
	return f.Pending > 0 || f.Unread > 0
}

// projectFactsLocked counts what the decisions above need in one pass.
func (s *Server) projectFactsLocked() ProjectFacts {
	f := ProjectFacts{HasLedger: s.Store != nil, Closeout: s.closeout.Load(), Deleting: s.deleting.Load()}
	if s.Store == nil {
		return f
	}
	for _, g := range s.Store.Goals() {
		f.GoalsTotal++
		if g.Closed {
			f.GoalsClosed++
			continue
		}
		f.GoalsActive++
		switch {
		case g.Met && !g.ReceiptStale:
			f.GoalsMet++
		case g.Met && g.ReceiptStale:
			f.GoalsStale++
			f.GoalsOpen++
		default:
			f.GoalsOpen++
		}
	}
	for _, in := range s.Store.ListIntents() {
		switch in.Status {
		case model.StatusRunning:
			f.Running++
			f.Pending++
		case model.StatusClaimed, model.StatusWaiting, model.StatusReviewing:
			f.Pending++
		case model.StatusParked:
			f.Parked++
			f.Pending++
		case model.StatusPending, model.StatusFailed, model.StatusReviewFailed:
			if in.Hold {
				f.Held++
			} else {
				f.Pending++
			}
		}
	}
	for _, m := range s.Store.PendingFor(collab.CommanderID) {
		if m.NoticeFactID == "" && m.Kind != model.NoticeKindFact {
			f.Unread++
		}
	}
	return f
}
