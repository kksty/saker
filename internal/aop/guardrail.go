package aop

import (
	"context"
	"fmt"

	"github.com/kksty/saker/internal/aop/guardrail"
)

// The guardrail namespace is how the harness asks an operator to approve or
// reject a tool call a policy intercepted. It is a separate AOP namespace, not
// a core ProtocolMessage: the request and the reply are
// cyber.guardrail.ProtocolMessage values carried by the same application
// stream. The control plane lists pending reviews and resolves one by its
// operation id; it never executes the tool itself.

// Pending lists the tool reviews waiting for an operator decision in a session.
// The harness also includes child sessions of the given root session.
func (c *Conn) Pending(ctx context.Context, sessionID string) ([]*guardrail.Review, error) {
	message := &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_Pending{
		Pending: &guardrail.PendingRequest{SessionId: sessionID},
	}}
	env, err := c.callRequest(ctx, message)
	if err != nil {
		return nil, err
	}
	var reply guardrail.ProtocolMessage
	if err := decodeReply(env, &reply); err != nil {
		return nil, err
	}
	result := reply.GetPendingResult()
	if result == nil {
		return nil, fmt.Errorf("aop: 意外的 guardrail 回复 %T", reply.Message)
	}
	return result.GetReviews(), nil
}

// Resolve approves or rejects one pending review by its operation id. The
// harness refuses an operation that is not pending in that session; that
// refusal is returned as the error, not treated as a successful decision.
func (c *Conn) Resolve(ctx context.Context, sessionID, operationID string, approve bool) error {
	message := &guardrail.ProtocolMessage{Message: &guardrail.ProtocolMessage_Resolve{
		Resolve: &guardrail.ResolveRequest{SessionId: sessionID, OperationId: operationID, Approve: approve},
	}}
	env, err := c.callRequest(ctx, message)
	if err != nil {
		return err
	}
	var reply guardrail.ProtocolMessage
	if err := decodeReply(env, &reply); err != nil {
		return err
	}
	if reply.GetResolved() == nil {
		return fmt.Errorf("aop: 意外的 guardrail 回复 %T", reply.Message)
	}
	return nil
}
