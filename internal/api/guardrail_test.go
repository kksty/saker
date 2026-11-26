package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kksty/saker/internal/aop/guardrail"
	"github.com/kksty/saker/internal/aop/operation"
	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/testutil"
	"github.com/kksty/saker/internal/ws"
)

// guardrailHub is a minimal AOP application server for the guardrail namespace.
// It answers Pending/Resolve exactly the way the harness does: a namespace
// message in the reply envelope's Any payload, correlated by reply_to.
type guardrailHub struct {
	t           *testing.T
	pending     []*guardrail.Review
	unsupported bool
	resolveErr  *pb.ProtocolError

	mu       sync.Mutex
	resolved []*guardrail.ResolveRequest
}

func (h *guardrailHub) handler(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var env pb.Envelope
		if proto.Unmarshal(raw, &env) != nil || env.Payload == nil {
			continue
		}
		var msg guardrail.ProtocolMessage
		if env.Payload.UnmarshalTo(&msg) != nil {
			continue
		}
		switch {
		case msg.GetPending() != nil:
			if h.unsupported {
				h.replyError(conn, env.Id, "UNSUPPORTED_NAMESPACE", "guardrail namespace is not registered")
				continue
			}
			h.reply(conn, env.Id, &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_PendingResult{
				PendingResult: &guardrail.PendingResponse{Reviews: h.pending},
			}})
		case msg.GetResolve() != nil:
			h.mu.Lock()
			h.resolved = append(h.resolved, msg.GetResolve())
			h.mu.Unlock()
			if h.resolveErr != nil {
				h.replyError(conn, env.Id, h.resolveErr.Code, h.resolveErr.Message)
				continue
			}
			h.reply(conn, env.Id, &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_Resolved{
				Resolved: &guardrail.ResolveResponse{},
			}})
		}
	}
}

func (h *guardrailHub) reply(conn *ws.Conn, replyTo string, msg proto.Message) {
	h.t.Helper()
	payload, err := anypb.New(msg)
	if err != nil {
		h.t.Errorf("guardrail reply payload: %v", err)
		return
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: "reply", ReplyTo: replyTo, Payload: payload})
	if err != nil {
		h.t.Errorf("guardrail reply envelope: %v", err)
		return
	}
	_ = conn.WriteBinary(raw)
}

func (h *guardrailHub) replyError(conn *ws.Conn, replyTo, code, message string) {
	h.reply(conn, replyTo, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_ProtocolError{
		ProtocolError: &pb.ProtocolError{Code: code, Message: message},
	}})
}

func (h *guardrailHub) resolvedRequests() []*guardrail.ResolveRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*guardrail.ResolveRequest{}, h.resolved...)
}

// guardrailExec is an executor whose node is the fake hub. It never runs a
// turn; these tests only exercise management reads.
type guardrailExec struct{ endpoint string }

func (e *guardrailExec) Name() string { return "guardrail-fake" }
func (e *guardrailExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{}, nil
}
func (e *guardrailExec) CanResume(string, string) bool { return true }
func (e *guardrailExec) TerminalEndpoint(string) (string, string, string, bool) {
	return e.endpoint, "hub-token", "local", true
}

// guardrailServer builds a project with one running run on session sess-1 and a
// fake hub. The case directory and a live run journal are real, so the tests can
// assert the guardrail audit line.
func guardrailServer(t *testing.T, hub *guardrailHub) (*Server, model.Run, string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(hub.handler))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := filepath.Join(dir, "cases")
	ex := &guardrailExec{endpoint: srv.URL}
	s := New(st, ex, dir, caseRoot)
	in, err := st.CreateIntent(model.Intent{Title: "审批测试"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunRunning, SessionID: "sess-1", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(caseRoot, in.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "runs", run.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(run.ID, logPath); err != nil {
		t.Fatal(err)
	}
	closeJournal := func() { s.closeJournal(run.ID) }
	t.Cleanup(closeJournal)
	return s, run, logPath, closeJournal
}

func pendingReview() *guardrail.Review {
	return &guardrail.Review{
		Call: &pb.ToolCall{
			Id: "call-1", Name: "bash", Kind: "tool",
			Arguments: &pb.EncodedValue{Data: []byte(`{"command":"nmap -sV 10.0.0.1"}`)},
		},
		Operation: &operation.Ref{OperationId: "op-1", CallId: "call-1"},
		SessionId: "sess-1",
		State:     guardrail.ReviewState_REVIEW_STATE_PENDING,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
	}
}

func TestGuardrailPendingReturnsApprovalAndInboxCount(t *testing.T) {
	testutil.ReportDialRetries(t)
	hub := &guardrailHub{t: t, pending: []*guardrail.Review{pendingReview()}}
	s, _, _, _ := guardrailServer(t, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/guardrail/pending?session=sess-1", nil)
	rec := httptest.NewRecorder()
	s.guardrailPending(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		State    string `json:"state"`
		Count    int    `json:"count"`
		Attempts int    `json:"attempts"`
		Pending  []struct {
			OperationID string          `json:"operation_id"`
			Tool        string          `json:"tool"`
			Arguments   json.RawMessage `json:"arguments"`
			SessionID   string          `json:"session_id"`
			State       string          `json:"state"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "available" || out.Count != 1 || out.Attempts != 1 || len(out.Pending) != 1 {
		t.Fatalf("pending response = %s", rec.Body.String())
	}
	item := out.Pending[0]
	if item.OperationID != "op-1" || item.Tool != "bash" || item.SessionID != "sess-1" || item.State != "REVIEW_STATE_PENDING" {
		t.Fatalf("pending item = %+v", item)
	}
	if !strings.Contains(string(item.Arguments), "nmap") {
		t.Fatalf("arguments lost: %s", item.Arguments)
	}

	// The operator inbox is a local projection of the last read: no dial.
	inbox := httptest.NewRecorder()
	s.operatorInbox(inbox, httptest.NewRequest(http.MethodGet, "/api/operator-inbox", nil))
	var view struct {
		Guardrail struct {
			State    string         `json:"state"`
			Count    int            `json:"count"`
			Sessions map[string]int `json:"sessions"`
		} `json:"guardrail"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(inbox.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Guardrail.State != "available" || view.Guardrail.Count != 1 || view.Guardrail.Sessions["sess-1"] != 1 || view.Total < 1 {
		t.Fatalf("inbox guardrail = %s", inbox.Body.String())
	}
}

func TestGuardrailPendingUnavailableRecordsJournal(t *testing.T) {
	hub := &guardrailHub{t: t, unsupported: true}
	s, _, logPath, _ := guardrailServer(t, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/guardrail/pending?session=sess-1", nil)
	rec := httptest.NewRecorder()
	s.guardrailPending(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unavailable pending = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "unavailable" || out.Count != 0 || !strings.Contains(out.Reason, "guardrail") {
		t.Fatalf("unavailable response = %s", rec.Body.String())
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"guardrail"`) || !strings.Contains(string(raw), "pending_unavailable") {
		t.Fatalf("unavailable attempt was not journaled: %s", raw)
	}
}

func TestGuardrailPendingWithoutHostIsUnavailable(t *testing.T) {
	s, _, _, _ := guardrailServer(t, &guardrailHub{t: t})
	// Replace the executor with one that has no AOP node.
	s.Exec = &noHostExec{}
	req := httptest.NewRequest(http.MethodGet, "/api/guardrail/pending?session=sess-1", nil)
	rec := httptest.NewRecorder()
	s.guardrailPending(rec, req)
	var out struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || out.State != "unavailable" || !strings.Contains(out.Reason, "宿主") {
		t.Fatalf("no-host pending = %d %s", rec.Code, rec.Body.String())
	}
}

type noHostExec struct{}

func (*noHostExec) Name() string { return "no-host" }
func (*noHostExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{}, nil
}

func TestOperatorInboxGuardrailNeverChecked(t *testing.T) {
	s, _, _, _ := guardrailServer(t, &guardrailHub{t: t})
	rec := httptest.NewRecorder()
	s.operatorInbox(rec, httptest.NewRequest(http.MethodGet, "/api/operator-inbox", nil))
	var view struct {
		Guardrail struct {
			State string `json:"state"`
			Count int    `json:"count"`
		} `json:"guardrail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Guardrail.State != "never_checked" || view.Guardrail.Count != 0 {
		t.Fatalf("inbox guardrail = %s", rec.Body.String())
	}
}

func TestGuardrailResolveApprove(t *testing.T) {
	hub := &guardrailHub{t: t, pending: []*guardrail.Review{pendingReview()}}
	s, _, logPath, _ := guardrailServer(t, hub)
	// Prime the inbox cache with one pending item.
	prime := httptest.NewRecorder()
	s.guardrailPending(prime, httptest.NewRequest(http.MethodGet, "/api/guardrail/pending?session=sess-1", nil))

	body := `{"session_id":"sess-1","operation_id":"op-1","approve":true,"reason":"授权测试"}`
	req := httptest.NewRequest(http.MethodPost, "/api/guardrail/resolve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.guardrailResolve(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		State       string `json:"state"`
		OperationID string `json:"operation_id"`
		Approve     bool   `json:"approve"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "resolved" || out.OperationID != "op-1" || !out.Approve {
		t.Fatalf("resolve response = %s", rec.Body.String())
	}
	resolved := hub.resolvedRequests()
	if len(resolved) != 1 || !resolved[0].GetApprove() || resolved[0].GetOperationId() != "op-1" {
		t.Fatalf("host saw = %+v", resolved)
	}
	// The decision is an audit event on the run's journal, not a new ledger row.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"action":"resolve"`) || !strings.Contains(string(raw), `"approve":true`) {
		t.Fatalf("approval was not journaled: %s", raw)
	}
}

func TestGuardrailResolveHostRejectionIsNotSuccess(t *testing.T) {
	hub := &guardrailHub{t: t, resolveErr: &pb.ProtocolError{Code: "NOT_FOUND", Message: "operation is not pending"}}
	s, _, _, _ := guardrailServer(t, hub)
	body := `{"session_id":"sess-1","operation_id":"op-gone","approve":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/guardrail/resolve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.guardrailResolve(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rejected resolve = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != "failed" || !strings.Contains(out.Reason, "待审批") {
		t.Fatalf("rejection response = %s", rec.Body.String())
	}
}

func TestGuardrailResolveValidationAndOperatorOnly(t *testing.T) {
	s, _, _, _ := guardrailServer(t, &guardrailHub{t: t})
	for name, body := range map[string]string{
		"not json":          `{`,
		"missing session":   `{"operation_id":"op-1","approve":true}`,
		"missing operation": `{"session_id":"sess-1","approve":true}`,
		"missing approve":   `{"session_id":"sess-1","operation_id":"op-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.guardrailResolve(rec, httptest.NewRequest(http.MethodPost, "/api/guardrail/resolve", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", name, rec.Code, rec.Body.String())
			}
		})
	}
	// A harness session cannot approve its own intercepted tool call.
	ctx := WithAgentSurface(context.Background())
	rec := httptest.NewRecorder()
	s.guardrailResolve(rec, httptest.NewRequest(http.MethodPost, "/api/guardrail/resolve", strings.NewReader(`{"session_id":"s","operation_id":"o","approve":true}`)).WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session resolve = %d %s", rec.Code, rec.Body.String())
	}
	pending := httptest.NewRecorder()
	s.guardrailPending(pending, httptest.NewRequest(http.MethodGet, "/api/guardrail/pending?session=s", nil).WithContext(ctx))
	if pending.Code != http.StatusForbidden {
		t.Fatalf("session pending = %d %s", pending.Code, pending.Body.String())
	}
}
