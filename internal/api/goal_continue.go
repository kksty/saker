package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/store"
)

// Machine-readable project states for the explicit "append a goal and continue"
// action. A plain "paused" boolean cannot tell a completion stand-by (the
// chartered work finished) from a manual pause (the operator stopped it) from a
// restart pause (the machine parked a running project) from a credential fault (a
// human must repair the key/profile first): the first three are resumable by this
// explicit action, the fault is refused, an unattributable pause is left for the
// operator to resume deliberately, and waiting work is not a pause at all. The
// console switches its copy on these codes instead of parsing the human-readable
// reason.
const (
	pauseKindRunning           = "running"
	pauseKindWaitingHuman      = "waiting_human"
	pauseKindWaitingOther      = "waiting_other"
	pauseKindWaitingAction     = "waiting_action"
	pauseKindWaitingResume     = "waiting_resume"
	pauseKindCompletionStandby = project.PauseKindCompletionStandby
	pauseKindManualPause       = project.PauseKindManualPause
	pauseKindCommanderStandby  = project.PauseKindCommanderStandby
	pauseKindRestart           = project.PauseKindRestart
	pauseKindProviderFault     = "provider_fault"
	// pauseKindUnrecorded is a paused project whose manifest carries no cause: a
	// pause written before the codes existed. It is not manual_pause — nobody
	// knows who paused it, and the console must not invent an author.
	pauseKindUnrecorded = "unrecorded"
)

// Structured causes written by the pause paths in api.go. They are deliberately
// not the console prose: the reason is for people, the cause is what the resume
// contract switches on.
const (
	pauseCauseManual     = "manual"
	pauseCauseCompletion = "completion"
	// stopReason* are the structured reasons a commander may attach to
	// POST /api/project/standby. goal_achieved is the only one the control plane
	// can check against the ledger: it is refused while any goal is still open,
	// so "目标已达成" cannot be a prose claim without a stamp.
	stopReasonGoalAchieved = "goal_achieved"
	stopReasonBlocked      = "blocked"
	stopReasonLowValue     = "low_value"
	// pauseCauseCommander is the commander's explicit "stop this engagement"
	// request (POST /api/project/standby). It is a real pause: persists across
	// restarts, cancels executing directions, and only the operator can resume.
	// It is distinct from the operator's own pause so the console can say who
	// stopped the project instead of guessing from the reason text.
	pauseCauseCommander = "commander"
	// pauseCauseRestart is the boot path's forced pause of a project that was
	// running. Recorded so a restart is never reported as a manual pause.
	pauseCauseRestart = "restart"
)

// projectState is the classified parking state of an engagement. Reason is the
// decider's human-readable pause reason (free text, for display only); Kind is
// the machine code the append-and-continue contract uses.
type projectState struct {
	Paused bool   `json:"paused"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// projectState is projectStateLocked with the control mutex taken. Readers that
// already hold controlMu (the append-and-continue section) must call the locked
// variant instead: pauseCause is guarded by that mutex.
func (s *Server) projectState() projectState {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.projectStateLocked()
}

// projectStateLocked classifies the current pause without guessing from prose
// where a structured cause exists:
//
//   - a completion stand-by, a manual pause, a commander stop request and the
//     boot path's forced pause are recorded in pauseCause by the paths that apply
//     them;
//   - a paused project whose manifest carries no cause is reported as
//     "unrecorded", not as a manual pause: the manifest does not say who paused
//     it (a pause written before the codes existed), and inventing the operator's
//     name is the one answer that can be plainly false;
//   - the credential-fault path is the only production path that pauses dispatch
//     without persisting the manifest flag (a human must repair the key first),
//     so "runtime paused while the manifest still says running" is the fault;
//   - not paused but with a waiting direction is waiting_human: live work that
//     must not be disturbed, not a park.
func (s *Server) projectStateLocked() projectState {
	st := projectState{Paused: s.paused.Load()}
	if s.Decider != nil {
		st.Reason = s.Decider.Status().PausedReason
	}
	if st.Paused {
		switch s.pauseCause {
		case pauseCauseCompletion:
			st.Kind = pauseKindCompletionStandby
		case pauseCauseCommander:
			st.Kind = pauseKindCommanderStandby
		case pauseCauseManual:
			st.Kind = pauseKindManualPause
		case pauseCauseRestart:
			st.Kind = pauseKindRestart
		default:
			if s.Project != nil && !s.Project.IsPaused() {
				st.Kind = pauseKindProviderFault
			} else {
				st.Kind = pauseKindUnrecorded
			}
		}
		return st
	}
	st.Kind = pauseKindRunning
	if s.Store != nil {
		for _, in := range s.Store.ListIntents() {
			if in.Status == model.StatusWaiting {
				if in.Wait == nil || in.Wait.WakePending || (in.Wait.Operator != nil && in.Wait.Operator.CompletedAt != nil) {
					if st.Kind == pauseKindRunning {
						st.Kind = pauseKindWaitingResume
					}
					continue
				}
				if in.Wait != nil && in.Wait.Operator != nil && in.Wait.Operator.CompletedAt == nil {
					st.Kind = pauseKindWaitingHuman
					break
				}
				if in.Wait != nil && len(in.Wait.Actions) > 0 {
					st.Kind = pauseKindWaitingAction
				} else if st.Kind == pauseKindRunning {
					st.Kind = pauseKindWaitingOther
				}
			}
		}
	}
	return st
}

// resumeScope is the one-shot target attached by the operator's "continue
// current goal" action. StartCommander consumes it and injects a section into
// the next commander prompt, so the round knows it is continuing that open goal
// without a persistent instruction or a chat message.
type resumeScope struct {
	GoalID    string
	Statement string
}

func (s *Server) setResumeScope(scope resumeScope) {
	if s == nil || strings.TrimSpace(scope.GoalID) == "" {
		return
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	sc := scope
	s.resumeScope = &sc
}

func (s *Server) consumeResumeScope() *resumeScope {
	if s == nil {
		return nil
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	sc := s.resumeScope
	s.resumeScope = nil
	return sc
}

// goalsClosed reports whether the chartered endpoint is reached: at least one
// goal exists and every one is met with a current receipt.
func (s *Server) goalsClosed() bool {
	if s == nil || s.Store == nil {
		return false
	}
	goals := s.Store.Goals()
	if len(goals) == 0 {
		return false
	}
	active := 0
	for _, g := range goals {
		if g.Closed {
			continue
		}
		active++
		// A stale receipt means the completion still needs a new judgement.
		if !g.Closed && (!g.Met || g.ReceiptStale) {
			return false
		}
	}
	return active > 0
}

// hasOperatorInput reports whether the commander's queue holds a message that
// asks it to do something (operator or task speech). Mechanical fact notices are
// bookkeeping, not a new direction.
func (s *Server) hasOperatorInput() bool {
	if s == nil || s.Store == nil {
		return false
	}
	for _, m := range s.Store.PendingFor(collab.CommanderID) {
		if m.NoticeFactID == "" && m.Kind != model.NoticeKindFact {
			return true
		}
	}
	return false
}

// closedRoundSection scopes a round that only exists because the operator spoke
// after the engagement was closed. Without it the commander has to reconcile
// "every goal is met" with "act on this message", and its own contract leaves
// both answers open (messages may supplement the task; goals are the operator's).
const closedRoundSection = `
## 已收口项目的补测轮
所有终点目标都已达成，完成收据保持不变。本轮之所以开轮，是因为队列里有操作员/执行者消息：按这些消息和当前有效指令处理，产出照常写成事实（结论仍需证据，候选仍按复核规则处理）。不要创建、改写或追加目标，也不要把本轮工作说成新的目标达成；如果消息要求的工作已经超出这些目标的范围，先说明并请操作员追加目标。处理完就结束本轮：没有待办时项目会再次自动暂停。
`

// continueWork reports whether an explicit resume has anything to act on.
//
// Resuming is a dispatch re-arm, not a state toggle: waking the commander is a
// forced round (decider.Run skips its attention gate for "operator"), so a
// closed project with no open goal, no unsettled direction and no unread
// operator message would spend a model round reading a board that already says
// "done", then settle right back into completion stand-by.
//
// The rule itself lives on ProjectFacts.continuable: the same counters feed the
// console's 继续 button, so the button and this check cannot disagree about
// whether a resume would do anything. A project without goals, a project without
// a ledger, and a provider-fault pause all stay resumable so recovery stays
// available.
func (s *Server) continueWork() bool {
	if s == nil {
		return true
	}
	return s.projectFactsLocked().continuable()
}

// appendGoalAndContinue is the explicit operator action for a finished or parked
// engagement: append a NEW independent goal and, when the project is parked in a
// resumable stand-by (completion, a manual pause, or the commander's own stop
// request this request observes), clear the pause and wake the commander so the
// new goal is acted on. It never touches a stamped goal's receipt: completion
// history stays exactly what was claimed.
//
// It is not reachable from the harness surface: the route lives under
// /api/project/, which the agent-token gate (cmd/saker auth.agentRouteHidden)
// treats as operator-only, and the participant mux never registers it.
//
// The body carries an idempotency_key: a retry after a lost response or after a
// failed resume finds the goal it already created instead of appending a second
// one, then completes (or reports again) the resume. Fault-paused projects are
// refused before the goal is written, so a refused request has no side effect.
func (s *Server) appendGoalAndContinue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement        string `json:"statement"`
		Start            string `json:"start"`
		QuantifiedTarget string `json:"quantified_target"`
		IdempotencyKey   string `json:"idempotency_key"`
		Note             string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error(), "code": "goal_invalid"})
		return
	}
	req.Statement = strings.TrimSpace(req.Statement)
	req.Start = strings.TrimSpace(req.Start)
	req.QuantifiedTarget = strings.TrimSpace(req.QuantifiedTarget)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case req.Statement == "":
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "statement 不能为空：目标要能判断真假", "code": "goal_invalid"})
		return
	case req.IdempotencyKey == "":
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "idempotency_key 必填：响应丢失或恢复失败后要用同一个 key 重试，不能重复追加目标",
			"code":  "goal_invalid"})
		return
	case s.Store == nil:
		writeErr(w, http.StatusServiceUnavailable, "这个项目没有账本，不能追加目标")
		return
	}

	// Serialise classify+resume with projectPause/AutoPause/projectResume: the
	// explicit "continue" must not race a concurrent pause into a half-applied
	// state. The append runs in the same section; a goal write is a short SQLite
	// transaction.
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.closeout.Load() {
		writeErr(w, http.StatusConflict, "指挥官正在收口，请等待本轮结束或先暂停取消")
		return
	}

	before := s.projectStateLocked()
	if before.Kind == pauseKindProviderFault {
		// A credential fault is not ours to clear: a human must fix the external
		// gateway first. Refuse before writing anything, so the
		// refusal leaves the ledger untouched.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             "项目因模型/凭据故障而暂停：先修复外部模型网关，再用同一个 idempotency_key 重试",
			"code":              "project_fault_paused",
			"pause_kind":        before.Kind,
			"pause_kind_before": before.Kind,
			"pause_reason":      before.Reason,
			"created":           false,
			"resumed":           false,
			"needs_human":       true,
		})
		return
	}

	goal, created, err := s.Store.AddGoalIdempotent(model.Goal{
		Statement:        req.Statement,
		Start:            req.Start,
		QuantifiedTarget: req.QuantifiedTarget,
		IdempotencyKey:   req.IdempotencyKey,
		UpdatedBy:        documentAuthor(r),
	})
	if err != nil {
		if errors.Is(err, store.ErrGoalIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "goal_idempotency_conflict"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "追加目标失败：" + err.Error(), "code": "goal_append_failed"})
		return
	}
	if created {
		text := goal.Statement
		if req.Note != "" {
			text += "（" + req.Note + "）"
		}
		s.recordAction("goal", goal.ID, text)
	}

	// Re-classify after the append: the credential-fault path does not take
	// controlMu, so a fault that landed during this request must still keep the
	// project paused. The goal stays (retrying with the same key finds it) and a
	// retry after the human fix completes the resume.
	current := s.projectStateLocked()
	if current.Paused && current.Kind == pauseKindProviderFault {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             "目标已追加，但项目在请求期间因模型/凭据故障暂停：未恢复项目；修复后用同一个 idempotency_key 重试会找回该目标并继续",
			"code":              "project_fault_paused",
			"goal":              goal,
			"created":           created,
			"resumed":           false,
			"retryable":         true,
			"pause_kind_before": before.Kind,
			"pause_kind_after":  current.Kind,
			"pause_reason":      current.Reason,
		})
		return
	}

	resumedFrom := ""
	if current.Paused {
		switch current.Kind {
		// Which pauses may this explicit append clear? The test is whether the
		// pause carries a policy decision that appending a goal does not answer:
		//
		//   - completion stand-by and manual pause: the operator is literally
		//     saying "there is more to do, keep going".
		//   - commander stand-by: the commander asked to stop, and only the
		//     operator may override it — this request is that override.
		//   - restart pause: the machine paused itself because the control plane
		//     restarted while the project was running. The boot recovery normally
		//     clears it already; this branch is the fallback when that recovery
		//     did not finish. It records no intent to stop, so there is nothing to
		//     override, and leaving it out would make an operator's "append a goal
		//     and continue" silently do half its job for a reason nobody chose.
		case pauseKindCompletionStandby, pauseKindManualPause, pauseKindCommanderStandby, pauseKindRestart:
			resumedFrom = current.Kind
		}
		// unrecorded stays paused on purpose: the manifest does not say who
		// paused it, so it may be a commander's considered stop. Auto-resuming
		// would overwrite a decision we cannot see; the operator gets a pause
		// banner and resumes deliberately with 继续项目 instead.
	}
	resumed := false
	if resumedFrom != "" {
		if err := s.setProjectPaused(false); err != nil {
			// The manifest write failed, so the runtime pause was never flipped:
			// the project stays paused exactly as the manifest still says. The
			// goal exists; a retry with the same key finds it and retries the
			// resume, so the failure is reported as retryable, not final.
			after := s.projectStateLocked()
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":             "目标已追加，但无法保存继续状态，项目保持暂停；用同一个 idempotency_key 重试会找回该目标并完成恢复：" + err.Error(),
				"code":              "resume_failed",
				"goal":              goal,
				"created":           created,
				"resumed":           false,
				"retryable":         true,
				"pause_kind_before": before.Kind,
				"pause_kind_after":  after.Kind,
				"pause_reason":      after.Reason,
			})
			return
		}
		s.resumeProjectRuntimeLocked()
		resumed = true
		// Same kick as projectResume: the scheduler was gated while paused, so
		// pending children and satisfied waits need one explicit sweep.
		go s.pump()
	}

	woken := false
	if (created || resumed) && s.Decider != nil && !s.Decider.Paused() && !s.paused.Load() {
		// A forced wake, not a heartbeat: the commander must act on the new goal
		// without waiting for the board digest to move. A duplicate request that
		// created and resumed nothing does not wake a second time.
		s.Decider.Wake("operator")
		woken = true
	}

	after := s.projectStateLocked()
	note := ""
	switch {
	case resumed && resumedFrom == pauseKindCompletionStandby:
		note = "项目此前处于完成待命，已追加目标并显式恢复派发；旧目标的完成收据保持不变。"
	case resumed && resumedFrom == pauseKindCommanderStandby:
		note = "项目此前由指挥官请求停止，已按本次显式操作追加目标并恢复派发；指挥官请求停止的原因仍在它的暂停说明里。"
	case resumed && resumedFrom == pauseKindRestart:
		note = "项目此前因控制面重启而暂停、自动恢复未完成，已按本次显式操作追加目标并恢复派发；旧目标的完成收据保持不变。"
	case resumed:
		note = "项目此前是人工暂停，已按本次显式操作恢复；旧目标的完成收据保持不变。"
	case !created:
		note = "这个 idempotency_key 已创建过该目标：没有重复追加。"
	case after.Paused && after.Kind == pauseKindUnrecorded:
		// The goal exists but the project is still parked, and this is the case
		// where saying nothing would be read as success. Name the pause and hand
		// the decision back instead of resuming a stop we cannot attribute.
		note = "暂停原因未记录：目标已追加，但项目仍保持暂停；确认现场后点「继续项目」再派发。"
	case before.Kind == pauseKindWaitingHuman:
		note = "项目未暂停（有意图在等待人工）：只追加了目标，没有改动运行/等待状态。"
	case before.Kind == pauseKindWaitingOther, before.Kind == pauseKindWaitingAction:
		note = "目标已追加；现有外部结果等待继续保留，无需为此人工确认。"
	default:
		note = "项目在运行中：只追加了目标，没有改动在跑意图。"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"goal":              goal,
		"created":           created,
		"resumed":           resumed,
		"resumed_from":      resumedFrom,
		"decider_woken":     woken,
		"decider_paused":    s.Decider != nil && s.Decider.Paused(),
		"paused_before":     before.Paused,
		"pause_kind_before": before.Kind,
		"paused_after":      after.Paused,
		"pause_kind_after":  after.Kind,
		"pause_reason":      after.Reason,
		"retryable":         false,
		"note":              note,
	})
}
