package aopexec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kksty/saker/internal/aop/guardrail"
	"github.com/kksty/saker/internal/aop/operation"
	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

// guardrailAOPEvent builds the event the harness guardrail runtime publishes:
// Emitter="guardrail", the verdict as the Event_Extension payload, and the
// operation ref as a sidecar extension (runtime.go emit). callID/operationID may
// be empty to model a host that publishes no ref.
func guardrailAOPEvent(t *testing.T, payload proto.Message, callID, operationID string) *pb.Event {
	t.Helper()
	encoded, err := anypb.New(payload)
	if err != nil {
		t.Fatalf("encode guardrail payload: %v", err)
	}
	ev := &pb.Event{
		Id:        "ev-guardrail",
		SessionId: "session-1",
		TurnId:    "turn-1",
		Emitter:   "guardrail",
		Payload:   &pb.Event_Extension{Extension: encoded},
	}
	if callID != "" || operationID != "" {
		ref, err := anypb.New(&operation.Ref{CallId: callID, OperationId: operationID})
		if err != nil {
			t.Fatalf("encode operation ref: %v", err)
		}
		ev.Extensions = append(ev.Extensions, ref)
	}
	return ev
}

// TestGuardrailDecisionMapsEveryAction pins the three verdicts the policy can
// reach. The Decision payload has no tool, so the journal keeps the tool empty
// rather than guessing it from a neighbouring event; the call identity comes from
// the operation ref sidecar.
func TestGuardrailDecisionMapsEveryAction(t *testing.T) {
	emitted := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		action guardrail.Action
		want   string
	}{
		{guardrail.Action_ACTION_RECORD, "record"},
		{guardrail.Action_ACTION_REVIEW, "review"},
		{guardrail.Action_ACTION_BLOCK, "block"},
	} {
		t.Run(test.want, func(t *testing.T) {
			ev := guardrailAOPEvent(t, &guardrail.Decision{
				Action: test.action,
				Reason: "策略判定：" + test.want,
			}, "call-1", "op-1")
			ev.EmittedAt = timestamppb.New(emitted)

			got := event(ev)
			if got.Kind != executor.KindGuardrailDecision {
				t.Fatalf("kind = %q, want %q: %+v", got.Kind, executor.KindGuardrailDecision, got)
			}
			if got.GuardrailAction != test.want {
				t.Fatalf("action = %q, want %q", got.GuardrailAction, test.want)
			}
			if got.GuardrailReason != "策略判定："+test.want {
				t.Fatalf("reason = %q", got.GuardrailReason)
			}
			if got.GuardrailTool != "" {
				t.Fatalf("decision must not invent a tool, got %q", got.GuardrailTool)
			}
			if got.GuardrailState != "" {
				t.Fatalf("decision has no review state, got %q", got.GuardrailState)
			}
			if got.GuardrailCallID != "call-1" || got.GuardrailOperationID != "op-1" {
				t.Fatalf("call identity = %q/%q", got.GuardrailCallID, got.GuardrailOperationID)
			}
			wantText := "guardrail " + test.want + "：策略判定：" + test.want
			if got.Text != wantText {
				t.Fatalf("text = %q, want %q", got.Text, wantText)
			}
			// Ownership and time must survive the payload mapping.
			if got.ID != "ev-guardrail" || got.SessionID != "session-1" || got.TurnID != "turn-1" {
				t.Fatalf("event identity lost: %+v", got)
			}
			if !got.At.Equal(emitted) {
				t.Fatalf("at = %v, want %v", got.At, emitted)
			}
			if !belongsToTurn(ev, "session-1", "turn-1") || belongsToTurn(ev, "session-1", "turn-2") {
				t.Fatalf("guardrail event turn ownership is wrong")
			}
			if len(got.Raw) == 0 {
				t.Fatal("raw JSON must still be recorded for a guardrail event")
			}
		})
	}
}

// TestGuardrailReviewMapsEveryState covers the approval lifecycle the console
// renders: pending → approved/rejected/expired/canceled. The payload carries the
// call (tool) and the nested decision, so the journal names both.
func TestGuardrailReviewMapsEveryState(t *testing.T) {
	expires := time.Date(2026, 11, 18, 12, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		state guardrail.ReviewState
		want  string
	}{
		{guardrail.ReviewState_REVIEW_STATE_PENDING, "pending"},
		{guardrail.ReviewState_REVIEW_STATE_APPROVED, "approved"},
		{guardrail.ReviewState_REVIEW_STATE_REJECTED, "rejected"},
		{guardrail.ReviewState_REVIEW_STATE_EXPIRED, "expired"},
		{guardrail.ReviewState_REVIEW_STATE_CANCELED, "canceled"},
	} {
		t.Run(test.want, func(t *testing.T) {
			ev := guardrailAOPEvent(t, &guardrail.Review{
				Call:             &pb.ToolCall{Id: "call-1", Name: "bash", Kind: "bash"},
				Operation:        &operation.Ref{CallId: "call-1", OperationId: "op-1"},
				SessionId:        "session-1",
				Decision:         &guardrail.Decision{Action: guardrail.Action_ACTION_REVIEW, Reason: "需要人工确认"},
				State:            test.state,
				ExpiresAt:        timestamppb.New(expires),
				ResolutionSource: "control",
			}, "", "")

			got := event(ev)
			if got.Kind != executor.KindGuardrailReview {
				t.Fatalf("kind = %q: %+v", got.Kind, got)
			}
			if got.GuardrailState != test.want {
				t.Fatalf("state = %q, want %q", got.GuardrailState, test.want)
			}
			if got.GuardrailAction != "review" {
				t.Fatalf("action = %q, want review", got.GuardrailAction)
			}
			if got.GuardrailTool != "bash" || got.GuardrailCallID != "call-1" || got.GuardrailOperationID != "op-1" {
				t.Fatalf("review identity = tool %q call %q operation %q", got.GuardrailTool, got.GuardrailCallID, got.GuardrailOperationID)
			}
			if got.GuardrailReason != "需要人工确认" {
				t.Fatalf("reason = %q", got.GuardrailReason)
			}
			if got.GuardrailExpiresAt == nil || !got.GuardrailExpiresAt.Equal(expires) {
				t.Fatalf("expires at = %v, want %v", got.GuardrailExpiresAt, expires)
			}
			if got.GuardrailResolutionSource != "control" {
				t.Fatalf("resolution source = %q, want control", got.GuardrailResolutionSource)
			}
			wantText := "guardrail review bash：需要人工确认"
			if got.Text != wantText {
				t.Fatalf("text = %q, want %q", got.Text, wantText)
			}
			if got.SessionID != "session-1" || got.TurnID != "turn-1" {
				t.Fatalf("ownership lost: %+v", got)
			}
		})
	}
}

// TestGuardrailReviewUnknownStateStaysVisible keeps a state a newer host adds
// visible instead of flattening it into "unspecified".
func TestGuardrailReviewUnknownStateStaysVisible(t *testing.T) {
	got := event(guardrailAOPEvent(t, &guardrail.Review{
		State: guardrail.ReviewState(9),
	}, "", ""))
	if got.GuardrailState != "state_9" {
		t.Fatalf("state = %q, want state_9", got.GuardrailState)
	}
}

// TestGuardrailDecisionWithoutRefKeepsTheEvent models a host that publishes no
// operation ref: the verdict must still be recorded, just without call identity.
func TestGuardrailDecisionWithoutRefKeepsTheEvent(t *testing.T) {
	got := event(guardrailAOPEvent(t, &guardrail.Decision{
		Action: guardrail.Action_ACTION_BLOCK,
		Reason: "命令被策略阻断",
	}, "", ""))
	if got.Kind != executor.KindGuardrailDecision || got.GuardrailAction != "block" {
		t.Fatalf("decision lost: %+v", got)
	}
	if got.GuardrailCallID != "" || got.GuardrailOperationID != "" {
		t.Fatalf("invented call identity: %+v", got)
	}
	if got.Text != "guardrail block：命令被策略阻断" {
		t.Fatalf("text = %q", got.Text)
	}
}

// TestGuardrailDecodeFailureKeepsTheEvent: a known type_url with a malformed
// body must not drop the event or demote it to raw. The journal says the detail
// could not be decoded, and the event identity is still there.
func TestGuardrailDecodeFailureKeepsTheEvent(t *testing.T) {
	for _, test := range []struct {
		name, typeURL, wantKind, wantText string
	}{
		{"decision", "type.googleapis.com/cyber.guardrail.Decision", executor.KindGuardrailDecision, "判定解码失败"},
		{"review", "type.googleapis.com/cyber.guardrail.Review", executor.KindGuardrailReview, "审批解码失败"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ev := &pb.Event{
				Id:        "ev-broken",
				SessionId: "session-1",
				TurnId:    "turn-1",
				Emitter:   "guardrail",
				// A length-delimited field claims 127 bytes it does not have: a real
				// wire error, not an unknown type.
				Payload: &pb.Event_Extension{Extension: &anypb.Any{TypeUrl: test.typeURL, Value: []byte{0x1a, 0x7f}}},
			}
			got := event(ev)
			if got.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q: %+v", got.Kind, test.wantKind, got)
			}
			if !strings.Contains(got.Text, test.wantText) || !strings.Contains(got.Text, "解码失败") {
				t.Fatalf("text = %q", got.Text)
			}
			if got.ID != "ev-broken" || got.SessionID != "session-1" || got.TurnID != "turn-1" {
				t.Fatalf("event identity lost: %+v", got)
			}
		})
	}
}

// TestGuardrailUnknownPayloadKeepsRawBehaviour: an Any this client has no
// descriptor for keeps the previous mapping (kind=raw), because the journal must
// not guess at a payload it does not understand.
func TestGuardrailUnknownPayloadKeepsRawBehaviour(t *testing.T) {
	ev := &pb.Event{
		Id:      "ev-unknown",
		Emitter: "guardrail",
		Payload: &pb.Event_Extension{Extension: &anypb.Any{
			TypeUrl: "type.googleapis.com/cyber.guardrail.FutureVerdict",
			Value:   []byte{1, 2, 3},
		}},
	}
	got := event(ev)
	if got.Kind != executor.KindRaw {
		t.Fatalf("kind = %q, want %q", got.Kind, executor.KindRaw)
	}
	if got.GuardrailAction != "" || got.Text != "" {
		t.Fatalf("unknown payload was interpreted: %+v", got)
	}
}

// TestGuardrailJournalJSONShape pins the journal line the console reads
// (web/src/lib/guardrail.ts): kind plus the stable guardrail_* keys, nested under
// the same event identity every other harness event uses.
func TestGuardrailJournalJSONShape(t *testing.T) {
	decision, err := json.Marshal(event(guardrailAOPEvent(t, &guardrail.Decision{
		Action: guardrail.Action_ACTION_BLOCK,
		Reason: "命令被策略阻断",
	}, "call-1", "op-1")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind":"guardrail_decision"`,
		`"session_id":"session-1"`,
		`"turn_id":"turn-1"`,
		`"guardrail_action":"block"`,
		`"guardrail_reason":"命令被策略阻断"`,
		`"guardrail_call_id":"call-1"`,
		`"guardrail_operation_id":"op-1"`,
		`"text":"guardrail block：命令被策略阻断"`,
	} {
		if !strings.Contains(string(decision), want) {
			t.Fatalf("journal line missing %s: %s", want, decision)
		}
	}
	if !json.Valid(decision) {
		t.Fatalf("journal line is not valid JSON: %s", decision)
	}

	review, err := json.Marshal(event(guardrailAOPEvent(t, &guardrail.Review{
		Call:             &pb.ToolCall{Id: "call-1", Name: "bash"},
		Operation:        &operation.Ref{CallId: "call-1", OperationId: "op-1"},
		Decision:         &guardrail.Decision{Action: guardrail.Action_ACTION_REVIEW, Reason: "需要人工确认"},
		State:            guardrail.ReviewState_REVIEW_STATE_PENDING,
		ExpiresAt:        timestamppb.New(time.Date(2026, 11, 18, 12, 10, 0, 0, time.UTC)),
		ResolutionSource: "control",
	}, "", "")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind":"guardrail_review"`,
		`"guardrail_action":"review"`,
		`"guardrail_tool":"bash"`,
		`"guardrail_state":"pending"`,
		`"guardrail_expires_at":"2026-11-18T12:10:00Z"`,
		`"guardrail_resolution_source":"control"`,
		`"text":"guardrail review bash：需要人工确认"`,
	} {
		if !strings.Contains(string(review), want) {
			t.Fatalf("review journal line missing %s: %s", want, review)
		}
	}

	// An auto consequence assessment carries no deadline: "not reported" must be
	// omitted, not written as a zero time.
	auto, err := json.Marshal(event(guardrailAOPEvent(t, &guardrail.Review{
		Decision:         &guardrail.Decision{Action: guardrail.Action_ACTION_BLOCK, Reason: "自动评估：有破坏性"},
		State:            guardrail.ReviewState_REVIEW_STATE_REJECTED,
		ResolutionSource: "auto",
	}, "", "")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auto), "guardrail_expires_at") {
		t.Fatalf("absent review deadline was invented: %s", auto)
	}
	if !strings.Contains(string(auto), `"guardrail_resolution_source":"auto"`) {
		t.Fatalf("auto resolution not recorded: %s", auto)
	}
}
