package api

// Spawning a worker, listing workers, and killing one.
//
// A worker is still an intent in the ledger: it has a case, a claim, a run, the
// same concurrency cap and the same cancel machinery. "Spawn" is a first-class
// participant action only in the sense that it is one call: create the intent
// (idempotently) and try to start it, reporting whether it started instead of
// leaving the caller to guess between "queued", "refused" and "created twice".
//
// Authorization for kill is ownership, not project membership: a run may stop a
// worker it created or a descendant of its own direction, and nothing else. The
// operator is the exception: the console routes (workers_console.go) reuse this
// implementation with caller.operator set, so both surfaces answer with the same
// request/response shape instead of growing a second contract.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

const maxSpawnTitle = 200

// peerPublishNeedsSource is the refusal for an executor that publishes a peer
// direction (no parent) without naming any recorded fact. The api also inherits
// Doc/Node/Skills from the caller's own direction in the store; this check is
// the "there is something to stand on" half of the same rule.
const peerPublishNeedsSource = "平级发布必须引用至少一条已记录事实（from_fact_ids/context_fact_ids/pinned_fact_ids）；没有依据的 bootstrap 只由指挥官或操作员发起。"

// maxSpawnBudgetInfos caps how many budget refusals a run row keeps. The info is
// deduped by text, so normally it is one per gate; the cap is the guard against
// a retry loop inflating the row.
const maxSpawnBudgetInfos = 8

// workerCaller is who is acting: an executor (identity from its token) or the
// operator (console token). Only two things depend on it: the CreatedBy stamped
// on a spawned intent and the kill authorization rule.
type workerCaller struct {
	participant string // CreatedBy on spawn: "pt_in_x" or actorOf(r)
	intentID    string // caller's own direction (agent only)
	runID       string // caller's live run (agent only)
	operator    bool   // console: sees the whole project, kills any active worker
}

type spawnReq struct {
	ContextFactIDs     []string `json:"context_fact_ids"`
	PinnedFactIDs      []string `json:"pinned_fact_ids"`
	InterestEntityRefs []string `json:"interest_entity_refs"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Eval               string   `json:"eval"`
	// Profile/Tier are retired (the external gateway owns model routing). They
	// are still parsed so an old client gets an explicit 400.
	Profile        string   `json:"profile"`
	Tier           string   `json:"tier"`
	Node           string   `json:"node"`
	ParentIntentID string   `json:"parent_intent_id"`
	IdempotencyKey string   `json:"idempotency_key"`
	FromFactIDs    []string `json:"from_fact_ids"`
	MaxTurns       int      `json:"max_turns"`
}

// workerView is one row of the participant-side roster: enough to decide
// whether to message, peek at or kill this worker, without the full ledger row
// (notes, handoff state, doc) that belongs to the console.
type workerView struct {
	IntentID       string `json:"intent_id"`
	ParticipantID  string `json:"participant_id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	ParentIntentID string `json:"parent_intent_id,omitempty"`
	CreatedBy      string `json:"created_by_participant_id,omitempty"`
	DelegatedByRun string `json:"delegated_by_run_id,omitempty"`
	Node           string `json:"node,omitempty"`
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

// agentSpawn creates one worker intent and attempts to start it. A capacity
// refusal is not an error: the worker exists (started=false, reason says why)
// and retrying with the same idempotency_key starts that same worker rather
// than creating a second one.
func (s *Server) agentSpawn(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.spawnWorker(w, r, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

// spawnWorker is the one spawn implementation for the operator and the agent.
// The only difference is the caller: an executor may parent under its own live
// direction only, while the operator may parent under any active direction.
func (s *Server) spawnWorker(w http.ResponseWriter, r *http.Request, caller workerCaller) {
	var req spawnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Eval = strings.TrimSpace(req.Eval)
	req.Node = strings.TrimSpace(req.Node)
	req.ParentIntentID = strings.TrimSpace(req.ParentIntentID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.Title == "" && req.Description == "" {
		writeErr(w, http.StatusBadRequest, "title 或 description 至少填一个")
		return
	}
	if req.Title == "" {
		req.Title = firstLine(req.Description)
	}
	if len([]rune(req.Title)) > maxSpawnTitle {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("title 最多 %d 字", maxSpawnTitle))
		return
	}
	// Docs/17 §3.2: the key is mandatory because a retried spawn must not create
	// a second individual when the first request's response was lost.
	if req.IdempotencyKey == "" {
		writeErr(w, http.StatusBadRequest, "idempotency_key 必填：重试要能找回同一个 worker，而不是再造一个")
		return
	}
	if req.MaxTurns < 0 || req.MaxTurns > executor.MaxTurnsCap {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("max_turns 必须在 0–%d 之间", executor.MaxTurnsCap))
		return
	}
	if strings.TrimSpace(req.Profile) != "" || strings.TrimSpace(req.Tier) != "" {
		writeErr(w, http.StatusBadRequest, "模型选择由外部网关管理，请移除 profile/tier")
		return
	}
	if err := s.validateSpawnPlacement(req.Node); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkPremiseRefs(req.FromFactIDs); err != nil {
		writePremiseError(w, err)
		return
	}
	// A parentless spawn is a peer publish by an executor: it must stand on
	// recorded facts, and it must stay on the node its own direction runs on.
	// The operator/console keeps the old "spawn anything" behaviour.
	peer := req.ParentIntentID == ""
	if !caller.operator && peer {
		if !hasAnyFactRef(req.FromFactIDs, req.ContextFactIDs, req.PinnedFactIDs) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": peerPublishNeedsSource, "code": "peer_publish_needs_source",
			})
			return
		}
		if req.Node != "" {
			if own, ok := s.Store.GetIntent(caller.intentID); ok && req.Node != own.Node {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": fmt.Sprintf("平级发布不能切换节点：你所在意图在节点 %s，这次请求指定节点 %s；不填 node 会继承所在意图。",
						peerNodeLabel(own.Node), peerNodeLabel(req.Node)),
					"code": "peer_publish_node_locked",
				})
				return
			}
		}
	}
	if req.ParentIntentID != "" {
		parent, ok := s.Store.GetIntent(req.ParentIntentID)
		if !ok {
			writeErr(w, http.StatusNotFound, "父意图不存在："+req.ParentIntentID)
			return
		}
		if caller.operator {
			// The operator may hang work under any direction that is still
			// active (same rule as console child delegation).
			switch parent.Status {
			case model.StatusPending, model.StatusRunning, model.StatusClaimed:
			default:
				writeErr(w, http.StatusConflict, "父意图已结束（"+parent.Status+"）：先 retry/reopen")
				return
			}
		} else {
			// An executor may only hang work under its own direction, and only
			// from the turn that is currently running (same rule as child
			// delegation).
			if parent.ID != caller.intentID {
				writeErr(w, http.StatusForbidden, "只能挂在自己的意图下造人")
				return
			}
			if parent.Status != model.StatusRunning || parent.RunID != caller.runID {
				writeErr(w, http.StatusConflict, "只有当前正在运行的 turn 可以造人")
				return
			}
		}
	}
	created, isNew, err := s.Store.CreateSpawnIntent(model.Intent{
		Title: req.Title, Description: req.Description, Eval: req.Eval,
		Node: req.Node, ParentID: req.ParentIntentID,
		FromFactIDs: req.FromFactIDs, MaxTurns: req.MaxTurns,
		ContextFactIDs: req.ContextFactIDs, PinnedFactIDs: req.PinnedFactIDs,
		InterestEntityRefs: req.InterestEntityRefs,
		CreatedBy:          caller.participant, DelegatedByRun: caller.runID,
		IdempotencyKey: req.IdempotencyKey,
	}, caller.intentID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSpawnIdempotencyConflict):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrLineageBudget):
			s.noteSpawnBudgetRefusal(caller.runID, err.Error())
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "lineage_budget"})
		case errors.Is(err, store.ErrPeerNodeLocked):
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "peer_publish_node_locked"})
		default:
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	started, runID, reason := s.tryStartWorker(created.ID)
	cur, _ := s.Store.GetIntent(created.ID)
	if cur.ID == "" {
		cur = created
	}
	latest := s.latestRuns()
	view := s.workerViewOf(cur, latest[cur.ID], caller)
	var run *model.Run
	if found, ok := s.Store.GetRun(runID); ok {
		run = &found
	}
	code := http.StatusCreated
	if !isNew {
		code = http.StatusOK
	}
	// The canonical shape is the nested view; the flat aliases keep the spawn
	// response usable by a caller that only wants "what did I get, did it
	// start" (worker_id == intent_id: a worker is an intent in this ledger).
	writeJSON(w, code, map[string]any{
		"created": isNew, "started": started, "reason": reason,
		"intent": cur, "run": run, "worker": view,
		"worker_id": cur.ID, "intent_id": cur.ID, "run_id": runID,
		"resolved_model": view.Model,
	})
}

// hasAnyFactRef reports whether the request named at least one applicable fact.
// Interest entity refs are deliberately not counted: they are a coarse focus
// hint, not a recorded fact a conclusion can stand on.
func hasAnyFactRef(groups ...[]string) bool {
	for _, ids := range groups {
		for _, id := range ids {
			if strings.TrimSpace(id) != "" {
				return true
			}
		}
	}
	return false
}

func peerNodeLabel(node string) string {
	node = strings.TrimSpace(node)
	if node == "" {
		return "（默认节点）"
	}
	return node
}

// noteSpawnBudgetRefusal records the refusal on the caller's current run so the
// next round sees why the publish did not happen instead of only the HTTP
// answer. Deduped by text and capped: a retry loop must not grow the row.
func (s *Server) noteSpawnBudgetRefusal(runID, text string) {
	if runID == "" || text == "" {
		return
	}
	info := "平级发布被谱系预算拒绝：" + text
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		for _, existing := range r.Infos {
			if existing == info {
				return
			}
		}
		if len(r.Infos) >= maxSpawnBudgetInfos {
			return
		}
		r.Infos = append(r.Infos, info)
	})
}

// validateSpawnPlacement applies the node admission boundary before creation.
func (s *Server) validateSpawnPlacement(node string) error {
	if s.Project != nil {
		if node != "" {
			if err := s.checkNode(node); err != nil {
				return err
			}
		}
	} else if node != "" && !s.HasNode(node) {
		return fmt.Errorf("没有这个节点：%s", node)
	}
	return nil
}

// tryStartWorker starts an already-created worker if the project has a slot.
// started=false with a reason is a normal outcome (capacity, pause, held); the
// intent stays in the ledger and the idempotent retry can start it later.
func (s *Server) tryStartWorker(intentID string) (started bool, runID, reason string) {
	cur, ok := s.Store.GetIntent(intentID)
	if !ok {
		return false, "", "worker 不存在"
	}
	if cur.Status == model.StatusRunning || cur.Status == model.StatusClaimed {
		return cur.RunID != "", cur.RunID, ""
	}
	if model.Settled(cur.Status) {
		return false, cur.RunID, "worker 已结束（" + cur.Status + "）"
	}
	if cur.Hold {
		return false, cur.RunID, "worker 已被 kill（Hold）：不会自动重启"
	}
	if running, limit := s.Lifecycle.ProjectCapacity(); limit > 0 && running >= limit {
		return false, "", fmt.Sprintf("并发已满（%d/%d）：worker 已创建但未启动；用同一 idempotency_key 重试会启动它，不会重复创建",
			running, limit)
	}
	runID, err := s.StartRun(intentID)
	if err != nil {
		return false, "", err.Error()
	}
	return true, runID, ""
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
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
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
		Title: in.Title, Status: in.Status, ParentIntentID: in.ParentID,
		CreatedBy: in.CreatedBy, DelegatedByRun: in.DelegatedByRun,
		Node: in.Node, Profile: profile, ResolvedProfile: resolved,
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
	return s.mayKillWorker(caller.intentID, caller.participant, target)
}

// mayKillWorker is the ownership rule: the caller created the target, or the
// target is a descendant of the caller's own direction. A participant cannot
// kill itself (that is what a normal cancel/handoff is for).
func (s *Server) mayKillWorker(callerIntentID, callerParticipant string, target model.Intent) bool {
	if callerIntentID != "" && target.ID == callerIntentID {
		return false
	}
	if callerParticipant != "" && target.CreatedBy == callerParticipant {
		return true
	}
	if callerIntentID == "" {
		return false
	}
	seen := map[string]bool{target.ID: true}
	cur := target
	for cur.ParentID != "" {
		if cur.ParentID == callerIntentID {
			return true
		}
		parent, ok := s.Store.GetIntent(cur.ParentID)
		if !ok || seen[parent.ID] {
			return false
		}
		seen[parent.ID] = true
		cur = parent
	}
	return false
}

// agentKillWorker stops a worker the caller owns. Hold is set before the run is
// cancelled so the canceled turn does not fall back to pending and get picked
// up again by the scheduler; Aborted makes the settle path fail the direction
// (not leave it pending forever).
func (s *Server) agentKillWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.killWorker(w, r, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

type restartWorkerReq struct {
	Note string `json:"note"`
}

// agentRestartWorker is the participant face's restart: a parent may bring back
// a worker it created (or a descendant of its own direction) after a stop or a
// failure. Ownership is the same rule as kill; the stop's actor decides whether
// this caller may release it.
func (s *Server) agentRestartWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "缺少协作身份")
		return
	}
	s.restartWorker(w, r, workerCaller{
		participant: id.ParticipantID, intentID: id.IntentID, runID: id.RunID,
	})
}

// restartWorker re-dispatches a finished worker under its own brief (the task
// book is not revised here). The CAS reserve mirrors retry: status and run are
// unchanged at the read, then the intent is claimed as pending with Hold for the
// start; StartIntentRun clears Hold. A failed start restores the exact prior
// status/hold/actor and records why, so a capacity refusal cannot silently turn
// a plain failed worker into an unknown-holder stop.
func (s *Server) restartWorker(w http.ResponseWriter, r *http.Request, caller workerCaller) {
	raw := strings.TrimSpace(r.PathValue("id"))
	if intentID, ok := collab.IntentIDFromParticipant(raw); ok {
		raw = intentID
	}
	target, found := s.Store.GetIntent(raw)
	if !found {
		writeErr(w, http.StatusNotFound, "没有这个 worker："+raw)
		return
	}
	if caller.intentID == target.ID {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "不能重启自己：自己的方向用 retry/continue",
			"code":  "worker_self_restart",
		})
		return
	}
	if !caller.operator && !s.mayKillWorker(caller.intentID, caller.participant, target) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "未授权：只能重启自己创建的 worker 或自己意图的后代",
			"code":  "worker_forbidden",
		})
		return
	}
	switch target.Status {
	case model.StatusFailed, model.StatusPending, model.StatusReviewFailed:
	default:
		writeErr(w, http.StatusConflict, "worker 当前是 "+target.Status+"：只有 failed/pending/review_failed 可以重启")
		return
	}
	if target.Hold && !s.mayReleaseWorkerHold(target, caller) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": s.heldWorkerText(target),
			"code":  "worker_held",
		})
		return
	}
	var req restartWorkerReq
	if !decodeOptionalJSON(w, r.Body, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 500 {
		writeErr(w, http.StatusBadRequest, "重启说明超过 500 字：备注要短")
		return
	}
	if note == "" {
		note = "worker restart：" + caller.participant
	}
	prevStatus, prevHold := target.Status, target.Hold
	prevHeldBy, prevReason := target.HeldBy, target.HoldReason
	prevParkReason, prevParkedFacts := target.ParkReason, target.ParkedPremiseFactIDs
	prevParkedClaims, prevParkedAt := target.ParkedPremiseClaims, target.ParkedAt
	// The hold state is part of the CAS: CancelIntent on a pending worker only
	// flips Hold/HeldBy, so matching status+run alone would let this restart
	// clear an operator/commander stop that landed after the read.
	_, prepared, err := s.Store.UpdateIntentIfHeld(target.ID, target.Status, target.RunID, target.Hold, target.HeldBy, func(i *model.Intent) {
		i.Notes = append(i.Notes, model.IntentNote{At: time.Now(), Kind: model.IntentNoteRestart, Text: note})
		i.Status = model.StatusPending
		i.Hold = true // reserve for this explicit restart; StartIntentRun clears it
		i.ClaimedBy = ""
		i.ParkReason = ""
		i.ParkedPremiseFactIDs = nil
		i.ParkedPremiseClaims = nil
		i.ParkedAt = nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !prepared {
		writeErr(w, http.StatusConflict, "worker 已由其他请求接管，请刷新后查看当前运行")
		return
	}
	// A fresh session has no conversation memory: the unconfirmed tail must ride
	// this restart, exactly as it does for a fresh retry.
	if n, err := s.Store.RedeliverAllUnconfirmed(collab.ParticipantID(target.ID)); err != nil {
		log.Printf("worker restart %s: 重投未确认消息失败：%v", target.ID, err)
	} else if n > 0 {
		log.Printf("worker restart %s: %d 条未确认消息回队", target.ID, n)
	}
	runID, startErr := s.startRunWith(target.ID, "", "", false, false)
	if startErr != nil {
		// Do not leave the reserved pending+Hold row behind: restore the exact
		// state it had before this attempt (a plain failed worker must not gain
		// an unknown-holder hold just because a capacity refusal happened). Skip
		// the restore if a concurrent stop changed the hold under us; that stop
		// is the newer decision.
		_, _ = s.Store.UpdateIntent(target.ID, func(i *model.Intent) {
			if i.RunID != target.RunID || i.Status != model.StatusPending {
				return
			}
			if i.HeldBy != prevHeldBy || i.HoldReason != prevReason {
				return
			}
			i.Status = prevStatus
			i.Hold = prevHold
			i.HeldBy, i.HoldReason = prevHeldBy, prevReason
			i.ParkReason, i.ParkedPremiseFactIDs = prevParkReason, prevParkedFacts
			i.ParkedPremiseClaims, i.ParkedAt = prevParkedClaims, prevParkedAt
			i.Notes = append(i.Notes, model.IntentNote{
				At: time.Now(), Kind: model.IntentNoteRestart,
				Text: "重启未启动：" + startErr.Error(),
			})
		})
		writeErr(w, http.StatusConflict, "重启未能启动："+startErr.Error())
		return
	}
	cur, _ := s.Store.GetIntent(target.ID)
	latest := s.latestRuns()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"restarted": true, "intent_id": target.ID, "run_id": runID,
		"worker": s.workerViewOf(cur, latest[cur.ID], caller),
	})
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
	if !caller.operator && !s.mayKillWorker(caller.intentID, caller.participant, target) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "未授权：只能 kill 自己创建的 worker 或自己意图的后代",
			"code":  "worker_forbidden",
		})
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
