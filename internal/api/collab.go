package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

const maxWaitTimeout = 7 * 24 * time.Hour

// caller identifies the current participant or the project console.
type caller struct {
	participant string
	runID       string
	agent       bool
}

func (s *Server) caller(r *http.Request) caller {
	if id, ok := identityOf(r); ok {
		return caller{participant: id.ParticipantID, runID: id.RunID, agent: true}
	}
	return caller{participant: collab.CommanderID}
}

type waitIntentReq struct {
	Actions        []string `json:"actions"`
	Mode           string   `json:"mode"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// waitIntent waits for external receipts, yielding the current slot if needed.
func (s *Server) waitIntent(w http.ResponseWriter, r *http.Request) {
	intentID := r.PathValue("id")
	in, ok := s.Store.GetIntent(intentID)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	c := s.caller(r)
	runID := in.RunID
	if c.agent {
		if c.participant != collab.ParticipantID(intentID) {
			writeErr(w, http.StatusForbidden, "只能为自己的意图登记等待")
			return
		}
		if in.Status != model.StatusRunning || in.RunID != c.runID {
			writeErr(w, http.StatusConflict, "只有当前正在运行的 turn 可以登记等待")
			return
		}
		runID = c.runID
	} else {
		switch in.Status {
		case model.StatusRunning, model.StatusWaiting, model.StatusPending:
		default:
			writeErr(w, http.StatusConflict, "意图已结束（"+in.Status+"）：不再等待")
			return
		}
	}
	var req waitIntentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效的等待条件 JSON")
		return
	}
	if len(req.Actions) == 0 {
		writeErr(w, http.StatusBadRequest, "请指定要等待的外部动作")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = model.WaitAll
	}
	var deadline *time.Time
	if req.TimeoutSeconds > 0 {
		// Clamp seconds before converting: a large JSON integer can overflow
		// time.Duration and turn a future deadline into an immediate timeout.
		seconds := min(req.TimeoutSeconds, int(maxWaitTimeout/time.Second))
		d := time.Duration(seconds) * time.Second
		at := time.Now().Add(d)
		deadline = &at
	}
	state, err := s.Store.RegisterWait(intentID, runID, model.Wait{
		Actions: req.Actions, Mode: mode, Deadline: deadline,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if state.Ready {
		// A re-registered wait on an already waiting direction may have moved it
		// back to pending; let the scheduler pick it up.
		if cur, ok := s.Store.GetIntent(intentID); ok && cur.Status == model.StatusPending {
			go s.pump()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ready": true, "reason": state.Reason,
			"results": state.Results, "pending": state.Pending,
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ready": false, "waiting": true, "reason": state.Reason,
		"results": state.Results, "pending": state.Pending,
	})
	// A persisted wait is a scheduler yield, not a request for the model to
	// voluntarily finish. Returning the slot lets other work proceed.
	if in.Status == model.StatusRunning && runID != "" {
		_ = s.CancelRunWithCause(runID, model.CancelCauseActionWait, "等待外部动作结果：让出执行槽位")
	}
}

// hasCapacity is the one slot check used by the scheduler. The API cap counts
// intent runs only; verifier/report purpose runs are not intent work. It reads
// the lifecycle service's answer so the scheduler and dispatch cannot disagree.
func (s *Server) hasCapacity() bool {
	return s.Lifecycle.HasCapacity()
}

// pump resumes paused intents and wakes intents whose condition is
// satisfied or whose deadline has passed. It is safe to call from many places:
// StartRun's claim and the MaxRuns check make a second scheduler a no-op.
func (s *Server) pump() {
	if s.automaticReviewsEnabled() {
		s.SchedulePendingReviews()
	}
	s.pumpMu.Lock()
	defer s.pumpMu.Unlock()
	if s.deleting.Load() || s.paused.Load() || s.closing.Load() {
		return
	}
	for _, in := range s.Store.ListIntents() {
		if in.Status != model.StatusPending || in.Hold || in.Wait != nil || in.Aborted || !s.pauseInterrupted(in) {
			continue
		}
		if !s.hasCapacity() {
			return
		}
		if err := s.continuePaused(in); err != nil {
			log.Printf("scheduler: 恢复暂停中断的意图 %s 没起来：%v", in.ID, err)
		}
	}
	for _, in := range s.Store.ListIntents() {
		if in.Wait == nil || in.Hold {
			continue
		}
		if in.Status != model.StatusWaiting && in.Status != model.StatusPending {
			continue
		}
		if !in.Wait.WakePending {
			if state, ok := s.Store.EvaluateWait(in.ID); ok && state.Ready {
				if _, _, err := s.Store.SatisfyWait(in.ID); err != nil {
					log.Printf("scheduler: 结算等待 %s 失败：%v", in.ID, err)
					continue
				}
			}
		}
		cur, ok := s.Store.GetIntent(in.ID)
		if !ok || cur.Wait == nil || !cur.Wait.WakePending {
			continue
		}
		if !s.hasCapacity() {
			return
		}
		if cur.Status == model.StatusWaiting {
			// ClaimIntent accepts pending/failed only. The wake is a new turn on
			// a pending direction; the wait (and its structured results) stays
			// attached until startWake consumes it.
			prepared, err := s.Store.UpdateIntent(cur.ID, func(i *model.Intent) {
				i.Status = model.StatusPending
				i.Hold = false
			})
			if err != nil {
				log.Printf("scheduler: 准备唤醒 %s 失败：%v", cur.ID, err)
				continue
			}
			cur = prepared
		}
		s.startWake(cur)
	}
}

// pauseInterrupted reports whether the direction's current run is one the
// whole-project pause stopped, i.e. work the operator's resume is asking for.
// A pause cancels runs and never holds the intent, so the settlement leaves
// exactly this state behind: pending, with its last run canceled as "pause".
func (s *Server) pauseInterrupted(in model.Intent) bool {
	if in.RunID == "" {
		return false
	}
	run, ok := s.Store.GetRun(in.RunID)
	return ok && run.Status == model.RunCanceled && run.CancelCause == model.CancelCausePause
}

// continuePaused continues a direction the project pause stopped. It prefers the
// preserved native session (docs §8); when the host no longer has it, StartRun
// rebuilds the session from the ledger and records why, instead of pretending
// the context survived. Both the scheduler's child path and the top-level
// direction path go through here so the two cannot drift.
func (s *Server) continuePaused(in model.Intent) error {
	if aopexec.CanResume(filepath.Join(s.CaseRoot, in.ID)) {
		_, err := s.startRunWith(in.ID, "项目已显式恢复，继续原意图。先核对中断前已登记的动作与结果；unknown 不代表未执行，不得盲目重放。", in.RunID, true, true)
		return err
	}
	_, err := s.StartRun(in.ID)
	return err
}

// startWake re-dispatches a direction whose wait is satisfied. It continues the
// old session when the host still has it; after a host restart it builds a fresh
// brief with the child results instead of pretending the context survived.
func (s *Server) startWake(in model.Intent) {
	workDir := filepath.Join(s.CaseRoot, in.ID)
	resume := aopexec.CanResume(workDir)
	prompt := s.wakePrompt(in, resume)
	prevRun := in.RunID
	var runID string
	var err error
	if resume {
		runID, err = s.startRunWith(in.ID, prompt, prevRun, true, true)
	} else {
		runID, err = s.startRunWith(in.ID, prompt, "", false, true)
	}
	if err != nil && resume && s.hasCapacity() {
		// The marker may point at a host/session that no longer exists (restart,
		// DB replaced). Do not leave the direction waiting on a dead trajectory:
		// build a fresh brief with the child results and say so honestly.
		log.Printf("scheduler: 唤醒 %s 续话失败，改走新会话：%v", in.ID, err)
		prompt = s.wakePrompt(in, false)
		runID, err = s.startRunWith(in.ID, prompt, "", false, true)
	}
	if err != nil {
		// Capacity or a transient host error: leave the wait (and WakePending)
		// intact so the next finish/watchdog tick tries again.
		log.Printf("scheduler: 唤醒 %s 失败：%v", in.ID, err)
		return
	}
	log.Printf("scheduler: 唤醒 %s（%s，run %s）", in.ID, in.Wait.WakeReason, runID)
}

func (s *Server) wakePrompt(in model.Intent, resume bool) string {
	if resume {
		return "等待已结束，继续原意图；结果由控制面附在本轮交接中。"
	}
	// A fresh wake session is built before the dispatcher writes this turn's
	// optional indexes, so this prompt cannot name them as written (audit
	// F12.6). The dispatcher appends the truthful "本轮已写" pointer when the
	// write actually succeeded; this text only survives a failed write honestly.
	prompt := BuildPrompt(s.Store.Snapshot(), in, s.Constraints(), s.pocDir(), s.KBDir, s.Timeout, false)
	return prompt + "\n\n按本轮输入清单查询有界索引，细节按 fact ID 查。"
}

// readyWaitSection is attached by dispatch for every path that consumes a ready
// wait, including explicit retry/run and replacement sessions.
func (s *Server) readyWaitSection(in model.Intent) string {
	if in.Wait == nil || !in.Wait.WakePending {
		return ""
	}
	if len(in.Wait.Actions) > 0 {
		return "\n\n## 外部动作结果（执行状态，不是事实结论）\n" + s.waitResultsSection(in) + "\nunknown 表示结果尚未确认；核对外部状态后自行决定下一步。"
	}
	if in.Wait != nil && in.Wait.Operator != nil {
		o := in.Wait.Operator
		section := "\n\n## 人工操作交接（操作员声明，尚未验证）\n请求：" + o.Request +
			"\n完成说明：" + o.Response + "\n关联登录材料：" + strings.Join(o.AccessIDs, ", ") +
			"\n人工已结束操作。先核对实际身份、租户和受保护资源的访问状态，再决定后续工作。\n"
		if o.Resolution == model.OperatorUnavailable {
			section = "\n\n## 人工操作无法完成（未获得所请求的访问能力）\n请求：" + o.Request +
				"\n操作员原因与约束：" + o.Response +
				"\n人工操作没有完成，不得视为登录成功。仅在原授权和当前实际可用的访问范围内继续；依赖缺失登录条件的覆盖项应注明未覆盖或停放，不得写成已验证安全。遵守操作员明确的仅未登录测试约束。除非操作员提供新条件，不重复索要这次已明确不可用的前提。\n"
		}
		return section
	}
	return ""
}

func (s *Server) waitResultsSection(in model.Intent) string {
	w := in.Wait
	if w == nil {
		return "（控制面没有找到等待记录）"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "原因：%s；模式：%s；截止：%s\n", w.WakeReason, w.Mode, deadlineText(w.Deadline))
	for _, r := range w.Results {
		fmt.Fprintf(&b, "- 外部动作 `%s` state=%s result=%q ref=%q\n", r.ActionID, r.Status, r.Result, r.ResultRef)
	}
	if len(w.Results) == 0 {
		b.WriteString("（本次等待尚无已确认结果；待完成项可由等待记录查询）\n")
	}
	return b.String()
}

func deadlineText(t *time.Time) string {
	if t == nil {
		return "无"
	}
	return t.Format(time.RFC3339)
}

// canSettleWait reports whether the finish of runID has the right to settle
// this direction's wait. RunID is the most recent holder; a waiting direction's
// own run ends and must be the one that settles it. A run replaced by handoff,
// retry or steer therefore has no settlement right, and neither has anything
// while the direction is being handed over (Hold).
func (s *Server) afterIntentSettled() {
	s.routeAndDeliver()
	s.pump()
}
