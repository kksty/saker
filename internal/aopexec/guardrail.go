package aopexec

// Guardrail audit journaling (P4-J).
//
// The harness guardrail runtime judges every tool invocation and publishes the
// verdict as an AOP Event: Emitter="guardrail", payload Event_Extension whose Any
// is one of the cyber.guardrail messages:
//
//   - Decision: the policy verdict for one call (action record/review/block and
//     a reason). The runtime emits it for every invocation, including when the
//     call is allowed, because the verdict itself is the audit fact.
//   - Review: the approval lifecycle of one intercepted call (pending, then the
//     resolved state: approved/rejected/expired/canceled). In auto mode the
//     runtime also emits a Review to record the consequence assessment.
//
// The payload is the Event_Extension oneof, not a member of Event.extensions, so
// the traffic/artifact branches (which read focused, known extensions) never see
// it, and rawJSON deliberately drops the unknown Any. A union that prints only
// "extension" loses the entire verdict, so this file decodes it with the vendored
// generated types (internal/aop/guardrail, the same descriptors the host uses)
// and executor.event turns the result into stable journal fields.
//
// A payload that fails to decode is still an event: the caller keeps it and the
// Text says why the detail is missing. An unknown type_url keeps the old
// behaviour (kind=raw), because saker must not guess at a payload it has no
// descriptor for.

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/kksty/saker/internal/aop/guardrail"
	"github.com/kksty/saker/internal/aop/operation"
	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

// guardrailAudit is the decoded content of one guardrail payload. An empty
// string means the payload does not carry that field (a Decision has no tool),
// never "the field is empty": the journal must not turn "not reported" into a
// measured empty value.
type guardrailAudit struct {
	// Kind is executor.KindGuardrailDecision or executor.KindGuardrailReview.
	Kind string
	// Action is the decision's verdict in the journal's stable vocabulary:
	// record | review | block | unspecified (unknown enum numbers keep action_<n>).
	Action string
	// Reason is the policy's explanation, clipped to the journal Text length.
	Reason string
	// Tool is the intercepted tool's name (Review only: a Decision has no call).
	Tool string
	// State is the review lifecycle: pending | approved | rejected | expired |
	// canceled | unspecified. Empty for a Decision.
	State string
	// CallID / OperationID identify the judged call. They come from the payload
	// (Review.call) or from the operation ref the runtime publishes as a sidecar
	// extension on every guardrail event (a Decision has no call of its own).
	CallID      string
	OperationID string
	// ExpiresAt is the review deadline (pending rows); nil when the payload
	// carries none. ResolutionSource is the trusted adapter that resolved the
	// review ("auto", "cli", "control"): it separates an automatic consequence
	// assessment from an operator approval.
	ExpiresAt        *time.Time
	ResolutionSource string
	// Text is the human line: "guardrail block bash：原因".
	Text string
}

// decodeGuardrail reads the guardrail payload of an AOP event.
//
// ok is false for every other payload, including an unknown Any type: those keep
// the previous kind=raw mapping. When the type_url is a guardrail message the
// event is ours even if its body is malformed, so a broken verdict is visible
// instead of silently becoming raw.
func decodeGuardrail(ev *pb.Event) (guardrailAudit, bool) {
	ext := ev.GetExtension()
	if ext == nil {
		return guardrailAudit{}, false
	}
	switch {
	case ext.MessageIs(new(guardrail.Decision)):
		return decodeGuardrailDecision(ev, ext), true
	case ext.MessageIs(new(guardrail.Review)):
		return decodeGuardrailReview(ev, ext), true
	}
	return guardrailAudit{}, false
}

func decodeGuardrailDecision(ev *pb.Event, ext *anypb.Any) guardrailAudit {
	out := guardrailAudit{Kind: executor.KindGuardrailDecision}
	out.CallID, out.OperationID = guardrailRef(ev)
	value := new(guardrail.Decision)
	if err := ext.UnmarshalTo(value); err != nil {
		out.Action = "unspecified"
		out.Text = truncate("guardrail 判定解码失败："+err.Error(), 400)
		return out
	}
	out.Action = guardrailActionName(value.GetAction())
	out.Reason = truncate(value.GetReason(), 400)
	out.Text = truncate(guardrailLine(out.Action, "", out.Reason), 400)
	return out
}

func decodeGuardrailReview(ev *pb.Event, ext *anypb.Any) guardrailAudit {
	out := guardrailAudit{Kind: executor.KindGuardrailReview}
	out.CallID, out.OperationID = guardrailRef(ev)
	value := new(guardrail.Review)
	if err := ext.UnmarshalTo(value); err != nil {
		out.Action, out.State = "unspecified", "unspecified"
		out.Text = truncate("guardrail 审批解码失败："+err.Error(), 400)
		return out
	}
	if call := value.GetCall(); call != nil {
		out.Tool = truncate(call.GetName(), 200)
		if out.CallID == "" {
			out.CallID = call.GetId()
		}
	}
	if op := value.GetOperation(); op != nil {
		if out.CallID == "" {
			out.CallID = op.GetCallId()
		}
		if out.OperationID == "" {
			out.OperationID = op.GetOperationId()
		}
	}
	out.Action = "unspecified"
	if decision := value.GetDecision(); decision != nil {
		out.Action = guardrailActionName(decision.GetAction())
		out.Reason = truncate(decision.GetReason(), 400)
	}
	out.State = guardrailReviewStateName(value.GetState())
	out.ResolutionSource = truncate(value.GetResolutionSource(), 100)
	if expires := value.GetExpiresAt(); expires != nil && expires.IsValid() {
		at := expires.AsTime()
		out.ExpiresAt = &at
	}
	out.Text = truncate(guardrailLine(out.Action, out.Tool, out.Reason), 400)
	return out
}

// guardrailRef reads the operation ref the runtime publishes beside every
// guardrail payload (event.extensions). A Decision payload carries no call of its
// own, so this is the only way for the journal to name the judged invocation; a
// Review carries call/operation too, and the payload wins when both exist.
func guardrailRef(ev *pb.Event) (callID, operationID string) {
	for _, sidecar := range ev.GetExtensions() {
		if sidecar == nil || !sidecar.MessageIs(new(operation.Ref)) {
			continue
		}
		ref := new(operation.Ref)
		if err := sidecar.UnmarshalTo(ref); err != nil {
			continue
		}
		return ref.GetCallId(), ref.GetOperationId()
	}
	return "", ""
}

// guardrailActionName maps the enum onto the journal's stable lowercase
// vocabulary. An unknown enum number is kept as action_<n> rather than being
// flattened into "unspecified": a host that grows a fourth action must stay
// visible in the audit trail.
func guardrailActionName(action guardrail.Action) string {
	if name, ok := guardrail.Action_name[int32(action)]; ok {
		return strings.ToLower(strings.TrimPrefix(name, "ACTION_"))
	}
	return fmt.Sprintf("action_%d", int32(action))
}

// guardrailReviewStateName maps the review lifecycle enum onto the same
// vocabulary the console labels (pending/approved/rejected/expired/canceled).
func guardrailReviewStateName(state guardrail.ReviewState) string {
	if name, ok := guardrail.ReviewState_name[int32(state)]; ok {
		return strings.ToLower(strings.TrimPrefix(name, "REVIEW_STATE_"))
	}
	return fmt.Sprintf("state_%d", int32(state))
}

// guardrailLine is the journal Text: "guardrail block bash：原因". The console
// parses this shape as the fallback label for rows written before the structured
// fields existed (web/src/lib/guardrail.ts), so the action must stay the second
// word and the tool follows it verbatim.
func guardrailLine(action, tool, reason string) string {
	var b strings.Builder
	b.WriteString("guardrail ")
	b.WriteString(action)
	if tool != "" {
		b.WriteString(" ")
		b.WriteString(tool)
	}
	if reason != "" {
		b.WriteString("：")
		b.WriteString(reason)
	}
	return b.String()
}
