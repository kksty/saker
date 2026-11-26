package api

// Guardrail (tool approval) API.
//
// Guardrail is a separate AOP namespace: a policy can hold a tool call and ask
// the operator to approve or reject it. The control plane only relays that
// question to the console and the answer back to the host — it never executes
// the tool and it never decides on its own. The whole route is operator-only:
// a harness session approving its own intercepted tool call would defeat the
// policy.
//
// Nothing here writes the ledger schema. A pending read and a resolve are run
// events in the existing journal (kind=guardrail), which is where an operator
// auditing one run already looks; a project with no live run simply has no
// journal and gets the process log instead.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/aop/guardrail"
)

// guardrailPendingView is one pending tool call as the console renders it. The
// arguments are opaque provider JSON: they are relayed only when valid, never
// re-encoded through a guessed schema.
type guardrailPendingView struct {
	OperationID   string          `json:"operation_id"`
	CallID        string          `json:"call_id,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	ArgumentsText string          `json:"arguments_text,omitempty"`
	SessionID     string          `json:"session_id"`
	State         string          `json:"state,omitempty"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

// guardrailSessionState is one session's read result in a fan-out response.
type guardrailSessionState struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Count     int    `json:"count"`
}

// guardrailPendingResponse is the one answer of GET /api/guardrail/pending.
// State is "available" (the host answered; the list may still be empty) or
// "unavailable" (no host, no guardrail namespace, or every session read
// failed) with a reason. It is a read projection: HTTP 200 either way, like the
// tools catalogue, so the console renders a state instead of treating "no
// guardrail configured" as a failure.
type guardrailPendingResponse struct {
	State     string                  `json:"state"`
	Reason    string                  `json:"reason,omitempty"`
	CheckedAt time.Time               `json:"checked_at"`
	Attempts  int                     `json:"attempts"`
	Count     int                     `json:"count"`
	Pending   []guardrailPendingView  `json:"pending"`
	Sessions  []guardrailSessionState `json:"sessions,omitempty"`
}

// guardrailResolveResponse is the answer of POST /api/guardrail/resolve. State
// is "resolved", "failed" (the host refused: not pending / wrong session) or
// "unavailable" (no host or namespace). The action never silently succeeds.
type guardrailResolveResponse struct {
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	SessionID   string `json:"session_id"`
	OperationID string `json:"operation_id"`
	Approve     bool   `json:"approve"`
	Attempts    int    `json:"attempts"`
}

// guardrailJournalEvent is one run-journal line. kind=guardrail keeps it apart
// from model/tool events while reusing the run's existing audit file.
type guardrailJournalEvent struct {
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Action      string    `json:"action"` // pending_read|pending_unavailable|resolve|resolve_failed
	SessionID   string    `json:"session_id"`
	OperationID string    `json:"operation_id,omitempty"`
	Approve     *bool     `json:"approve,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	V           int       `json:"v,omitempty"` // stamped by runJournal.append
}

// guardrailPending handles GET /api/guardrail/pending?session=<id>. Without a
// session it reads every live run's session, which is what the inbox badge
// needs; with one it reads exactly that session (the run page).
func (s *Server) guardrailPending(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "工具审批属于操作员：会话不能读取或代为批准自己的拦截调用。",
			"code":  "operator_only",
		})
		return
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	sessions := []string{session}
	if session == "" {
		sessions = s.liveSessions()
	}
	resp := s.readGuardrailPending(r.Context(), sessions)
	s.noteGuardrailSnapshot(resp)
	writeJSON(w, http.StatusOK, resp)
}

// readGuardrailPending performs the bounded host reads. Every session is read
// once (no polling): a blocked turn waits for the operator, so the read either
// sees it or reports why it could not.
func (s *Server) readGuardrailPending(ctx context.Context, sessions []string) guardrailPendingResponse {
	resp := guardrailPendingResponse{Pending: []guardrailPendingView{}, CheckedAt: time.Now()}
	if len(sessions) == 0 {
		resp.State = "unavailable"
		resp.Reason = "没有正在进行中的 AOP 会话；工具审批只针对运行中的会话。"
		resp.Attempts = 1
		s.appendGuardrailJournal("", guardrailJournalEvent{
			Kind: "guardrail", At: resp.CheckedAt, Action: "pending_unavailable",
			Reason: resp.Reason, Attempts: 1,
		})
		return resp
	}
	ctx, cancel := context.WithTimeout(ctx, hostReadTimeout)
	defer cancel()
	reads := make([]guardrailSessionRead, len(sessions))
	var wg sync.WaitGroup
	for i, id := range sessions {
		wg.Add(1)
		go func(index int, sessionID string) {
			defer wg.Done()
			reads[index] = s.readGuardrailSession(ctx, sessionID)
		}(i, id)
	}
	wg.Wait()

	available := 0
	var reasons []string
	for _, read := range reads {
		state := guardrailSessionState{SessionID: read.SessionID, Count: len(read.Pending)}
		if read.Err != nil {
			state.State = "unavailable"
			state.Reason = guardrailUnavailableReason(read.Err)
			reasons = append(reasons, read.SessionID+"："+state.Reason)
			// One journal line per failed read: it is the audit trail of "the
			// control plane tried to ask and why it could not".
			s.appendGuardrailJournal(read.SessionID, guardrailJournalEvent{
				Kind: "guardrail", At: time.Now(), Action: "pending_unavailable",
				SessionID: read.SessionID, Reason: state.Reason, Attempts: 1,
			})
		} else {
			state.State = "available"
			available++
			for _, review := range read.Pending {
				resp.Pending = append(resp.Pending, guardrailPendingItem(review, read.SessionID))
			}
		}
		resp.Sessions = append(resp.Sessions, state)
		resp.Attempts++
	}
	resp.Count = len(resp.Pending)
	switch {
	case available == 0:
		resp.State = "unavailable"
		resp.Reason = strings.Join(reasons, "；")
		if len(sessions) == 1 {
			resp.Reason = reasons[0]
		}
	case available < len(reads):
		resp.State = "available"
		resp.Reason = "部分会话读取失败：" + strings.Join(reasons, "；")
	default:
		resp.State = "available"
	}
	return resp
}

type guardrailSessionRead struct {
	SessionID string
	Pending   []*guardrail.Review
	Err       error
}

// readGuardrailSession dials the node that owns the session and lists pending
// reviews. The connection is closed immediately: an approval is a management
// query, not a subscription, so no watch is left behind.
func (s *Server) readGuardrailSession(ctx context.Context, sessionID string) guardrailSessionRead {
	read := guardrailSessionRead{SessionID: sessionID}
	run, _ := s.runForSession(sessionID)
	node := run.Node
	base, token, _, ok := s.hostForNode(node)
	if !ok {
		read.Err = errNoHostLink
		return read
	}
	conn, err := aop.New(base, token).Dial(ctx)
	if err != nil {
		read.Err = fmt.Errorf("连接宿主失败：%w", err)
		return read
	}
	defer conn.Close()
	reviews, err := conn.Pending(ctx, sessionID)
	if err != nil {
		read.Err = err
		return read
	}
	read.Pending = reviews
	return read
}

func guardrailPendingItem(review *guardrail.Review, sessionID string) guardrailPendingView {
	item := guardrailPendingView{
		SessionID:   sessionID,
		OperationID: review.GetOperation().GetOperationId(),
		State:       review.GetState().String(),
	}
	if call := review.GetCall(); call != nil {
		item.CallID = call.GetId()
		item.Tool = call.GetName()
		item.Kind = call.GetKind()
	}
	if operation := review.GetOperation(); operation != nil && operation.GetOperationId() != "" {
		item.OperationID = operation.GetOperationId()
	}
	if call := review.GetCall(); call != nil && call.GetArguments() != nil {
		data := call.GetArguments().GetData()
		if len(data) > 0 {
			if json.Valid(data) {
				item.Arguments = json.RawMessage(data)
			} else {
				item.ArgumentsText = clipRunes(string(data), 2000)
			}
		}
	}
	if expires := review.GetExpiresAt(); expires != nil && expires.IsValid() {
		at := expires.AsTime()
		item.ExpiresAt = &at
	}
	return item
}

// guardrailResolve handles POST /api/guardrail/resolve. A missing approve field
// is a 400: "reject" is an explicit decision, not the zero value of an omitted
// one.
func (s *Server) guardrailResolve(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "工具审批属于操作员：会话不能批准自己的拦截调用。",
			"code":  "operator_only",
		})
		return
	}
	var in struct {
		SessionID   string `json:"session_id"`
		OperationID string `json:"operation_id"`
		Approve     *bool  `json:"approve"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是 JSON：需要 session_id、operation_id、approve"})
		return
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.OperationID = strings.TrimSpace(in.OperationID)
	in.Reason = clipRunes(strings.TrimSpace(in.Reason), 1000)
	if in.SessionID == "" || in.OperationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 session_id 或 operation_id"})
		return
	}
	if in.Approve == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 approve：true 批准，false 拒绝"})
		return
	}
	resp := s.resolveGuardrail(r.Context(), in.SessionID, in.OperationID, *in.Approve, in.Reason)
	code := http.StatusOK
	if resp.State != "resolved" {
		code = http.StatusConflict
	}
	writeJSON(w, code, resp)
}

func (s *Server) resolveGuardrail(ctx context.Context, sessionID, operationID string, approve bool, reason string) guardrailResolveResponse {
	resp := guardrailResolveResponse{
		SessionID: sessionID, OperationID: operationID, Approve: approve, Attempts: 1,
	}
	run, _ := s.runForSession(sessionID)
	base, token, _, ok := s.hostForNode(run.Node)
	if !ok {
		resp.State, resp.Reason = "unavailable", errNoHostLink.Error()
		s.appendGuardrailJournal(sessionID, guardrailJournalEvent{
			Kind: "guardrail", At: time.Now(), Action: "resolve_failed", SessionID: sessionID,
			OperationID: operationID, Approve: &approve, Reason: resp.Reason,
		})
		return resp
	}
	ctx, cancel := context.WithTimeout(ctx, hostReadTimeout)
	defer cancel()
	conn, err := aop.New(base, token).Dial(ctx)
	if err != nil {
		resp.State = "unavailable"
		resp.Reason = "连接宿主失败：" + s.redactSecrets(err.Error())
		s.appendGuardrailJournal(sessionID, guardrailJournalEvent{
			Kind: "guardrail", At: time.Now(), Action: "resolve_failed", SessionID: sessionID,
			OperationID: operationID, Approve: &approve, Reason: resp.Reason,
		})
		return resp
	}
	defer conn.Close()
	if err := conn.Resolve(ctx, sessionID, operationID, approve); err != nil {
		resp.State = "failed"
		resp.Reason = s.redactSecrets(guardrailResolveFailure(err))
		s.appendGuardrailJournal(sessionID, guardrailJournalEvent{
			Kind: "guardrail", At: time.Now(), Action: "resolve_failed", SessionID: sessionID,
			OperationID: operationID, Approve: &approve, Reason: resp.Reason,
		})
		return resp
	}
	resp.State = "resolved"
	resp.Reason = reason
	s.appendGuardrailJournal(sessionID, guardrailJournalEvent{
		Kind: "guardrail", At: time.Now(), Action: "resolve", SessionID: sessionID,
		OperationID: operationID, Approve: &approve, Reason: reason,
	})
	s.noteGuardrailResolved(sessionID)
	return resp
}

// guardrailResolveFailure translates the host's refusal without hiding it: not
// pending, expired and wrong-session are different operator problems.
func guardrailResolveFailure(err error) string {
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case aop.IsConnectNotFound(err), strings.Contains(lower, "not_found"), strings.Contains(lower, "not found"):
		return "宿主没有这个待审批项（可能已过期、已由别处处理，或 session 不匹配）：" + text
	case strings.Contains(lower, "unsupported"):
		return "宿主未启用工具审批（guardrail/JEV）：" + text
	default:
		return "宿主拒绝这次审批：" + text
	}
}

// guardrailUnavailableReason classifies a failed pending read for the console:
// "no JEV" and "host down" need different operator actions.
func guardrailUnavailableReason(err error) string {
	if errors.Is(err, errNoHostLink) {
		return err.Error()
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "unsupported"), strings.Contains(lower, "unknown namespace"),
		strings.Contains(lower, "guardrail namespace"):
		return "宿主没有启用工具审批（guardrail/JEV）：AOP guardrail 命名空间不可用，请在 guardrail 配置 provider=jev 后重启宿主。"
	case strings.Contains(lower, "not_found"), strings.Contains(lower, "not found"):
		return "宿主没有这个会话（可能已结束）：" + err.Error()
	default:
		return "读取待审批项失败：" + err.Error()
	}
}

// appendGuardrailJournal records one attempt in the owning run's journal. A
// session without a live run has no journal (the run already settled): that is
// the process log's business, not an error the operator should see twice.
func (s *Server) appendGuardrailJournal(sessionID string, ev guardrailJournalEvent) {
	run, ok := s.runForSession(sessionID)
	if !ok {
		log.Printf("guardrail: %s session=%s reason=%s", ev.Action, sessionID, ev.Reason)
		return
	}
	ev.RunID = run.ID
	if err := s.appendEvent(run.ID, ev); err != nil && !errors.Is(err, errNoJournal) {
		log.Printf("run %s: 写 guardrail 事件失败：%v", run.ID, err)
	}
}

// noteGuardrailSnapshot keeps the last pending read for the operator inbox
// projection. The inbox is a local read: it never dials the host itself, so a
// console poll does not turn into N AOP reads per page.
func (s *Server) noteGuardrailSnapshot(resp guardrailPendingResponse) {
	s.guardrailMu.Lock()
	defer s.guardrailMu.Unlock()
	s.guardrailAt = resp.CheckedAt
	s.guardrailState = resp.State
	s.guardrailReason = resp.Reason
	if s.guardrailCounts == nil {
		s.guardrailCounts = map[string]int{}
	}
	for _, read := range resp.Sessions {
		count := read.Count
		if read.State != "available" {
			count = 0
		}
		if count > 0 {
			s.guardrailCounts[read.SessionID] = count
		} else {
			delete(s.guardrailCounts, read.SessionID)
		}
	}
}

// noteGuardrailResolved drops one resolved operation from the cached count. The
// host is the authority; this only keeps the inbox badge from staying stale
// until the next pending read.
func (s *Server) noteGuardrailResolved(sessionID string) {
	s.guardrailMu.Lock()
	defer s.guardrailMu.Unlock()
	if s.guardrailCounts == nil {
		return
	}
	if count := s.guardrailCounts[sessionID]; count > 1 {
		s.guardrailCounts[sessionID] = count - 1
	} else {
		delete(s.guardrailCounts, sessionID)
	}
}

// guardrailInboxView is the read-only projection operatorInbox merges. State is
// "never_checked" until the console has called the pending endpoint at least
// once; Pending is a count, not the list (the run page owns the details).
func (s *Server) guardrailInboxView() operatorInboxGuardrail {
	s.guardrailMu.Lock()
	defer s.guardrailMu.Unlock()
	view := operatorInboxGuardrail{
		State:    s.guardrailState,
		Reason:   s.guardrailReason,
		Count:    totalGuardrailCounts(s.guardrailCounts),
		Sessions: map[string]int{},
	}
	for sessionID, count := range s.guardrailCounts {
		view.Sessions[sessionID] = count
	}
	if view.State == "" {
		view.State = "never_checked"
		view.Reason = "还没有发起工具审批查询；打开运行页或刷新审批卡片后会更新。"
	}
	if !s.guardrailAt.IsZero() {
		at := s.guardrailAt
		view.CheckedAt = &at
	}
	return view
}

func totalGuardrailCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
