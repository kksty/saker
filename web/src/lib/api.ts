// One thin client over the HTTP API. Two namespaces, exactly as the server
// mounts them: instance-level endpoints (/api/projects, /api/vulns, …) and the
// project namespace (/p/<id>/api/…). See cmd/saker/hub.go and internal/api/api.go.

import { decodeLocationPart, locationPath } from './navigation.ts'
import { focusSearchParams } from './focus.ts'
import type { FocusPage, FocusRequest } from './focus.ts'

import type {
  ExternalAction,
  AccessEntry, BrowserLease, OperatorRequest, AuthorizationRequest, OperatorInbox, NativeArtifact,
  AppendGoalRequest, AppendGoalResult,
  AssetHost,
  BoardResponse,
  ChatResult,
  ChatTarget,
  CommunicationQueue,
  CommunicationStatus,
  ContinueIntentResult,
  Decision,
  DeciderStatus,
  DocumentHistoryResponse,
  DocumentKind,
  DocumentSaveResponse,
  DocumentsResponse,
  DirectiveRequest,
  DirectiveResponse,
  Evidence,
  Event,
  Fact,
  FleetResponse,
  Goal,
  GuardrailPending,
  GuardrailResolveResult,
  ImportResult,
  IOAObservations,
  Intent,
  Instruction,
  InstructionHistoryResponse,
  InstructionPage,
  KBResponse,
  HTTPFlow,
  HTTPFlowRecord,
  KBEntry,
  KBCandidate,
  KBCandidateInput,
  KillWorkerResponse,
  Message,
  ModelsCatalog,
  NodesResponse,
  Participant,
  PeerPolicy,
  PeerPolicyResponse,
  Playbook,
  PlaybooksResponse,
  Poc,
  PremisePolicy,
  PremisePolicyResponse,
  ProjectExportResult,
  ProjectImportResult,
  ProjectInfo,
  Proposal,
  ProposalDecisionResponse,
  ProposalPage,
  SkippedProject,
  VulnLibraryQuarantine,
  CreateProjectOptions,
  ReportMeta,
  ReportFinding,
  ReportPrecheck,
  Review,
  RetryPreview,
  RetryResult,
  Run,
  RunCommandsResponse,
  RunHistoryPage,
  Notebook,
  NoteRef,
  RunContext,
  RuntimeSettings,
  SettingsCheck,
  SettingsView,
  ToolsResponse,
  RuntimeToolsResponse,
  UsageSummary,
  UsageBreakdown,
  VerifierStatus,
  VerifyPendingResult,
  VulnEntry,
  WorkersResponse,
} from './types'

export interface ReadPage { next_cursor?: string; has_more: boolean; total: number; snapshot_version: {epoch: string; seq: number} }

/** Per-fact counters carried with a projected page (the fact body omits
 *  evidence_ids/refs there, so the counts are the only honest source). */
export interface GraphFactMeta {
  id: string
  needs_review: boolean
  reviewed_claim_version: number
  evidence_count: number
  ref_count: number
}

export interface GraphRead extends ReadPage {
  facts: Fact[]
  intents: Intent[]
  goals: Goal[]
  edges: { source: string; target: string; kind: string }[]
  boundary: { id: string; state: string }[]
  details?: { id: string; query: string; omitted: string[] }[]
  /** Only covers the facts returned on this page. */
  fact_meta?: GraphFactMeta[]
  intents_total?: number
  facts_total?: number
  goals_total?: number
  edges_total?: number
  boundary_total?: number
}

/** GET /api/changes: the journal window after a snapshot watermark, used to
 *  tell the operator "there are new changes, refresh" without polling the graph. */
export interface ChangePage {
  changes: { seq: number; kind: string; id: string; at: string }[]
  next_cursor?: string
  has_more: boolean
  high_watermark: { epoch: string; seq: number }
  pending: number
}

export interface FactRead extends ReadPage { facts: Fact[] }

// ---- communication backlog (GET /api/communication) ----
//
// The wire shapes live in types.ts (one-to-one with store.QueueStats); this
// section only adds the small read-only helpers the Messages page needs. The
// console shows pressure but never interprets queued as delivered: a queued
// notice reaches the next prompt, and only a live session consumes it now.

export interface QueueLimits {
  max_messages: number
  max_bytes: number
}

/** The tighter of the two declared bounds, clamped to 0..1 for the meter. */
export function queueLoad(q: CommunicationQueue, limits: QueueLimits): number {
  const byCount = limits.max_messages > 0 ? q.pending / limits.max_messages : 0
  const byBytes = limits.max_bytes > 0 ? q.bytes / limits.max_bytes : 0
  return Math.min(1, Math.max(byCount, byBytes))
}

/** At 80% the queue is visibly close to refusing new work; deferred already did. */
const queuePressureThreshold = 0.8

export function queueUnderPressure(q: CommunicationQueue, limits: QueueLimits): boolean {
  return q.deferred || queueLoad(q, limits) >= queuePressureThreshold
}

export interface BacklogSummary {
  queued: number
  bytes: number
  deferred: number
  recipients: number
}

export function backlogSummary(status: CommunicationStatus | undefined): BacklogSummary {
  const out: BacklogSummary = { queued: 0, bytes: 0, deferred: 0, recipients: 0 }
  for (const q of status?.queues ?? []) {
    out.queued += q.pending
    out.bytes += q.bytes
    if (q.deferred) out.deferred++
    if (q.pending > 0 || q.deferred) out.recipients++
  }
  return out
}

export interface FactNotice {
  factId: string
  version: number
  coalesced: number
}

/** The mechanical sender is the control plane's router, not a participant: it
 *  must never read as the commander or the operator speaking. */
export function isMechanicalSender(id: string | undefined): boolean {
  return id === 'ledger'
}

/** A routed notice points at the record by id + version; its body is never the
 *  fact text. Returns null for any row that is not one. */
export function readFactNotice(m: Message): FactNotice | null {
  if (!isMechanicalSender(m.from_participant_id) || !m.notice_fact_id) return null
  return { factId: m.notice_fact_id, version: m.notice_version ?? 0, coalesced: m.coalesced ?? 0 }
}


export class ApiError extends Error {
  status: number
  /** Structured error code from the server (`{"error": "...", "code": "peer_denied"}`). */
  code?: string
  /** Parsed JSON body, for endpoints whose errors carry more than a message/code
   *  (append-goal: needs_human / retryable / pause_reason). */
  body?: unknown
  constructor(status: number, message: string, code?: string, body?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.body = body
  }
}

/**
 * Optional transport controls for one API read or write.
 *
 * `signal` cancels the request (fetch rejects with the signal's reason, an
 * `AbortError`); `timeoutMs` adds a deadline for that one request. Existing
 * callers pass neither and keep the current behavior exactly.
 */
export interface RequestOptions {
  signal?: AbortSignal
  timeoutMs?: number
}

/**
 * One AbortSignal from the caller's options.
 *
 * `AbortSignal.timeout()` aborts with a `TimeoutError`, not an `AbortError`, and
 * `AbortSignal.any()` is only Newly Available (Chrome 116+): when it is missing
 * the caller's signal wins and the deadline degrades to "no deadline" instead of
 * losing cancellation. No timer is created unless `timeoutMs` is requested, so
 * debounced/streamed reads keep their existing lifetime.
 */
export function abortSignalFor(options?: RequestOptions): AbortSignal | undefined {
  const timeout =
    options?.timeoutMs && options.timeoutMs > 0 ? AbortSignal.timeout(options.timeoutMs) : undefined
  if (!options?.signal) return timeout
  return timeout && typeof AbortSignal.any === 'function'
    ? AbortSignal.any([options.signal, timeout])
    : options.signal
}

/** `?a=1&b=2` suffix for one request; absent/empty values are omitted and values
 *  are form-encoded by URLSearchParams instead of being concatenated by hand. */
function queryString(params: Record<string, string | number | boolean | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === '') continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ''
}

/** The project this page is looking at: /p/<id>/…, or "" before one is chosen. */
export function projectID(): string {
  const m = /^\/p\/([^/]+)/.exec(window.location.pathname)
  return m ? decodeLocationPart(m[1]) : ''
}

/** Base of the project namespace, "" when the URL carries no project. */
function pbase(): string {
  const id = projectID()
  return id ? `/p/${encodeURIComponent(id)}` : ''
}

export function redirectToLogin(): void {
  const params = new URLSearchParams({ next: locationPath() })
  window.location.assign(`/login?${params}`)
}

async function request<T>(
  url: string,
  init?: RequestInit,
  json = true,
  options?: RequestOptions,
): Promise<T> {
  const res = await fetch(url, {
    ...init,
    signal: abortSignalFor(options) ?? init?.signal,
    headers: json ? { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } : init?.headers,
  })
  const text = await res.text()
  let body: unknown = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = text
    }
  }
  if (!res.ok) {
    if (res.status === 401) {
      redirectToLogin()
      throw new ApiError(401, '需要登录')
    }
    const msg =
      body && typeof body === 'object' && 'error' in body
        ? String((body as { error: unknown }).error)
        : `HTTP ${res.status}`
    // The collaboration endpoints attach a machine-readable code (peer_denied,
    // worker_forbidden, ...) so the console can show the reason instead of
    // guessing from the prose.
    const code =
      body && typeof body === 'object' && typeof (body as { code?: unknown }).code === 'string'
        ? (body as { code: string }).code
        : undefined
    throw new ApiError(res.status, msg, code, body)
  }
  return body as T
}

/**
 * Why a body read was refused. The server explains itself in `{"error": "..."}`
 * — a report body is withheld while its checks fail or the ledger moved, and
 * that reason is what the operator has to see. Falling back to "could not read"
 * would hide the gate behind a transport-looking failure.
 */
async function refusal(res: Response, fallback: string): Promise<ApiError> {
  const body: unknown = await res.json().catch(() => null)
  const reason =
    body && typeof body === 'object' && 'error' in body ? String((body as { error: unknown }).error) : ''
  return new ApiError(res.status, reason || fallback, undefined, body)
}

const get = <T>(url: string, options?: RequestOptions) => request<T>(url, undefined, true, options)
const post = <T>(url: string, body?: unknown, options?: RequestOptions) =>
  request<T>(url, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) }, true, options)
const put = <T>(url: string, body?: unknown, options?: RequestOptions) =>
  request<T>(url, { method: 'PUT', body: JSON.stringify(body) }, true, options)
const del = <T>(url: string, options?: RequestOptions) =>
  request<T>(url, { method: 'DELETE' }, true, options)
// The archive is the request body, not a JSON field: a project can be hundreds
// of MB, and decoding it as base64 would add a third copy in the page.
const upload = <T>(url: string, file: Blob) =>
  request<T>(url, { method: 'POST', body: file, headers: { 'Content-Type': 'application/zip' } }, false)

// ---- instance: projects, vuln library, health ----

export const api = {
  externalActions: () => get<{actions: ExternalAction[]}>(`${pbase()}/api/actions`),
  resolveAction: (id: string, body: {version:number;state:'completed'|'unknown';result:string;result_ref?:string}) =>
    post<ExternalAction>(`${pbase()}/api/actions/${encodeURIComponent(id)}/result`, body),
  access: () => get<{ access: AccessEntry[] }>(`${pbase()}/api/access`).then(r => r.access),
  createAccess: (body: {name?: string; kind: string; origin?: string; note?: string; cdp_url?: string; intent_id?: string; request_id?: string; material?: unknown}) => post<AccessEntry>(`${pbase()}/api/access`, body),
  deleteAccess: (id: string) => del<{deleted: boolean}>(`${pbase()}/api/access/${encodeURIComponent(id)}`),
  browserAction: (id: string, action: 'open' | 'inspect' | 'save' | 'detach', lease_id: string) => post<{returned: boolean; output: string; verified: boolean; access?: AccessEntry}>(`${pbase()}/api/access/${encodeURIComponent(id)}/browser`, {action, lease_id}),
  browserLease: (id: string, body: {action: 'acquire' | 'release' | 'recover'; lease_id?: string; note?: string; confirmed_idle?: boolean}) => post<{lease?: BrowserLease; released?: boolean}>(`${pbase()}/api/access/${encodeURIComponent(id)}/lease`, body),
  operatorRequests: () => get<{requests: OperatorRequest[]}>(`${pbase()}/api/operator-requests`).then(r => r.requests),
  operatorInbox: () => get<OperatorInbox>(`${pbase()}/api/operator-inbox`),
  // Tool approval is project-level and operator-only (a harness session gets
  // 403): the host holds the intercepted call until the operator answers. The
  // pending read is a 200 projection with state "available" | "unavailable", so
  // "no guardrail configured" renders as a state instead of an error.
  guardrailPending: (session = '', options?: RequestOptions) =>
    get<GuardrailPending>(`${pbase()}/api/guardrail/pending${queryString({ session })}`, options),
  // A 409 (state "failed" | "unavailable") is an answer, not a transport error:
  // it still carries the host's reason and the console shows it in place.
  guardrailResolve: async (body: { session_id: string; operation_id: string; approve: boolean; reason?: string }) => {
    try {
      return await post<GuardrailResolveResult>(`${pbase()}/api/guardrail/resolve`, body)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.body && typeof e.body === 'object' && 'state' in e.body) {
        return e.body as GuardrailResolveResult
      }
      throw e
    }
  },
  updateOperatorRequest: (id: string, body: {action: string; asset: string; impact: string; authorization: string}) =>
    put<{request: AuthorizationRequest}>(`${pbase()}/api/operator-requests/${encodeURIComponent(id)}`, body).then(r => r.request),
  resolveOperatorRequest: (id: string, resolution: string) =>
    post<{request: AuthorizationRequest}>(`${pbase()}/api/operator-requests/${encodeURIComponent(id)}/resolve`, {resolution}).then(r => r.request),
  requestOperatorDetails: (id: string) =>
    post<{request: AuthorizationRequest; woken: boolean}>(`${pbase()}/api/operator-requests/${encodeURIComponent(id)}/request-details`, {}),
  completeOperator: (intent: string, request_id: string, response: string, resolution: 'completed' | 'unavailable' = 'completed') => post<{accepted: boolean}>(`${pbase()}/api/intents/${encodeURIComponent(intent)}/operator/complete`, {request_id, response, resolution}),
  artifacts: (query = '') => get<{artifacts: NativeArtifact[]}>(`${pbase()}/api/artifacts${queryString({ q: query, limit: 100 })}`).then(r => r.artifacts),
  artifactURL: (ref: string) => `${pbase()}/api/artifacts/data${queryString({ ref })}`,
  health: (options?: RequestOptions) => get<{ ok: boolean; executor: string }>(`${pbase()}/api/health`, options),
  version: () => get<{ version: string }>('/api/version'),

  // The offline knowledge library is instance-level: every project reads the
  // same world knowledge, so it is not under /p/<id>/ either.
  kb: () => get<KBResponse>('/api/kb'),
  kbCandidates: () => get<{candidates: KBCandidate[]}>('/api/kb/candidates').then(r => r.candidates),
  saveKBCandidate: (body: KBCandidateInput) => body.id
    ? put<KBCandidate>(`/api/kb/candidates/${encodeURIComponent(body.id)}`, body)
    : post<KBCandidate>('/api/kb/candidates', body),
  publishKB: (id: string, version: number) => post<KBEntry>(`/api/kb/candidates/${encodeURIComponent(id)}/publish`, {version}),
  reloadKB: () => post<{entries: KBEntry[]}>('/api/kb/reload'),
  deleteKB: (id: string, revision: string) => request<{deleted: boolean}>(`/api/kb/entries/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({revision})}),
  deleteKBCandidate: (id: string, version: number) => request<{deleted: boolean}>(`/api/kb/candidates/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({version})}),
  playbookSource: (project: string, id: string) => get<Playbook>(`/p/${encodeURIComponent(project)}/api/playbooks/${encodeURIComponent(id)}`),
  nominatePlaybookKB: (project: string, id: string, body: KBCandidateInput & {source_updated_at: string}) => post<KBCandidate>(`/p/${encodeURIComponent(project)}/api/playbooks/${encodeURIComponent(id)}/kb-candidate`, body),

  // The LLM settings are instance-level: the decider and every local harness
  // run read them, so they are not under /p/<id>/.
  settings: () => get<SettingsView>('/api/settings'),
  saveSettings: (body: Partial<SettingsView>) => put<SettingsView>('/api/settings', body),
  // The instance-level gate shared by verifier and report runs. It is not a
  // project setting: one value bounds every project's auxiliary calls.
  runtimeSettings: () => get<RuntimeSettings>('/api/runtime-settings'),
  // Both fields are optional and independent: an omitted one keeps its stored
  // value (a PUT body is never a full replacement). `reviews_disabled` is the
  // instance default, not a live switch over running projects.
  saveRuntimeSettings: (body: { auxiliary_concurrency?: number; reviews_disabled?: boolean }) =>
    put<RuntimeSettings>('/api/runtime-settings', body),
  // One explicit connectivity check against the saved unified gateway. The key
  // stays on the server; probed/ms/models/source are server metadata: source
  // "host" means the live harness measured it, "self" the controller's probe.
  checkModel: (options?: RequestOptions) =>
    post<SettingsCheck>('/api/settings/check', undefined, options),
  // The endpoint's own catalogue, read by the controller (the API key never
  // leaves the server). An empty list is not an answer: the call fails instead.
  models: () => get<ModelsCatalog>('/api/models'),


  // The instance default for --max-runs rides along so the project-concurrency
  // form can show "inherit N" instead of guessing; round_timeout_seconds is the
  // same thing for --round-timeout (0 = unlimited), and reviews_disabled_default
  // lets a following project name the instance value it inherits (true = reviews
  // closed). Each project's effective and override values are in the list.
  projectIndex: () =>
    get<{
      projects: ProjectInfo[]
      max_runs: number
      round_timeout_seconds: number
      /** True = a project with no override attaches with reviews closed. */
      reviews_disabled_default?: boolean
      /** Data-root directories that did not load: the instance keeps serving the
       *  rest, so this is how "a project is missing" becomes visible. */
      skipped_projects?: SkippedProject[]
      /** A vuln-library index set aside at boot. `null` = nothing quarantined this
       *  boot; the console shows it so an empty search is not mistaken for an
       *  empty library. */
      vuln_library_quarantine?: VulnLibraryQuarantine | null
    }>('/api/projects'),
  projects: () => api.projectIndex().then((r) => r.projects),
  // The 态势 page: one instance-wide snapshot instead of N board requests.
  fleet: () => get<FleetResponse>('/api/fleet'),
  // An operator-authored engagement: 起点 becomes the project brief (scope),
  // 终点 becomes the single endpoint goal. The commander does not derive either.
  //
  // The options are applied before the first dispatch, so a project can start with
  // reviews closed or with its own concurrency/timeout instead of "create, pause,
  // edit settings, resume". Omitted keys keep today's behaviour exactly (empty
  // object spreads to no fields); `max_runs: 0` means unlimited, and an empty
  // `round_timeout` means "inherit the instance flag".
  createProject: (
    name: string,
    start: string,
    end = '',
    description = '',
    options: CreateProjectOptions = {},
  ) => post<ProjectInfo>('/api/projects', { name, scope: start, goal: end, description, ...options }),
  // Every field is optional server-side: omitted keeps the current value, and
  // max_runs: null restores the instance default.
  // round_timeout: "" inherits --round-timeout, "15m"/"0" set
  // the project's own commander round wall clock (0 = unlimited).
  // reviews_disabled is the project's own override: absent is not a value here
  // (this endpoint is a patch), `null` clears it so the project follows the
  // instance default again, `true`/`false` pins it. When the project is attached
  // the switch applies immediately, with the same cancel/re-arm semantics as the
  // project's own review control.
  // The brief and the constraint document are NOT editable here any more: they are
  // the `scope`/`constraints` documents (PUT /api/documents/{kind} with a
  // revision), and the server answers 400 document_write_moved for them. Retired
  // fields are rejected with 400, not silently ignored: verify_auto (a review
  // switch that never existed) and premise_policy (one fixed mode, so a write
  // could only restate it).
  updateProject: (
    id: string,
    body: {
      max_runs?: number | null
      round_timeout?: string
      /** `null` clears the override so the project follows the instance default. */
      reviews_disabled?: boolean | null
    },
  ) => put<ProjectInfo>(`/api/projects/${encodeURIComponent(id)}`, body),
  deleteProject: (id: string) =>
    del<{ deleted: string; vulns_kept: number; projects: number }>(
      `/api/projects/${encodeURIComponent(id)}${queryString({ confirm: id })}`,
    ),
  // Whole-project transfer. Export refuses a running project server-side: the
  // archive must describe one committed state, so the button first tells the
  // operator to pause. Import is an upload of the .sakerproj file.
  exportProject: (id: string) =>
    post<ProjectExportResult>(`/api/projects/${encodeURIComponent(id)}/export`),
  projectExportURL: (name: string) => `/api/exports/${encodeURIComponent(name)}`,
  importProject: (file: File) => upload<ProjectImportResult>('/api/projects/import', file),

  // The instance library. `total` is the whole library; `matched` is how many
  // rows satisfy the current project/text filter (ignoring limit). The project
  // page reads the same list with a fixed project. Additive, so an older server
  // without `matched` still renders (see Vulns.tsx).
  vulns: (params: { project?: string; q?: string; limit?: number } = {}) => {
    const suffix = queryString({
      project: params.project,
      q: params.q,
      limit: params.limit || undefined,
    })
    return get<{ vulns: VulnEntry[]; total: number; matched?: number }>(`/api/vulns${suffix}`)
  },
  vulnEvidenceURL: (path: string) => `/api/vulns/evidence/${path.split('/').map(encodeURIComponent).join('/')}`,
  // Saving a library evidence copy is an explicit second request: the plain URL
  // answers inline (preview), this one asks the server for an attachment.
  vulnEvidenceDownloadURL: (path: string) => `${api.vulnEvidenceURL(path)}${queryString({ download: 1 })}`,

  // ---- project: execution locations and their binding (docs/14-nodes.md) ----

  nodes: () => get<NodesResponse>(`${pbase()}/api/nodes`),
	nodeJoin: () => get<{server_url:string;args:string[];workspace:string;note:string}>(`${pbase()}/api/nodes/join`),
  setNodes: (nodes: string[], def: string) =>
    put<NodesResponse>(`${pbase()}/api/nodes`, { nodes, default: def }),

  // ---- project: execution supervision and peer policy ----
  //
  // A worker is an intent with a run; the operator sees the whole project's
  // roster and may stop anything still active.

  workers: () => get<WorkersResponse>(`${pbase()}/api/workers`),
  killWorker: (id: string) =>
    post<KillWorkerResponse>(`${pbase()}/api/workers/${encodeURIComponent(id)}/kill`),
  // Who may message whom. Open is the default; allowlist is directional.
  peers: () => get<PeerPolicyResponse>(`${pbase()}/api/peers`),
  savePeers: (body: PeerPolicy) => put<PeerPolicyResponse>(`${pbase()}/api/peers`, body),

  // Premise policy: there is one mode. Every necessary premise must be a
  // reviewable kind with a current-claim independent proof and intact originals;
  // legacy standard/strict manifests are migrated when the project loads.
  // Read-only on purpose — with one mode and a normalized manifest, a save could
  // only restate the value while looking like a decision.
  premisePolicy: () => get<PremisePolicyResponse>(`${pbase()}/api/premise-policy`),

  // ---- project namespace ----

  board: () => get<BoardResponse>(`${pbase()}/api/board`),

  // The task-scoped hot/warm/cold projection. It is an index, not a second
  // ledger: cold rows stay readable through layer="cold" and the premise
  // directory in the run's case remains complete. The server scopes a
  // participant to its own direction and refuses a different intent.
  focus: (request: FocusRequest) =>
    get<FocusPage>(`${pbase()}/api/focus?${focusSearchParams(request).toString()}`),

  intents: async () => {
    const out: Intent[] = []; let cursor = ''; const seen = new Set<string>()
    do { const page = await get<ReadPage & {intents: Intent[]}>(`${pbase()}/api/intents${queryString({ limit: 500, cursor })}`)
      out.push(...(page.intents ?? [])); if (!page.has_more) break
      if (!page.next_cursor || seen.has(page.next_cursor)) throw new Error('意图分页没有推进')
      cursor = page.next_cursor; seen.add(cursor)
    } while (cursor)
    return out
  },
  // One direction plus the facts it declared (server-side `intent_id` filter).
  // `facts_total` is the full count; the array is one page of at most 200 rows,
  // so the detail must say "共 N 条，这里列出 M 条" instead of hiding the cap.
  intent: (id: string) =>
    get<{ intent: Intent; facts: Fact[]; facts_total?: number; facts_has_more?: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}`,
    ),
  // Continue a finished direction on the harness session its last run left
  // behind: the brief is not re-sent, only how the previous turn ended. 409 with
  // code continue_unavailable means there is no session left and a fresh retry
  // is the way forward (ApiError.code carries it through).
  continueIntent: (id: string, note?: string) => {
    const text = note?.trim()
    return post<ContinueIntentResult>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/continue`,
      text ? { note: text } : {},
    )
  },
  intentUsage: (id: string) =>
    get<{ direct: UsageSummary }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/usage`,
    ),
  // verifyBytes re-hashes every original behind the premises. It is opt-in and
  // used by the one surface that explains why a direction cannot be dispatched:
  // a list or a poll cannot afford the read, but the answer to "why not" must not
  // differ from what the dispatch gate will do.
  intentReadiness: (id: string, opts?: { verifyBytes?: boolean }) => get<{
    intent_id: string; policy: string; ready: boolean; integrity_checked?: boolean;
    review_hint?: string; self_reference_hint?: string;
    premises: { id: string; title?: string; claim_version?: number; status: string; usable: boolean; reason?: string; review_id?: string; verifier_run?: string; integrity: string; review?: { state: 'queued' | 'running' | 'cooling' | 'paused' | 'failed' | 'ineligible' | 'disabled' } }[]
  }>(`${pbase()}/api/intents/${encodeURIComponent(id)}/readiness${opts?.verifyBytes ? '?verify_bytes=1' : ''}`),
  handoffIntent: (id: string, note: string, expectedVersion: number) =>
    post<{ intent: Intent; run_id: string }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/handoff`,
      { note, expected_version: expectedVersion },
    ),
  createIntent: (body: {
    title: string
    description: string
    doc?: string
    from_fact_ids?: string[]
    skills?: string[]
    node?: string
  }) => post<Intent>(`${pbase()}/api/intents`, body),
  runIntent: (id: string) =>
    post<{ run_id: string; intent_id: string }>(`${pbase()}/api/intents/${encodeURIComponent(id)}/run`),
  // steer cancels the current run and resumes the same trajectory as a new
  // turn. It is the advanced "stop, then redirect" path; say is the default.
  steerIntent: (id: string, note: string, refFact = '') =>
    post<{ run_id: string; intent_id: string; resumed: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/steer`,
      { note, ref_fact: refFact },
    ),
  // say delivers into the live run through the host's IOA bridge. It does not
  // cancel or replace the run; accepted means IOA took the message, not that
  // the model has already read it (D69).
  sayIntent: (id: string, text: string, interrupt = false, refFact = '') =>
    post<{ message_id: string; intent_id: string; accepted: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/say`,
      { text, interrupt, ref_fact: refFact },
    ),
  // chat is the operator side of the project conversation, typed in the 通信
  // page: one sentence to the commander alone, or to the commander plus every
  // executor that can still receive it. The commander's next round reads it and
  // finished directions are skipped instead of revived.
  chat: (body: { to: ChatTarget; text: string; idempotency_key?: string }) =>
    post<ChatResult>(`${pbase()}/api/chat`, body),
  // One operator directive entry: a durable message to the commander or to one
  // direction. The 通信 page's composer never composes a lifetime, so it always
  // sends persistent=false / steer=false; the 操作员指令 surface composes
  // persistent=true through this same endpoint (a persistent directive is a
  // versioned instruction, not a message).
  directive: (body: DirectiveRequest) =>
    post<DirectiveResponse>(`${pbase()}/api/directives`, body),

  // Operator instructions: versioned guidance injected into every later
  // dispatch. The list returns active rows unless state=all (the console asks
  // for all so revoked rows stay auditable); revocation is an update carrying
  // the version and the text this page read, so a stale write is refused.
  instructions: (state: 'active' | 'all' = 'all', limit = 200) =>
    get<InstructionPage>(`${pbase()}/api/instructions${queryString({ state, limit })}`),
  instructionHistory: (id: string, limit = 100) =>
    get<InstructionHistoryResponse>(
      `${pbase()}/api/instructions/${encodeURIComponent(id)}/history${queryString({ limit })}`,
    ),
  // Editing the body and revoking are the same versioned update: the version
  // and state this page read travel back, so a stale write is refused (409)
  // instead of overwriting what another window changed.
  updateInstruction: (
    id: string,
    body: { version: number; text: string; state: Instruction['state'] },
  ) => put<Instruction>(`${pbase()}/api/instructions/${encodeURIComponent(id)}`, body),
  revokeInstruction: (id: string, body: { version: number; text: string; state: 'revoked' }) =>
    put<Instruction>(`${pbase()}/api/instructions/${encodeURIComponent(id)}`, body),

  // Proposals: executors suggest a next intent; the console (operator) and the
  // commander decide. Approval only creates a pending intent — a separate
  // dispatch starts the run — so the UI keeps those as two distinct actions.
  proposals: (status?: Proposal['status'] | 'active' | 'closed', limit = 100, cursor?: string) =>
    get<ProposalPage>(`${pbase()}/api/proposals${queryString({ status: status ?? '', limit, cursor })}`),
  proposal: (id: string) => get<{ proposal: Proposal }>(`${pbase()}/api/proposals/${encodeURIComponent(id)}`),
  approveProposal: (id: string, body: { expected_version: number; note?: string }) =>
    post<ProposalDecisionResponse>(`${pbase()}/api/proposals/${encodeURIComponent(id)}/approve`, body),
  rejectProposal: (id: string, body: { expected_version: number; reason: string }) =>
    post<ProposalDecisionResponse>(`${pbase()}/api/proposals/${encodeURIComponent(id)}/reject`, body),
  dispatchProposal: (id: string, body: { expected_version: number }) =>
    post<ProposalDecisionResponse>(`${pbase()}/api/proposals/${encodeURIComponent(id)}/dispatch`, body),

  // The three project documents. One call returns all three with their revision;
  // a save carries the revision it read, so a newer write cannot be overwritten
  // silently (the server answers 409 document_conflict).
  documents: () => get<DocumentsResponse>(`${pbase()}/api/documents`),
  /** The same three documents of another project, read from the instance-wide
   *  project list: `documents()` follows the URL's project, so a row in that list
   *  needs its own read. Read-only — the write path stays on the project's page. */
  projectDocuments: (id: string) =>
    get<DocumentsResponse>(`/p/${encodeURIComponent(id)}/api/documents`),
  documentHistory: (kind: DocumentKind, limit = 20) =>
    get<DocumentHistoryResponse>(`${pbase()}/api/documents/${kind}/history${queryString({ limit })}`),
  saveDocument: (kind: DocumentKind, body: { text: string; revision: number; goal_id?: string }) =>
    put<DocumentSaveResponse>(`${pbase()}/api/documents/${kind}`, body),

  // Sticky operator stop: holds the direction (and its whole subtree) so the
  // pump cannot re-open it; an explicit retry is the only way back. The reason
  // is mandatory on both sides — an unexplained stop loses work, so an empty
  // reason never reaches the wire.
  cancelIntent: (id: string, reason: string) => {
    const text = reason.trim()
    if (!text) {
      return Promise.reject(new ApiError(400, '停止必须写理由：没有理由的停止等于丢工作'))
    }
    return post<{ intent_id: string; run_id: string; held: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/cancel`,
      { reason: text },
    )
  },
  parkIntent: (id: string, reason: string) =>
    post<Intent>(`${pbase()}/api/intents/${encodeURIComponent(id)}/park`, { reason }),
  reopenIntent: (id: string) =>
    post<Intent>(`${pbase()}/api/intents/${encodeURIComponent(id)}/reopen`),

  // Retry has two shapes, chosen from a preview: resume the failed run's harness
  // session (memory kept) or start a fresh session (memory dropped). Both
  // re-inject unconfirmed messages; the preview says how many each would redeliver.
  retryPreview: (id: string) =>
    get<RetryPreview>(`${pbase()}/api/intents/${encodeURIComponent(id)}/retry-preview`),
  retryIntent: (id: string, opts: { note?: string; resume: boolean }) =>
    post<RetryResult>(`${pbase()}/api/intents/${encodeURIComponent(id)}/retry`, {
      note: opts.note || undefined,
      resume: opts.resume,
    }),

  retrievalStatus: () => get<{configured:boolean;model?:string}>(`${pbase()}/api/retrieval/status`),
  indexFacts: (cursor = '') => post<ReadPage & {indexed_this_page:number;remaining_to_visit:number}>(`${pbase()}/api/retrieval/index`,{cursor}),
  // Light journal probe: the page is a fixed snapshot, so the console compares
  // high_watermark with data.snapshot_version instead of merging live writes in.
  // `since` asks "did any ledger *content* change after this seq?" (run/message
  // bookkeeping advances the durable seq without a versions row, so a raw
  // watermark comparison would report changes that never touched the graph).
  changes: (params: {cursor?:string;limit?:number;since?:number} = {}) =>
    get<ChangePage>(`${pbase()}/api/changes${queryString(params)}`),
  // order/projection are explicit optimizations for the map's snapshot read
  // (structural ids + graph projection); omitting them keeps the legacy page.
  graph: (params: {root?:string;cursor?:string;limit?:number;depth?:number;order?:'structural'|'recent';projection?:'full'|'graph'} = {}, options?: RequestOptions) =>
    get<GraphRead>(`${pbase()}/api/graph${queryString(params)}`, options),
  factsPage: (params: {mode?:string;q?:string;kind?:string;intent?:string;status?:string;tag?:string;cursor?:string;limit?:number} = {}, options?: RequestOptions) =>
    get<FactRead>(`${pbase()}/api/facts${queryString(params)}`, options),
  facts: (params: { q?: string; kind?: string; intent?: string; limit?: number } = {}) => {
    const suffix = queryString({
      q: params.q,
      kind: params.kind,
      intent: params.intent,
      limit: params.limit || undefined,
    })
    return get<{ facts: Fact[] }>(`${pbase()}/api/facts${suffix}`).then((r) => r.facts ?? [])
  },
  fact: (id: string) =>
    get<{ fact: Fact; version?: number; evidence: Evidence[]; reviews: Review[] }>(
      `${pbase()}/api/facts/${encodeURIComponent(id)}`,
    ),
  verifyFact: (id: string) =>
    post<{ queued: boolean; fact_id: string }>(`${pbase()}/api/facts/${encodeURIComponent(id)}/verify`),
  // The vulnerability library is instance-level, so its rows carry project_id
  // and the URL has no /p/ prefix: these two take the project explicitly instead
  // of reading it from the address bar.
  verifyFactInProject: (projectId: string, factId: string) =>
    post<{ queued: boolean; fact_id: string }>(
      `/p/${encodeURIComponent(projectId)}/api/facts/${encodeURIComponent(factId)}/verify`,
    ),
  // Queue a verifier run for every eligible unverified vuln/cred of the project.
  // 202 and {queued, skipped}; 409 when a batch is already running, 503 when no
  // verifier is configured.
  verifyPendingFacts: (projectId: string) =>
    post<VerifyPendingResult>(
      `/p/${encodeURIComponent(projectId)}/api/facts/verify-pending`,
    ),
  // The review switch is per project and independent of the project pause (D137):
  // pausing reviews never resumes the commander, and a paused project can still
  // re-prove a finding. Project-scoped, like the batch endpoint.
  verifierStatus: () => get<VerifierStatus>(`${pbase()}/api/verifier`),
  pauseReviews: () =>
    post<{ paused: boolean; canceled: number }>(`${pbase()}/api/verifier/pause`),
  resumeReviews: () => post<{ paused: boolean }>(`${pbase()}/api/verifier/resume`),
  // The stronger switch: closing reviews stops the verifier for this project
  // (live runs are canceled, the pump and the fixed settlement review stop, and
  // unverified facts keep their text). It starts closed under --no-verify; the
  // console can open it again at runtime — resume alone cannot (409).
  disableReviews: () =>
    post<{ disabled: boolean; canceled: number }>(`${pbase()}/api/verifier/disable`),
  enableReviews: () => post<{ disabled: boolean }>(`${pbase()}/api/verifier/enable`),
  // A human verdict: reject the conclusion, or put it back to unverified. There
  // is deliberately no "confirm" — that is what a verifier rerun is for.
  // A human verdict binds to the assertion version the operator actually read:
  // the server refuses (409) when the claim changed meanwhile, so a stale page
  // cannot silently reject a conclusion nobody saw.
  reviewFact: (id: string, verdict: 'reject' | 'needs_evidence', notes: string, claimVersion?: number) =>
    post<{ review: Review }>(`${pbase()}/api/facts/${encodeURIComponent(id)}/review`, {
      verdict,
      notes,
      ...(claimVersion ? { claim_version: claimVersion } : {}),
    }),
  // Withdrawal is neither a delete nor a verdict: the row stays in the ledger,
  // and a reason is mandatory. The server decides who may call it (operator, or
  // the authoring direction for its own still-unverified fact).
  withdrawFact: (id: string, reason: string) =>
    post<{ fact: Fact }>(`${pbase()}/api/facts/${encodeURIComponent(id)}/withdraw`, { reason }),
  reviews: (fact?: string) =>
    get<{ reviews: Review[] }>(`${pbase()}/api/reviews${queryString({ fact })}`).then(
      (r) => r.reviews ?? [],
    ),

  // Project knowledge, not facts: an executor declares the tactic in the run
  // that used it (confirmed); the commander can only nominate a candidate, and
  // a same-titled declaration promotes it (D70). There is no create here on
  // purpose — a human writes through a run, not by typing a recipe.
  playbooksPage: (params: {q?:string;tag?:string;status?:string;archived?:string;cursor?:string}={}) => {
    const suffix = queryString({ limit: 50, ...params })
    return get<ReadPage & PlaybooksResponse & {counts:Record<string,number>}>(`${pbase()}/api/playbooks${suffix}`)
  },
  archivePlaybook: (p:Playbook,archived:boolean) => post<Playbook>(`${pbase()}/api/playbooks/${encodeURIComponent(p.id)}/archive`,{updated_at:p.updated_at,archived}),
  playbookHistory: (id:string,cursor='') => get<ReadPage & {history:{seq:number;version:number;operation:string;at:string;value?:Playbook}[]}>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}/history${queryString({ limit: 20, cursor })}`),
  retirePlaybook: (id: string) =>
    post<Playbook>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}/retire`),
  deletePlaybook: (id: string, updated_at: string) => request<{deleted: boolean}>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({updated_at})}),

  evidence: () => get<{ evidence: Evidence[] }>(`${pbase()}/api/evidence`).then((r) => r.evidence ?? []),
  // Opening this URL previews (Content-Disposition: inline); saving the original
  // is a separate, explicit request. The distinction matters for binary captures
  // that the browser would otherwise dump straight into the download tray.
  evidenceURL: (id: string) => `${pbase()}/api/evidence/${encodeURIComponent(id)}`,
  evidenceDownloadURL: (id: string) =>
    `${pbase()}/api/evidence/${encodeURIComponent(id)}${queryString({ download: 1 })}`,

  goals: () => get<{ goals: Goal[] }>(`${pbase()}/api/goals`).then((r) => r.goals ?? []),
  // The statement is the `goal` document, so an edit must carry the revision it
  // read; without one the server answers 409 instead of overwriting a newer save.
	updateGoal: (id: string, statement: string, start: string, revision: number) => post<Goal>(`${pbase()}/api/goals/${encodeURIComponent(id)}/update`, {statement,start,revision}),
	closeGoal: (id: string, reason: string) => post<Goal>(`${pbase()}/api/goals/${encodeURIComponent(id)}/close`, {reason}),
  // Operator-defined endpoint: 起点 (where we start) + 终点 (falsifiable end
  // state). There is no quantified-criterion input any more.
  createGoal: (statement: string, start: string) =>
    post<Goal>(`${pbase()}/api/goals`, { statement, start }),

  state: () => get<Notebook>(`${pbase()}/api/state`),
  putState: (state: string, version: number) => put<{ saved: boolean; bytes: number; version: number; receipt: NoteRef; projection_warning?: string }>(`${pbase()}/api/state`, { state, version }),

  decisions: (limit = 50) =>
    get<{ decisions: Decision[] }>(`${pbase()}/api/decisions${queryString({ limit })}`).then(
      (r) => r.decisions ?? [],
    ),
  decide: (reason = 'operator') => post<{ queued: boolean }>(`${pbase()}/api/decide`, { reason }),
  pauseDecider: () => post<DeciderStatus>(`${pbase()}/api/decider/pause`),
  resumeDecider: () => post<DeciderStatus>(`${pbase()}/api/decider/resume`),
  // The whole-project switch: pause the commander and cancel every live run.
  // Canceled directions go back to pending, so Resume lets the commander pick
  // them up again.
  closeoutProject: () => post<{ paused: boolean; completed: boolean }>(`${pbase()}/api/project/closeout`),
  pauseProject: () =>
    post<{ paused: boolean; canceled: number }>(`${pbase()}/api/project/pause`),
  // goal_id attaches the open goal to the next commander round: the explicit
  // "continue current goal" scope. It is not a new goal and not a chat message.
  resumeProject: (goal_id?: string) =>
    post<{ paused: boolean; scope_goal_id?: string }>(
      `${pbase()}/api/project/resume`,
      goal_id ? { goal_id } : {},
    ),
  // The explicit operator action for a finished or parked engagement: append a
  // new independent goal and, when the project is in a resumable stand-by
  // (completion or manual pause), clear the pause and wake the commander. It is
  // NOT ordinary chat: a plain message never resumes a paused project. The
  // idempotency_key is required so a retry (lost response / failed resume) finds
  // the goal it already created instead of appending a second one.
  appendGoal: (body: AppendGoalRequest) =>
    post<AppendGoalResult>(`${pbase()}/api/project/append-goal`, body),

  runs: () => get<{ runs: Run[] }>(`${pbase()}/api/runs`).then((r) => r.runs ?? []),
  // Project-level token accounting: commander / executor(+support) / judge,
  // plus the combined and all-in totals. Absent fields are unknown, not zero.
  usageBreakdown: (options?: RequestOptions) =>
    get<UsageBreakdown>(`${pbase()}/api/usage/breakdown`, options),

  // Participant conversation. Messages are records, not facts: the console shows
  // the delivery state ("accepted" is not "read") and never feeds them to the
  // graph.
  messages: (limit = 200) =>
    get<{ messages: Message[] }>(`${pbase()}/api/messages${queryString({ limit })}`).then((r) => r.messages ?? []),
	operatorMessages: (after: number) => get<{messages:Message[];next:number;more:boolean}>(`${pbase()}/api/operator-messages${queryString({ after })}`),
	operatorMessagesSeen:(through:number)=>post<{read:number}>(`${pbase()}/api/operator-messages/seen`,{through}),

  // Per-recipient queue pressure + routing checkpoints. The operator sees every
  // queue; a participant credential is narrowed to its own by the server.
  communication: (recipient = '') =>
    get<CommunicationStatus>(`${pbase()}/api/communication${queryString({ recipient })}`),

  ioaObservations: () => get<IOAObservations>(`${pbase()}/api/ioa/events`),
  instanceIOAObservations: () => get<IOAObservations>('/api/ioa/events'),
  participants: () =>
    get<{ participants: Participant[] }>(`${pbase()}/api/participants`).then(
      (r) => r.participants ?? [],
    ),

  // The executor's view: the brief, the graph it was handed, and what it
  // declared/asked. Read from the case dir, nothing extra is stored.
  runContext: (id: string, options?: RequestOptions) =>
    get<RunContext>(`${pbase()}/api/runs/${encodeURIComponent(id)}/context`, options),

  // Tool-call leaderboard + installed arsenal binaries (api/tools.go).
  tools: () => get<ToolsResponse>(`${pbase()}/api/tools`),
  runtimeTools: () => get<RuntimeToolsResponse>(`${pbase()}/api/tools/runtime`),
  events: (id: string, limit = 300, includeInput = false, options?: RequestOptions) =>
    get<{ events: Event[] }>(
      `${pbase()}/api/runs/${encodeURIComponent(id)}/events${queryString({ limit, include_input: includeInput })}`,
      options,
    ).then((r) => r.events ?? []),
  cancelRun: (id: string) =>
    post<{ canceled: string }>(`${pbase()}/api/runs/${encodeURIComponent(id)}/cancel`),
  // Read-only native-session projections: the harness session, not the ledger,
  // is the authority for what the model actually did. 409 no_native_session /
  // 503 host_unavailable / 502 host_read_failed say why a read could not happen.
  runHistory: (id: string, afterCursor = '', limit = 100) =>
    get<RunHistoryPage>(
      `${pbase()}/api/runs/${encodeURIComponent(id)}/history${queryString({ after_cursor: afterCursor, limit })}`,
    ),
  runCommands: (id: string) =>
    get<RunCommandsResponse>(`${pbase()}/api/runs/${encodeURIComponent(id)}/commands`),
  // Playable/previewable URL of one preserved medium. The name is generated by
  // aopexec.PreserveMedia (one path element), so a path is reduced to its
  // basename; the server rejects anything that is not a plain file name.
  runMediaURL: (id: string, name: string) =>
    `${pbase()}/api/runs/${encodeURIComponent(id)}/media/${encodeURIComponent(name)}`,
  // Operator "ignore" for a finished direction's non-fatal handoff warnings.
  // The run rows stay in the ledger; the status-page count and board stop
  // counting this run. Refused by the server for a still-running/failed intent.
  dismissRunWarnings: (id: string) =>
    post<Run>(`${pbase()}/api/runs/${encodeURIComponent(id)}/warnings/dismiss`),

  reports: () => get<{ reports: ReportMeta[] }>(`${pbase()}/api/reports`).then((r) => r.reports ?? []),
  // Advisory precheck run before a report is queued: how many conclusions would
  // land, how many are still unverified (they can only be candidates), how many
  // are rejected, and whether a report is already being written (D100).
  reportPrecheck: () => get<ReportPrecheck>(`${pbase()}/api/reports/precheck`),
  createReport: () => post<{ queued: boolean; run_id: string }>(`${pbase()}/api/reports`),
  report: (id: string) => get<ReportMeta>(`${pbase()}/api/reports/${encodeURIComponent(id)}`),
  // A body is only served for a report that passed its own checks and still
  // matches the ledger; anything else comes back as 409 with the reason, which
  // is shown as-is. checks.json is readable in every state.
  reportFileText: async (id: string, path: string, options?: RequestOptions) => {
    const url = `${pbase()}/api/reports/${encodeURIComponent(id)}/file${queryString({ path })}`
    const res = await fetch(url, { signal: abortSignalFor(options) })
    if (!res.ok) throw await refusal(res, `无法读取 ${path}`)
    return res.text()
  },
  reportFileURL: (id: string, path: string) =>
    `${pbase()}/api/reports/${encodeURIComponent(id)}/file${queryString({ path })}`,
  // The rows a ready report actually carries, read from the same deliverable
  // file the operator can download. The console shows their `severity`; it does
  // not grade. Only a ready report has this file readable (the delivery gate
  // returns 409 otherwise), so callers gate on status before asking.
  reportFindings: async (id: string, options?: RequestOptions): Promise<ReportFinding[]> => {
    const url = `${pbase()}/api/reports/${encodeURIComponent(id)}/file${queryString({ path: 'findings.json' })}`
    const res = await fetch(url, { signal: abortSignalFor(options) })
    if (!res.ok) throw await refusal(res, '无法读取 findings.json')
    const body = await res.json().catch(() => null)
    return Array.isArray(body) ? (body as ReportFinding[]) : []
  },

  // The template library is read server-side filtered/paged: total is the whole
  // library, matched is how many rows satisfy q (before offset/limit). Omit
  // every param to get the whole list, which is what older callers expect.
  pocs: (params: { q?: string; offset?: number; limit?: number } = {}) => {
    const suffix = queryString({
      q: params.q,
      // 0 is the default page: keep it off the wire exactly as before.
      offset: params.offset || undefined,
      limit: params.limit || undefined,
    })
    return get<{ dir: string; pocs: Poc[] | null; total?: number; matched?: number }>(
      `/api/pocs${suffix}`,
    )
  },
  importPocs: (path: string) => post<ImportResult>('/api/pocs/import', { path }),
  importTraffic: (path: string) => post<ImportResult>(`${pbase()}/api/traffic/import`, { path }),
  httpFlows: (q = '', offset = 0, options?: RequestOptions) =>
    get<{flows: HTTPFlowRecord[]; total: number}>(
      `${pbase()}/api/traffic/flows${queryString({ q, offset, limit: 100 })}`,
      options,
    ),
  httpFlow: (ref: string) => get<HTTPFlow>(`${pbase()}/api/traffic/flow${queryString({ ref })}`),
  httpFlowURL: (ref: string) =>
    `${pbase()}/api/traffic/flow${queryString({ ref, download: 1 })}`,
  importAssets: (path: string) => post<ImportResult>(`${pbase()}/api/assets/import`, { path }),

  assets: () => get<{ hosts: AssetHost[] }>(`${pbase()}/api/assets`).then((r) => r.hosts ?? []),

  exportBundle: () =>
    post<{ name: string; path: string; size: number; files: number; missing: number }>(
      `${pbase()}/api/bundles/export`,
    ),
  bundleURL: (name: string) => `${pbase()}/api/bundles/${encodeURIComponent(name)}`,
}

/** Switch the page to another project. A full navigation keeps one source of
 *  truth: the project lives in the URL, not in client state. */
export function gotoProject(id: string) {
  window.location.assign(`/p/${encodeURIComponent(id)}/`)
}
