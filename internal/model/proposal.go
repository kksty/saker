package model

import "time"

// Proposal status values. A proposal is a participant's suggested next intent
// waiting for a commander/operator decision; approval only turns it into a
// pending intent, and a separate dispatch starts the run.
const (
	ProposalProposed   = "proposed"
	ProposalApproved   = "approved"
	ProposalRejected   = "rejected"
	ProposalDispatched = "dispatched"
	ProposalWithdrawn  = "withdrawn"
)

// Proposal is control state, not an observation: it records that an executor
// suggested one next step, what happened to that suggestion, and which intent
// (if any) it produced. It never enters the fact/report/board/ContentHash
// paths. The version history keeps every transition, so the row itself only
// carries the current state.
//
// The proposed work itself is expressed with the same fields an intent has
// (title/description/doc/eval/premises/context/skills/node/max_turns). Approval
// feeds those fields through the ledger's one intent-creation path, so a
// proposal cannot carry a state an intent could not have.
type Proposal struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Version     uint64 `json:"version"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Doc         string `json:"doc,omitempty"`
	Eval        string `json:"eval,omitempty"`
	// Rationale is why the proposer thinks this step is worth it. It is read by
	// the deciding session (the notification carries it) and stays on the
	// proposal; the intent created by approval has no rationale field.
	Rationale      string   `json:"rationale,omitempty"`
	FromFactIDs    []string `json:"from_fact_ids,omitempty"`
	ContextFactIDs []string `json:"context_fact_ids,omitempty"`
	PinnedFactIDs  []string `json:"pinned_fact_ids,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	Node           string   `json:"node,omitempty"`
	MaxTurns       int      `json:"max_turns,omitempty"`
	// The proposer's participant identity and, when it is an intent participant,
	// the run/generation it spoke from. These are the same coordinates its
	// messages carry, so a decision notification can be delivered back.
	FromParticipantID string `json:"from_participant_id"`
	FromIntentID      string `json:"from_intent_id,omitempty"`
	FromRunID         string `json:"from_run_id,omitempty"`
	FromGeneration    int    `json:"from_generation,omitempty"`
	// IdempotencyKey is the proposer's retry key, unique per proposer.
	IdempotencyKey string `json:"idempotency_key"`
	// Decision provenance. DecidedAt is nil until the proposal leaves proposed.
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	// IntentID is the pending intent approval created; RunID is the run a
	// separate dispatch started. Both empty on an undecided proposal.
	IntentID  string    `json:"intent_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
