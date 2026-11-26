// Package model defines the ledger objects of Saker.
//
// Every object here is intentionally small and plain: no hashes, no CVSS, no
// version numbers, no leases. See docs/06-decisions.md for why.
package model

import (
	"strings"
	"time"
)

// Severity values. A conclusion may carry an optional operator-facing grade.
// It is a label, like the display title, never part of the assertion: changing
// it neither rewrites the claim nor invalidates a review (store.claimHash does
// not include it). Empty means ungraded, and an unknown value is not guessed
// into a neighbouring tier on read.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
)

// NormalizeSeverity is the single write-time normaliser for a declared grade.
// The three input paths that can carry one (a fact declaration, an imported
// audit finding, and a re-declaration) all go through it, so no read path has
// to re-map a value. Anything outside the four grades becomes ungraded.
func NormalizeSeverity(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return v
	}
	return ""
}

// SeverityRank orders the grades for display and sorting; 0 is ungraded and
// sorts last when the caller asks for severity order.
func SeverityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// Intent status values. An intent is the unit of offensive work dispatched to
// an executor; it is claimed atomically, never leased.
const (
	StatusPending = "pending"
	StatusClaimed = "claimed"
	StatusRunning = "running"
	// StatusWaiting is a business state: the direction's current turn yielded
	// because it is waiting for an external result, a human action, or a wake. It
	// has no live run, so the watchdog must not "recover" it and dispatch must not
	// claim it; only its wait condition (or its deadline) moves it back to pending.
	StatusWaiting = "waiting"
	// StatusReviewing is the fixed settlement phase between a successful run and
	// done: the run's new report-bound facts are being re-proven. It has no live
	// executor run and must not be dispatched again, but it is not settled: a
	// waiting parent and the commander must not treat its facts as final yet.
	StatusReviewing = "reviewing"
	StatusDone      = "done"
	StatusFailed    = "failed"
	// StatusReviewFailed is a terminal review verdict on a completed run: the
	// work ran, but at least one produced conclusion could not be proven. It is
	// retryable in place like failed; it is not a dispatchable pending state.
	StatusReviewFailed = "review_failed"
	StatusParked       = "parked"
)

// Run status values. A cancellation is our decision, not the executor's
// failure, so it must not feed the escalation ladder (docs/06, D19).
const (
	RunRunning  = "running"
	RunDone     = "done"
	RunFailed   = "failed"
	RunCanceled = "canceled"
)

// Run cancel causes. Canceled is one status but several very different events:
// a steer is a continuation, a restart is deployment, a stop-loss is policy, and
// a watchdog kill is supervision. Keeping the cause structured lets the console
// say "转续话" without re-parsing the free-text exit_reason.
const (
	CancelCauseSteer    = "steer"
	CancelCauseRetry    = "retry"
	CancelCauseStopLoss = "stop_loss"
	// CancelCauseHandoff is a planned replacement: the direction continues in a
	// new session with a handoff, so it must not read as a failure or steer.
	CancelCauseHandoff  = "handoff"
	CancelCauseRestart  = "restart"
	CancelCauseWatchdog = "watchdog"
	CancelCauseOperator = "operator"
	// CancelCauseProjectStop is an administrative project stop (delete) rather
	// than a deployment restart: the delete cancels live runs immediately, with
	// no evidence window, and the ledger/metrics must not report it as a process
	// shutdown.
	CancelCauseProjectStop = "project_stop"
	// CancelCauseWorkerKill is a participant stopping a worker it created (or a
	// descendant of its own direction): the direction is over, but the ledger
	// says it was a kill, not a failure or an operator cancel.
	CancelCauseWorkerKill = "worker_kill"
	// CancelCausePause is the whole-project switch. It is not the operator
	// canceling one direction: it stops every live intent run and the report
	// runs that dispatch never started. Verifier runs are deliberately not part
	// of it: reviews have their own switch (CancelCauseReviewPause) so an
	// operator can re-prove a finding while the engagement is paused (D137).
	CancelCausePause = "pause"
	// CancelCauseReviewPause is the review kill switch, independent of the
	// project pause: it stops live verifier runs and refuses new ones without
	// resuming the commander or the executor (D137).
	CancelCauseReviewPause = "review_pause"
	// OperatorWait yields a turn to a persisted human condition, preserving
	// the session and wait without running the model/evaluator while waiting.
	CancelCauseOperatorWait = "operator_wait"
	// CancelCauseModelStall is the liveness stop: a weak model kept answering
	// with empty assistant messages (each followed by another llm_request) and
	// never called a tool or ended the turn. It is not a task verdict — the
	// direction is held for an operator/decision retry, not marked done/failed
	// by the model's opinion (docs/17 §3.6 liveness, final-e2e I1).
	CancelCauseModelStall = "model_stall"
	// CancelCauseRoundTimeout is the commander round's wall-clock limit: the
	// decision did not finish in time, so the round was cut. It is neither a
	// project stop nor the model's verdict — the round's actions are recorded,
	// the harness session survives, and the next wake decides again (D91).
	CancelCauseRoundTimeout = "round_timeout"
)

// Fact origins. Declared facts come from <workdir>/.saker/facts.jsonl, derived
// facts are computed by the ledger from the filesystem, heuristic facts are the
// fallback extraction. The board ranks them in that order (docs/06, D18).
const (
	OriginDeclared  = "declared"
	OriginDerived   = "derived"
	OriginHeuristic = "heuristic"
)

// Interjection is one message a human or the commander delivered into a live
// run. MessageID is the IOA message id; delivery is best-effort from the
// control plane's point of view (the harness has no end-to-end ack for this
// path), so the UI calls it "accepted" rather than "read" (D69).
type Interjection struct {
	At        time.Time `json:"at"`
	Text      string    `json:"text"`
	RefFact   string    `json:"ref_fact,omitempty"`
	Interrupt bool      `json:"interrupt,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
}

// Intent notes are commentary about a direction, not constraints on it. They
// are recorded on the intent for the operator and the next decision; the
// executor brief never reads this field, because a note is written by the
// decider (which saw the run's internals) about the executor, not to it.
const (
	IntentNoteRetry     = "retry"
	IntentNoteSteer     = "steer"
	IntentNoteReview    = "review"
	IntentNoteBootstrap = "bootstrap"
	// IntentNoteOrphan explains a child that was failed because its parent
	// direction ended before it ever started. It is a note, not a park: the
	// child is not promoted to an independent direction, and an explicit retry
	// still works.
	IntentNoteOrphan = "orphan"
	// IntentNoteRestart is the audit note a parent writes when it restarts a
	// worker it owns; it is a restart, not a re-scoped retry.
	IntentNoteRestart = "restart"
	// IntentNoteSettle is the audit note for a completion the commander
	// declared on recorded evidence (POST /api/intents/{id}/settle). It is not
	// a run result: DoneDeclaredBy keeps that distinction on the row.
	IntentNoteSettle = "settle"
)

// HoldActor* values live on Intent.HeldBy: who set the sticky stop. A worker
// killed by its parent stores that parent's participant id instead of a role.
const (
	HoldActorOperator  = "operator"
	HoldActorCommander = "commander"
	HoldActorWorker    = "worker"
	HoldActorWatchdog  = "watchdog"
	HoldActorUnknown   = "unknown"
)

// IntentNote is one retry/steer remark kept next to the direction it explains.
// It used to be appended to Doc under a heading, which made the note part of
// the executor's constraint section and forced BuildPrompt to strip a heading by
// name — a content-shape patch that any "## " in the note could walk around.
type IntentNote struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // retry|steer
	Text    string    `json:"text"`
	RefFact string    `json:"ref_fact,omitempty"`
}

// Intent is one direction of offensive work.
type Intent struct {
	// ContextFactIDs are explicit references the direction reads: recorded
	// observations that inform the work without being necessary premises.
	// FromFactIDs stays the hard prerequisite list; a reference never blocks a
	// dispatch, and the two are different edges in the graph.
	ContextFactIDs []string `json:"context_fact_ids,omitempty"`
	// PinnedFactIDs are references the operator wants kept in the hot layer
	// regardless of age or relevance ranking. They change focus, not validity.
	PinnedFactIDs []string `json:"pinned_fact_ids,omitempty"`
	// InterestEntityRefs names recorded entity identities (entity_ref values,
	// never prose) whose other observations should surface for this direction.
	InterestEntityRefs []string `json:"interest_entity_refs,omitempty"`
	// ContextVersion is the optimistic concurrency token of the selection above.
	ContextVersion uint64   `json:"context_version"`
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Doc            string   `json:"doc,omitempty"`
	FromFactIDs    []string `json:"from_fact_ids,omitempty"`
	// FromFactClaims records the claim version of each premise when the
	// direction was created. A later revision of a premise is surfaced in the
	// board frontier as "premise_changed": it is a judgement input for the
	// commander, not an automatic block (the dispatch gate still checks the
	// records that exist now).
	FromFactClaims map[string]uint64 `json:"from_fact_claims,omitempty"`
	// Skills are extra harness skills for this intent: a name under
	// <data>/skills/ or an absolute SKILL.md path (docs/06, D23).
	Skills []string `json:"skills,omitempty"`
	// Eval is the optional acceptance criterion for the harness's own goal
	// evaluator (-e). Empty means "stop when the agent stops calling tools";
	// a criterion lets a long run keep going until it is actually met.
	Eval string `json:"eval,omitempty"`
	// MaxTurns is an optional cap on the harness agent loop. 0 means the
	// harness default; audit has no equivalent flag.
	MaxTurns int `json:"max_turns,omitempty"`
	// Node is the execution location this direction belongs on; empty means the
	// registry's default node (docs/14-nodes.md). The run records where it
	// actually ran, which may differ if the node disappeared.
	Node string `json:"node,omitempty"`
	// Profile is the saved model configuration this direction should run on;
	// empty means the instance default. Only the local node can honour it (a
	// remote host has its own configuration); the run freezes the resolved
	// profile and revision so a later edit cannot rewrite history.
	Profile string `json:"profile,omitempty"`
	// CreatedBy records the participant that registered the intent.
	CreatedBy string `json:"created_by_participant_id,omitempty"`
	// Wait is set while the direction is waiting for children. It is business
	// state, not a sleep: the run ends, the slot is released, and a result event
	// (or the deadline) moves the direction back to pending.
	Wait *Wait `json:"wait,omitempty"`
	// HandoffVersion is the optimistic-concurrency version of Handoff. A
	// replacement request names the version it saw; an old run cannot overwrite a
	// newer handoff.
	HandoffVersion   int      `json:"handoff_version,omitempty"`
	HandoffOperation string   `json:"handoff_operation,omitempty"`
	Handoff          *Handoff `json:"handoff,omitempty"`
	// Aborted marks a descendant whose subtree was cancelled. A cancelled run
	// normally returns the intent to pending (D19), but an aborted child must
	// settle as failed so its parent's wait does not hang on a direction nobody
	// will ever dispatch.
	Aborted bool   `json:"aborted,omitempty"`
	Status  string `json:"status"`
	// Hold is the operator/commander's "do not auto-dispatch" flag. A canceled
	// intent goes back to pending (D19), and without this the next round's
	// dispatchPending would immediately start it again — cancel would not
	// cancel. Held intents stay pending until an explicit retry/run clears it.
	Hold bool `json:"hold,omitempty"`
	// HeldBy records who set the current Hold, because "may this be restarted?"
	// depends on it: the operator's stop is not the commander's to override, a
	// parent may restart the worker it killed, and an automatic liveness stop may
	// be retried by whoever owns the direction. Values are the HoldActor*
	// constants, or the participant id ("pt_<intent>") of the parent that killed
	// this worker. Empty on legacy rows and whenever Hold is false.
	HeldBy string `json:"held_by,omitempty"`
	// HoldReason is the short text recorded with HeldBy (the stop reason without
	// the "止损：" display prefix). It is audit context, not an instruction.
	HoldReason string `json:"hold_reason,omitempty"`
	// Bootstrap marks a no-premise direction created by a harness session after
	// the project already had facts. It is held until the operator confirms the
	// first touch; only the operator may retry/run it (the commander cannot clear
	// the hold itself).
	Bootstrap  bool `json:"bootstrap,omitempty"`
	Attempts   int  `json:"attempts"`
	SteerCount int  `json:"steer_count,omitempty"`
	// SayCount records mid-flight messages independently of continuations.
	// Neither counter is an admission or fee limit.
	SayCount int `json:"say_count,omitempty"`
	// Interjections is the audit trail of messages delivered into this
	// direction while it was running. It is deliberately not a fact: it is an
	// instruction, not an observation.
	Interjections []Interjection `json:"interjections,omitempty"`
	// Notes is the retry/steer commentary about this direction. It is shown in
	// the console and read by the decider; BuildPrompt never reads it, so a
	// note cannot become an executor constraint (D72).
	Notes     []IntentNote `json:"notes,omitempty"`
	ClaimedBy string       `json:"claimed_by,omitempty"`
	// ReviewFailedFacts is the unresolved set from the last fixed-review phase.
	// A retry must re-review these facts (or see them corrected) even when the
	// new run declares nothing new; otherwise a bare re-run could flip
	// review_failed to done without changing the rejected assertion.
	ReviewFailedFacts []string `json:"review_failed_facts,omitempty"`
	ParkReason        string   `json:"park_reason,omitempty"`
	// ParkedPremiseFactIDs is the snapshot of premises that were not usable when
	// the direction was parked. It is empty for a park that was not premise-
	// blocked. The control plane uses it to tell a stale park reason from a still
	// blocked direction: when every recorded premise becomes usable, the parked
	// direction gets a parked_premise_ready attention (and the console offers a
	// reopen) instead of leaving the old prose in place forever.
	ParkedPremiseFactIDs []string `json:"parked_premise_fact_ids,omitempty"`
	// ParkedPremiseClaims records the claim version each blocked premise had at
	// park time, so the notice can say the blocker actually changed rather than
	// merely being re-read.
	ParkedPremiseClaims map[string]uint64 `json:"parked_premise_claims,omitempty"`
	ParkedAt            *time.Time        `json:"parked_at,omitempty"`
	// DoneDeclaredBy is the actor that declared this direction done without a
	// run closing it (commander/operator settle). Empty means the run produced
	// the completion, which is the only kind that carries a run's outcome.
	DoneDeclaredBy string     `json:"done_declared_by,omitempty"`
	DoneNote       string     `json:"done_note,omitempty"`
	DoneFactIDs    []string   `json:"done_fact_ids,omitempty"`
	DoneRunIDs     []string   `json:"done_run_ids,omitempty"`
	DoneAt         *time.Time `json:"done_at,omitempty"`
	RunID          string     `json:"run_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// DoneDeclared reports a completion written by a person/commander judgement
// rather than by a run. Surfaces that answer "did the executor finish this?"
// must use this to keep the two apart.
func (in Intent) DoneDeclared() bool { return in.DoneDeclaredBy != "" }

// ClearDoneDeclared drops the declared-completion provenance. It belongs to the
// completed turn it described: once a retry, continue or reopen puts the
// direction back on the queue, the row must not keep claiming somebody declared
// it done (the settle note stays in Notes as history).
func (in *Intent) ClearDoneDeclared() {
	in.DoneDeclaredBy = ""
	in.DoneNote = ""
	in.DoneFactIDs = nil
	in.DoneRunIDs = nil
	in.DoneAt = nil
}

// Wait is the persisted condition a direction yielded on. Evaluation, the
// transition to pending, and the wake flag all happen under the store lock, so a
// child that finishes just before registration cannot be lost: RegisterWait
// evaluates once while holding the same lock and returns ready immediately.
type Wait struct {
	// Actions is mutually exclusive with Operator. Only these explicitly selected
	// external operations can release the waiting turn.
	Actions []string `json:"actions,omitempty"`
	// Operator is an external condition, completed only by an operator receipt.
	// It is mutually exclusive with Actions and has no automatic deadline.
	Operator *OperatorWait `json:"operator,omitempty"`
	// Mode is all (every action settled) or any (the first settled action).
	Mode string `json:"mode,omitempty"`
	// Deadline is optional. A ticker wakes the parent with a timeout result
	// instead of leaving it waiting forever.
	Deadline     *time.Time `json:"deadline,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	// RegisteredByRun is the run whose turn yielded. A later run's completion
	// must not consume this wait.
	RegisteredByRun string `json:"registered_by_run,omitempty"`
	// WakePending means the condition is satisfied (or the deadline fired) but
	// the parent has not been re-dispatched yet. The scheduler consumes it.
	WakePending bool   `json:"wake_pending,omitempty"`
	WakeReason  string `json:"wake_reason,omitempty"`
	// Results is the structured result event the parent wakes with. It is
	// persisted so a restart can rebuild the wake prompt.
	Results []WaitResult `json:"results,omitempty"`
}

// OperatorWait keeps a human handoff distinct from evidence and child results.
// A receipt distinguishes a completed action from an unavailable prerequisite;
// neither verifies login or any security claim.
type OperatorWait struct {
	ID          string     `json:"id"`
	Request     string     `json:"request"`
	AccessIDs   []string   `json:"access_ids,omitempty"`
	Response    string     `json:"response,omitempty"`
	Resolution  string     `json:"resolution,omitempty"` // completed|unavailable; empty on old completed receipts
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

const (
	OperatorCompleted   = "completed"
	OperatorUnavailable = "unavailable"
)

// OperatorRequest is a project-level ask for the operator. It is control
// state, not a fact and not an intent: the operator inbox reads it, and the
// commander may fill in the concrete action/asset/impact/authorization fields
// when the operator asks for details. Generic requests are created by the
// control plane when the goal is open and no direction is in flight, so the
// console always has something to show even if the model writes nothing.
type OperatorRequest struct {
	ID string `json:"id"`
	// Action/Asset/Impact/Authorization are the concrete ask. A generic request
	// carries only Action and Authorization; the commander fills the rest.
	Action        string `json:"action,omitempty"`
	Asset         string `json:"asset,omitempty"`
	Impact        string `json:"impact,omitempty"`
	Authorization string `json:"authorization,omitempty"`
	Status        string `json:"status"`
	// Generic marks the control-plane fallback. It becomes false when the
	// commander submits concrete fields.
	Generic bool `json:"generic,omitempty"`
	// RequestedBy is "decider" for the fallback or "commander" after a detail
	// update; it is provenance, not authorization.
	RequestedBy string `json:"requested_by,omitempty"`
	// Resolution is the operator's receipt text.
	Resolution string `json:"resolution,omitempty"`
	// DetailRequested is the operator's one-shot "让指挥官列明细" action. The
	// commander prompt carries the request id once, then the flag is cleared.
	DetailRequested bool       `json:"detail_requested,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

const (
	OperatorRequestPending   = "pending"
	OperatorRequestResolved  = "resolved"
	OperatorRequestDismissed = "dismissed"

	OperatorRequestFromDecider   = "decider"
	OperatorRequestFromCommander = "commander"
	OperatorRequestFromOperator  = "operator"
)

// Wait modes and wake reasons.
const (
	WaitAll = "all"
	WaitAny = "any"

	WaitReasonTimeout = "timeout"
)

// WaitResult is one settled outcome of a wait. Status is the settled state of
// the awaited operation; StopReason is the harness stop reason of the run that
// produced it.
type WaitResult struct {
	ActionID    string    `json:"action_id,omitempty"`
	Result      string    `json:"result,omitempty"`
	ResultRef   string    `json:"result_ref,omitempty"`
	Status      string    `json:"status"`
	RunID       string    `json:"run_id,omitempty"`
	StopReason  string    `json:"stop_reason,omitempty"`
	FactIDs     []string  `json:"fact_ids,omitempty"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	At          time.Time `json:"at"`
}

// Settled reports whether a status is terminal for waiting purposes. Waiting is
// deliberately not settled: a direction that is itself waiting is still open work.
func Settled(status string) bool {
	switch status {
	case StatusDone, StatusFailed, StatusReviewFailed, StatusParked:
		return true
	}
	return false
}

// Fact is a recorded observation or conclusion produced by one intent.
//
// Confidence is observed (an executor saw it) or inferred (derived by the
// decider or by heuristic ingestion). Status is unverified until a verifier
// re-runs the proof; only report-bound facts are verified.
type Fact struct {
	// DeclarationID is the writer's stable identity for this observation: the
	// same id re-declared means "the same observation, updated"; a different id
	// is a different observation even with the same title. It defaults to the
	// facts.jsonl byte range, which makes re-reading a settled file idempotent.
	DeclarationID string `json:"declaration_id,omitempty"`
	// ClaimVersion starts at 1 and increments whenever the assertion changes
	// (summary/doc/evidence/tags/proof/context or observation time). Reviews
	// bind to the version they proved; a changed claim returns to unverified.
	// The display title and the review status are not part of the claim.
	ClaimVersion uint64 `json:"claim_version"`
	// ClaimHash fingerprints the proven assertion, so a review can detect that
	// it judged different content even when the version was not supplied.
	ClaimHash string `json:"claim_hash"`
	// ObservedAt is when the writer says it observed the thing; empty means
	// "when it was recorded". It is part of the claim: a redated observation is
	// a new assertion version.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	// ExecutionContextRef is the explicit site the observation came from (a
	// browser resource, a run context). It is recorded identity, never inferred
	// from prose, and it is part of the claim fingerprint.
	ExecutionContextRef string `json:"execution_context_ref,omitempty"`
	EntityRef           string `json:"entity_ref,omitempty"`
	SourceRunID         string `json:"source_run_id,omitempty"`
	DeclarationRef      string `json:"declaration_ref,omitempty"`
	ID                  string `json:"id"`
	IntentID            string `json:"intent_id"`
	Kind                string `json:"kind"` // host|service|cred|vuln|loot|artifact|note
	Title               string `json:"title"`
	Summary             string `json:"summary"`
	Doc                 string `json:"doc,omitempty"`
	// DeclaredConfidence is the writer's own claim about how this was obtained:
	// observed (something was seen) or inferred (read off, guessed, derived).
	// It is the base the effective Confidence is recomputed from, and the store
	// never rewrites it — an inference must not become an observation by being
	// forgotten.
	DeclaredConfidence string `json:"declared_confidence,omitempty"`
	// Confidence is derived, not declared: it is inferred when the base is
	// inferred or when any hard premise of the direction is not verified,
	// otherwise the base itself. It labels the assertion without being part of
	// it, so moving it neither bumps the claim version nor invalidates a review
	// (store.claimHash excludes it).
	Confidence string `json:"confidence"`
	Origin     string `json:"origin,omitempty"` // declared|derived|heuristic
	// Severity is an optional label: critical|high|medium|low, empty = ungraded.
	// It is bookkeeping, not part of the assertion — it is deliberately absent
	// from store.claimHash, so re-grading a proven conclusion keeps its version
	// and its review. The writer may declare it; no read path re-maps it.
	Severity string `json:"severity,omitempty"`
	// SourceRef is the stable identity of the thing this fact was translated
	// from, when the writer knows it: an audit finding is
	// "report:<reportID>#<findingID>". Two findings that cite the same file are
	// different findings; before this, the importer deduped by evidence overlap
	// and silently dropped the second one (D74).
	SourceRef string `json:"source_ref,omitempty"`
	// Proof says how the conclusion is reproduced: "dynamic" (default) re-runs
	// the attack, "static" replays the command over the same artifact. A static
	// finding has no live surface to attack, so re-running an exploit would
	// reject it for the wrong reason (D35).
	Proof       string   `json:"proof,omitempty"` // dynamic|static
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	// PendingEvidence is what a still-running declaration named as evidence
	// before the ledger preserved it. Files are only admitted at settlement, so
	// until then the fact has no evidence rows; without this field a reader
	// cannot tell "wrote no evidence" from "evidence not preserved yet", and a
	// commander reading the brief would demand a re-declaration that cannot
	// change anything mid-run. It is display state, not part of the assertion:
	// it is excluded from claimHash, never counts as evidence, and settlement
	// clears it when the authoritative set is attached.
	PendingEvidence []string `json:"pending_evidence,omitempty"`
	// Tags are optional labels for reuse: playbooks use them for recall
	// (`product:thinkphp`, `technique:rce`, `port:8080`). They are not an
	// enum and never gate a write; the brief uses them to decide which
	// playbooks to put in front of the next run.
	Tags []string `json:"tags,omitempty"`
	// Refs are the citations this fact made that the ledger has no bytes for:
	// a URL (only a human can open it), a tool artifact (waiting for the
	// artifact store), or a file the run named that is gone. They are recorded
	// instead of dropping the fact: a finding whose only support was a live URL
	// still deserves a line in "scope and limitations" (D44).
	Refs   []Ref  `json:"refs,omitempty"`
	Status string `json:"status"` // unverified|verified|rejected|withdrawn
	// The withdrawal bookkeeping below records that the authoring direction or
	// the operator said "this record should not stand". It is not a review
	// verdict: a withdrawn fact is unusable as a premise or a report finding,
	// but the row, its evidence and its version history stay readable. The
	// fields bind to RetractedClaimVersion; a later correction starts a new
	// assertion version and clears them (store.UpdateFact), so withdrawal can
	// never be used to launder new content under an old id.
	RetractedAt           *time.Time `json:"retracted_at,omitempty"`
	RetractedBy           string     `json:"retracted_by,omitempty"` // participant id
	RetractedRunID        string     `json:"retracted_run_id,omitempty"`
	RetractionReason      string     `json:"retraction_reason,omitempty"`
	RetractedClaimVersion uint64     `json:"retracted_claim_version,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// Withdrawn reports whether the current assertion was withdrawn. Withdrawal is
// an authorship statement ("this record should not have been made"), distinct
// from rejected ("an independent rerun did not reproduce it").
func (f Fact) Withdrawn() bool { return f.Status == "withdrawn" }

// Unusable reports that the row must not support a premise, a goal receipt, a
// handoff or a report finding. The row itself stays in the ledger for audit.
func (f Fact) Unusable() bool { return f.Status == "withdrawn" || f.Status == "rejected" }

// AssumptionLabel marks an assertion whose declared base is inference instead of
// observation. It states what the row is, not what to do with it: the reader
// decides. The label is derived at read time and never stored, so it cannot
// drift from the declaration.
const AssumptionLabel = "假设（推断）"

// Assumption reports whether the writer declared this assertion as inferred
// rather than observed. DeclaredConfidence is the writer's own claim and the
// store never rewrites it (store.AddFact only fills it when empty), so the label
// survives a passing review — unlike Confidence, which is derived and can move.
// A row written before the field existed falls back to Confidence, the same
// slot its claim hash reads (store.claimHash).
func (f Fact) Assumption() bool {
	base := f.DeclaredConfidence
	if base == "" {
		base = f.Confidence
	}
	return base == "inferred"
}

// Ref is where a claim says its support lives, when that place is not a file
// the ledger can open. Scheme is one of the Ref* constants below.
type Ref struct {
	Scheme  string `json:"scheme"`
	Locator string `json:"locator"`
}

// Reference schemes. Only "file" can produce bytes today; "artifact" will when
// there is an artifact store, and "url" never will — fetching it at report time
// would attack the target again, outside any authorised run.
const (
	RefFile     = "file"
	RefURL      = "url"
	RefArtifact = "artifact"
)

// Playbook is project knowledge, not a finding: a reusable tactic that outlived
// the run which discovered it. It is a separate table because a fact is an
// observation with evidence, while a playbook is a recipe with provenance — it
// is never verified, never reported, never counted as coverage (D70).
//
// Two writers, two tiers:
//   - the executor that used the tactic writes the confirmed row in the same
//     run (Origin=executor, Status=confirmed);
//   - the commander may only nominate a candidate (Origin=commander,
//     Status=candidate), and it must cite the facts it inferred it from.
//
// Status is the writer's declaration, not a verdict: only Confirmed(facts)
// answers "may this be called confirmed", from a non-empty provenance whose
// every source is verified, carries evidence and is not withdrawn or rejected.
// It is re-derived on every read, so a later review promotes the row and a
// withdrawal downgrades it without rewriting the stored declaration.
//
// Doc is prompt text handed to a later executor. It is never executed, and it
// is treated as untrusted: the brief says "核对再用".
type Playbook struct {
	Archived         bool               `json:"archived,omitempty"`
	SourceRunID      string             `json:"source_run_id,omitempty"`
	FromFactVersions map[string]uint64  `json:"from_fact_versions,omitempty"`
	Pattern          *ExperiencePattern `json:"pattern,omitempty"`
	RequiredTags     []string           `json:"required_tags,omitempty"`
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	// When is one line: what this buys you and when it applies.
	When string `json:"when"`
	// Doc is steps/commands/limits, written by whoever ran it.
	Doc  string   `json:"doc,omitempty"`
	Tags []string `json:"tags,omitempty"`
	// Outcome is success (try it) or dead_end (do not retry without a new
	// premise). A dead end is knowledge too.
	Outcome string `json:"outcome,omitempty"`
	Origin  string `json:"origin"` // executor|commander
	// Status is what the writer declared (confirmed|candidate|retired). It is
	// not the confirmation label: readers must ask Confirmed(facts), because the
	// declaration cannot see whether its sources were verified afterwards.
	Status string `json:"status"`
	// FromFactIDs is provenance. A commander nomination without at least one
	// existing fact is refused: no observation, no knowledge.
	FromFactIDs []string  `json:"from_fact_ids,omitempty"`
	IntentID    string    `json:"intent_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ExperiencePattern describes recorded relationships to look up. Variable
// names and entity bindings are authored explicitly, never inferred from titles.
type ExperiencePattern struct {
	Nodes []PatternNode `json:"nodes"`
	Edges []PatternEdge `json:"edges"`
}
type PatternNode struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Kind      string   `json:"kind,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	EntityVar string   `json:"entity_var,omitempty"`
}
type PatternEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// Playbook origins and tiers. They are not fact origins: a playbook is never
// "declared evidence", so mixing the two vocabularies would blur the line this
// table exists to draw.
const (
	PlaybookFromExecutor  = "executor"
	PlaybookFromCommander = "commander"

	PlaybookConfirmed = "confirmed"
	PlaybookCandidate = "candidate"
	PlaybookRetired   = "retired"

	PlaybookSuccess = "success"
	PlaybookDeadEnd = "dead_end"
)

// Message is one participant-to-participant message. It is a record of a
// conversation, not a fact: nothing in the fact, board, review or report paths
// reads it, and a message never becomes evidence.
//
// Participants are derived from intents (one logical participant per intent,
// stable across run generations); the ids are assigned by the control plane, not
// supplied by the caller. Seq is the per-project read cursor; IdempotencyKey is
// the caller's retry key and is unique per sender.
type Message struct {
	GoalInstruction     bool     `json:"goal_instruction,omitempty"`
	DirectiveRecipients []string `json:"directive_recipients,omitempty"`
	NoticeIntentID      string   `json:"notice_intent_id,omitempty"`
	NoticeFactID        string   `json:"notice_fact_id,omitempty"`
	NoticeVersion       uint64   `json:"notice_version,omitempty"`
	Coalesced           int      `json:"coalesced,omitempty"`
	// Durable messages survive the ordinary chat queue retention policy.
	Durable            bool   `json:"durable,omitempty"`
	InstructionID      string `json:"instruction_id,omitempty"`
	InstructionVersion uint64 `json:"instruction_version,omitempty"`
	ID                 string `json:"id"`
	Seq                uint64 `json:"seq"`

	ProjectID string `json:"project_id"`

	FromParticipantID string `json:"from_participant_id"`
	FromRunID         string `json:"from_run_id,omitempty"`
	FromGeneration    int    `json:"from_generation,omitempty"`

	ToParticipantID string `json:"to_participant_id"`
	// ToGeneration is the recipient generation the sender addressed. It is
	// rejected when the target has moved on, so an old run cannot deliver into
	// a new holder of the same intent.
	ToGeneration int `json:"to_generation,omitempty"`

	// Kind describes semantics, not a role. A task result defaults to durable
	// retention; other kinds can explicitly request Durable without text parsing.
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`

	FactRefs  []string `json:"fact_refs,omitempty"`
	InReplyTo string   `json:"in_reply_to,omitempty"`

	// Interrupt is the old say's interrupt flag carried as a message property:
	// it asks the harness to stop the current model request instead of draining
	// the text before the next one (D69). It is only meaningful for a message
	// delivered into a live turn.
	Interrupt bool `json:"interrupt,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// EventID is the application-level identity of this message. It is assigned
	// once when the message enters the queue and is reused by every retry and
	// every process restart, so the harness can dedupe a redelivery by id
	// instead of guessing from retry counts. Empty on rows written before the
	// field existed; the delivery path fills it from ID on first claim.
	EventID string `json:"event_id,omitempty"`
	// Chain is the delivery chain for result semantics (task.result v1). It is
	// empty for ordinary messages, which the protocol forbids from carrying it.
	Chain []string `json:"chain,omitempty"`

	// Status is the delivery lifecycle: queued (stored, not delivered),
	// accepted (IOA accepted, or the next run carried it in its prompt; the
	// model may not have read it yet), confirmed (the recipient acknowledged
	// it), or unknown (the send timed out; it may or may not have arrived —
	// never retry automatically). A withdrawn or swept message is gone, not a
	// status: there is no retracted/expired row.
	Status string `json:"status"`
	// DeliveryID is the IOA message id, when there was one, or the synthetic
	// "prompt:<run>" marker when the run's prompt carried the text.
	DeliveryID string `json:"delivery_id,omitempty"`
	// DeliveredRunID is the run this message was handed to: the prompt that
	// carried it (DeliveryPath=prompt) or the live run it was said into
	// (DeliveryPath=ioa). Empty means never delivered. It is how a failed
	// run's possibly-unread messages are found and requeued.
	DeliveredRunID string `json:"delivered_run_id,omitempty"`
	// DeliveryPath says how the bytes reached the recipient: "prompt" (the
	// run's prompt/inbox section) or "ioa" (said into the live session). The
	// redelivery rules use it: an IOA message may still be only in the live
	// session's in-memory inbox, while a prompt message is already part of the
	// session transcript and comes back with a resume.
	DeliveryPath string `json:"delivery_path,omitempty"`
	// Redeliveries counts how many times the control plane put this message
	// back on the queue after a failed run. >0 makes the injected text carry a
	// [重投] marker so the model can dedupe by message_id instead of acting
	// twice on the same instruction.
	Redeliveries int `json:"redeliveries,omitempty"`
	// Error is the transport failure, kept verbatim for the operator.
	Error string `json:"error,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
}

// Message delivery states. Sending is persisted before transport I/O; a crash
// leaves an unknown outcome. Accepted is transport acceptance, confirmed is a
// recipient receipt. Only queued messages can still be retracted or swept.
const (
	MessageQueued    = "queued"
	MessageSending   = "sending"
	MessageAccepted  = "accepted"
	MessageConfirmed = "confirmed"
	MessageUnknown   = "unknown"
)

// Message kinds used by the JSON contract and the console. They are labels,
// not an enum: a participant may send any short kind.
const (
	MessageQuestion = "question"
	MessageUpdate   = "update"
	MessageResult   = "result"
	MessageNote     = "note"
)

// Evidence is an original artifact. New rows point URI at a byte-for-byte
// preserved copy under the case root and record its Digest, so a later rewrite
// of the working path cannot silently change what the ledger proves. Rows
// written before preservation have no Digest: for those URI is whatever the
// working path held when they were admitted, and that limitation is explicit
// rather than repaired by a startup scan (docs/06 D150).
type Evidence struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	IntentID string `json:"intent_id"`
	Name     string `json:"name"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size"`
	URI      string `json:"uri"`
	// Digest is the SHA-256 (hex) of the preserved bytes URI opens. Empty means
	// the row predates preservation or the source had no stable bytes.
	Digest string `json:"digest,omitempty"`
	// SourceURI is where the bytes were found when admitted (case-relative
	// working path, or the original imported/captured file). It is provenance,
	// not an address: readers open URI, never SourceURI.
	SourceURI string `json:"source_uri,omitempty"`
	// From records where the bytes came from when the executor declared a source:
	// "url:<u>" or "artifact:<id>". Empty for a plain file. It is provenance, not
	// an address: the ledger still opens URI (D44).
	From      string    `json:"from,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Goal is an operator-defined endpoint: Start is where the work begins (the
// known assets and what has already been proven), Statement is the falsifiable
// end state. Both are prose, never scored by the program.
//
// met=true requires an evidence_id that resolves to a real evidence row: a
// claim nobody can click through is not a finding (docs/03, iron rule 4).
//
// A stamp is also a decision someone has to be able to read later, so it
// carries its receipt: Note is the one-line reason, From is the cited facts and
// CompletedAt is when it was closed. Without them the console can only say
// "已达成" — which is exactly the question ("why?") the operator asks next.
type Goal struct {
	Closed         bool       `json:"closed,omitempty"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	ClosedReason   string     `json:"closed_reason,omitempty"`
	InstructionRef string     `json:"instruction_ref,omitempty"`
	CreatedByRun   string     `json:"created_by_run_id,omitempty"`
	// UpdatedBy is who wrote this revision of the statement: "operator" or the
	// run id of the commander round that did. It exists because the console
	// credential is shared, so "the request was authenticated" never said which
	// of the two wrote the words — and an audit column that reads "operator" for
	// a model's write is worse than no column. Each goal version is a ledger
	// snapshot, so the value is per-revision like CreatedByRun.
	UpdatedBy        string `json:"updated_by,omitempty"`
	CompletionPolicy string `json:"completion_policy,omitempty"`
	ID               string `json:"id"`
	Statement        string `json:"statement"`
	// Start is the operator's starting point for this goal (what is known now).
	// Empty on the goal created together with a project: there the project brief
	// is the start, so repeating it would only duplicate the brief in prompts.
	Start             string            `json:"start,omitempty"`
	Met               bool              `json:"met"`
	EvidenceID        string            `json:"evidence_id,omitempty"`
	Note              string            `json:"note,omitempty"`
	From              []string          `json:"from,omitempty"`
	FromClaimVersions map[string]uint64 `json:"from_claim_versions,omitempty"`
	CompletedAt       *time.Time        `json:"completed_at,omitempty"`
	// MetByRun is the commander run that stamped this goal. Empty means the
	// operator pinned it by hand — including every goal stamped before this
	// field existed — and only the operator may withdraw such a stamp. Without
	// the distinction a commander session could erase the operator's judgement
	// (and its one-line reason) with a single reopen call.
	MetByRun string `json:"met_by_run_id,omitempty"`
	// QuantifiedTarget is the legacy quantified criterion written by older
	// consoles. Operator-defined goals carry Start + Statement now; the field is
	// kept so existing ledgers load and read exactly as before.
	QuantifiedTarget string    `json:"quantified_target,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	// ReceiptStale/ReceiptChanged are computed, never persisted: they say that a
	// fact the completion cited has changed since the stamp, so the operator (or
	// commander) must re-judge the goal. The stamp itself is never rewritten
	// automatically — history keeps what was actually claimed then.
	ReceiptStale   bool     `json:"receipt_stale,omitempty"`
	ReceiptChanged []string `json:"receipt_changed,omitempty"`
	// IdempotencyKey makes an explicit "append a goal and continue" retry-safe:
	// a repeated request with the same key returns the goal it already created
	// instead of appending a second one. Empty on goals created by the commander
	// or by the plain create endpoint.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Review is one verifier verdict on one fact.
type Review struct {
	// ClaimVersion/ClaimHash are the assertion the verdict actually proved. An
	// asynchronous verifier carries the version it started with; if the claim
	// changed meanwhile the store refuses the stale verdict. A zero version on
	// input means a synchronous human verdict recorded under the store lock.
	ClaimVersion uint64    `json:"claim_version"`
	ClaimHash    string    `json:"claim_hash"`
	ID           string    `json:"id"`
	FactID       string    `json:"fact_id"`
	Verdict      string    `json:"verdict"` // pass|needs_evidence|reject
	Notes        string    `json:"notes,omitempty"`
	VerifierRun  string    `json:"verifier_run,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Usage is the token accounting of one turn. Known=false means the harness did
// not report usage at all (do not print 0 as if it were measured); Missing counts
// model calls inside the turn that returned no usage.
type Usage struct {
	InputTokens       int  `json:"input_tokens,omitempty"`
	OutputTokens      int  `json:"output_tokens,omitempty"`
	CachedInputTokens int  `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int  `json:"cache_write_tokens,omitempty"`
	TotalTokens       int  `json:"total_tokens,omitempty"`
	Requests          int  `json:"requests,omitempty"`
	Missing           int  `json:"missing,omitempty"`
	Known             bool `json:"known"`
}

// Handoff is a task handover snapshot: what a replacement model needs to
// continue without copying the old conversation. It is history/state, not an
// operator constraint and not a fact; the new session reads it as a brief.
type Handoff struct {
	ID      string `json:"id"`
	Version int    `json:"version"`

	FromRun        string `json:"from_run,omitempty"`
	FromProfile    string `json:"from_profile,omitempty"`
	FromGeneration int    `json:"from_generation,omitempty"`
	ToProfile      string `json:"to_profile,omitempty"`
	ToGeneration   int    `json:"to_generation,omitempty"`

	// Note is the caller's short account of what the next model should know.
	// It is history, not an executor constraint.
	Note string `json:"note,omitempty"`
	// Facts are confirmed/evidenced fact ids; Evidence are their case-relative
	// files. Attempts are prior run stop reasons, newest first.
	Facts             []string          `json:"facts,omitempty"`
	FactVersions      map[string]uint64 `json:"fact_versions,omitempty"`
	FactClaimVersions map[string]uint64 `json:"fact_claim_versions,omitempty"`
	Evidence          []string          `json:"evidence,omitempty"`
	Attempts          []string          `json:"attempts,omitempty"`
	Wait              *Wait             `json:"wait,omitempty"`
	// MessageCursor is the highest message seq visible when the handoff was
	// written, so the next session can read only newer peer messages.
	MessageCursor uint64    `json:"message_cursor,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// MaxRunWarnings bounds one run's diagnostic list. A run that keeps failing must
// not grow its warning banner for the life of the process: the console renders
// the whole list, the board quotes it and the next attempt's prompt carries it.
// Both writers — the API's appendRunWarning and the ledger's handoff merge —
// stop here, so this is the one number; do not restate it in either package.
const MaxRunWarnings = 20

// Run is one executor invocation for one intent.
type Run struct {
	NativeSubagentCalls      int       `json:"native_subagent_calls,omitempty"`
	NativeSubagentActive     int       `json:"native_subagent_active,omitempty"`
	NativeSubagentDurationMS int64     `json:"native_subagent_duration_ms,omitempty"`
	CommanderRound           int       `json:"commander_round,omitempty"`
	NoteRead                 *NoteRef  `json:"note_read,omitempty"`
	NoteWrites               []NoteRef `json:"note_writes,omitempty"`
	// Queue time is distinct from execution; nil means it was not measured.
	QueuedAt           *time.Time `json:"queued_at,omitempty"`
	ExecutionStartedAt *time.Time `json:"execution_started_at,omitempty"`
	QueueMS            *int64     `json:"queue_ms,omitempty"`
	ExecutionMS        *int64     `json:"execution_ms,omitempty"`
	// ContextFactVersions records the record revisions of the intent's explicit
	// references/pins at the moment this run's input was frozen; it answers
	// "what did this run actually read" after later edits.
	ContextFactVersions map[string]uint64 `json:"context_fact_versions,omitempty"`
	// PremiseClaimVersions records the assertion versions of the required
	// premises, so a reviewer or the operator can tell whether the conclusion
	// was based on the claim that still stands.
	PremiseClaimVersions map[string]uint64 `json:"premise_claim_versions,omitempty"`
	// ResourceRefs names the registered execution-site resources this run
	// referenced (browser/action/wait/session projections). It is a reference
	// list, not ownership: the resource's live state and holder are read from
	// its own owner record.
	ResourceRefs        []string          `json:"resource_refs,omitempty"`
	InstructionVersions map[string]uint64 `json:"instruction_versions,omitempty"`
	InstructionNotices  map[string]uint64 `json:"instruction_notices,omitempty"`
	InstructionAcks     map[string]uint64 `json:"instruction_acks,omitempty"`
	DeclarationStart    *int64            `json:"declaration_start,omitempty"`
	PremiseVersions     map[string]uint64 `json:"premise_versions,omitempty"`
	PremisePolicy       string            `json:"premise_policy,omitempty"`
	// ReviewSkipped records that this run's inputs were frozen without the
	// verified-only gate because the instance runs with review disabled
	// (--no-verify). The policy field alone cannot tell the two apart.
	ReviewSkipped bool   `json:"review_skipped,omitempty"`
	CleanupPhase  string `json:"cleanup_phase,omitempty"`
	ID            string `json:"id"`
	IntentID      string `json:"intent_id"`
	// Node is the execution location that actually ran (not the one requested):
	// a vanished node must be visible in the ledger, not silently substituted.
	Node string `json:"node"`
	// Purpose is "" for an intent run, "verifier" for a review, "report" for a
	// report draft. It used to be encoded in Node, which stopped being true once
	// there was more than one node.
	Purpose string `json:"purpose,omitempty"`
	// Profile/ResolvedProfile/Tier/ProfileSource are read-only history from the
	// retired model-profile layer: they let old ledger rows keep saying which
	// binding ran. New runs never write them (the external gateway owns routing),
	// and a run without one simply omits the field.
	Profile         string `json:"profile,omitempty"`
	ResolvedProfile string `json:"resolved_profile,omitempty"`
	Tier            string `json:"tier,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ProfileSource   string `json:"profile_source,omitempty"`
	// Usage records provider token observations. The ledger counts tokens only:
	// it has no price or cost field, and a row that carries the retired keys in
	// JSON keeps them out of the model as unknown fields.
	Usage Usage `json:"usage,omitempty"`
	// HandoffFrom / HandoffVersion record that this run started as a replacement
	// session after a handoff, and which snapshot it consumed.
	HandoffFrom    string `json:"handoff_from,omitempty"`
	HandoffVersion int    `json:"handoff_version,omitempty"`
	// Generation is the intent's claim count at this run (matches the participant
	// token generation); a later replacement session increments it.
	Generation int        `json:"generation,omitempty"`
	Status     string     `json:"status"` // running|done|failed|canceled
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	ExitReason string     `json:"exit_reason,omitempty"`
	// CancelCause is structured only when Status is canceled: steer / retry /
	// stop_loss / restart / watchdog / operator / worker_kill / pause /
	// review_pause. Empty for old rows.
	CancelCause string `json:"cancel_cause,omitempty"`
	Events      int    `json:"events"`
	// SessionID is the native harness identity, independent of this run ID.
	SessionID string `json:"session_id,omitempty"`
	// ToolFailures counts structured failed tool results, never prose matches.
	ToolFailures int `json:"tool_failures,omitempty"`
	// LastEventAt is a live pulse, not bookkeeping history: it is touched in
	// memory as events arrive so the board can tell a thinking run from a dead
	// one. The authoritative Events count is written when the run settles.
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	// LastTurnStartedAt is when the control plane saw this run's last
	// turn_started event. A failed run uses it to decide which messages were
	// delivered after the last turn began and may therefore not have been
	// drained/read yet (accepted messages UpdatedAt >= this time). Zero means the run
	// never started a turn: nothing it carried could have been read.
	LastTurnStartedAt *time.Time `json:"last_turn_started_at,omitempty"`
	DeclaredFacts     int        `json:"declared_facts"`
	// StopReason is the harness's own verdict on the turn (completed / canceled
	// / error). It sits next to the exit code because a harness can exit 0 after
	// a failed turn (docs/11-protocol.md).
	StopReason string `json:"stop_reason,omitempty"`
	// FailureClass is how the run died, recorded by the side that saw it die:
	// transport (the link broke), intentional (the host closed on purpose),
	// unknown (stream ended with no reason). It answers "was this the link?"
	// from what happened, not from words found in the error text.
	FailureClass string `json:"failure_class,omitempty"`
	// AutoResumeAttempt counts the transport self-heal restarts of this
	// direction in the current episode (0 = none). The console reads it to say
	// "the link dropped, rerun #N" instead of showing a bare failure.
	AutoResumeAttempt int `json:"auto_resume_attempt,omitempty"`
	// LastState is the last harness status payload (compaction, context
	// budget). It is diagnostic, not a ledger fact.
	LastState string `json:"last_state,omitempty"`
	// Compacted records that the harness compressed this run's session context
	// during the turn. Compaction is lossy, so a later continue treats the
	// session's memory as clipped and leans on `.saker/worker-note.md` and facts.
	Compacted bool `json:"compacted,omitempty"`
	// LastEventKind / LastEventTool / LastEventText are the most recent
	// meaningful executor event, touched in memory as events arrive. They are
	// the board's one-line glance ("it is calling bash, not stuck"); the whole
	// tail is the run_events read tool. Usage/delta events do not overwrite
	// them: "still alive" is not "doing something" (D57).
	LastEventKind string `json:"last_event_kind,omitempty"`
	LastEventTool string `json:"last_event_tool,omitempty"`
	LastEventText string `json:"last_event_text,omitempty"`
	// Warnings are non-fatal handoff problems the control plane noticed while
	// settling the run — a declared evidence path that is out of bounds or does
	// not exist, for example. They are operator diagnostics and the next
	// round's hint; they never change the run's outcome, they are deduped and
	// capped, and old rows simply have none (omitempty). They do not replace
	// the run event log: each one is also a `warning` event there.
	Warnings []string `json:"warnings,omitempty"`
	// Infos are non-fatal handoff notes the control plane noticed while settling
	// the run and deliberately does not count as warnings: today, a declaration
	// the writer corrected by re-declaring the same identity in the same batch.
	// They are still shown on the run, but they neither enter the board's
	// handoff_warnings nor the next round's "previous handoff problems" section.
	Infos []string `json:"infos,omitempty"`
	// WarningsDismissed records the operator's "ignore" on a finished run's
	// non-fatal handoff warnings. The rows stay in the ledger and the run detail
	// can still show them; the board's handoff_warnings and the status-page count
	// skip them. A dismissed row is not rewritten: the original diagnostics
	// remain auditable.
	WarningsDismissed bool `json:"warnings_dismissed,omitempty"`
	Tokens            int  `json:"tokens,omitempty"`
	// ContextTokens is the model's context size after the last call, touched in
	// memory while the run is live and written when it settles. ContextWindow
	// is the budget it is measured against, captured when the run starts.
	ContextTokens int `json:"context_tokens,omitempty"`
	// FinalContextTokens is the last context size the harness itself reported in
	// the turn (usage.detail.context_tokens / turn_ended.context_tokens). After
	// a harness compaction it is the post-compaction size while ContextTokens
	// keeps the pre-compaction peak; a continuation must budget against this
	// number because it is what the harness session still holds. 0 means the
	// harness did not report one (unknown, not an empty session).
	FinalContextTokens int `json:"final_context_tokens,omitempty"`
	// HandoffDone is the commander pressure cycle's state at the end of this
	// round: the note has already been rewritten by a verified handoff and the
	// session must keep working until the harness compacts. It is persisted
	// because the in-memory decider flag dies with the process: seeding the
	// peak of a session whose cycle already closed made the first round after a
	// restart a spurious handoff round, and a handoff round dispatches nothing
	// (D80). A continuation round records the flag it inherited; a rotation or
	// a compaction round records false because both start a new cycle.
	HandoffDone   bool   `json:"handoff_done,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
	// BriefPath is the archived snapshot of the dispatch prompt, relative to the
	// case root (`<case>/.saker/briefs/<runID>.md`). Every run that dispatches text
	// to a model writes one — a fresh dispatch, a resume/wake, a steer
	// continuation and a handoff run alike — because "what did this run actually
	// read" is the only evidence a later reader can trust, and a handoff is now
	// refused when the run it replaces has none. Old rows have none (omitempty).
	// Known credentials are redacted before the file is written.
	BriefPath string `json:"brief_path,omitempty"`
	// ContinuedFrom is the run this one continues (a steer), "" for a fresh
	// dispatch. Two runs on one intent are otherwise indistinguishable from two
	// independent attempts in the ledger (D42).
	ContinuedFrom string `json:"continued_from,omitempty"`
}
