package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type operatorInboxHandoff struct {
	IntentID string             `json:"intent_id"`
	Title    string             `json:"title"`
	Status   string             `json:"status"`
	Request  model.OperatorWait `json:"request"`
	// At is when the direction registered the wait, so the console can mix every
	// pending row into one newest-first list instead of always stacking
	// authorization above handoffs.
	At string `json:"at,omitempty"`
}

type operatorInboxAcceptance struct {
	GoalID         string   `json:"goal_id"`
	Statement      string   `json:"statement"`
	ReceiptChanged []string `json:"receipt_changed,omitempty"`
	// At is the goal's last revision: the receipt went stale because the
	// statement changed then, which is what the operator is being asked about.
	At string `json:"at,omitempty"`
}

// operatorInboxGuardrail is the read-only tool-approval projection. The count
// comes from the last guardrail pending read (guardrail.go); the inbox itself
// never dials the host, so loading it stays local. State is "never_checked"
// before the console has asked once.
type operatorInboxGuardrail struct {
	State     string         `json:"state"`
	Reason    string         `json:"reason,omitempty"`
	Count     int            `json:"count"`
	CheckedAt *time.Time     `json:"checked_at,omitempty"`
	Sessions  map[string]int `json:"sessions,omitempty"`
}

// operatorInboxRecent is one authorization request that no longer waits for a
// person, kept so the console can show what the control plane recorded next to
// what the text claims. It is history, not a task: the summary ignores it.
type operatorInboxRecent struct {
	ID         string     `json:"id"`
	Action     string     `json:"action,omitempty"`
	Asset      string     `json:"asset,omitempty"`
	Impact     string     `json:"impact,omitempty"`
	Status     string     `json:"status"`
	Resolution string     `json:"resolution,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// inboxRecentLimit bounds the closed rows: without it the payload grows with
// every request ever decided. Newest resolution first; a row closed without a
// timestamp falls back to its creation time and comes after the timed ones.
const inboxRecentLimit = 20

func recentInboxRows(all []model.OperatorRequest, limit int) []operatorInboxRecent {
	rows := make([]operatorInboxRecent, 0, len(all))
	for _, req := range all {
		if req.Status == model.OperatorRequestPending {
			continue
		}
		rows = append(rows, operatorInboxRecent{
			ID: req.ID, Action: req.Action, Asset: req.Asset, Impact: req.Impact,
			Status: req.Status, Resolution: req.Resolution,
			CreatedAt: req.CreatedAt, ResolvedAt: req.ResolvedAt,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].ResolvedAt, rows[j].ResolvedAt
		switch {
		case a == nil && b == nil:
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return a.After(*b)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

type operatorInbox struct {
	Authorization []model.OperatorRequest   `json:"authorization"`
	Handoff       []operatorInboxHandoff    `json:"handoff"`
	Acceptance    []operatorInboxAcceptance `json:"acceptance"`
	Warnings      []boardRunWarnings        `json:"warnings"`
	Guardrail     operatorInboxGuardrail    `json:"guardrail"`
	Total         int                       `json:"total"`
	// Summary is the same data as the lists above, split by category with an
	// explicit state per category. The badge, the overview and the detail view
	// all read it, so their numbers cannot disagree.
	Summary operatorInboxSummary `json:"summary"`
	// Recent carries the rows that no longer wait for a person. The console
	// needs them for the overlap an operator has to look at: a request dismissed
	// with a resolution that says work resumed, or a pending row whose text says
	// it already happened, is invisible if the inbox only lists what is waiting.
	// A closed row is not work: it never counts toward Total or the summary.
	Recent []operatorInboxRecent `json:"recent"`
}

// operatorInbox is the console's single "what needs a human" read. It merges
// the project-level authorization requests with the per-intent human handoffs,
// stale goal receipts and run warnings that used to be scattered across pages.
// It is operator supervision: the route is hidden from harness sessions.
func (s *Server) operatorInbox(w http.ResponseWriter, r *http.Request) {
	out := operatorInbox{
		Authorization: []model.OperatorRequest{},
		Handoff:       []operatorInboxHandoff{},
		Acceptance:    []operatorInboxAcceptance{},
		Warnings:      []boardRunWarnings{},
	}
	openRequests := s.Store.OperatorRequests()
	for _, req := range openRequests {
		if req.Status == model.OperatorRequestPending {
			out.Authorization = append(out.Authorization, req)
		}
	}
	out.Recent = recentInboxRows(openRequests, inboxRecentLimit)
	for _, in := range s.Store.ListIntents() {
		if in.Wait == nil || in.Wait.Operator == nil || in.Wait.Operator.CompletedAt != nil {
			continue
		}
		out.Handoff = append(out.Handoff, operatorInboxHandoff{
			IntentID: in.ID, Title: in.Title, Status: in.Status, Request: *in.Wait.Operator,
			At: in.Wait.RegisteredAt.Format(time.RFC3339),
		})
	}
	for _, g := range s.Store.Snapshot().Goals {
		if !g.ReceiptStale {
			continue
		}
		// The goal row keeps the stamp time, not the statement revision time; the
		// acceptance row is "the receipt no longer matches", so the stamp is the
		// time it still had. Rows without a stamp sort to the end.
		at := ""
		if g.CompletedAt != nil {
			at = g.CompletedAt.Format(time.RFC3339)
		}
		out.Acceptance = append(out.Acceptance, operatorInboxAcceptance{
			GoalID: g.ID, Statement: g.Statement, ReceiptChanged: g.ReceiptChanged,
			At: at,
		})
	}
	out.Warnings = recentHandoffWarnings(s.Store, 5, false)
	out.Guardrail = s.guardrailInboxView()
	out.Total = len(out.Authorization) + len(out.Handoff) + len(out.Acceptance) + len(out.Warnings) + out.Guardrail.Count
	// Built from the very slices above: one read feeds both the lists and the
	// counts the console shows elsewhere.
	out.Summary = inboxSummary(out.Authorization, out.Handoff, out.Acceptance, out.Warnings, out.Guardrail)
	writeJSON(w, 200, out)
}

// createOperatorRequest is the commander's concrete authorization ask. It is a
// request, never an authorization, and it spends no model round: the operator
// sees it in the inbox. It never pauses the project or the decider either — the
// ask is one todo row, and the commander keeps dispatching other directions while
// the operator decides; the answer comes back as operator speech when the row is
// resolved. An executor is refused — a direction may not summon the operator to
// expand its own scope. A pending generic fallback is filled in place, so one
// idle episode produces one evolving request instead of a generic row plus a
// duplicate.
func (s *Server) createOperatorRequest(w http.ResponseWriter, r *http.Request) {
	if _, participant := identityOf(r); participant {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "创建授权请求只能由指挥官或操作员提交。",
			"code":  "commander_or_operator_only",
		})
		return
	}
	var req struct {
		Action        string `json:"action"`
		Asset         string `json:"asset"`
		Impact        string `json:"impact"`
		Authorization string `json:"authorization"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeErr(w, 400, "请求体不是 JSON")
		return
	}
	requestedBy := model.OperatorRequestFromOperator
	if harnessSession(r) {
		requestedBy = model.OperatorRequestFromCommander
	}
	out, created, err := s.Store.CreateOperatorRequest(req.Action, req.Asset, req.Impact, req.Authorization, requestedBy)
	if err != nil {
		if errors.Is(err, store.ErrOperatorRequestPending) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	if created {
		s.recordAction("operator_request_create", out.ID, out.Action)
	} else {
		s.recordAction("operator_request_detail", out.ID, out.Action)
	}
	writeJSON(w, 200, map[string]any{"request": out, "created": created})
}

// updateOperatorRequest is the commander's detail fill, also usable by the
// operator. It never marks the request resolved and never grants authorization.
func (s *Server) updateOperatorRequest(w http.ResponseWriter, r *http.Request) {
	// The operator and the commander (project mux, no participant identity) may
	// fill details. A direction's executor has a participant identity and must
	// not write the operator's inbox on another direction's behalf.
	if _, participant := identityOf(r); participant {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "补齐授权明细只能由指挥官或操作员提交。",
			"code":  "commander_or_operator_only",
		})
		return
	}
	var req struct {
		Action        string `json:"action"`
		Asset         string `json:"asset"`
		Impact        string `json:"impact"`
		Authorization string `json:"authorization"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req) != nil {
		writeErr(w, 400, "请求体不是 JSON")
		return
	}
	out, err := s.Store.UpdateOperatorRequest(r.PathValue("id"), req.Action, req.Asset, req.Impact, req.Authorization)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.recordAction("operator_request_detail", out.ID, out.Action)
	writeJSON(w, 200, map[string]any{"request": out})
}

func (s *Server) resolveOperatorRequest(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "处理操作员请求属于操作员：指挥官只能补明细，不能替操作员确认授权。",
			"code":  "operator_only",
		})
		return
	}
	var req struct {
		Resolution string `json:"resolution"`
	}
	if !decodeOptionalJSON(w, http.MaxBytesReader(w, r.Body, 16384), &req) {
		return
	}
	out, err := s.Store.ResolveOperatorRequest(r.PathValue("id"), req.Resolution)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.recordAction("operator_request_resolve", out.ID, strings.TrimSpace(req.Resolution))
	s.notifyCommanderOfResolvedRequest(r.Context(), out)
	writeJSON(w, 200, map[string]any{"request": out})
}

// notifyCommanderOfResolvedRequest tells the commander that a request it filed
// has an answer. A request is a ledger row, not a project pause: the commander
// keeps dispatching other directions while the operator decides, so the
// answer is news the next round must act on (use the granted scope, or decide it
// is still insufficient) rather than something that only the 5-minute heartbeat
// would eventually surface. It rides the message service as durable operator
// speech, so a commander session that is between rounds reads it in its next
// prompt and a live one gets it over IOA.
//
// The notification does not turn the resolution into an authorization by itself:
// the three project documents and the current ledger still decide what may run.
// A failed write is logged and the decider is still woken, because the request
// row already carries the answer the round can read back.
func (s *Server) notifyCommanderOfResolvedRequest(ctx context.Context, out model.OperatorRequest) {
	action := strings.TrimSpace(out.Action)
	text := "操作员已处理授权请求 " + out.ID
	if action != "" {
		text += "（" + action + "）"
	}
	text += "。处理说明：" + strings.TrimSpace(out.Resolution) + "。"
	text += "请求只是待办，不阻塞其他方向：按项目文档与当前账本判断下一步，其它可推进方向照常派发；需要更多授权时再登记一条具体请求。"
	msg, _, err := s.Store.AddMessage(model.Message{
		ProjectID:         s.projectID(),
		FromParticipantID: collab.OperatorID,
		ToParticipantID:   collab.CommanderID,
		Kind:              "operator_request_resolved",
		Text:              text,
		Durable:           true,
		IdempotencyKey:    "operator-request:" + out.ID + ":resolved",
		Status:            model.MessageQueued,
	})
	if err != nil {
		log.Printf("操作员请求 %s 已处理，但通知指挥官失败：%v", out.ID, err)
		if s.Decider != nil {
			s.Decider.Wake("operator")
		}
		return
	}
	s.deliverToCommander(ctx, msg)
}

// operatorRequestDetailSection is the one-shot prompt section for a request the
// operator asked the commander to enrich. It is injected before the board and
// the flags are cleared once the prompt carries them: a failed round can be
// retried by clicking again, but an unchanged round does not repeat forever.
func (s *Server) operatorRequestDetailSection() (string, []string) {
	var rows []model.OperatorRequest
	for _, req := range s.Store.OperatorRequests() {
		if req.Status == model.OperatorRequestPending && req.DetailRequested {
			rows = append(rows, req)
		}
	}
	if len(rows) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n## 操作员请求补齐授权明细\n操作员点了「让指挥官列明细」。只补明细，不派新意图，不要 complete/standby。\n")
	ids := make([]string, 0, len(rows))
	for _, req := range rows {
		ids = append(ids, req.ID)
		fmt.Fprintf(&b, "- request_id=%s；当前：%s\n", req.ID, strings.TrimSpace(req.Action))
		fmt.Fprintf(&b, "  用 `PUT /api/operator-requests/%s` 提交 `{\"action\":\"…\",\"asset\":\"…\",\"impact\":\"…\",\"authorization\":\"…\"}`；填写后请求会出现在操作员收件箱，它不是授权。\n", req.ID)
	}
	return b.String(), ids
}

// requestOperatorDetails asks the commander for one round that fills the
// concrete fields. The request id rides the next prompt once; it does not spend
// a round unless the operator clicks.
func (s *Server) requestOperatorDetails(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "让指挥官列明细属于操作员请求。",
			"code":  "operator_only",
		})
		return
	}
	if s.paused.Load() {
		writeErr(w, http.StatusConflict, "项目已暂停：先继续项目，再让指挥官列明细")
		return
	}
	if s.Decider == nil {
		writeErr(w, http.StatusConflict, "没有自动指挥官：不能请求明细")
		return
	}
	out, err := s.Store.RequestOperatorDetails(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Decider.Wake("operator")
	writeJSON(w, http.StatusAccepted, map[string]any{"request": out, "woken": true})
}
