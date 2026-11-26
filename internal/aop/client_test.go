package aop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/kksty/saker/internal/aop/guardrail"
	"github.com/kksty/saker/internal/aop/operation"
	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/ws"
)

// The session workspace is a harness fork extension on OpenSessionRequest: the
// upstream schema has no per-session working directory (D110). Pin the wire
// shape so a refactor cannot silently send sessions back to the agent's
// process directory.
func TestWorkDirExtensionPayload(t *testing.T) {
	if WorkDirExtension("  ") != nil {
		t.Fatal("empty work dir must not create an extension")
	}
	const dir = `C:\data\projects\p\cases\in_1`
	ext := WorkDirExtension(dir)
	if ext.GetTypeUrl() != WorkDirExtensionURL {
		t.Fatalf("type url = %q", ext.GetTypeUrl())
	}
	var payload workDirPayload
	if err := json.Unmarshal(ext.GetValue(), &payload); err != nil {
		t.Fatalf("payload = %q: %v", ext.GetValue(), err)
	}
	if payload.WorkDir != dir {
		t.Fatalf("work dir = %q, want %q", payload.WorkDir, dir)
	}
	if !payload.SingleTask {
		t.Fatal("control plane lost ownership of turn scheduling")
	}
}

// The eval criterion is a harness extension, not prompt text. Pin the wire
// shape here: the harness validates it with cyber.agent.AgentRunOptions, and a
// wrong type URL or field number silently disables evaluation.
func TestAgentRunOptionsExtensionCarriesEvalCriteria(t *testing.T) {
	ext := agentRunOptions("完成验收标准")
	if ext.GetTypeUrl() != agentRunOptionsTypeURL {
		t.Fatalf("type url = %q", ext.GetTypeUrl())
	}
	num, typ, n := protowire.ConsumeTag(ext.GetValue())
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatalf("tag = %d/%d/%d", num, typ, n)
	}
	criteria, m := protowire.ConsumeString(ext.GetValue()[n:])
	if m < 0 || criteria != "完成验收标准" {
		t.Fatalf("criteria = %q, n = %d", criteria, m)
	}
}

// TestSessionLifecycle drives the client against a minimal AOP server: open a
// session, run a turn, receive events, close. It pins the two things that are
// easy to get wrong — reply correlation by reply_to, and WatchEvents being a
// subscription rather than an RPC.
func TestSessionLifecycle(t *testing.T) {
	var openedWorkDir *anypb.Any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/aop/application/ws" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer testkey" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		watchID := ""
		for {
			raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env pb.Envelope
			if proto.Unmarshal(raw, &env) != nil {
				continue
			}
			var msg pb.ProtocolMessage
			if env.Payload == nil || env.Payload.UnmarshalTo(&msg) != nil {
				continue
			}
			switch m := msg.Message.(type) {
			case *pb.ProtocolMessage_OpenSessionRequest:
				for _, extension := range m.OpenSessionRequest.GetExtensions() {
					if extension.GetTypeUrl() == WorkDirExtensionURL {
						openedWorkDir = extension
					}
				}
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_OpenSessionResponse{
					OpenSessionResponse: &pb.OpenSessionResponse{Outcome: &pb.OpenSessionResponse_Accepted{
						Accepted: &pb.Session{Id: "s1", State: "open", NodeId: m.OpenSessionRequest.GetNodeId()},
					}},
				}})
			case *pb.ProtocolMessage_WatchEventsRequest:
				watchID = env.Id
			case *pb.ProtocolMessage_RunTurnRequest:
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_RunTurnResponse{
					RunTurnResponse: &pb.RunTurnResponse{Outcome: &pb.RunTurnResponse_Accepted{
						Accepted: &pb.TurnReceipt{SessionId: "s1", TurnId: m.RunTurnRequest.GetTurnId(), State: "running"},
					}},
				}})
				// Events are delivered under the subscription id, not the turn id.
				for seq, ev := range []*pb.Event{
					{Seq: 1, Payload: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
					{Seq: 2, Payload: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{StopReason: "completed"}}},
				} {
					ev.Id = "e" + string(rune('0'+seq))
					_ = replyEnv(conn, watchID, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_Event{Event: ev}})
				}
			case *pb.ProtocolMessage_CloseSessionRequest:
				_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_CloseSessionResponse{
					CloseSessionResponse: &pb.CloseSessionResponse{Outcome: &pb.CloseSessionResponse_Accepted{
						Accepted: &pb.Session{Id: "s1", State: "closed"},
					}},
				}})
			}
		}
	}))
	defer srv.Close()

	// The server exists but may not be scheduled yet; start the operation
	// budget only after it has answered something, so a cold handler cannot
	// consume the budget the protocol assertions need.
	waitTestServerAnswers(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	defer cancel()
	c := New("ws"+strings.TrimPrefix(srv.URL, "http"), "testkey")
	c.BaseURL = srv.URL
	conn := dialTestConn(t, c)
	defer conn.Close()

	const sessionWorkDir = "/data/projects/p/cases/in_1"
	sess, err := conn.OpenSession(ctx, "local", "t", sessionWorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if sess.GetId() != "s1" || sess.GetNodeId() != "local" {
		t.Fatalf("session = %+v", sess)
	}
	if openedWorkDir == nil {
		t.Fatal("OpenSessionRequest carried no work dir extension")
	}
	var payload workDirPayload
	if err := json.Unmarshal(openedWorkDir.GetValue(), &payload); err != nil || payload.WorkDir != sessionWorkDir {
		t.Fatalf("work dir payload = %q, %v", openedWorkDir.GetValue(), err)
	}
	if err := conn.Watch(ctx, sess.GetId(), ""); err != nil {
		t.Fatal(err)
	}
	receipt, err := conn.RunTurn(ctx, sess.GetId(), "turn-1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GetTurnId() != "turn-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	for want := 1; want <= 2; want++ {
		select {
		case ev := <-conn.Events():
			if int(ev.GetSeq()) != want {
				t.Fatalf("event seq = %d, want %d", ev.GetSeq(), want)
			}
		case <-ctx.Done():
			t.Fatalf("missing event %d", want)
		}
	}
	if err := conn.CloseSession(ctx, sess.GetId(), "done"); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationWSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080/api/aop/application/ws"},
		{"https://hub.example/", "wss://hub.example/api/aop/application/ws"},
		{"ws://already", "ws://already/api/aop/application/ws"},
	}
	for _, c := range cases {
		if got := applicationWSURL(c.in); got != c.want {
			t.Fatalf("applicationWSURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func replyEnv(conn *ws.Conn, replyTo string, msg *pb.ProtocolMessage) error {
	payload, err := anypb.New(msg)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: "reply", ReplyTo: replyTo, Payload: payload})
	if err != nil {
		return err
	}
	return conn.WriteBinary(raw)
}

// replyAny answers a namespace request with its own message type (guardrail,
// command, …). The type_url of the Any is what the caller decodes by.
func replyAny(conn *ws.Conn, replyTo string, msg proto.Message) error {
	payload, err := anypb.New(msg)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: "reply", ReplyTo: replyTo, Payload: payload})
	if err != nil {
		return err
	}
	return conn.WriteBinary(raw)
}

// testHub is a minimal application-endpoint double for wire-level client tests:
// it upgrades, hands each request envelope and its Any payload to the test, and
// records the requests the test wants to assert on.
type testHub struct {
	mu       sync.Mutex
	watchIDs []string
	runTurns []string
	cancels  []*pb.CancelOperation
	server   *httptest.Server
}

func (h *testHub) recordWatch(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.watchIDs = append(h.watchIDs, id)
}

func (h *testHub) recordRunTurn(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runTurns = append(h.runTurns, id)
}

func (h *testHub) recordCancel(op *pb.CancelOperation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels = append(h.cancels, op)
}

func (h *testHub) snapshot() (watchIDs, runTurns []string, cancels []*pb.CancelOperation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.watchIDs...), append([]string(nil), h.runTurns...), append([]*pb.CancelOperation(nil), h.cancels...)
}

func startTestHub(t *testing.T, handle func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any)) *testHub {
	t.Helper()
	hub := &testHub{}
	hub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/aop/application/ws" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer testkey" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
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
			if proto.Unmarshal(raw, &env) != nil {
				continue
			}
			handle(hub, conn, &env, env.Payload)
		}
	}))
	t.Cleanup(hub.server.Close)
	return hub
}

// dialTestHub opens a client connection to the fake hub and closes it at the
// end of the test.
func dialTestHub(t *testing.T, hub *testHub) (*Conn, context.Context) {
	t.Helper()
	waitTestServerAnswers(t, hub.server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	t.Cleanup(cancel)
	conn := dialTestConn(t, New(hub.server.URL, "testkey"))
	t.Cleanup(func() { _ = conn.Close() })
	return conn, ctx
}

// coreMessage decodes a request payload as the core ProtocolMessage namespace
// (the only one that carries turn events).
func coreMessage(payload *anypb.Any) (*pb.ProtocolMessage, bool) {
	if payload == nil {
		return nil, false
	}
	var msg pb.ProtocolMessage
	if payload.UnmarshalTo(&msg) != nil {
		return nil, false
	}
	return &msg, true
}

// The evaluator goal is a separate extension, not the turn input: the wire must
// carry saker.RunGoal with the short goal and the pacing spec beside the
// AgentRunOptions criterion. A host that ignores it falls back to the input.
func TestRunTurnCarriesRunGoalExtensionBesideAgentRunOptions(t *testing.T) {
	var mu sync.Mutex
	var extensions []*anypb.Any
	hub := startTestHub(t, func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any) {
		msg, ok := coreMessage(payload)
		if !ok || msg.GetRunTurnRequest() == nil {
			return
		}
		mu.Lock()
		extensions = append(extensions, msg.GetRunTurnRequest().GetExtensions()...)
		mu.Unlock()
		_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_RunTurnResponse{
			RunTurnResponse: &pb.RunTurnResponse{Outcome: &pb.RunTurnResponse_Accepted{
				Accepted: &pb.TurnReceipt{SessionId: "s1", TurnId: "t1", State: "running"},
			}},
		}})
	})
	conn, ctx := dialTestHub(t, hub)
	if _, err := conn.RunTurnOpts(ctx, "s1", "t1", "完整任务书", TurnOptions{
		Eval: "产出一条可复核证据", Goal: "枚举攻击面", EvalRounds: "3",
		// A resumed turn never re-opens the session, so the workspace has to
		// travel with the turn itself.
		WorkDir: "/data/projects/p/cases/in_1",
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]*anypb.Any(nil), extensions...)
	mu.Unlock()
	var sawCriteria, sawGoal, sawWorkDir bool
	for _, ext := range got {
		switch ext.GetTypeUrl() {
		case agentRunOptionsTypeURL:
			sawCriteria = true
		case WorkDirExtensionURL:
			sawWorkDir = true
		case RunGoalExtensionURL:
			sawGoal = true
			var payload runGoalPayload
			if err := json.Unmarshal(ext.GetValue(), &payload); err != nil {
				t.Fatalf("RunGoal payload = %q: %v", ext.GetValue(), err)
			}
			if payload.Goal != "枚举攻击面" || payload.EvalRounds != "3" {
				t.Fatalf("RunGoal = %+v", payload)
			}
		}
	}
	if !sawCriteria || !sawGoal || !sawWorkDir {
		t.Fatalf("RunTurn extensions = %v", got)
	}
}

func TestRunGoalExtensionPayload(t *testing.T) {
	if RunGoalExtension("", " ") != nil {
		t.Fatal("an empty goal and pacing must not create an extension")
	}
	ext := RunGoalExtension(" 枚举攻击面 ", " 3 ")
	if ext.GetTypeUrl() != RunGoalExtensionURL {
		t.Fatalf("type url = %q", ext.GetTypeUrl())
	}
	var payload runGoalPayload
	if err := json.Unmarshal(ext.GetValue(), &payload); err != nil {
		t.Fatalf("payload = %q: %v", ext.GetValue(), err)
	}
	if payload.Goal != "枚举攻击面" || payload.EvalRounds != "3" {
		t.Fatalf("payload = %+v", payload)
	}
	// A pacing spec without a goal still travels: the fork accepts a round
	// budget it can apply to the input-derived goal.
	roundsOnly := RunGoalExtension("", "十轮以内")
	if roundsOnly == nil || !strings.Contains(string(roundsOnly.GetValue()), `"eval_rounds":"十轮以内"`) {
		t.Fatalf("rounds-only payload = %q", roundsOnly.GetValue())
	}
}

// A business rejection is an answer, not a lost reply: it must not be retried.
func TestRunTurnDoesNotRetryBusinessRejection(t *testing.T) {
	hub := startTestHub(t, func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any) {
		msg, ok := coreMessage(payload)
		if !ok || msg.GetRunTurnRequest() == nil {
			return
		}
		h.recordRunTurn(env.Id)
		_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_ProtocolError{
			ProtocolError: &pb.ProtocolError{Code: "UNAVAILABLE", Message: "node is not connected"},
		}})
	})
	conn, ctx := dialTestHub(t, hub)
	_, err := conn.RunTurnOpts(ctx, "s1", "t1", "brief", TurnOptions{})
	if err == nil || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Fatalf("want the protocol code in the error, got %v", err)
	}
	// Give a wrong retry a chance to appear on the wire.
	time.Sleep(200 * time.Millisecond)
	if _, ids, _ := hub.snapshot(); len(ids) != 1 {
		t.Fatalf("a business rejection was retried: %v", ids)
	}
}

// A transport timeout may have lost the reply after the host already started
// the turn. The retry must reuse the same envelope id — the host keys request
// replay by it — instead of opening a second turn.
func TestRunTurnRetriesOnceWithTheSameEnvelopeIDOnTransportTimeout(t *testing.T) {
	hub := startTestHub(t, func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any) {
		msg, ok := coreMessage(payload)
		if !ok || msg.GetRunTurnRequest() == nil {
			return
		}
		h.recordRunTurn(env.Id)
		// Never answer: the client must time out and retry.
	})
	conn, ctx := dialTestHub(t, hub)
	// Force a transport read timeout on the client side instead of waiting for
	// the real write deadline. The socket stays writable, so the retry can go
	// out even though the reader has given up.
	if err := conn.sock.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RunTurnOpts(ctx, "s1", "t1", "brief", TurnOptions{}); err == nil {
		t.Fatal("expected a transport error")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, ids, _ := hub.snapshot()
		if len(ids) >= 2 {
			if ids[0] != ids[1] {
				t.Fatalf("the retry used a new envelope id: %v", ids)
			}
			if len(ids) > 2 {
				t.Fatalf("more than one retry: %v", ids)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the transport timeout was not retried: %v", ids)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type testTimeoutError struct{}

func (testTimeoutError) Error() string   { return "i/o timeout" }
func (testTimeoutError) Timeout() bool   { return true }
func (testTimeoutError) Temporary() bool { return true }

func TestRetryableTurnErrorClassification(t *testing.T) {
	live := context.Background()
	// A write/read deadline is a transport timeout: the turn may have started,
	// so one idempotent retry is allowed.
	if !retryableTurnError(live, &net.OpError{Op: "write", Net: "tcp", Err: testTimeoutError{}}) {
		t.Fatal("a transport timeout was not retryable")
	}
	if !isTransportTimeout(os.ErrDeadlineExceeded) {
		t.Fatal("deadline exceeded was not classified as a transport timeout")
	}
	// The caller's own expired budget is not a transient transport hiccup.
	expired, cancel := context.WithTimeout(live, time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if retryableTurnError(expired, context.DeadlineExceeded) {
		t.Fatal("retried after the caller's own deadline")
	}
	// A decoded rejection is never a timeout.
	rejection := errors.New("aop: UNAVAILABLE：node is not connected")
	if isTransportTimeout(rejection) || retryableTurnError(live, rejection) {
		t.Fatal("a business rejection was classified as retryable")
	}
	if retryableTurnError(live, nil) {
		t.Fatal("nil error was retryable")
	}
}

// Watch records the subscription's envelope id; Close cancels that operation on
// the host instead of leaving the subscription to the socket teardown.
func TestCloseCancelsTheWatchSubscription(t *testing.T) {
	hub := startTestHub(t, func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any) {
		msg, ok := coreMessage(payload)
		if !ok {
			return
		}
		switch m := msg.Message.(type) {
		case *pb.ProtocolMessage_WatchEventsRequest:
			h.recordWatch(env.Id)
		case *pb.ProtocolMessage_CancelOperation:
			h.recordCancel(m.CancelOperation)
		}
	})
	conn, ctx := dialTestHub(t, hub)
	if err := conn.Watch(ctx, "s1", ""); err != nil {
		t.Fatal(err)
	}
	id := conn.WatchID()
	if id == "" {
		t.Fatal("Watch did not record its envelope id")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		watchIDs, _, cancels := hub.snapshot()
		if len(cancels) > 0 {
			if len(watchIDs) != 1 || cancels[0].GetTargetId() != watchIDs[0] {
				t.Fatalf("cancel target = %q, watch id = %v", cancels[0].GetTargetId(), watchIDs)
			}
			if cancels[0].GetReason() == "" {
				t.Fatal("cancel operation carried no reason")
			}
			if len(cancels) != 1 {
				t.Fatalf("cancel sent %d times", len(cancels))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Close sent no CancelOperation for the watch subscription")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The guardrail namespace is not the core ProtocolMessage one: the client must
// decode the reply by its Any type_url, and a policy refusal (sent as a core
// ProtocolError) must surface as an error rather than a parsed reply.
func TestGuardrailPendingAndResolve(t *testing.T) {
	var mu sync.Mutex
	var resolved []*guardrail.ResolveRequest
	deny := false
	hub := startTestHub(t, func(h *testHub, conn *ws.Conn, env *pb.Envelope, payload *anypb.Any) {
		if payload == nil || payload.MessageName() != "cyber.guardrail.ProtocolMessage" {
			return
		}
		var msg guardrail.ProtocolMessage
		if payload.UnmarshalTo(&msg) != nil {
			return
		}
		if pending := msg.GetPending(); pending != nil {
			_ = replyAny(conn, env.Id, &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_PendingResult{
				PendingResult: &guardrail.PendingResponse{Reviews: []*guardrail.Review{{
					Call:      &pb.ToolCall{Id: "call-1", Name: "bash", Kind: "bash"},
					Operation: &operation.Ref{OperationId: "op-1"},
					SessionId: pending.GetSessionId(),
					State:     guardrail.ReviewState_REVIEW_STATE_PENDING,
				}}},
			}})
			return
		}
		resolve := msg.GetResolve()
		if resolve == nil {
			return
		}
		mu.Lock()
		resolved = append(resolved, resolve)
		denied := deny
		mu.Unlock()
		if denied {
			_ = replyEnv(conn, env.Id, &pb.ProtocolMessage{Message: &pb.ProtocolMessage_ProtocolError{
				ProtocolError: &pb.ProtocolError{Code: "GUARDRAIL_DENIED", Message: "pending review not found in this session"},
			}})
			return
		}
		_ = replyAny(conn, env.Id, &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_Resolved{
			Resolved: &guardrail.ResolveResponse{},
		}})
	})
	conn, ctx := dialTestHub(t, hub)

	reviews, err := conn.Pending(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].GetOperation().GetOperationId() != "op-1" || reviews[0].GetCall().GetName() != "bash" {
		t.Fatalf("pending reviews = %+v", reviews)
	}
	if err := conn.Resolve(ctx, "s1", "op-1", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(resolved) != 1 || resolved[0].GetOperationId() != "op-1" || resolved[0].GetSessionId() != "s1" || !resolved[0].GetApprove() {
		t.Fatalf("resolve request = %+v", resolved)
	}
	deny = true
	mu.Unlock()
	if err := conn.Resolve(ctx, "s1", "op-404", false); err == nil || !strings.Contains(err.Error(), "GUARDRAIL_DENIED") {
		t.Fatalf("a refused resolve returned %v", err)
	}
}

// DisconnectReason is the single place that turns the reader's terminal error
// into the conclusion a run reports. A peer that closed on purpose is not a
// broken link: only the transport branch may say so.
func TestDisconnectReasonSeparatesPeerCloseFromTransportError(t *testing.T) {
	peer := DisconnectReason(&ws.CloseError{Code: 1001, Text: "host closing"})
	if strings.Contains(peer, "传输错误") {
		t.Fatalf("an intentional peer close was reported as a transport error: %s", peer)
	}
	for _, want := range []string{"宿主主动关闭", "1001", "host closing"} {
		if !strings.Contains(peer, want) {
			t.Fatalf("peer close reason %q lost %q", peer, want)
		}
	}

	// A clean FIN with no close frame: the stream ended without a stated
	// reason. Deliberate-because-bare is still not transport jitter.
	plain := DisconnectReason(io.EOF)
	if strings.Contains(plain, "传输错误") || !strings.Contains(plain, "对端关闭了 TCP") {
		t.Fatalf("clean EOF = %q", plain)
	}

	// A reader that stopped without recording a cause says exactly that.
	if got := DisconnectReason(nil); !strings.Contains(got, "没有留下原因") {
		t.Fatalf("nil = %q", got)
	}

	// The reset that a real verification round produced is a transport error.
	reset := DisconnectReason(&net.OpError{Op: "read", Net: "tcp", Err: errors.New(
		"wsarecv: An existing connection was forcibly closed by the remote host.")})
	if !strings.Contains(reset, "传输错误：") {
		t.Fatalf("a reset was not classified as a transport error: %s", reset)
	}
	// A read/write deadline also ends the stream without the peer saying
	// anything, so it belongs to the same transport class.
	timeout := DisconnectReason(&net.OpError{Op: "read", Net: "tcp", Err: testTimeoutError{}})
	if !strings.Contains(timeout, "传输错误：") {
		t.Fatalf("a read timeout = %q", timeout)
	}
}

// The turn loop sees only the events channel closing. The reader's reason must
// outlive that close, or the run can never say who ended the stream.
func TestReadErrorSurvivesTheClosedEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Upgrade(w, r)
		if err != nil {
			return
		}
		// End the stream by closing the TCP connection without a close frame:
		// this is what a peer that dies before saying why looks like.
		_ = c.Close()
	}))
	defer srv.Close()

	waitTestServerAnswers(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	defer cancel()
	conn := dialTestConn(t, New(srv.URL, ""))
	defer conn.Close()

	select {
	case _, ok := <-conn.Events():
		if ok {
			t.Fatal("no event can arrive on this stream")
		}
	case <-ctx.Done():
		t.Fatal("the reader never reported the closed stream")
	}
	if conn.ReadError() == nil {
		t.Fatal("the events channel closed but the reader kept no reason")
	}
	if got := DisconnectReason(conn.ReadError()); strings.Contains(got, "读端没有留下原因") {
		t.Fatalf("the reason was lost: %s", got)
	}
}
