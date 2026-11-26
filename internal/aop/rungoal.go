package aop

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/types/known/anypb"
)

// RunGoalExtensionURL is the harness fork's per-turn evaluator goal extension on
// RunTurnRequest. Upstream AOP derives the evaluator's goal from the turn input
// text, but the control plane sends the executor's whole brief as the turn
// input: that would ask the evaluator to judge "you are an executor…" instead
// of the intent. The extension carries the short control-plane goal (and the
// optional pacing spec) beside the brief; the fork reads the value (JSON with
// goal and eval_rounds) and hands it to the evaluator only, leaving the agent's
// input untouched. An old harness ignores unknown extensions and falls back to
// the input-derived goal, so sending it is additive and never breaks a host
// that predates the fork.
//
// The type name is a wire contract with another process; see
// WorkDirExtensionURL for why it is not renamed with the product.
const RunGoalExtensionURL = "type.googleapis.com/saker.RunGoal"

type runGoalPayload struct {
	Goal string `json:"goal"`
	// EvalRounds accepts the same values as AgentRunOptions.eval_rounds: a hard
	// round number or natural language the evaluator follows. The fork reads it
	// as a raw value so a number is not rejected; saker always sends a string.
	EvalRounds string `json:"eval_rounds,omitempty"`
}

// RunGoalExtension encodes an evaluated turn's goal. Empty goal and empty round
// pacing returns nil, which keeps the upstream behavior (the evaluator derives
// its goal from the turn input) for runs that are not evaluated.
func RunGoalExtension(goal, evalRounds string) *anypb.Any {
	goal = strings.TrimSpace(goal)
	evalRounds = strings.TrimSpace(evalRounds)
	if goal == "" && evalRounds == "" {
		return nil
	}
	raw, err := json.Marshal(runGoalPayload{Goal: goal, EvalRounds: evalRounds})
	if err != nil {
		return nil
	}
	return &anypb.Any{TypeUrl: RunGoalExtensionURL, Value: raw}
}
