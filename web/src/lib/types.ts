// Wire types for the ledger API. They mirror internal/model, internal/board and
// the handler payloads in internal/api + cmd/tw/hub.go one for one: the console
// adds no interpretation the server does not already make.

export type IntentStatus =
  | 'pending'
  | 'claimed'
  | 'running'
  | 'waiting'
  | 'reviewing'
  | 'done'
  | 'failed'
  | 'review_failed'
  | 'parked'
export type RunStatus = 'running' | 'done' | 'failed' | 'canceled'
export type FactStatus = 'unverified' | 'verified' | 'rejected' | 'withdrawn'

export interface AccessEntry {
  id: string; name: string; kind: 'browser' | 'state' | 'request'; origin: string; url: string
  account?: string; role?: string; tenant?: string; note?: string; session?: string; profile?: string; created_at: string
  lease?: BrowserLease
  intent_id?: string
}
export interface BrowserLease {
  id: string
  owner: string
  run_id?: string
  intent_id?: string
  generation?: number
  acquired_at: string
  note?: string
  state: 'held' | 'recovery'
  busy?: boolean
}
export interface OperatorRequest {
  intent_id: string; title: string; status: IntentStatus; run_id?: string; profile?: string
  /** When the direction registered the wait; the inbox mixes every pending row by
   *  time. Absent on older servers, and such rows sort after the timed ones. */
  at?: string
  request: { id: string; request: string; access_ids?: string[]; response?: string; resolution?: 'completed' | 'unavailable'; completed_at?: string }
}

/** A project-level authorization ask (control state, not an intent wait). */
export interface AuthorizationRequest {
  id: string
  action?: string
  asset?: string
  impact?: string
  authorization?: string
  status: 'pending' | 'resolved' | 'dismissed'
  generic?: boolean
  requested_by?: string
  resolution?: string
  detail_requested?: boolean
  created_at: string
  updated_at: string
  resolved_at?: string
}

export interface OperatorInbox {
  authorization: AuthorizationRequest[]
  handoff: OperatorRequest[]
  acceptance: { goal_id: string; statement: string; receipt_changed?: string[]; at?: string }[]
  warnings: { run: string; intent_id?: string; warnings: string[]; at?: string }[]
  /** Tool-approval projection merged by the server (GET /api/operator-inbox). */
  guardrail?: OperatorInboxGuardrail
  /** Requests that are no longer pending (resolved / dismissed), newest first.
   *  Read-only history: it does NOT count toward `total` or `summary` — a closed
   *  request is not work waiting on the operator. It exists because a closed
   *  request can still carry two states that disagree (dismissed while the work
   *  resumed), and the console has to show both. */
  recent?: AuthorizationRequest[]
  total: number
  /** The same read, split by category with each category's state. The badge,
   *  the overview card and the detail list all read these counts — none of them
   *  counts rows itself, so the three can never disagree. */
  summary?: OperatorInboxSummary
}

/** One actionable inbox record, with the id the detail view uses. */
export interface OperatorInboxItem {
  kind: 'authorization' | 'handoff' | 'acceptance' | 'warning' | 'guardrail' | string
  id: string
  title?: string
}

/** `ok` / `none` are known answers; `never_checked` / `unavailable` are unknown
 *  ones, and the console must show those instead of a count of 0. */
export interface OperatorInboxCategory {
  kind: string
  state: 'ok' | 'none' | 'never_checked' | 'unavailable' | string
  reason?: string
  count: number
  items: OperatorInboxItem[]
}

export interface OperatorInboxSummary {
  categories: OperatorInboxCategory[]
  total: number
  /** False when any category's answer is unknown: then no total may be shown. */
  countable: boolean
}

/**
 * The inbox's read-only tool-approval state. It reflects the last pending read
 * (the inbox never dials the host itself): `never_checked` before the console
 * has asked once, `available` when the host answered (a zero count is valid),
 * `unavailable` with the reason the read could not happen (no guardrail/JEV,
 * no live session, host unreachable). "Unavailable" is a configuration state,
 * not an error: the console must not paint it red.
 */
export interface OperatorInboxGuardrail {
  state: 'never_checked' | 'available' | 'unavailable' | string
  reason?: string
  count: number
  checked_at?: string
  /** session id → pending count for sessions that currently have any. */
  sessions?: Record<string, number>
}

/** One intercepted tool call waiting for an operator decision. */
export interface GuardrailPendingItem {
  operation_id: string
  call_id?: string
  tool?: string
  kind?: string
  /** Provider JSON arguments, relayed verbatim when the host sent valid JSON. */
  arguments?: unknown
  /** Undecodable/non-JSON arguments, clipped by the server. */
  arguments_text?: string
  session_id: string
  state?: string
  expires_at?: string
}

/** One session's result inside a fan-out pending read. */
export interface GuardrailSessionState {
  session_id: string
  state: 'available' | 'unavailable' | string
  reason?: string
  count: number
}

/** GET /api/guardrail/pending: a 200 read projection either way. */
export interface GuardrailPending {
  state: 'available' | 'unavailable' | string
  reason?: string
  checked_at: string
  attempts: number
  count: number
  pending: GuardrailPendingItem[]
  sessions?: GuardrailSessionState[]
}

/** POST /api/guardrail/resolve answer; 409 carries the same shape as a failure. */
export interface GuardrailResolveResult {
  state: 'resolved' | 'failed' | 'unavailable' | string
  reason?: string
  session_id: string
  operation_id: string
  approve: boolean
  attempts: number
}
export interface NativeArtifact {
  ref: string; run_id: string; intent_id: string; result_id: string; event_id: string
  tool: string; kind: string; target: string; media_type: string; size: number; created_at: string
}

export interface HostRuntimeStatus {
  endpoint: string
  pid: number
  online: boolean
  ready: boolean
  restarts: number
  failed_ready: number
  unhealthy: boolean
  last_error?: string
}

export interface NodeInfo {
  local?: boolean
  agent?: string
  name?: string
  online?: boolean
  ready?: boolean
  working_dir?: string
  model?: string
  config_error?: string
  id: string
  note?: string
  /** The instance default from nodes.json (not the project default). */
  default?: boolean
  /** How a request reaches it: an AOP host (the local machine is one too), or a local cyber-audit run. */
  transport?: 'audit' | 'aop'
  /** The AOP host URL, when transport is aop. */
  endpoint?: string
  /** The audit binary; empty for an AOP node. */
  bin?: string
  /** True when the console can open a shell on it (AOP hosts only). */
  terminal?: boolean
  host?: HostRuntimeStatus
  /** True when this project may dispatch to the node. */
  bound?: boolean
  /** True when intents without an explicit node land here. */
  project_default?: boolean
}

export interface NodesResponse {
  discovery_error?: string
  nodes: NodeInfo[]
  /** Instance default from nodes.json. */
  default: string
  /** The local execution host; no per-project binding is required. */
  project_default?: string
}


/** One saved harness model configuration. The key is never part of this shape. */

export interface ToolRank {
  local?: boolean
  completed?: number
  failures?: number
  duration_ms?: number
  /** The three most frequent normalised failure labels (api/tools.go). */
  error_kinds?: ToolErrorKind[]
  tool: string
  calls: number
  runs: number
  last_at?: string
}

/** One normalised failure bucket for a tool: the journal has no error codes, so
 *  the server reads the failed result's own text ("exit code 1", "unsupported
 *  flag --tlsv1.2") and keeps at most three labels per tool. */
export interface ToolErrorKind {
  label: string
  count: number
}

/** One binary arsenal installed, with the version its manifest recorded. */
export interface ArsenalTool {
  name: string
  version?: string
  size?: number
  /** bundle / download / manual — how it got here. */
  source?: string
  managed_by?: string
  installed_at?: string
  /** Every arsenal root that has this binary (instance and/or user). */
  dirs?: string[]
}

export interface ToolsResponse {
  leaderboard: ToolRank[]
  runs_scanned: number
  arsenal: ArsenalTool[]
  arsenal_dirs?: string[]
  /** Where the console looked for local tools, and whether PATH carries it. This
   *  is the only place a "tool missing" report can be read as a *location* fact
   *  instead of a guess. */
  tool_roots?: ToolRoot[]
  tool_roots_note?: string
}

/** One directory the instance treats as a tool root (api/tools.go toolRoot). */
export interface ToolRoot {
  path: string
  /** The `<root>/bin` directory a tool would be invoked from. */
  bin: string
  /** instance | harness | user | configured */
  source: string
  /** `<root>/bin` exists. */
  exists: boolean
  /** The instance resolved this root from its own data-directory rule. */
  resolved?: boolean
  /** The directory is on the host's PATH. */
  path_injected?: boolean
  note?: string
}

export interface RuntimeCommand {
  name: string
  description?: string
  usage?: string
}

export interface RuntimeToolsResponse {
  state: 'connected' | 'unavailable'
  error?: string
  agent_id?: string
  provider?: string
  model?: string
  model_error?: string
  commands: RuntimeCommand[]
  checked_at: string
}

export interface Interjection {
  at: string
  text: string
  ref_fact?: string
  interrupt?: boolean
  message_id?: string
}

/**
 * A retry/steer remark about a direction. It is commentary for the operator and
 * the next decision, not a constraint on the executor: the dispatch brief never
 * reads it (D72).
 */
export interface IntentNote {
  at: string
  kind: 'retry' | 'steer' | string
  text: string
  ref_fact?: string
}

export interface Intent {
  /**
   * Explicit reads the direction selected (references), distinct from the hard
   * prerequisites in from_fact_ids: a reference is context, not a gate.
   */
  context_fact_ids?: string[]
  /** Pinned reads: context the operator/commander wants first in the focus view. */
  pinned_fact_ids?: string[]
  /** Recorded entity identities this direction watches (e.g. "host:10.0.0.5"). */
  interest_entity_refs?: string[]
  /** Optimistic-concurrency token of the context selection; PUT /context carries it. */
  context_version?: number
  id: string
  title: string
  description: string
  doc?: string
  from_fact_ids?: string[]
  skills?: string[]
  node?: string
  /** Saved model configuration; empty means the instance default. */
  profile?: string
  parent_intent_id?: string
  created_by_participant_id?: string
  delegated_by_run_id?: string
  /** Acceptance criterion handed to the harness's own goal evaluator (-e). */
  eval?: string
  max_turns?: number
  handoff_version?: number
  status: IntentStatus
  attempts: number
  steer_count?: number
  say_count?: number
  interjections?: Interjection[]
  notes?: IntentNote[]
  claimed_by?: string
  park_reason?: string
  /** Premises that were unusable when the direction was parked. When every one
   *  becomes usable the console shows "前提已就绪" and the commander gets a
   *  parked_premise_ready attention instead of trusting the old prose. */
  parked_premise_fact_ids?: string[]
  /** Claim versions of those premises at park time. */
  parked_premise_claims?: Record<string, number>
  parked_at?: string
  run_id?: string
  /** Operator/commander stop: an auto-dispatch must not resurrect it (D19). */
  hold?: boolean
  /** Who set the current hold: operator / commander / watchdog / a parent
   *  participant id. Empty on legacy rows. */
  held_by?: string
  /** The stop reason recorded with held_by (audit context, not an instruction). */
  hold_reason?: string
  /** No-premise harness direction after facts exist: operator must confirm the
   *  first touch; a session cannot retry/run it itself. */
  bootstrap?: boolean
  /** Facts still unresolved from the last fixed-review phase. */
  review_failed_facts?: string[]
  /** Subtree cancelled: a child settles as failed so a parent wait cannot hang. */
  aborted?: boolean
  /** Business state while a direction yields for delegated children. */
  wait?: IntentWait
  /** The person or commander that declared this direction done on recorded
   *  evidence instead of a run closing it (`commander` / `operator`). Empty means
   *  the completion came from a run, so the two are never read as the same thing. */
  done_declared_by?: string
  /** The settler's own words, shown on hover next to the badge. */
  done_note?: string
  /** Facts cited as proof of the declared completion. */
  done_fact_ids?: string[]
  /** Runs cited as proof of the declared completion. */
  done_run_ids?: string[]
  done_at?: string
  created_at: string
  updated_at: string
}

/** The persisted condition a direction yielded on (model.Wait). */
export interface IntentWait {  actions?: string[]
  operator?: OperatorRequest['request']
  children: string[]
  /** all: every child settled; any: the first settled child. */
  mode?: string
  deadline?: string
  registered_at?: string
  registered_by_run?: string
  implicit?: boolean
  wake_pending?: boolean
  wake_reason?: string
}

/** PUT /api/intents/{id}/context: the whole selection plus the version the
 *  editor displayed. The server refuses a stale version (409) and a foreign
 *  participant (403) instead of overwriting another edit. */
export interface IntentContextRequest {
  version: number
  references: string[]
  pins: string[]
  entities: string[]
}

/** The context PUT answers with the updated direction (context_version +1). */
export type IntentContextResponse = Intent

// Ref is a citation the ledger kept but cannot open: a url, a tool artifact, or
// a file the run named that is gone. It is provenance, not proof (D44).
export interface Ref {
  scheme: 'file' | 'url' | 'artifact' | string
  locator: string
}

export interface Fact {
  /** Stable declaration identity from the writer (dedupes re-imports/new observations). */
  declaration_id?: string
  /** Assertion version: +1 whenever the claim content changes; a review proves one version. */
  claim_version?: number
  /** Fingerprint of the claim content the version belongs to. */
  claim_hash?: string
  /** When the observation happened (not when it was recorded). */
  observed_at?: string
  /** Execution-site reference the observation came from (context, not a hash). */
  execution_context_ref?: string
  id: string
  intent_id: string
  kind: string // host|service|cred|vuln|loot|artifact|note
  title: string
  summary: string
  doc?: string
  confidence: string // observed|inferred
  /** Operator-facing triage label: critical|high|medium|low, absent = unlabelled. It is a
   *  display tag like the title, not claim content: it stays out of claim_hash,
   *  so changing it never bumps claim_version or re-opens a review. The console
   *  only shows it and filters on it; it never derives or re-ranks it. */
  severity?: string
  origin?: string // declared|derived|heuristic
  /** Stable identity when the fact was translated from a known source (audit finding). */
  source_ref?: string
  /** Recorded entity identity this observation is about (e.g. "host:10.0.0.5"). */
  entity_ref?: string
  /** Optional reuse labels (playbook recall); the graph projection clears them. */
  tags?: string[]
  proof?: string // dynamic|static
  evidence_ids?: string[]
  refs?: Ref[]
  status: FactStatus
  /** Withdrawal audit: who retracted this assertion, when, and why. */
  retracted_at?: string
  retracted_by?: string
  retracted_run_id?: string
  retraction_reason?: string
  retracted_claim_version?: number
  created_at: string
}

export interface Evidence {
  id: string
  run_id: string
  intent_id: string
  name: string
  mime?: string
  size: number
  uri: string
  created_at: string
}

/** Project knowledge, not a finding: a reusable tactic (D70).
 *
 * Two writers, two tiers: an executor declares one in the run that used it
 * (`confirmed`), the commander can only nominate a candidate. Neither is a
 * fact: no evidence, no review, no report.
 */
export type PlaybookStatus = 'confirmed' | 'candidate' | 'retired' | string
export type PlaybookOutcome = 'success' | 'dead_end' | string
export type PlaybookOrigin = 'executor' | 'commander' | string

export interface Playbook {
	archived?: boolean
	source_run_id?: string
	from_fact_versions?: Record<string,number>
	pattern?: {nodes: {name:string;type:'fact'|'intent';kind?:string;tags?:string[];entity_var?:string}[];edges:{source:string;target:string;kind:string}[]}
  id: string
  title: string
  when: string
  doc?: string
  tags?: string[]
  outcome?: PlaybookOutcome
  origin: PlaybookOrigin
  status: PlaybookStatus
  /** Provenance for a commander nomination; executor entries carry intent_id. */
  from_fact_ids?: string[]
  intent_id?: string
  created_at: string
  updated_at: string
}

export interface PlaybooksResponse {
  playbooks: Playbook[]
  count: number
}

export interface Goal {
  closed?: boolean
  closed_at?: string
  closed_reason?: string
  instruction_ref?: string
  id: string
  statement: string
  /** Operator-defined starting point for this goal (what is known now). Empty
   *  on the goal created with a project: the project brief is the start there. */
  start?: string
  met: boolean
  evidence_id?: string
  /** One line written at seal time: why the commander judged it met. */
  note?: string
  /** Facts the seal cited (f_ids). Provenance, not proof: the evidence is separate. */
  from?: string[]
  /** When the seal was made (RFC3339); absent on goals sealed before this field. */
  completed_at?: string
  /** Read-only, computed by the server: a completed goal's cited facts changed
   *  after the stamp. The historical receipt is intentionally kept as-is, so the
   *  console shows this warning instead of rewriting `met`. */
  receipt_stale?: boolean
  /** Read-only: the cited fact ids whose claim changed after the stamp. */
  receipt_changed?: string[]
  quantified_target?: string
  created_at: string
  /** Read-only: the revision an edit must carry back. Without it the server
   *  answers 409, so a save cannot silently overwrite someone else's edit. */
  revision?: number
}

/**
 * The machine-readable project state the explicit "append a goal and continue"
 * contract switches on (internal/api/goal_continue.go). The console shows copy
 * from these codes, not from the human-readable pause reason: a completion
 * stand-by, a manual pause and the commander's stop request are resumable by
 * that explicit action, a provider fault is refused until a human repairs the
 * key/profile, and waiting work is not a pause at all.
 */
export type PauseKind =
  | 'running'
  | 'waiting_human'
  | 'completion_standby'
  | 'manual_pause'
  | 'commander_standby'
  | 'restart_pause'
  | 'unrecorded'
  | 'provider_fault'
  | string

/** POST /p/<project>/api/project/append-goal body. The idempotency key is
 *  required: a retry after a lost response or a failed resume must find the goal
 *  it already created instead of appending a second one. */
export interface AppendGoalRequest {
  statement: string
  /** Operator-defined starting point for the appended goal. */
  start?: string
  /** Legacy fields kept for older servers; the console no longer fills them. */
  quantified_target?: string
  note?: string
  idempotency_key: string
}

/** 200 answer of the append-goal action. `created=false` means this key already
 *  named a goal (a retry, not a duplicate); `resumed` is the explicit pause this
 *  request cleared; `note` is the server's human-readable receipt. */
export interface AppendGoalResult {
  goal: Goal
  created: boolean
  resumed: boolean
  resumed_from?: PauseKind | ''
  decider_woken: boolean
  decider_paused?: boolean
  paused_before: boolean
  pause_kind_before: PauseKind
  paused_after: boolean
  pause_kind_after: PauseKind
  pause_reason?: string
  retryable: boolean
  note?: string
}

/** Extra structured fields the append-goal endpoint attaches to its 409/500
 *  answers (ApiError.body). `needs_human` means the operator must repair the
 *  key/profile before retrying; `retryable` means the goal already exists and a
 *  retry with the same idempotency key completes the resume. */
export interface AppendGoalErrorBody {
  goal?: Goal
  code?: string
  needs_human?: boolean
  retryable?: boolean
  pause_kind?: PauseKind
  pause_kind_before?: PauseKind
  pause_kind_after?: PauseKind
  pause_reason?: string
}

export interface Review {
  /** The assertion version this verdict actually proved (0 = legacy unbound row). */
  claim_version?: number
  /** Fingerprint of the assertion the verifier proved. */
  claim_hash?: string
  id: string
  fact_id: string
  verdict: 'pass' | 'needs_evidence' | 'reject'
  notes?: string
  verifier_run?: string
  created_at: string
}

/** Provider-reported token accounting. Fields absent/zero are not a measured 0
 *  unless `known` is true. */
export interface TokenUsage {
  input_tokens?: number
  output_tokens?: number
  cached_input_tokens?: number
  cache_write_tokens?: number
  total_tokens?: number
  requests?: number
  missing?: number
  known: boolean
}

export interface TokenTotals {
  total_tokens: number
  hour_tokens: number
  input_tokens: number
  output_tokens: number
  cached_input_tokens: number
  cache_write_tokens: number
  requests: number
  missing_usage: number
  unknown_usage_runs: number
}

export interface ExternalAction {
  id: string
  intent_id: string
  run_id: string
  generation: number
  idempotency_key: string
  name: string
  reference?: string
  state: 'started' | 'completed' | 'unknown'
  version: number
  result?: string
  result_ref?: string
  resolved_by_run?: string
  created_at: string
  updated_at: string
}


export interface UsageSummary {
  runs: number
  input_tokens: number
  output_tokens: number
  cached_input_tokens: number
  cache_write_tokens: number
  total_tokens: number
  requests: number
  missing_usage: number
  unknown_usage_runs: number
}

/** One side of the project token accounting (model-reported usage only). */
export interface UsageBucket {
  runs?: number
  /** Judge buckets count calls, not runs. */
  calls?: number
  input?: number
  output?: number
  total?: number
}

/** The pluggable guardrail judge's usage (JEV and LLM are kept apart). */
export interface JudgeUsageBreakdown {
  jev?: UsageBucket
  llm?: UsageBucket
  total?: UsageBucket
}

/**
 * GET /api/usage/breakdown: project-level token accounting.
 *
 * Buckets: commander; executor (+ support = executor_total); judge (JEV / LLM
 * separate); combined = commander + executor_total (no judge); all = combined +
 * judge. Every number is model-reported — an absent bucket/field is unknown, not
 * zero, and the console must render it as "—". `sources` optionally explains
 * which journal/ledger inputs were available.
 */
export interface UsageBreakdown {
  commander?: UsageBucket
  executor?: UsageBucket
  support?: UsageBucket
  /** Executor + review/report support runs. */
  executor_total?: UsageBucket
  judge?: JudgeUsageBreakdown
  combined?: UsageBucket
  all?: UsageBucket
  sources?: Record<string, unknown>
}

export interface NoteRef {
  version: number
  sha256: string
  writer: string
  run_id?: string
  round?: number
}

export interface Notebook {
  state: string
  path: string
  version: number
  receipt: NoteRef
  fact_versions?: Record<string, number>
  fact_claim_versions?: Record<string, number>
  stale_fact_refs?: string[]
  written_at?: string
  run_id?: string
}

export interface Run {
  native_subagent_calls?: number
  native_subagent_active?: number
  native_subagent_duration_ms?: number
  commander_round?: number
  note_read?: NoteRef
  note_writes?: NoteRef[]
  queued_at?: string
  execution_started_at?: string
  queue_ms?: number
  execution_ms?: number
  /** Frozen at dispatch: premise fact id → assertion version the run was based on. */
  premise_claim_versions?: Record<string, number>
  /** Frozen at dispatch: referenced/pinned fact id → ledger record revision it saw. */
  context_fact_versions?: Record<string, number>
  /** Execution-site resource references in play for this run (phase 4 projection). */
  resource_refs?: string[]
  session_id?: string
  tool_failures?: number
  id: string
  intent_id: string
  node: string
  purpose?: string
  /** Requested model configuration (empty = instance default). */
  profile?: string
  /** The saved profile that actually ran, and the binding fingerprint at dispatch. */
  resolved_profile?: string
  profile_source?: 'explicit' | 'default' | 'project_worker' | 'project_strong' | string
  provider?: string
  /** Actual model: the harness's resolved value when it reported one. */
  model?: string
  usage?: TokenUsage
  /** Non-fatal problems the run recorded (evidence paths, native subagent, stalls). */
  warnings?: string[]
  /** Non-problem handoff notes (today: expected in-batch corrections). They are
   *  deliberately not counted as warnings by the board or the next prompt. */
  infos?: string[]
  /** The operator ignored this run's non-fatal handoff warnings; the rows stay
   *  in the ledger but the status page and board stop counting them. */
  warnings_dismissed?: boolean
  /** Read-only path of the archived task brief, relative to the case root. */
  brief_path?: string
  /** The run this one continued (steer/handoff/wake), if any. */
  continued_from?: string
  handoff_from?: string
  handoff_version?: number
  generation?: number
  status: RunStatus
  started_at: string
  /** When the current/last turn began (a resume continues the same run). */
  last_turn_started_at?: string
  ended_at?: string
  exit_reason?: string
  /** Structured reason for a canceled run: steer/retry/stop_loss/restart/watchdog. */
  cancel_cause?: string
  /** How the run died, recorded by the side that saw it die: transport (the link
   *  broke), intentional (the host closed on purpose), unknown (no reason). The
   *  console reads it; it never re-derives the class from error text. */
  failure_class?: string
  /** Transport self-heal restarts of this direction in the current episode
   *  (0 = none). Shown as "网络中断，自动重跑第 N 次（上限 3）". */
  auto_resume_attempt?: number
  events: number
  declared_facts: number
  stop_reason?: string
  tokens?: number
  log_path?: string
  /** Peak context pressure of the run (running = live). */
  context_tokens?: number
  context_window?: number
  last_state?: string
}

/** One native command the run's harness session exposes (aop.ListCommands). */
export interface RunCommand {
  name: string
  aliases?: string[]
  usage?: string
  description?: string
}

/** GET /api/runs/{id}/commands: the session's command menu, read-only. */
export interface RunCommandsResponse {
  run_id: string
  session_id: string
  commands: RunCommand[]
  source: string
}

/** One host canonical event: the harness proto JSON stays raw on purpose. */
export interface NativeSessionEvent {
  cursor: string
  event: unknown
}

/** GET /api/runs/{id}/history (aop.ListEvents page). */
export interface RunHistoryPage {
  run_id: string
  session_id: string
  events: NativeSessionEvent[]
  next_cursor?: string
  has_more: boolean
  source: string
}

/**
 * What a retry would do, before it is committed (api retry-preview).
 *
 * Resume continues the harness session recorded by the failed run (memory kept);
 * a new session discards it. Both re-inject unconfirmed messages: resume only
 * the ones the failed run had already delivered, a new session every unconfirmed
 * message of the intent.
 */
export interface RetryPreview {
  intent_id: string
  status: string
  can_resume: boolean
  /** Why resume is unavailable (no session, session gone, wrong status, …). */
  resume_error?: string
  resume_session?: string
  resume_redeliver: number
  new_session_redeliver: number
}

/** Result of POST /p/<p>/api/intents/{id}/retry (202). */
export interface RetryResult {
  intent_id: string
  run_id: string
  resume: boolean
  redelivered: number
}

/** One participant-to-participant message (not a fact: it never enters the graph). */
export type MessageStatus = 'queued' | 'sending' | 'accepted' | 'confirmed' | 'unknown' | string

/** One executor recipient of an operator chat turn: a ledger row with its
 *  delivery state, or a skipped direction with the reason. */
export interface ChatItem {
  intent_id: string
  participant_id: string
  title?: string
  status?: string
  message_id?: string
  reason?: string
}

export type ChatTarget = 'commander' | 'all'

export interface Instruction {
  id: string
  intent_id?: string
  text: string
  version: number
  state: 'active' | 'revoked'
  created_by: string
  source_message_id?: string
  created_at: string
  updated_at: string
}

/** GET /api/instructions: operator rows (the console reads with state=all). */
export interface InstructionPage {
  instructions: Instruction[]
  total?: number
  has_more?: boolean
  next_cursor?: string
}

/** One ledger change of an instruction; `value` is the row at that version. */
export interface InstructionHistoryChange {
  seq: number
  operation?: string
  version: number
  at?: string
  fields?: string[]
  value?: Instruction
}

/** GET /api/instructions/<id>/history */
export interface InstructionHistoryResponse {
  history: InstructionHistoryChange[]
  total?: number
  has_more?: boolean
  next_cursor?: string
}

/** POST /api/chat: one operator sentence into the project conversation. */
export interface ChatResult {
  to: ChatTarget
  commander: { message_id: string; status: string }
  delivered: ChatItem[]
  skipped: ChatItem[]
}

/**
 * The one operator "下发" entry. The console sends a durable message only:
 * to the commander (`target: 'commander'`) or to one direction
 * (`target: 'intent'` + `intent_id`). Persistent instructions are composed by
 * the dedicated 操作员指令 surface (same endpoint, persistent=true), never by the
 * composer: a hint typed into chat must not silently become a standing rule.
 */
export type DirectiveTarget = 'commander' | 'project' | 'intent' | 'executors'
export type DirectiveKind = 'message' | 'instruction' | 'steer' | 'broadcast'

/** POST /api/directives. Invalid combinations are 400 `directive_invalid`:
 *  persistent+commander, one-shot+project, empty/overlong text; unknown intent
 *  is 404; a finished one-shot direction or a non-running steer is 409;
 *  a full recipient queue is 429 `message_backpressure`. */
export interface DirectiveRequest {
  text: string
  target: DirectiveTarget
  intent_id?: string
  persistent: boolean
  /** Only meaningful for one-shot intent targets; the server 409s if it is not running. */
  steer: boolean

  idempotency_key: string
}

export interface DirectiveResponse {
  batch_id?: string
  total?: number
  failed?: number
  recipients?: {intent_id: string; message?: Message; created: boolean; error?: string}[]
  kind: DirectiveKind
  target: DirectiveTarget
  persistent: boolean
  /** false when the idempotency key already named this directive (retry, no duplicate). */
  created: boolean
  intent_id?: string
  /** Present for kind=message: the durable row's delivery state. */
  delivery?: MessageStatus
  message?: Message
  /** Present for kind=instruction: the created/revived instruction row. */
  instruction?: Instruction
  /** Present for kind=steer: the new run continuing the same trajectory. */
  run_id?: string
  /** Present when the server turned a direction message into an escalation the
   *  commander has to answer. Absent means the commander was not told. */
  escalation?: { id: string; intent_id?: string }
}

export interface Message {
  /** Mechanical fact-change notice: which fact and record version changed. */
  notice_fact_id?: string
  notice_version?: number
  /** Mechanical local-result notice: task and run to inspect, not new premises. */
  notice_intent_id?: string
  notice_run_id?: string
  /** How many later changes to the same fact this queued notice absorbed. */
  coalesced?: number
  id: string
  seq: number
  project_id: string
  from_participant_id: string
  from_run_id?: string
  from_generation?: number
  to_participant_id: string
  to_generation?: number
  /** Set on an instruction-change notice: which row, which revision. */
  instruction_id?: string
  instruction_version?: number
  kind?: string
  text: string
  fact_refs?: string[]
  in_reply_to?: string
  idempotency_key?: string
  status: MessageStatus
  delivery_id?: string
  /** The run the message was delivered to, when delivery got that far. */
  delivered_run_id?: string
  /** How it reached the peer: the dispatch brief or the live-run IOA bridge. */
  delivery_path?: 'prompt' | 'ioa' | string
  /** How many times a retry re-injected this unconfirmed message. */
  redeliveries?: number
  error?: string
  created_at: string
  updated_at: string
  acked_at?: string
}

/** One recipient's queue pressure (GET /api/communication; store.QueueStats).
 *  Cursor is a journal sequence the router advanced to, not a read receipt. */
export interface CommunicationQueue {
  recipient: string
  pending: number
  bytes: number
  /** Oldest queued/in-flight row, when the queue is not empty. */
  oldest?: string
  /** A change was deferred because this queue was full; it is retried, not lost. */
  deferred: boolean
  cursor: number
}

/** GET /api/communication: per-recipient backlog plus the declared bounds. */
export interface CommunicationStatus {
  queues: CommunicationQueue[]
  max_messages: number
  max_bytes: number
  /** The project switch: notices persist while paused but are not delivered. */
  paused?: boolean
}

/** A delegated child direction as the parent's panel shows it. */
export interface ChildIntent {
  id: string
  parent_intent_id: string
  title: string
  description?: string
  status: IntentStatus
  profile?: string
  node?: string
  attempts: number
  run_id?: string
  last_run_status?: string
  stop_reason?: string
  /** Structured stop cause of the latest run (stop_loss/operator/pause/…). */
  cancel_cause?: string
  /** The child direction was explicitly stopped (sticky hold), not failed. */
  hold?: boolean
  facts: number
  created_at: string
}

/** Body of POST /api/intents/{id}/children. The console generates the
 *  idempotency key: retrying with the same key finds the same child instead of
 *  creating a duplicate. Empty profile = the same default chain as any intent. */
export interface CreateChildIntentBody {
	node?: string
  title: string
  description?: string
  eval?: string
  profile?: string
  from_fact_ids?: string[]
  max_turns?: number
  idempotency_key: string
}

/** Result of POST /api/intents/{id}/children. created=false means the key
 *  already named a child; started=false with queued set means capacity was full
 *  and the child waits for a free run slot. */
export interface CreateChildIntentResult {
  child: ChildIntent & { run_id?: string }
  created: boolean
  started: boolean
  queued?: string
}

/** Result of POST /api/intents/{id}/continue (202): the next turn on the
 *  previous run's harness session. */
export interface ContinueIntentResult {
  intent_id: string
  run_id: string
  resumed: boolean
  continued_from: string
}

export interface Participant {
  id: string
  kind: 'intent' | 'commander' | string
  intent_id?: string
  title: string
  status?: string
  generation?: number
  run_id?: string
  profile?: string
  model?: string
  contactable: boolean
  note?: string
}

export interface Event {
	event_id?: string
	turn_id?: string
  session_id?: string
  kind: string
  at: string
  tool?: string
  role?: string
  text?: string
  ok?: boolean
  stop_reason?: string
  tokens?: number
  raw?: unknown
  /** Present on tool_result when the harness reported the call identity. */
  call_id?: string
  /** Tool execution time as the harness measured it; absent means not reported. */
  duration_ms?: number
  /** Harness status payload (compaction, context budget, …). */
  state?: string
  input_tokens?: number
  output_tokens?: number
  cached_input_tokens?: number
  cache_write_tokens?: number
  usage_requests?: number
  usage_missing?: number
  context_tokens?: number
  context_window?: number
  /** Guardrail event fields (kind guardrail_decision / guardrail_review). */
  guardrail_action?: 'record' | 'review' | 'block' | string
  guardrail_reason?: string
  guardrail_tool?: string
  guardrail_state?: 'pending' | 'approved' | 'rejected' | 'expired' | 'canceled' | string
  guardrail_operation_id?: string
  guardrail_call_id?: string
}

export interface IOARecord {
  seq: number
  ioa_message_id: string
  /** IOA node id that produced the record; an addressing fingerprint, not a participant. */
  sender?: string
  created_at?: string
  observed_at: string
  error?: string
  content?: { text?: string; [key: string]: unknown }
  envelope?: {
    id?: string
    event: string
    source?: { session_id?: string; task_id?: string; node_id?: string }
    target?: { session_id?: string; task_id?: string; node_id?: string }
    interrupt?: boolean
    chain?: string[]
    agent?: { name?: string; type?: string; mode?: string; model?: string }
    status?: string
    call_id?: string
  }
}

export interface IOAObservations {
  records: IOARecord[]
  sessions?: Record<string, string>
  cursor?: string
}

export interface BoardItem {
  id: string
  title: string
  summary?: string
  status?: string
  kind?: string
  origin?: string
  /** Stable external identity; historical "ioa:" records describe lifecycle observations. */
  source_ref?: string
  evidence?: number
  attempts?: number
  intent_id?: string
  created_at: string
  /** Live pulse for running intents (D57). */
  pulse?: RunPulse
}

export interface RunPulse {
  tool_failures?: number
  run: string
  started_at: string
  last_event_at?: string
  idle_seconds: number
  events: number
  declared_facts?: number
  /** Last model call's context size; the live pressure bar's numerator. */
  context_tokens?: number
  /** Configured context budget at run start. */
  context_window?: number
  /** Harness status payload (compaction / context pressure). */
  state?: string
  /** Most recent meaningful event: what the run is doing, not merely "alive". */
  last_kind?: string
  last_tool?: string
  last_text?: string
}

export interface BoardView {
  generated_at: string
  case?: string
  intents: {
    total: number
    pending: number
    /** Subset of pending the operator/commander stopped (D19). */
    held?: number
    claimed: number
    running: number
    /** Directions whose turn yielded for child intents. */
    waiting?: number
    /** Successful runs whose fixed review phase is still running. */
    reviewing?: number
    done: number
    failed: number
    /** Directions whose fixed review rejected at least one conclusion. */
    review_failed?: number
    parked: number
  }
  facts: {
    reviewable_unverified?: number
    notes?: number
    total: number
    with_evidence: number
    with_refs?: number
    verified: number
    by_kind: Record<string, number>
    by_origin: Record<string, number>
  }
  evidence: number
  runs: { total: number; running: number; done: number; failed: number; canceled: number }
  /** A closed goal is neither open nor met (withdrawn by the operator). */
  goals: { total: number; met: number; closed?: number; needs_review?: GoalItem[]; open: GoalItem[] }
  /** Knowledge-table size, never its text: a playbook is not a finding (D70). */
  playbooks?: { total: number; confirmed: number; candidates: number; retired?: number;archived?:number }
  open_intents: BoardItem[]
  waiting_intents?: BoardItem[]
  reviewing_intents?: BoardItem[]
  review_failed_intents?: BoardItem[]
  running_intents: BoardItem[]
  recent_facts: BoardItem[]
  done_intents: string[]
  /** Graph-as-memory: whether each open direction's premise still stands. */
  frontier?: FrontierItem[]
  /** Factual port aggregate: hosts that hold a service fact on a port. */
  ports?: PortGroup[]
  /** Known-host map: what the ledger has actually seen, not the scope text. */
  coverage?: CoverageItem[]
  /** Directions the operator/commander explicitly stopped. */
  held_intents?: BoardItem[]
}

export interface CoverageItem {
  host: string
  services?: number
  last_seen: string
  last_intent?: string
  open?: boolean
  running?: boolean
}

export interface FrontierItem {
  id: string
  title: string
  status?: string
  ready: boolean
  blocked_on?: string[]
  last_stop?: string
  last_declared?: number
  /** Number of hard premises (from_fact_ids) the direction cites. */
  prerequisites?: number
  /** True when the direction has no hard premise: it can start without a prior
   *  assertion. A reference/pin is not a premise and does not clear this. */
  no_premise?: boolean
  /** Premise fact ids whose claim version changed after this direction was
   *  created. An old conclusion cannot stand in for the new assertion. */
  premise_changed?: string[]
  /** Open descendant directions below this one (the parent may be waiting). */
  descendants?: number
}

export interface OperatorFrontierItem extends FrontierItem {
  held?: boolean
  premise_issues?: { fact_id: string; reason: string }[]
  /** Premises whose original evidence is missing or damaged. Advisory only:
   *  the direction can still dispatch. */
  no_evidence_facts?: string[]
  omitted_premise_issues?: number
}

export interface GoalItem {
  id: string
  statement: string
  start?: string
  quantified_target?: string
}

export interface PortGroup {
  port: string
  hosts: string[]
  members: string[]
}

export interface DeciderStatus {
  context_tokens?: number
  context_window?: number
  context_percent?: number
  compacted?: boolean
  handoff_missing?: number
  handoff_released?: boolean
  no_action_rounds?: number
  enabled: boolean
  model?: string
  rounds: number
  tokens_total: number
  stalled: number
  last_round_at?: string
  last_wake?: string
  last_digest?: string
  last_error?: string
  last_fallback?: string
  last_actions?: DecisionAction[]
  last_notes?: string[]
  last_attention?: AttentionReason[]
  pending_attention?: AttentionReason[]
  attention_delivered?: boolean
  /** Live phase of the round in flight; present while idle too. */
  activity?: DeciderActivity
  /** Idle heartbeat cadence, for the live card's countdown copy. */
  heartbeat_seconds?: number
  /** Operator kill switch: no model calls while true. */
  paused?: boolean
  /** Why it is paused (operator, provider failure, completed engagement, restart). */
  paused_reason?: string
  /** Seconds since the decider loop started (unattended runs). */
  uptime_seconds?: number
  /** Tokens spent by dispatched runs in the last hour (recorded, not capped). */
  run_tokens_last_hour?: number
  /** Long-session cache size and how many old rounds were dropped (D56). */
  session_rounds?: number
  session_dropped?: number
}

/** What the commander is doing right now (internal/decider Activity). */
export interface DeciderActivity {
  /** idle | debounce | board | model | tool | apply | paused | blocked */
  phase: string
  round?: number
  /** The wake that started this round: heartbeat / intent_done / operator … */
  wake?: string
  detail?: string
  tool?: string
  /** When the current phase began; the card derives its bar from this. */
  since?: string
}

export interface BoardResponse extends BoardView {
  operator_frontier?: OperatorFrontierItem[]
  reviews?: { automatic: boolean; paused: boolean; disabled?: boolean; pending: number; processing: number; cooling: number }
  usage?: TokenTotals
  decider: DeciderStatus
  project_paused?: boolean
  /** Who parked the project, as the server's machine code (PauseKind); only set
   *  while paused. A commander stop must not be inferred from the reason prose. */
  project_pause_kind?: PauseKind
  /** Board fingerprint for observation; durable attention controls model wakes. */
  digest?: string
  /** The watchdog's cadence and last sweep: observation, not policy (api.Watch). */
  guard?: {
    interval_seconds: number
    idle_timeout_seconds?: number
    last_tick_at?: string
  }

  /** Re-dispatch beyond a direction's first attempt (retry/wake/steer/handoff). */
  redispatch?: RedispatchStats
  /** Latest runs' non-fatal problems, newest first (bounded by the server). */
  handoff_warnings?: HandoffWarning[]
  /** The operator's action contract (internal/api/project_actions.go): what
   *  pause/resume/append-goal would do right now and why. The console renders
   *  the buttons from this instead of deriving 「已收口」 from paused + goals. */
  project_actions?: ProjectActions
}

/** One project-level action and the machine code saying why it is (not) available. */
export interface ProjectAction {
  id: 'pause' | 'resume' | 'append_goal' | string
  enabled: boolean
  /** Stable code; copy may change, the code may not (`ok`, `nothing_to_continue`,
   *  `closeout_in_progress`, `paused_manual`, `paused_completion`, …). */
  reason_code: string
  /** One sentence for the operator, written by the backend. */
  reason: string
}

/** The counters the action decisions were read from — for explaining, not deciding. */
export interface ProjectActionFacts {
  has_ledger: boolean
  goals_total: number
  goals_met: number
  goals_open: number
  goals_closed?: number
  goals_active: number
  goals_stale_receipt?: number
  pending_directions: number
  held_directions?: number
  parked_directions?: number
  running_directions?: number
  unread_messages?: number
  closeout?: boolean
  deleting?: boolean
}

/** A two-sided truth ("all goals met" AND "three directions still open"). */
export interface ProjectNote {
  code: string
  text: string
}

export interface ProjectActions {
  state: { paused: boolean; kind: string; reason?: string }
  facts: ProjectActionFacts
  actions: ProjectAction[]
  notes?: ProjectNote[]
}



export interface RedispatchStats {
  total: number
  /** Directions that needed at least one re-dispatch. */
  directions: number
}

export interface HandoffWarning {
  run: string
  intent_id?: string
  warnings: string[]
}

export interface DecisionAction {
  kind: string // intent|steer|say|complete|noop|error
  id?: string
  text?: string
}

/** One read the decider made before acting (D54). */
export interface DecisionTool {
  tool: string
  args?: string
  ms?: number
  error?: string
}

export interface Decision {
  context_tokens?: number
  context_window?: number
  context_percent?: number
  compacted?: boolean
  handoff?: boolean
  handoff_missing?: number
  handoff_released?: boolean
  no_action_rounds?: number
  new_facts?: number
  run_id?: string
  note_read?: NoteRef
  handoff_receipt?: NoteRef
  at: string
  round: number
  wake: string
  board_digest: string
  model?: string
  ms: number
  tokens: number
  fallback?: string
  error?: string
  actions?: DecisionAction[]
  tools?: DecisionTool[]
  /** Long-session cache size when this round was decided (D56). */
  session_rounds?: number
  session_dropped?: number
  notes?: string[]
  attention?: AttentionReason[]
  attention_through?: number
  attention_delivered?: boolean
}

export interface AttentionReason {
  code: string
  kind: string
  id: string
  intent_id?: string
  seq?: number
  version?: number
  detail: string
}

export interface Poc {
  id: string
  name?: string
  severity?: string
  tags?: string
  file: string
  size: number
}

/** The latest verifier verdict on a mirrored vuln row. The library keeps only
 *  the newest review per entry (the full history stays on the source fact), so
 *  a row can explain why it did not pass without a second request. */
export interface VulnReview {
  verdict: 'pass' | 'needs_evidence' | 'reject'
  notes?: string
  verifier_run?: string
  created_at: string
}

export interface VulnEntry {
  id: string
  project_id: string
  project?: string
  source_id: string
  intent_id?: string
  intent?: string
  title: string
  summary?: string
  doc?: string
  confidence?: string
  /** Copied from the source fact when the library entry was written (the library
   *  is a projection, not a second judge): critical|high|medium|low, absent =
   *  unlabelled. */
  severity?: string
  /** dynamic|static, mirrored from the source fact; absent falls back to the
   *  fact default (dynamic), same as the graph inspector reads it. */
  proof?: 'dynamic' | 'static'
  /** Latest review verdict; absent means never reviewed. */
  review?: VulnReview
  status: string
  evidence?: string[]
  evidence_dropped?: string[]
  created_at: string
  updated_at: string
  orphaned?: boolean
}

/**
 * The per-project review switches. `disabled` is the runtime close (--no-verify
 * at boot, or the operator's disable endpoint): no review run can start at all,
 * so no verdict will ever arrive. `paused` is the temporary pause — it refuses
 * runs for now and can be resumed. They are independent of the project pause
 * (D137).
 */
export interface VerifierStatus {
  /** False when reviews are closed: kept for older consoles, mirror `disabled`. */
  enabled: boolean
  paused: boolean
  /** True = reviews are closed at runtime; resuming is refused (409). */
  disabled: boolean
}

export interface ReportCheck {
  rule: string
  ok: boolean
  detail?: string
}

export interface ReportMeta {
  id: string
  /** What a reader must act on, not what was written when the report was
   *  generated: `stale` is recomputed on read (the ledger moved after the run),
   *  and only `ready` has a deliverable body. `draft` is still being written,
   *  `incomplete` finished but failed its own checks. */
  status: 'draft' | 'incomplete' | 'ready' | 'stale'
  created_at: string
  finished_at?: string
  source_digest: string
  findings: number
  run_id?: string
  checks?: ReportCheck[]
}

/**
 * One row of a report's `findings.json`. `severity` is copied from the fact's
 * current label when the report was written (the report may not grade higher
 * than the ledger); absent means the fact was ungraded. The console only reads
 * it to show the operator what the deliverable actually carries — it never
 * re-grades, re-ranks or fills a missing value.
 */
export interface ReportFinding {
  fact: string
  verdict: string
  title: string
  severity?: string
  location?: string
  preconditions?: string
  impact?: string
  trace?: string
  evidence?: string[]
}

/** Something the review queue accepted: how many were queued, how many were
 *  already being reviewed by a run in flight (not errors, just skipped). */
export interface VerifyPendingResult {
  queued: number
  skipped: number
}

/** One verified conclusion no delivered report cites yet (`unreported.items`). */
export interface ReportUnreportedFact {
  /** The fact id. The API names this key `fact`, not `id`. */
  fact: string
  title: string
}

/**
 * The precheck's second half. `unreadable` and `uncounted` are named report ids,
 * never zeroes: a report whose rows could not be read cannot prove that a fact
 * was *not* cited, and a report that failed its own checks is not a delivered
 * citation. Both therefore stay visible instead of being folded into a count.
 */
export interface ReportUnreported {
  /** Verified, reportable facts no report cites. */
  total: number
  /** How many rows the server will return at most. */
  limit: number
  items: ReportUnreportedFact[]
  /** The boundary note written where the figure is produced. */
  note?: string
  unreadable?: string[]
  uncounted?: string[]
}

/** GET /p/<p>/api/reports/precheck, read before a report run starts.
 *
 * `reportable` is what would land in findings; `unverified` counts conclusions
 * that can only appear as candidates (writing them confirmed fails validation);
 * `rejected` are excluded; `running` means a report is already being written.
 * `unreported` is optional because a server older than that field simply does
 * not answer it — absent means "not provided", never "0 facts". */
export interface ReportPrecheck {
  reportable: number
  unverified: number
  rejected: number
  /** Withdrawn conclusions: kept in the ledger for audit, never in a report. */
  withdrawn?: number
  running: boolean
  unreported?: ReportUnreported
}

/**
 * How hard a direction's hard premises (from_fact_ids) must be before the
 * control plane will create/dispatch it. There is exactly one mode:
 * every necessary premise must be a reviewable kind and be verified at its
 * current claim version, with a receipt bound to that version and intact
 * original evidence. Legacy standard/strict manifests are migrated on load.
 */
export type PremisePolicy = 'verified_only'

/** GET /api/premise-policy. Read-only: there is one mode, so the two fields
 *  always agree and nothing here is selectable. Kept as a separate read because
 *  the graph panel decides whether host/service facts need reviewed proof. */
export interface PremisePolicyResponse {
  premise_policy: PremisePolicy
  /** What admission actually does: the policy while review runs, or
   *  review_disabled once the project's review switch is off. */
  effective: PremisePolicy | 'review_disabled'
  review_disabled: boolean
}

export interface ProjectInfo {
  id: string
  name: string
  scope?: string
  description?: string
  created: string
  intents: number
  facts: number
  evidence: number
  running: number
  /** This project's own concurrency cap; null = inherit the instance default. */
  max_runs?: number | null
  /** The cap dispatch actually enforces (the override, or --max-runs when none). */
  max_runs_effective: number
  /** This project's own commander round wall clock ("15m"); empty = inherit the
   *  instance --round-timeout, "0" = no cap (D91). */
  round_timeout?: string
  /** The one hard-premise admission mode, reported for display. It is not a
   *  setting: legacy manifests are normalized to verified_only on load and the
   *  server rejects writes to it. */
  premise_policy?: PremisePolicy
  /** The review switch actually in force: the project's own override when it has
   *  one, else the instance default. A running project reports the switch it
   *  runs with, not the config value a later attach would pick up. */
  reviews_disabled?: boolean
  /** This project's own stored override. `null`/absent = no override, so the
   *  project follows the instance default (and keeps following it when that
   *  default changes); `true`/`false` pins it. The effective value alone cannot
   *  tell "follows the instance (currently closed)" from "overridden closed". */
  reviews_disabled_override?: boolean | null
}

/** Options accepted by project creation, applied before the first dispatch so the
 *  operator never has to "create → pause → edit settings → resume". Every key is
 *  optional: an absent key keeps the instance default for that project. */
export interface CreateProjectOptions {
  /** `true` starts the project with no review runs at all (live reviews cannot
   *  exist yet, so nothing is canceled); `false` opens them even when the
   *  instance booted with reviews off. */
  reviews_disabled?: boolean
  /** This project's own executor concurrency; 0 = unlimited, absent = inherit. */
  max_runs?: number
  /** Commander round wall clock ("15m", "0"); absent = inherit --round-timeout. */
  round_timeout?: string
}

/** A directory under the data root that did not load. The instance still serves
 *  every project that did load; these are listed so the operator can see which
 *  one is missing and why instead of silently losing it. The directory is never
 *  deleted or rewritten by the loader. */
export interface SkippedProject {
  id: string
  /** Absolute path of the directory that failed to load. */
  dir: string
  /** The loader's own error text, verbatim. */
  error: string
}

/** One vuln-library index that was set aside at boot because it could not be
 *  read as a valid index. The file is renamed (never truncated or deleted) and
 *  the instance keeps running with an empty library, so the operator has to learn
 *  about it from the UI, not from a log line. `null` means this boot
 *  quarantined nothing; an absent field means an older build answered, which
 *  must not be read as "nothing was quarantined". */
export interface VulnLibraryQuarantine {
  /** Absolute path the broken index was moved to (`…vulns.json.corrupt-<stamp>`). */
  path: string
  /** The parser's own error text, verbatim. */
  reason: string
  /** When the boot quarantined it (RFC 3339, instance clock). */
  quarantined_at: string
}

/** One whole-project transfer archive. Files carries a SHA-256 per entry; the
 *  console shows the summary, never the file list. */
export interface ProjectExportManifest {
  format: string
  version: number
  exported_at: string
  source_version?: string
  project: { id: string; name: string; created: string; schema_version: number }
  counts: { intents: number; facts: number; evidence: number; runs: number; files: number; bytes: number }
  files: { path: string; size: number; sha256: string }[]
  /** Evidence rows whose original was already gone when the package was made. */
  missing?: string[]
}

export interface ProjectExportResult {
  name: string
  /** Download path for the archive (attachment, console-authenticated). */
  url: string
  size: number
  manifest: ProjectExportManifest
}

export interface ProjectImportResult {
  project: ProjectInfo
  manifest: ProjectExportManifest
}

/** One MCP server declaration; wire shape of cyber-harness tools/mcp.ServerConfig. */
export interface MCPServerConfig {
  command?: string
  args?: string[]
  env?: Record<string, string>
  cwd?: string
  url?: string
  headers?: Record<string, string>
  /** nil/absent mounts every tool; an explicit empty list mounts none. */
  tools?: string[] | null
  startupTimeoutSeconds?: number
  timeoutSeconds?: number
}

/** GET /api/settings/check: one bounded connectivity probe. */
export interface SettingsCheck {
  probed?: boolean
  available: boolean
  error?: string
  returned_model?: string
  ms?: number
  models?: string[]
  /** "host" = measured by the live harness; "self" = the controller's own probe. */
  source?: 'host' | 'self' | string
}

/** GET /api/models: the endpoint's catalogue, read by the controller. */
export interface ModelsCatalog {
  models: string[]
  limits: Record<string, number>
  base_url: string
  provider?: string
  source?: string
}

/**
 * Read-only projection of `extensions.guardrail` in the operator's cyber.yaml.
 * The console never edits it (the operator writes the section directly), and it
 * never sees a key: only "configured" flags and the routing metadata.
 */
export interface SettingsGuardrailSummary {
  configured: boolean
  provider: string
  mode?: string
  /** What an unavailable/failed judge does: "allow" is the explicit fail-open. */
  on_error?: string
  /** Judge and record, always allow — grey rollout. */
  shadow?: boolean
  /** Tool names narrowed by the policy; empty = every tool. */
  scope?: string[]
  allow_prefixes_count?: number
  jev_configured?: boolean
  llm?: { configured?: boolean; model?: string; base_url?: string }
}

/** Instance-level LLM configuration; the key itself is never returned. */
export interface SettingsView {
  base_url: string
  api_key?: string
  api_key_set: boolean
  model: string
  provider?: string
  context_window?: number
  max_tokens?: number
  configured: boolean
  /** The harness profile file the form edits (~/.cyber/cyber.yaml). */
  path: string
  /** Pre-harness settings.json, kept in sync for older binaries. */
  legacy_path?: string
  /** Set when the file exists but cannot be parsed. */
  harness_error?: string
  /** Product sections the local scan reads from extensions (masked on read). */
  mcp_servers?: Record<string, MCPServerConfig>
  search_has_key?: boolean
  search_key_count?: number
  /** The stored product section exists but cannot be parsed. */
  product_error?: string
  /** Write-only: never persisted or echoed. Empty keeps the stored key. */
  search_tavily_key?: string
  /** Write-only: remove the stored Tavily keys. */
  clear_search_key?: boolean
  /** Response-only save outcome: "online" | "restart" | "saved". */
  applied?: string
  requires_restart?: boolean
  restart_reason?: string
  /**
   * The scan node's capture-directory retention cap in bytes, shared by every
   * project on that node (traffic.body_retention_bytes). 0/absent means the
   * generated default (2 GiB) applies; this is NOT a per-project body quota.
   */
  traffic_body_retention_bytes?: number
  /** Read-only judge summary (absent on servers that predate the projection). */
  guardrail?: SettingsGuardrailSummary
}

/** Instance-level knobs that live beside the model profile: the auxiliary gate
 *  reviews and report runs share, and the instance default for the review switch. */
export interface RuntimeSettings {
  auxiliary_concurrency: number
  max_auxiliary_concurrency: number
  /** The instance default for reviews: `true` = a project with no override runs
   *  with reviews closed. The launch flag and a saved value are one level, so this
   *  is simply the value in effect. Read when a project attaches (boot, creation,
   *  reopen), not on every dispatch. */
  reviews_disabled?: boolean
}

export interface AssetHost {
  host: string
  ports: { port: string; summary?: string }[]
}

export interface ImportResult {
  imported?: number
  skipped?: number
  ids?: string[]
  errors?: string[]
  [k: string]: unknown
}

export interface Paged<T> {
  [k: string]: T
}

// ---- 态势（GET /api/fleet）：所有项目 + 节点的一份快照 ----

/** A direction as the fleet row list needs it (identity + live pulse). */
export interface FleetTask {
  id: string
  title: string
  node?: string
  status: IntentStatus | string
  attempts: number
  run_id?: string
  created_at: string
  pulse?: RunPulse
}

/** One bar on the fleet timeline. */
export interface FleetRun {
  id: string
  intent_id: string
  title: string
  node: string
  status: RunStatus
  purpose?: string
  stop_reason?: string
  cancel_cause?: string
  started_at: string
  ended_at?: string
  events: number
  tokens?: number
  declared_facts?: number
  context_tokens?: number
  context_window?: number
}

export interface TokenPoint {
  at: string
  tokens: number
  runs?: number
  /** Which node the spend happened on (empty for an unknown/legacy run). */
  node?: string
}

export interface FleetProject {
  usage?: TokenTotals
  id: string
  name: string
  scope?: string
  decider: DeciderStatus
  intents: {
    total: number
    pending: number
    claimed: number
    running: number
    /** Directions whose turn yielded for child intents. */
    waiting?: number
    reviewing?: number
    done: number
    failed: number
    review_failed?: number
    parked: number
  }
  facts: { total: number; with_evidence: number; verified: number }
  evidence: number
  goals: { total: number; met: number }
  runs: { total: number; running: number; done: number; failed: number; canceled: number }
  running: FleetTask[]
  open: FleetTask[]
  recent_runs: FleetRun[]
  /** Sparse per-minute token spend for the last three hours. */
  token_series?: TokenPoint[]
  /** Sum of the run tokens carried in this snapshot. */
  tokens: number

}

export interface FleetNodeHealth {
  online: boolean
  agents?: number
  latency_ms?: number
  checked_at?: string
  error?: string
}

export interface FleetNode {
  id: string
  transport?: string
  endpoint?: string
  note?: string
  /** Directions assigned to it right now (not a health probe). */
  running: number
  health: FleetNodeHealth
}

export interface FleetResponse {
  generated_at: string
  projects: FleetProject[]
  nodes: FleetNode[]
}

export interface KBEntry {
  id: string
  title: string
  description: string
  detail: string
  commands?: string[]
  tags?: string[]
  source?: string
  created_at?: string
  applicability?: string
  source_refs?: string[]
  updated_at?: string
  /** Which layer served this copy. Built-in entries ship in the binary; instance
   *  entries live only in <data>/kb. Candidate payloads have no layer. */
  origin?: 'builtin' | 'instance'
  /** Instance entry shadowing a built-in entry with the same id. */
  covers_builtin?: boolean
}

export interface KBCandidate {
  id: string
  version: number
  entry: KBEntry
  reason: string
  origin?: string
  base_revision: string
  created_at: string
  updated_at: string
  published: boolean
}

export interface KBResponse {
  dir: string
  entries: KBEntry[]
  revisions: Record<string, string>
  deleted_revisions: Record<string, string>
}

export type KBCandidateInput = Pick<KBCandidate, 'entry' | 'reason'> & Partial<Pick<KBCandidate, 'id' | 'version' | 'base_revision'>>

export interface HTTPFlowRecord {
  ref: string
  run_id: string
  intent_id: string
  session_id?: string
  event_id: string
  flow_id: string
  method: string
  url: string
  status: number
  at: string
  complete: boolean
  error?: string
  /** The scan node never persisted the body bytes: what we show is a preview. */
  preview_only?: boolean
  /** The stored/forwarded body is shorter than the observed one. */
  truncated?: boolean
  request_bytes: number
  response_bytes: number
  operation?: {call_id?: string; operation_id?: string; resource_id?: string; correlation?: number}
}
export interface HTTPFlow {
  record: HTTPFlowRecord
  flow: {
    request?: {method?: string; url?: string; protocol?: string; headers?: {name: string; value: string}[]; body?: string}
    response?: {status_code?: number; reason_phrase?: string; headers?: {name: string; value: string}[]; body?: string}
  }
}

/** One file in the executor's case directory. */
export interface RunCaseFile {
  name: string
  size: number
}

/** One spawnable/visible worker: an intent plus the state of its latest run. */
export interface WorkerView {
  intent_id: string
  participant_id: string
  title: string
  status: string
  parent_intent_id?: string
  created_by_participant_id?: string
  delegated_by_run_id?: string
  node?: string
  /** Historical run metadata only: new runs no longer resolve a profile. */
  profile?: string
  resolved_profile?: string
  run_id?: string
  run_status?: string
  model?: string
  running: boolean
  hold?: boolean
  /** Why the direction was parked. Shown on the roster so a parked row is not
   *  read as queued work; empty for every non-parked row. */
  park_reason?: string
  parked_at?: string
  last_event_kind?: string
  last_event_text?: string
  last_event_at?: string
  created_at: string
  /** True while the worker may be stopped; the server decides. */
  can_kill: boolean
}

export interface WorkersResponse {
  workers: WorkerView[]
  count: number
}

export interface KillWorkerResponse {
  killed: boolean
  intent_id: string
  run_id?: string
  cancel_cause?: string
  canceled: boolean
  worker: WorkerView
}

/** One directed peer rule: From may message To (participant ids or "commander"). */
export interface PeerRule {
  from: string
  to: string
}

/** Project message-authorization policy; mode open is the default. */
export interface PeerPolicy {
  mode: 'open' | 'allowlist' | string
  allow?: PeerRule[]
}

export interface PeerPolicyResponse {
  peer_policy: PeerPolicy
  /** Effective mode, already defaulted by the server ("open" when unset). */
  mode: string
}

/** What one run was handed and what it wrote back (api/runs/{id}/context). */
export interface RunContext {
  run_id: string
  intent_id: string
  intent_title: string
  intent_status: string
  status: string
  stop_reason?: string
  tokens?: number
  prompt?: string
  steer?: string[]
  graph?: unknown
  declared?: Record<string, unknown>[]
  asks?: Record<string, unknown>[]
  files?: RunCaseFile[]
}

// ---- project documents ------------------------------------------------------
//
// A project carries three plain-text documents and nothing else: 起点 (what is
// known, who is in scope), 目标 (the falsifiable end state) and 约束 (the one
// hard line plus the cleanup obligation). They are edited whole, every save is a
// revision, and there are no entries, ids or visible/revoked states to manage.
export type DocumentKind = 'scope' | 'goal' | 'constraints'

export interface ProjectDocument {
  text: string
  /** 0 means "never edited through the API yet": the text still comes from the
   *  project (scope, built-in constraints) or the goal row, and a first save
   *  sends 0 back. */
  revision: number
  updated_at?: string
  updated_by?: string
  /** Only on `goal`: which goal this document is the statement of. The goal
   *  document has one truth (the earliest open goal), so the console can say
   *  which one an edit lands on instead of guessing. */
  goal_id?: string
}

export interface DocumentsResponse {
  documents: Record<DocumentKind, ProjectDocument>
}

export interface DocumentRevision {
  revision: number
  text: string
  /** The server sends `updated_at`/`updated_by`; a goal revision may only carry
   *  the time it was written. Missing means "not recorded", never a fake date. */
  updated_at?: string
  updated_by?: string
  /** Only on the goal document: which goal the statement belongs to. */
  goal_id?: string
  /** Unified diff against the previous revision when the server computes one. */
  diff?: string
}

export interface DocumentHistoryResponse {
  revisions: DocumentRevision[]
}

export interface DocumentSaveResponse {
  revision: number
  updated_at: string
}