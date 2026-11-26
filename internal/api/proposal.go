package api

// Proposals are the executor's structured way to request a next step. A proposal
// is a control-state row in the project ledger, not a fact and not an intent:
// the executor proposes, the commander or the operator decides, and an approved
// proposal still needs an explicit dispatch before a run starts. Plain
// participant messages remain free text; this surface is what makes the request
// (and the decision) machine-readable and auditable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

const (
	proposalListLimit    = 50
	proposalListLimitMax = 200
	// proposalTextClip bounds the excerpt one notification carries; the ledger
	// row keeps the full title/description/rationale.
	proposalTextClip = 200
	// proposalReasonClip bounds the quoted rejection reason in the notification.
	proposalReasonClip = 400
	// proposalWakeReason labels the decider wake a queued proposal notification
	// needs; it is only a coalescing key in the decider's wake channel.
	proposalWakeReason = "proposal"
)

// proposalNotification is the honest delivery receipt of the one notification a
// transition sends. notified means the durable message row exists in the
// ledger (the inbox carries it on the recipient's next opportunity); a write
// failure is reported here instead of undoing the proposal transition.
type proposalNotification struct {
	Notified  bool   `json:"notified"`
	MessageID string `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// notifyCommanderOfProposal writes the durable message and hands it to the
// commander path. A failed delivery does not roll the proposal back: the ledger
// row is the truth and the caller's response says whether the notification
// reached the message service.
func (s *Server) notifyCommanderOfProposal(ctx context.Context, p model.Proposal, kind, key, text string) proposalNotification {
	msg, _, err := s.Store.AddMessage(model.Message{
		ProjectID:         s.projectID(),
		FromParticipantID: p.FromParticipantID,
		FromRunID:         p.FromRunID,
		FromGeneration:    p.FromGeneration,
		ToParticipantID:   collab.CommanderID,
		Kind:              kind,
		Text:              text,
		Durable:           true,
		IdempotencyKey:    key,
		Status:            model.MessageQueued,
	})
	if err != nil {
		return proposalNotification{Error: err.Error()}
	}
	delivered := s.deliverToCommander(ctx, msg)
	// A queued notification is picked up by the next round's inbox, but the
	// commander also has to wake up for that round to happen. deliverToCommander
	// wakes on its own when no session is live; this covers the remaining case
	// (live session but delivery could not happen).
	if delivered.Status != model.MessageAccepted && s.Decider != nil {
		s.Decider.Wake(proposalWakeReason)
	}
	if delivered.Status == model.MessageUnknown {
		// The transport attempted and failed; saying "notified" would overstate
		// what the ledger can prove (the row exists, the bytes may not have left).
		return proposalNotification{MessageID: delivered.ID, Error: "投递结果未知：" + delivered.Error}
	}
	return proposalNotification{Notified: true, MessageID: delivered.ID}
}

// notifyProposerOfDecision sends the decision back to the participant that
// proposed the work, using the same participant mailbox as peer messages so the
// direction can read it in its next run.
func (s *Server) notifyProposerOfDecision(ctx context.Context, p model.Proposal, actor, kind, key, text string) proposalNotification {
	if p.FromParticipantID == "" {
		return proposalNotification{Error: "提议没有提议者，无法通知"}
	}
	msg := model.Message{
		ProjectID:         s.projectID(),
		FromParticipantID: actor,
		ToParticipantID:   p.FromParticipantID,
		Kind:              kind,
		Text:              text,
		Durable:           true,
		IdempotencyKey:    key,
		Status:            model.MessageQueued,
	}
	intentID, isIntent := collab.IntentIDFromParticipant(p.FromParticipantID)
	var target model.Intent
	hasTarget := false
	if isIntent {
		target, hasTarget = s.Store.GetIntent(intentID)
		if hasTarget {
			// The recipient generation is part of the delivery contract: an old
			// holder must not receive a decision meant for the current one.
			msg.ToGeneration = target.Attempts
		}
	}
	created, _, err := s.Store.AddMessage(msg)
	if err != nil {
		return proposalNotification{Error: err.Error()}
	}
	if hasTarget {
		created = s.deliverMessage(ctx, target, created)
	}
	if created.Status == model.MessageUnknown {
		return proposalNotification{MessageID: created.ID, Error: "投递结果未知：" + created.Error}
	}
	return proposalNotification{Notified: true, MessageID: created.ID}
}

// writeProposalError maps the store's proposal errors to one machine code each.
// The body is always {"error","code"}: the model-facing client branches on the
// code, not on the Chinese prose.
func writeProposalError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "proposal_internal"
	switch {
	case errors.Is(err, store.ErrProposalInvalid):
		status, code = http.StatusBadRequest, "proposal_invalid"
	case errors.Is(err, store.ErrProposalNotFound):
		status, code = http.StatusNotFound, "proposal_not_found"
	case errors.Is(err, store.ErrProposalForbidden):
		status, code = http.StatusForbidden, "proposal_forbidden"
	case errors.Is(err, store.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "proposal_idempotency_conflict"
	case errors.Is(err, store.ErrProposalConflict):
		status, code = http.StatusConflict, "proposal_conflict"
	}
	writeJSON(w, status, map[string]any{"error": err.Error(), "code": code})
}

// proposalActor is who the decision/withdrawal is attributed to. The project
// gate stamps X-Tw-Actor for the commander's writes; a commander identity is
// used directly when present so a test or a direct handler call cannot record
// the commander's decision as the operator's.
func proposalActor(r *http.Request) string {
	if id, ok := identityOf(r); ok && id.ParticipantID == collab.CommanderID {
		return collab.CommanderID
	}
	return actorOf(r)
}

// proposalDecisionAllowed is the project-face authorization for approve/reject/
// dispatch: only a harness commander or the operator decides. An executor's
// participant identity fails even when the request also claims the commander
// role in a header — the identity is server-issued and outranks the claim.
func proposalDecisionAllowed(r *http.Request) bool {
	if harnessSession(r) && !commanderClaim(r) {
		return false
	}
	if id, ok := identityOf(r); ok && id.ParticipantID != collab.CommanderID {
		return false
	}
	return true
}

// visibleProposal narrows by the caller's participant identity. An executor only
// sees its own suggestions; the commander and the operator see the whole queue.
// A foreign proposal is answered 404, not 403: confirming existence would tell a
// direction about another direction's plan.
func visibleProposal(p model.Proposal, r *http.Request) bool {
	id, ok := identityOf(r)
	return !ok || p.FromParticipantID == id.ParticipantID
}

type createProposalReq struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Doc            string   `json:"doc"`
	Eval           string   `json:"eval"`
	Rationale      string   `json:"rationale"`
	FromFactIDs    []string `json:"from_fact_ids"`
	ContextFactIDs []string `json:"context_fact_ids"`
	PinnedFactIDs  []string `json:"pinned_fact_ids"`
	Skills         []string `json:"skills"`
	Node           string   `json:"node"`
	MaxTurns       int      `json:"max_turns"`
	IdempotencyKey string   `json:"idempotency_key"`
}

func (s *Server) createProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "提议必须由执行者参与者身份提交；指挥官请直接 POST /api/intents，操作员不代为提议",
			"code":  "proposal_forbidden",
		})
		return
	}
	var req createProposalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON：" + err.Error(), "code": "proposal_invalid"})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Eval = strings.TrimSpace(req.Eval)
	req.Rationale = strings.TrimSpace(req.Rationale)
	req.Node = strings.TrimSpace(req.Node)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "提议必须带 idempotency_key（重试复用同一个键）", "code": "proposal_invalid"})
		return
	}
	if req.Title == "" && req.Description == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title 或 description 至少填一个", "code": "proposal_invalid"})
		return
	}
	if req.Title == "" {
		req.Title = firstLine(req.Description)
	}
	if req.MaxTurns < 0 || req.MaxTurns > executor.MaxTurnsCap {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("max_turns 必须在 0–%d 之间（0 = 不设回合上限）", executor.MaxTurnsCap), "code": "proposal_invalid",
		})
		return
	}
	if req.Node != "" {
		// Validate the execution location now, not at approval: a proposal that
		// can never be dispatched should be refused while the proposer can still
		// fix it.
		if s.Project == nil {
			if !s.HasNode(req.Node) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有这个节点：" + req.Node, "code": "proposal_invalid"})
				return
			}
		} else if err := s.checkNode(req.Node); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "proposal_invalid"})
			return
		}
	}
	// Premises go through the same admission rule as intent creation; context
	// and pinned references only have to exist. Both checks are repeated when
	// the proposal is approved, because a decision happens later.
	if err := s.checkPremiseRefs(req.FromFactIDs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "proposal_invalid"})
		return
	}
	if missing := missingFactRef(s.Store, append(append([]string(nil), req.ContextFactIDs...), req.PinnedFactIDs...)); missing != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "引用的阅读上下文事实不存在：" + missing, "code": "proposal_invalid"})
		return
	}
	fromIntentID, _ := collab.IntentIDFromParticipant(id.ParticipantID)
	created, isNew, err := s.Store.CreateProposal(model.Proposal{
		ID:                store.NewID("pr"),
		Title:             req.Title,
		Description:       req.Description,
		Doc:               req.Doc,
		Eval:              req.Eval,
		Rationale:         req.Rationale,
		FromFactIDs:       req.FromFactIDs,
		ContextFactIDs:    req.ContextFactIDs,
		PinnedFactIDs:     req.PinnedFactIDs,
		Skills:            req.Skills,
		Node:              req.Node,
		MaxTurns:          req.MaxTurns,
		FromParticipantID: id.ParticipantID,
		FromIntentID:      fromIntentID,
		FromRunID:         id.RunID,
		FromGeneration:    id.Generation,
		IdempotencyKey:    req.IdempotencyKey,
	})
	if err != nil {
		writeProposalError(w, err)
		return
	}
	notification := s.notifyCommanderOfProposal(r.Context(), created, "proposal",
		"proposal:"+created.ID+":submitted", proposalSubmittedText(created))
	writeJSON(w, http.StatusCreated, map[string]any{"proposal": created, "created": isNew, "notification": notification})
}

// missingFactRef names the first reference the ledger does not have. The store
// exposes an existence check in bulk; the name is what makes the 400 actionable.
func missingFactRef(st *store.Store, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if st.ContextRefsExist(ids) {
		return ""
	}
	for _, id := range ids {
		if _, ok := st.FactByID(id); !ok {
			return id
		}
	}
	return ""
}

// proposalSubmittedText is deterministic from the proposal's creation-time
// fields: the idempotent retry of the same create must produce the same message
// (AddMessage rejects a reused key with different content). It intentionally
// omits the current version — the decision needs the live row anyway.
func proposalSubmittedText(p model.Proposal) string {
	eval := p.Eval
	if eval == "" {
		eval = p.Description
	}
	var b strings.Builder
	b.WriteString("执行者提议新意图（proposal_id=" + p.ID + "）\n")
	b.WriteString("标题：" + p.Title + "\n")
	b.WriteString("验收：" + clipRunes(eval, proposalTextClip) + "\n")
	if p.Rationale != "" {
		b.WriteString("理由：" + clipRunes(p.Rationale, proposalTextClip) + "\n")
	}
	b.WriteString("处理：GET /api/proposals/" + p.ID + " 取当前 version，再 POST /api/proposals/" + p.ID + "/approve 或 /reject（body 带 expected_version；驳回需 note=理由）。")
	b.WriteString("批准只创建待派发意图，还需 POST /api/proposals/" + p.ID + "/dispatch 才会起 run。")
	return b.String()
}

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = proposalListLimit
	}
	if limit > proposalListLimitMax {
		limit = proposalListLimitMax
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	afterCursor := cursor == ""
	page := make([]model.Proposal, 0, limit)
	total, remaining := 0, 0
	for _, p := range s.Store.Proposals() {
		if !visibleProposal(p, r) {
			continue
		}
		active := p.Status == model.ProposalProposed || p.Status == model.ProposalApproved
		matches := status == "" || status == p.Status || status == "active" && active || status == "closed" && !active
		if matches {
			total++
			if afterCursor {
				remaining++
				if len(page) < limit {
					page = append(page, p)
				}
			}
		}
		if p.ID == cursor {
			afterCursor = true
		}
	}
	if !afterCursor {
		writeErr(w, http.StatusBadRequest, "提议分页位置已经失效，请刷新")
		return
	}
	out := map[string]any{"proposals": page, "total": total, "truncated": remaining > len(page)}
	if remaining > len(page) {
		out["next_cursor"] = page[len(page)-1].ID
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.Store.GetProposal(id)
	if !ok || !visibleProposal(p, r) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "提议不存在：" + id, "code": "proposal_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p})
}

// proposalDecisionReq is the CAS body of every decision route. expected_version
// is mandatory: deciding from an unread version is how two sessions produce two
// opposite verdicts on one suggestion.
type proposalDecisionReq struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Note            string `json:"note"`
	Reason          string `json:"reason"`
}

// decodeProposalDecision decodes the CAS body and refuses a missing version.
func decodeProposalDecision(w http.ResponseWriter, r *http.Request, req *proposalDecisionReq) bool {
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON：" + err.Error(), "code": "proposal_invalid"})
		return false
	}
	if req.ExpectedVersion == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected_version 必填：先 GET 提议再带当前版本", "code": "proposal_invalid"})
		return false
	}
	return true
}

// decisionProposal loads and authorizes one proposal for a decision route. The
// returned error is already written to w.
func (s *Server) decisionProposal(w http.ResponseWriter, r *http.Request) (model.Proposal, bool) {
	if !proposalDecisionAllowed(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "只有指挥官或操作员可以决定提议；执行者只能提议和撤回自己的提议",
			"code":  "proposal_forbidden",
		})
		return model.Proposal{}, false
	}
	p, ok := s.Store.GetProposal(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "提议不存在：" + r.PathValue("id"), "code": "proposal_not_found"})
		return model.Proposal{}, false
	}
	return p, true
}

func (s *Server) approveProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := s.decisionProposal(w, r)
	if !ok {
		return
	}
	var req proposalDecisionReq
	if !decodeProposalDecision(w, r, &req) {
		return
	}
	if p.Status != model.ProposalProposed || p.Version != req.ExpectedVersion {
		writeProposalError(w, fmt.Errorf("%w：提议当前为 %s v%d（请求 v%d）", store.ErrProposalConflict, p.Status, p.Version, req.ExpectedVersion))
		return
	}
	// Approval is the gate that turns a suggestion into a direction: the
	// premises must be admissible now, not merely when the proposal was filed.
	if err := s.checkPremiseRefs(p.FromFactIDs); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "proposal_premise"})
		return
	}
	actor := proposalActor(r)
	decided, intent, err := s.Store.DecideProposal(p.ID, req.ExpectedVersion, true, req.Note, actor)
	if err != nil {
		writeProposalError(w, err)
		return
	}
	notification := s.notifyProposerOfDecision(r.Context(), decided, actor, "proposal_approved",
		"proposal:"+decided.ID+":approved",
		"提议 "+decided.ID+"（"+decided.Title+"）已批准，已创建待派发意图 "+intent.ID+"；批准不自动起 run，收到派发通知才算开始。")
	writeJSON(w, http.StatusOK, map[string]any{"proposal": decided, "intent_id": intent.ID, "notification": notification})
}

func (s *Server) rejectProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := s.decisionProposal(w, r)
	if !ok {
		return
	}
	var req proposalDecisionReq
	if !decodeProposalDecision(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "驳回必须写 reason：说明为什么不采纳这一步", "code": "proposal_invalid"})
		return
	}
	actor := proposalActor(r)
	decided, _, err := s.Store.DecideProposal(p.ID, req.ExpectedVersion, false, reason, actor)
	if err != nil {
		writeProposalError(w, err)
		return
	}
	notification := s.notifyProposerOfDecision(r.Context(), decided, actor, "proposal_rejected",
		"proposal:"+decided.ID+":rejected",
		"提议 "+decided.ID+"（"+decided.Title+"）被驳回："+clipRunes(reason, proposalReasonClip)+"。不要重复提交同一条；按理由调整下一步或转做当前意图内的工作。")
	writeJSON(w, http.StatusOK, map[string]any{"proposal": decided, "notification": notification})
}

func (s *Server) dispatchProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := s.decisionProposal(w, r)
	if !ok {
		return
	}
	// Dispatch is idempotent by intent claim: a retry after the run started must
	// report the existing run, never start a second one.
	if p.Status == model.ProposalDispatched {
		writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "intent_id": p.IntentID, "run_id": p.RunID})
		return
	}
	var req proposalDecisionReq
	if !decodeProposalDecision(w, r, &req) {
		return
	}
	// One dispatch attempt at a time per project. The intent claim stops a
	// duplicate while the run is live, but a run that fails immediately could
	// otherwise let a second concurrent retry start a new run before the first
	// request records its receipt. pumpMu is the project's scheduling lock (pump
	// holds it too), so this reuses the existing serialization instead of adding
	// a second truth about "a dispatch is in flight".
	s.pumpMu.Lock()
	current, ok := s.Store.GetProposal(p.ID)
	if !ok {
		s.pumpMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "提议不存在：" + p.ID, "code": "proposal_not_found"})
		return
	}
	if current.Status == model.ProposalDispatched {
		s.pumpMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"proposal": current, "intent_id": current.IntentID, "run_id": current.RunID})
		return
	}
	if current.Status != model.ProposalApproved || current.Version != req.ExpectedVersion {
		s.pumpMu.Unlock()
		writeProposalError(w, fmt.Errorf("%w：提议当前为 %s v%d（请求 v%d）", store.ErrProposalConflict, current.Status, current.Version, req.ExpectedVersion))
		return
	}
	if current.IntentID == "" {
		s.pumpMu.Unlock()
		writeProposalError(w, fmt.Errorf("%w：已批准的提议没有对应意图，状态不一致，请重新审批", store.ErrProposalConflict))
		return
	}
	runID, err := s.StartRun(current.IntentID)
	if err != nil {
		s.pumpMu.Unlock()
		// The proposal stays approved: the dispatch was refused by admission, not
		// decided against. Re-read and retry once the blocker is gone.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "提议已批准，但这次派发未获准入：" + err.Error(), "code": "proposal_conflict",
		})
		return
	}
	actor := proposalActor(r)
	dispatched, err := s.Store.MarkProposalDispatched(current.ID, current.Version, runID, actor)
	s.pumpMu.Unlock()
	if err != nil {
		// The run exists; the receipt write did not. Never pretend the dispatch
		// didn't happen and never start another run: say exactly what happened.
		log.Printf("提议 %s 已起 run %s，但派发回写失败：%v", current.ID, runID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "run 已启动（" + runID + "），但提议的派发状态回写失败：" + err.Error() + "；不要重复派发，先GET提议核对",
			"code":  "proposal_dispatch_incomplete",
		})
		return
	}
	notification := s.notifyProposerOfDecision(r.Context(), dispatched, actor, "proposal_dispatched",
		"proposal:"+dispatched.ID+":dispatched",
		"提议 "+dispatched.ID+"（"+dispatched.Title+"）已派发：intent="+dispatched.IntentID+" run="+runID+"。")
	writeJSON(w, http.StatusAccepted, map[string]any{"proposal": dispatched, "intent_id": dispatched.IntentID, "run_id": runID, "notification": notification})
}

func (s *Server) withdrawProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "撤回必须由提议者的参与者身份发起", "code": "proposal_forbidden"})
		return
	}
	p, found := s.Store.GetProposal(r.PathValue("id"))
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "提议不存在：" + r.PathValue("id"), "code": "proposal_not_found"})
		return
	}
	if p.FromParticipantID != id.ParticipantID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "只有提议者可以撤回这条提议", "code": "proposal_forbidden"})
		return
	}
	var req proposalDecisionReq
	if !decodeProposalDecision(w, r, &req) {
		return
	}
	withdrawn, err := s.Store.WithdrawProposal(p.ID, req.ExpectedVersion, id.ParticipantID)
	if err != nil {
		writeProposalError(w, err)
		return
	}
	notification := s.notifyCommanderOfProposal(r.Context(), withdrawn, "proposal_withdrawn",
		"proposal:"+withdrawn.ID+":withdrawn",
		"执行者撤回了提议 "+withdrawn.ID+"（"+withdrawn.Title+"），不需要审批。")
	writeJSON(w, http.StatusOK, map[string]any{"proposal": withdrawn, "notification": notification})
}
