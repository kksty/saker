// The engagement graph: facts are nodes, intents are steps, goals are nodes.
//
// An intent is the work between what the engagement knew and what it produced:
//   context fact(s) ─▶ step(intent) ─▶ produced fact
// When the run has not produced a fact yet, the step points at a ghost ring; the
// ring is replaced by the fact node the moment the ledger receives it. Initial
// intents have no context fact, so their step starts at the project node (the
// operator's brief) instead.
//
// Steps are real graph nodes rather than labels on an edge: that is what makes
// "what did we know → what did this direction produce → what came next" a chain
// instead of a fan of lines from the brief. The step is still one per intent, so
// a direction with several produced facts funnels them through one point.
//
// Every node carries a structural `depth` (BFS hops from the project brief). It
// is derived from from_facts/parent_intent, never from a hardcoded phase name:
// the doctrine that names stages is a content asset, not layout code.
//
// Everything here is a pure function of the ledger snapshot, so the canvas can be
// rebuilt on every poll without keeping state of its own.

import type { Fact, Goal, Intent, ProjectInfo, Run } from '@/lib/types'
import { intentStopped } from './status.ts'

export type NodeType = 'boundary' | 'fact' | 'intent' | 'step' | 'goal' | 'project' | 'ghost'

export interface GraphNode {
  boundary?: string
  /** Why a boundary node is not a real node (internal/retrieval/graph_page.go):
   *  `not_loaded` means the ledger has it but this page/snapshot does not, so it
   *  can be expanded; `missing` means the ledger no longer has the referenced id;
   *  `rejected` means it exists but was explicitly negated. The three must stay
   *  distinguishable on the map: expanding a missing reference would promise
   *  content that does not exist. */
  boundaryState?: string
  id: string
  type: NodeType
  label: string
  sub?: string
  kind?: string // fact kind
  status?: string // intent status / goal met|open
  /** The ledger's sticky stop (`intent.hold`): an explicit stop, not a verdict.
   *  The map paints it with `INTENT_COLORS.stopped` and the panel says 已停止,
   *  so a policy stop is never read as a failed attempt. */
  stopped?: boolean
  /** Fact triage label (critical|high|medium|low, absent = unlabelled), read straight from
   *  the server page. Shown in the inspector; never used to rank or recolour. */
  severity?: string
  x: number
  y: number
  vx: number
  vy: number
  degree: number
  /** Rendered half-width/half-height, set once the label is known. */
  w: number
  h: number
  /**
   * The pill label wrapped to its measured box (2–3 lines), set by the canvas's
   * measure pass together with w/h. Absent on fact/ghost dots and on models that
   * were never measured (tests): the painter then falls back to one fitted line.
   */
  labelLines?: string[]
  /** Hops from the project brief (project = 0). Set by buildGraph. */
  depth: number
  /** Claim count / attempts for a step; >1 means the direction was retried. */
  attempts?: number
  /** The latest run continued an earlier session (resume retry / steer / wake). */
  retried?: boolean
  /** Deterministic ordering inside a layer (creation time). */
  orderTime?: string
  /** Non-fatal run warnings carried by this intent. The step/ghost node keeps
   *  the whole list (it is that run's record); a fact node keeps only the
   *  warnings that name it, so one bad citation cannot look like a defect of
   *  every sibling fact. */
  warnings?: string[]
  /** The model this direction actually ran on (the run's resolved model, else
   *  the instance's unified-gateway model). Shown on the step pill. */
  model?: string
  /** 发起者 provenance (second channel). Set on step AND ghost nodes: the ghost
   *  is the same direction's un-produced end, so it must carry the same mark. */
  origin?: IntentOrigin
  /** The resolved direction that launched this one (executor origin only). When
   *  the creator intent is not in this snapshot the edge is skipped and this is
   *  absent — the console never invents an origin. */
  originIntent?: { id: string; title: string }
  /** Hops along the origin (发起) lineage, not the spawn tree. 0 = a commander/
   *  operator root; undefined = the chain cannot be resolved from this snapshot. */
  lineageDepth?: number
  /** Native subagent aggregate of this direction's latest executor run that
   *  actually called one. A run-internal helper, not a ledger run: no separate
   *  run/usage/fact is ever derived from it. Absent = no calls recorded. */
  helpers?: { calls: number; active: number; durationMs: number }
  /** The claim asserted a newer version than the latest review proved. */
  needsReview?: boolean
  /** Latest proved claim version; 0 = never reviewed (fact_meta contract). */
  reviewedClaimVersion?: number
  /** Number of evidence rows (survives projection, which clears evidence_ids). */
  evidenceCount?: number
  /** Number of openable refs (survives projection, which clears refs). */
  refCount?: number
  meta?: { fact?: Fact; intent?: Intent; goal?: Goal; project?: ProjectInfo }
}

/**
 * Who launched a direction (发起者), read only from the ledger's
 * created_by_participant_id. This is the graph's *second* channel: status colour
 * stays the first one.
 *
 * The string contract is the backend's (internal/collab): an intent's
 * participant id is "pt_<intentID>", the commander and the operator are the
 * exact strings "commander"/"operator". A row that predates the field, or names
 * a participant this console cannot classify, is `unknown` — never guessed into
 * one of the three roles.
 */
export type IntentOrigin = 'commander' | 'operator' | 'executor' | 'unknown'

/** Classify one intent's initiator from the wire field alone. */
export function intentOrigin(it: Pick<Intent, 'created_by_participant_id'>): IntentOrigin {
  const by = (it.created_by_participant_id ?? '').trim()
  if (by === 'commander') return 'commander'
  if (by === 'operator') return 'operator'
  // `pt_` alone is not a participant id: an intent participant always carries a
  // non-empty intent id after the prefix.
  if (by.startsWith('pt_') && by.length > 3) return 'executor'
  return 'unknown'
}

/** Human label for a provenance class; a legacy/absent row reads 未记录. */
export function originLabel(origin: IntentOrigin | undefined): string {
  switch (origin) {
    case 'commander':
      return '指挥官'
    case 'operator':
      return '操作员'
    case 'executor':
      return '执行者'
    default:
      return '未记录'
  }
}

/**
 * What an edge means, not merely where it goes:
 *   intent    — a step's produced fact / ghost ring (status colour carries it)
 *   premise   — a hard prerequisite (from_fact_ids): the step cannot be read
 *               without this fact; a missing/rejected premise blocks dispatch
 *   reference — an explicit reading choice (context_fact_ids); context, never a
 *               gate, and must not look like a premise
 *   pin       — an explicitly pinned read (pinned_fact_ids); still context, but
 *               the operator asked to see it first
 *   supports  — a fact that backs an achieved goal's evidence
 *   spawn     — parent direction delegated the child direction
 *   origin    — an executor launched a parentless peer direction (平级发起);
 *               derived from created_by/delegated_by_run, never a ledger row and
 *               never part of layout/depth/degree (see buildGraph)
 *   brief/host — project boundary and service-on-host grouping
 */
export type EdgeKind =
  | 'intent'
  | 'premise'
  | 'reference'
  | 'pin'
  | 'supports'
  | 'brief'
  | 'host'
  | 'goal'
  | 'spawn'
  | 'origin'

/**
 * The Chinese name of a node's kind for text surfaces (node panel, list rows).
 * The canvas itself never prints this: it uses shape and colour. A fact shows the
 * ledger's kind on its own (`vuln`, `artifact`): the badge already sits inside a
 * fact panel, so a 事实 prefix only shortened the line and said nothing.
 */
export function nodeTypeLabel(type: NodeType, factKind?: string): string {
  switch (type) {
    case 'step':
    case 'intent':
    case 'ghost':
      return '意图'
    case 'fact':
      return factKind || '事实'
    case 'goal':
      return '目标'
    case 'project':
      return '项目'
    case 'boundary':
      return '引用'
    default:
      return type
  }
}

/** One-word relation label for hover/legend; the produced-fact fan has none. */
export function edgeKindLabel(kind: EdgeKind): string {
  switch (kind) {
    case 'premise':
      return '必要前提'
    case 'reference':
      return '引用'
    case 'pin':
      return '固定'
    case 'supports':
      return '支持目标'
    case 'spawn':
      return '委托（等待）'
    case 'origin':
      return '发起'
    case 'brief':
      return '项目范围'
    case 'host':
      return '宿主机'
    case 'goal':
      return '目标证据'
    default:
      return ''
  }
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  kind: EdgeKind
  /** Present on intent/spawn edges: the work this edge stands for. */
  intent?: Intent
  /** On spawn edges: the parent intent that created the child (血缘分叉). */
  parentIntent?: string
  label?: string
  status?: string
  /** The direction behind this edge was explicitly stopped (intent.hold). */
  stopped?: boolean
  /** The premise is not a gate admission can accept: its source fact is an
   *  unreviewable kind or not verified, or the snapshot cannot open the source
   *  reference at all. Drawn as a dashed blocked gate instead of the normal
   *  hard-gate blue. */
  blocked?: boolean
}

/** Fact kinds an independent review can re-prove, so admission may accept them
 *  as necessary premises (model.ReviewableKind). A note, a clue or an unknown
 *  kind is reference material: it can never satisfy a `from_fact_ids` gate,
 *  whatever its status says. Kept as a copy of the Go rule on purpose: the
 *  graph must not learn a second, looser admission rule. */
export const REVIEWABLE_FACT_KINDS: ReadonlySet<string> = new Set([
  'host',
  'service',
  'vuln',
  'cred',
  'loot',
  'artifact',
])

/** Per-fact counters the page carries when the fact body is projected away. */
export interface FactMeta {
  id: string
  needs_review: boolean
  reviewed_claim_version: number
  evidence_count: number
  ref_count: number
}

export interface GraphModel {
  nodes: GraphNode[]
  edges: GraphEdge[]
  byId: Map<string, GraphNode>
  maxDepth: number
  /** edge.id → edge; selection/inspector lookup without scanning the list. */
  edgesById: Map<string, GraphEdge>
  /** intent.id → its step node; the step is the direction's node on the map. */
  stepByIntent: Map<string, GraphNode>
  /** fact.id (unprefixed) → the fact body; the vision for detail re-fetch. */
  factsById: Map<string, Fact>
  /** node id → upstream neighbour ids (edge sources); null-safe BFS lookup. */
  incoming: Map<string, string[]>
  /** node id → downstream neighbour ids (edge targets). */
  outgoing: Map<string, string[]>
}

/** Deterministic pseudo-random from an id, so a reload does not reshuffle. */
function hash(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0) / 4294967295
}

function place(n: GraphNode, i: number, total: number) {
  const angle = (i / Math.max(total, 1)) * Math.PI * 2
  const ring = 150 + Math.sqrt(i + 1) * 74
  const jitter = (hash(n.id + 'x') - 0.5) * 90
  const jitter2 = (hash(n.id + 'y') - 0.5) * 90
  n.x = Math.cos(angle) * ring + jitter
  n.y = Math.sin(angle) * ring + jitter2
  n.vx = 0
  n.vy = 0
}

export const stepID = (intentID: string) => `s:${intentID}`
export const ghostID = (intentID: string) => `w:${intentID}`

/** The ledger clips a warned fact title at 80 runes and appends "…"
 *  (internal/ledger/ingest.go clipRunes). The warning text is the only place a
 *  per-fact warning says which fact it is about, so reproduce that clip exactly. */
function clipFactTitle(title: string): string {
  const trimmed = title.trim()
  const runes = Array.from(trimmed)
  return runes.length <= 80 ? trimmed : runes.slice(0, 80).join('') + '…'
}

/** A handoff warning names the fact whose citation failed ("事实「…」的…").
 *  Attaching a run's whole warning list to every fact made one bad evidence path
 *  look like a defect of every sibling fact; a fact node keeps only the warnings
 *  that name its own title, and the direction node keeps the rest. */
function warningsForFact(all: string[] | undefined, title: string): string[] | undefined {
  if (!all || all.length === 0) return undefined
  const prefix = `事实「${clipFactTitle(title)}」`
  const own = all.filter((w) => w.startsWith(prefix))
  return own.length > 0 ? own : undefined
}

export function buildGraph(
  facts: Fact[],
  intents: Intent[],
  goals: Goal[],
  project?: ProjectInfo,
  runs: Run[] = [],
  defaultModel = '',
  boundaries: {id:string;state:string}[] = [],
  sourceEdges: {source:string;target:string;kind:string}[] = [],
  details: {id:string;omitted:string[]}[] = [],
  factMeta: FactMeta[] = [],
): GraphModel {
  const nodes: GraphNode[] = []
  const edges: GraphEdge[] = []
  const byId = new Map<string, GraphNode>()

  // Runs are the only place warnings are recorded. Aggregate them per intent so
  // every node that belongs to that direction can carry the same badge.
  const warningsByIntent = new Map<string, string[]>()
  const latestRun = new Map<string, Run>()
  // Native subagent calls are a run-internal aggregate (model.Run's fields are
  // written by the backend as the harness reports them). Only an executor run
  // (purpose empty) belongs to the direction itself: verifier/report/commander
  // runs are not this direction's turn. A run with no call is not a record.
  const helpersByIntent = new Map<string, { calls: number; active: number; durationMs: number }>()
  const helperRunAt = new Map<string, string>()
  const runByID = new Map<string, Run>()
  // The model is a property of the executor run, not of the latest run: verifier
  // and report runs hang off the same intent but never report a model, so the
  // pill must read the newest run that actually has one.
  const modeledRun = new Map<string, Run>()
  for (const r of runs) {
    runByID.set(r.id, r)
    if (!r.intent_id) continue
    const prev = latestRun.get(r.intent_id)
    if (!prev || (r.started_at ?? '') > (prev.started_at ?? '')) latestRun.set(r.intent_id, r)
    if ((r.model ?? '').trim() || r.resolved_profile) {
      const mprev = modeledRun.get(r.intent_id)
      if (!mprev || (r.started_at ?? '') > (mprev.started_at ?? '')) modeledRun.set(r.intent_id, r)
    }
    for (const w of r.warnings ?? []) {
      const list = warningsByIntent.get(r.intent_id) ?? []
      if (!list.includes(w)) list.push(w)
      warningsByIntent.set(r.intent_id, list)
    }
    // "This turn used helpers": the newest executor run that reports any call.
    // A newer zero-call run does not erase the last turn's aggregate; the badge
    // is about what was recorded, not about the latest empty run.
    if (r.purpose || (r.native_subagent_calls ?? 0) <= 0) continue
    const at = r.started_at ?? ''
    if (helperRunAt.has(r.intent_id) && at <= (helperRunAt.get(r.intent_id) ?? '')) continue
    helperRunAt.set(r.intent_id, at)
    helpersByIntent.set(r.intent_id, {
      calls: r.native_subagent_calls ?? 0,
      active: r.native_subagent_active ?? 0,
      durationMs: r.native_subagent_duration_ms ?? 0,
    })
  }
  const addNode = (n: GraphNode) => {
    nodes.push(n)
    byId.set(n.id, n)
  }

  const projectID = project ? `p:${project.id}` : 'p:project'
  addNode({
    id: projectID,
    type: 'project',
    label: project?.name ?? '项目',
    sub: project?.scope || project?.description,
    status: project ? 'project' : undefined,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 86,
    h: 20,
    depth: 0,
    meta: { project },
  })

  const factsOutByIntent = new Map<string, Fact[]>()
  for (const f of facts) {
    if (!f.intent_id) continue
    const list = factsOutByIntent.get(f.intent_id) ?? []
    list.push(f)
    factsOutByIntent.set(f.intent_id, list)
  }
  const factIDs = new Set(facts.map((f) => f.id))
  const intentByID = new Map(intents.map((it) => [it.id, it]))
  const factMetaByID = new Map(factMeta.map((m) => [m.id, m]))
  // A details row says the server omitted a field from an intent node. The
  // premise source list is asked once per intent; index the rows once instead
  // of scanning all details inside sourcesOf (was O(intents × details)).
  const omittedFromFacts = new Set(
    details.filter((d) => d.omitted.includes('from_fact_ids')).map((d) => d.id),
  )
  // Chronological order is the production order: a child intent is created after
  // its parent, so one pass can resolve depths without guessing.
  const ordered = intents.toSorted((a, b) => {
    const at = a.created_at ?? ''
    const bt = b.created_at ?? ''
    if (at !== bt) return at < bt ? -1 : 1
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
  })

  /** Where an intent's work starts: its context facts, else the brief. */
  const sourcesOf = (it: Intent): string[] => {
    const context = (it.from_fact_ids ?? [])
      .map((id) => `f:${id}`)

    if (omittedFromFacts.has(`intent:${it.id}`)) return context
    return context.length ? context : [projectID]
  }

  // ---- depth (structural, not a phase name) ----
  const factDepth = new Map<string, number>()
  const stepDepth = new Map<string, number>()
  for (const it of ordered) {
    const sources = sourcesOf(it)
    let depth = 1
    for (const s of sources) {
      const srcDepth = s.startsWith('f:') ? factDepth.get(s.slice(2)) ?? 1 : 0
      depth = Math.max(depth, srcDepth + 1)
    }
    const parent = it.parent_intent_id ? intentByID.get(it.parent_intent_id) : undefined
    if (parent) depth = Math.max(depth, (stepDepth.get(parent.id) ?? 0) + 1)
    stepDepth.set(it.id, depth)
    for (const f of factsOutByIntent.get(it.id) ?? []) {
      if (!factDepth.has(f.id)) factDepth.set(f.id, depth + 1)
    }
  }

  // ---- 发起者 provenance (second channel, derived) ----
  // A parentless direction whose created_by names another direction's
  // participant ("pt_<intentID>") was launched by that executor as a peer (平级
  // 发布), not delegated as a child. The ledger does not carry an edge for it, so
  // the console derives one from its own fields: created_by first, then the run
  // that created the direction (delegated_by_run_id → runs → intent_id) as a
  // fallback. Unresolvable creators are skipped: a guessed line would claim
  // provenance the ledger does not prove.
  const originOf = new Map<string, Intent>()
  for (const it of intents) {
    if (it.parent_intent_id) continue // delegated children already hang off the parent
    if (intentOrigin(it) !== 'executor') continue
    const by = (it.created_by_participant_id ?? '').trim()
    // Only the pt_ contract names an intent participant; anything else is unknown.
    const direct = by.startsWith('pt_') && by.length > 3 ? intentByID.get(by.slice(3)) : undefined
    const run = it.delegated_by_run_id ? runByID.get(it.delegated_by_run_id) : undefined
    const viaRun = run?.intent_id ? intentByID.get(run.intent_id) : undefined
    const creator = direct && direct.id !== it.id ? direct : viaRun && viaRun.id !== it.id ? viaRun : undefined
    if (creator) originOf.set(it.id, creator)
  }
  // Origin lineage hops (NOT the spawn tree and NOT the structural depth): a
  // commander/operator direction is a root (0), a resolved peer-launch is one
  // hop below its initiator. A delegated child or an unresolved row stays
  // undefined — the column shows "—" rather than a number the snapshot cannot
  // support.
  const lineageDepth = new Map<string, number>()
  const lineageWalk = new Set<string>()
  const lineageOf = (it: Intent): number | undefined => {
    if (lineageDepth.has(it.id)) return lineageDepth.get(it.id)
    if (lineageWalk.has(it.id)) return undefined // corrupt cycle: never invent depth
    lineageWalk.add(it.id)
    const creator = originOf.get(it.id)
    let depth: number | undefined
    if (creator) {
      const up = lineageOf(creator)
      depth = up === undefined ? undefined : up + 1
    } else if (intentOrigin(it) === 'commander' || intentOrigin(it) === 'operator') {
      depth = 0
    }
    if (depth !== undefined) lineageDepth.set(it.id, depth)
    return depth
  }
  for (const it of ordered) lineageOf(it)

  // ---- nodes ----

  for (const f of facts) {
    // The graph projection clears evidence_ids/refs from the fact body, so the
    // page carries their counts and the review state separately. Merge by id:
    // no meta row (e.g. a full projection) leaves the node's counters absent.
    const fm = factMetaByID.get(f.id)
    addNode({
      id: `f:${f.id}`,
      type: 'fact',
      label: f.title,
      sub: f.summary,
      kind: f.kind,
      status: f.status,
      severity: f.severity,
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 6,
      h: 6,
      depth: factDepth.get(f.id) ?? 1,
      orderTime: f.created_at,
      warnings: f.intent_id ? warningsForFact(warningsByIntent.get(f.intent_id), f.title) : undefined,
      ...(fm
        ? {
            needsReview: fm.needs_review,
            reviewedClaimVersion: fm.reviewed_claim_version,
            evidenceCount: fm.evidence_count,
            refCount: fm.ref_count,
          }
        : {}),
      meta: { fact: f },
    })
  }

  const stepByIntent = new Map<string, GraphNode>()
  for (const it of ordered) {
    const depth = stepDepth.get(it.id) ?? 1
    const run = latestRun.get(it.id)
    const modelRun = modeledRun.get(it.id)
    const factsOut = factsOutByIntent.get(it.id) ?? []
    // The executor run's own model wins (a handoff opens a fresh session); a
    // fresh intent with no run yet names the unified-gateway model, so every
    // pill has an answer.
    const ranModel = (modelRun?.model ?? '').trim()
    const step: GraphNode = {
      id: stepID(it.id),
      type: 'step',
      label: it.title,
      sub: it.description,
      status: it.status,
      stopped: intentStopped(it.hold),
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 7,
      h: 7,
      depth,
      attempts: it.attempts,
      retried: Boolean(run?.continued_from) || (it.attempts ?? 0) > 1,
      orderTime: it.created_at,
      warnings: warningsByIntent.get(it.id),
      model: ranModel || defaultModel,
      origin: intentOrigin(it),
      ...(originOf.has(it.id)
        ? { originIntent: { id: originOf.get(it.id)!.id, title: originOf.get(it.id)!.title } }
        : {}),
      ...(lineageDepth.has(it.id) ? { lineageDepth: lineageDepth.get(it.id)! } : {}),
      ...(helpersByIntent.has(it.id) ? { helpers: helpersByIntent.get(it.id)! } : {}),
      meta: { intent: it },
    }
    addNode(step)
    stepByIntent.set(it.id, step)
    if (factsOut.length === 0) {
      addNode({
        id: ghostID(it.id),
        type: 'ghost',
        label: it.title,
        sub: it.description,
        status: it.status,
        stopped: intentStopped(it.hold),
        x: 0,
        y: 0,
        vx: 0,
        vy: 0,
        degree: 0,
        w: 9,
        h: 9,
        depth: depth + 1,
        orderTime: it.created_at,
        warnings: warningsByIntent.get(it.id),
        model: step.model,
        origin: step.origin,
        ...(step.originIntent ? { originIntent: step.originIntent } : {}),
        ...(step.lineageDepth !== undefined ? { lineageDepth: step.lineageDepth } : {}),
        ...(step.helpers ? { helpers: step.helpers } : {}),
        meta: { intent: it },
      })
    }
  }

  const maxFactDepth = factDepth.size ? Math.max(...factDepth.values()) : 0
  const goalDepth = facts.length ? maxFactDepth + 1 : 1
  // Only a completed goal is a graph node: it is anchored to the fact that
  // carries its evidence. An unmet goal has no completed work to stand on, so it
  // is shown in the graph page's 终点 card instead of being drawn as a loose or
  // guessed node.
  for (const g of goals) {
    if (!g.met || g.closed) continue
    addNode({
      id: `g:${g.id}`,
      type: 'goal',
      label: g.statement,
      sub: g.start || g.quantified_target,
      status: 'met',
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 70,
      h: 16,
      depth: goalDepth,
      orderTime: (g as Goal & { created_at?: string }).created_at,
      meta: { goal: g },
    })
  }

  const boundaryState = new Map(boundaries.map(b => [b.id,b.state]))
  // A boundary leaf is a reference whose entity the snapshot does not carry. The
  // ledger's boundary stream can also name ids that ARE in the snapshot (for
  // example an open goal, which is deliberately not a map node). Drawing those
  // as "未载入引用" would invent an orphan ghost for a row the map already has,
  // so loaded rows are never turned into boundary leaves.
  const loadedRefIDs = new Set<string>([
    ...facts.map(f => `fact:${f.id}`),
    ...intents.map(it => `intent:${it.id}`),
    ...goals.map(g => `goal:${g.id}`),
  ])
  const pendingRefs = new Set<string>()
  for (const b of boundaries) if (!loadedRefIDs.has(b.id)) pendingRefs.add(b.id)
  for(const edge of sourceEdges){pendingRefs.add(edge.source);pendingRefs.add(edge.target)}
  for (const it of intents) for(const id of it.from_fact_ids ?? []) if(!factIDs.has(id)) pendingRefs.add(`fact:${id}`)
  for (const f of facts) if(f.intent_id && !intentByID.has(f.intent_id)) pendingRefs.add(`intent:${f.intent_id}`)
  const nodeID = (id:string) => id.startsWith('fact:') ? `f:${id.slice(5)}` : id.startsWith('intent:') ? stepID(id.slice(7)) : id.startsWith('goal:') ? `g:${id.slice(5)}` : id
  for (const id of pendingRefs) {
    if (loadedRefIDs.has(id)) continue
    if(!byId.has(nodeID(id))) {
      const state = boundaryState.get(id)
      // "The ledger cannot show this row here" is a different fact from "the
      // ledger says this id is gone". Only the first is an expandable leaf.
      const why = state === 'missing' ? '引用缺失' : state === 'rejected' ? '已否定' : state === 'not_loaded' ? '未载入' : '展开'
      addNode({id:nodeID(id),type:'boundary',label:`${id} · ${why}`,boundary:id,boundaryState:state,x:0,y:0,vx:0,vy:0,degree:0,w:9,h:9,depth:1})
    }
  }

  // ---- edges ----
  // A premise is a hard gate, and the graph must draw it as gate admission
  // actually answers — not as "an edge exists". Admission refuses a premise
  // that is an unreviewable kind or not verified at its current claim, and a
  // ledger reference the snapshot cannot open (missing / not loaded) is not a
  // proven premise either. Everything else (a reviewable kind verified at its
  // current assertion) is a gate admission may accept.
  const premiseSourceBlocked = (a: GraphNode): boolean => {
    if (a.type === 'boundary') return true
    if (a.type !== 'fact') return false
    return !REVIEWABLE_FACT_KINDS.has(a.kind ?? '') || a.meta?.fact?.status !== 'verified'
  }
  const link = (
    source: string,
    target: string,
    kind: EdgeKind,
    intent?: Intent,
    label?: string,
    parentIntent?: string,
    /** False for derived provenance lines: they are drawn/hit-tested but must
     *  not change a node's degree (degree is a layout input). */
    countDegree = true,
  ) => {
    const a = byId.get(source)
    const b = byId.get(target)
    if (!a || !b || source === target) return
    // The edge carries the gate's current state so the painter can show a
    // blocked premise (dashed red) instead of the usual premise blue.
    const blocked = kind === 'premise' && premiseSourceBlocked(a)
    edges.push({
      id: `${source}->${target}:${kind}:${intent?.id ?? ''}`,
      source,
      target,
      kind,
      intent,
      parentIntent,
      label,
      status: intent?.status,
      stopped: intentStopped(intent?.hold),
      blocked,
    })
    if (!countDegree) return
    a.degree++
    b.degree++
  }

  for (const it of ordered) {
    const sid = stepID(it.id)
    // A direction's own premise list is the hard prerequisite relation; it must
    // stay visually distinct from the reference/pin reads added below.
    for (const src of sourcesOf(it)) link(src, sid, 'premise', it)
    const factsOut = factsOutByIntent.get(it.id) ?? []
    if (factsOut.length === 0) {
      link(sid, ghostID(it.id), 'intent', it)
    } else {
      for (const f of factsOut) link(sid, `f:${f.id}`, 'intent', it)
    }
  }

  // Relation kinds the ledger can prove, mapped to their display meaning. The
  // console never invents a relation: 'produces' is the intent fan, and an
  // unknown kind stays the neutral intent edge it used to be.
  const sourceKind = (kind: string): EdgeKind => {
    switch (kind) {
      case 'delegates':
        return 'spawn'
      case 'premise':
        return 'premise'
      case 'reference':
        return 'reference'
      case 'pin':
        return 'pin'
      case 'supports':
        return 'supports'
      default:
        return 'intent'
    }
  }
  const intentAt = (nodeID: string): Intent | undefined => {
    const prefix = 'intent:'
    return nodeID.startsWith(prefix) ? intentByID.get(nodeID.slice(prefix.length)) : undefined
  }
  // Dedupe by the full relation, not just the endpoint pair: a fact can be both
  // a reference and a pin of the same direction, and neither may swallow the
  // other. The synthesized intent/premise edges still dedupe by pair+kind.
  const linkedEdges = new Set(edges.map(e => `${e.source}/${e.target}/${e.kind}`))
  for (const e of sourceEdges) {
    const source = nodeID(e.source), target = nodeID(e.target)
    const kind = sourceKind(e.kind)
    if (!linkedEdges.has(`${source}/${target}/${kind}`)) {
      // An edge selection opens the direction panel: carry the intent for any
      // edge whose endpoint is a step. A supports edge (fact→goal) has none.
      link(source, target, kind, intentAt(e.target) ?? intentAt(e.source), undefined, kind === 'spawn' ? intentAt(e.source)?.id : undefined)
      linkedEdges.add(`${source}/${target}/${kind}`)
    }
  }

  // Spawn lineage: a delegated child hangs off its parent's output anchor. The
  // child step is one layer deeper — that is the visible "branch" of the tree.
  const anchorOf = (it: Intent): string => {
    const out = factsOutByIntent.get(it.id) ?? []
    if (out.length > 0) return `f:${out[0].id}`
    if (byId.has(ghostID(it.id))) return ghostID(it.id)
    return stepID(it.id)
  }
  for (const it of intents) {
    const parentID = it.parent_intent_id
    if (!parentID) continue
    const parent = intentByID.get(parentID)
    if (!parent) continue
    link(anchorOf(parent), stepID(it.id), 'spawn', it, edgeKindLabel('spawn'), parentID)
  }

  // 发起边: executor-launched peer directions point back at their initiator.
  // Drawn as a low-key dashed line with an open arrow (render.ts) and excluded
  // from degree/layout/depth: it is provenance, not a hop in the work chain.
  for (const it of intents) {
    const creator = originOf.get(it.id)
    if (!creator) continue
    link(stepID(creator.id), stepID(it.id), 'origin', it, edgeKindLabel('origin'), creator.id, false)
  }

  // service "10.0.0.5:8080" sits on host "10.0.0.5".
  // Index hosts by title once (first fact wins, the old facts.find semantics):
  // the pairing was O(F²) when every service scanned the whole fact list.
  const hostByTitle = new Map<string, Fact>()
  for (const f of facts) {
    if (f.kind === 'host' && !hostByTitle.has(f.title)) hostByTitle.set(f.title, f)
  }
  for (const f of facts) {
    if (f.kind !== 'service') continue
    const host = f.title.includes(':') ? f.title.slice(0, f.title.lastIndexOf(':')) : ''
    const hostFact = host ? hostByTitle.get(host) : undefined
    if (hostFact) link(`f:${hostFact.id}`, `f:${f.id}`, 'host')
  }

  // A met goal points at the fact that carries its evidence. That is the line
  // the map draws for a completed goal: short, on the evidence it rests on. The
  // brief edge stays only as a fallback for a goal with no evidence fact, so a
  // goal is never a single line dragged across the whole map. The owner map is
  // built once (first fact wins) instead of scanning the facts per goal.
  const evidenceOwner = new Map<string, Fact>()
  for (const f of facts) {
    for (const ev of f.evidence_ids ?? []) {
      if (!evidenceOwner.has(ev)) evidenceOwner.set(ev, f)
    }
  }
  // An unmet goal is deliberately not linked: the editor's 终点 card explains the
  // target, and a line would pretend the goal already stands on current work.
  for (const g of goals) {
    if (!g.met || g.closed) continue
    if (g.evidence_id) {
      const owner = evidenceOwner.get(g.evidence_id)
      if (owner) link(`f:${owner.id}`, `g:${g.id}`, 'goal')
    }
    if (!edges.some((e) => e.kind === 'goal' && e.target === `g:${g.id}`)) {
      link(projectID, `g:${g.id}`, 'brief')
    }
  }

  // Force-mode initial placement; the layered layout overrides these positions.
  const factsN = nodes.filter((n) => n.type === 'fact')
  const goalsN = nodes.filter((n) => n.type === 'goal')
  const ghostsN = nodes.filter((n) => n.type === 'ghost')
  const stepsN = nodes.filter((n) => n.type === 'step')
  factsN.forEach((n, i) => place(n, i, factsN.length))
  goalsN.forEach((n, i) => place(n, i, goalsN.length))
  ghostsN.forEach((n, i) => place(n, i, ghostsN.length))
  stepsN.forEach((n, i) => place(n, i, stepsN.length))

  let maxDepth = 0
  for (const n of nodes) maxDepth = Math.max(maxDepth, n.depth)

  // Indexes the view layer reuses instead of rescanning nodes/edges for every
  // lookup: selection, inspector, fact detail and neighbourhood focus.
  const edgesById = new Map(edges.map((e) => [e.id, e]))
  const factsById = new Map(facts.map((f) => [f.id, f]))
  const incoming = new Map<string, string[]>()
  const outgoing = new Map<string, string[]>()
  for (const e of edges) {
    if (!byId.has(e.source) || !byId.has(e.target)) continue
    outgoing.set(e.source, [...(outgoing.get(e.source) ?? []), e.target])
    incoming.set(e.target, [...(incoming.get(e.target) ?? []), e.source])
  }
  return { nodes, edges, byId, maxDepth, edgesById, stepByIntent, factsById, incoming, outgoing }
}

/**
 * Which subject the canvas should dim around.
 *
 * A selection is the subject; a hover only leads while nothing is selected.
 * That is what keeps a click's dim in place: while an edge selection let hover
 * win, moving the pointer off the line released the dim and the whole map — every
 * premise and produced-fact line — came back to full brightness, so a click
 * looked like it had done nothing at all.
 */
export function focusTarget(
  selected: string | null,
  hovered: string | null,
): string | null {
  return selected ?? hovered
}

/**
 * The two ends of a selected relation.
 *
 * Selecting a line asks "what does *this* connection join", so the relation is
 * the subject: its two endpoints stay lit and the rest of the map dims. Premise
 * edges and intent-produced-fact edges are peers here — the selection names one
 * line, not the direction it belongs to, so a sibling premise of the same step
 * dims like everything else. A selected *node* is the wider case and lives in
 * graph/chain.ts (`chainView`): the main path plus one hop of branch.
 */
export function edgeFocus(edge: Pick<GraphEdge, 'source' | 'target'>): Set<string> {
  return new Set([edge.source, edge.target])
}

/**
 * The relations a chain is made of.
 *
 * `premise` (fact → step: what the direction needed) and `intent` (step → the
 * fact it produced) are the only two edges work advances along. Spawn is
 * delegation lineage, brief is the project anchor, host groups an entity,
 * reference and pin are reading choices — none of them shows that one step
 * followed another, so none of them may lengthen a chain. Counting spawn would
 * turn "one long penetration" into the whole delegation tree.
 */
const CHAIN_KINDS: ReadonlySet<EdgeKind> = new Set<EdgeKind>(['premise', 'intent'])

interface ChainGraph {
  byId: Map<string, GraphNode>
  /** Each node's chain predecessors (what it derives from), deduped. */
  up: Map<string, string[]>
  /** Each node's chain successors (what it produced or what consumed it). */
  down: Map<string, string[]>
}

/**
 * Builds the chain-only adjacency. Ghost rings are a direction's un-produced
 * end, not a step of the work, so they never join a chain: a path that ends on
 * an empty ring would claim progress that produced nothing.
 */
function chainGraph(g: Pick<GraphModel, 'nodes' | 'edges'>): ChainGraph {
  const byId = new Map(g.nodes.map((n) => [n.id, n]))
  const up = new Map<string, Set<string>>()
  const down = new Map<string, Set<string>>()
  for (const e of g.edges) {
    if (!CHAIN_KINDS.has(e.kind)) continue
    if (!byId.has(e.source) || !byId.has(e.target)) continue
    if (byId.get(e.target)?.type === 'ghost' || byId.get(e.source)?.type === 'ghost') continue
    const upSet = up.get(e.target) ?? new Set<string>()
    upSet.add(e.source)
    up.set(e.target, upSet)
    const downSet = down.get(e.source) ?? new Set<string>()
    downSet.add(e.target)
    down.set(e.source, downSet)
  }
  const flatten = (m: Map<string, Set<string>>) =>
    new Map([...m].map(([k, v]) => [k, [...v]] as [string, string[]]))
  return { byId, up: flatten(up), down: flatten(down) }
}

/** A ghost ring stands for its direction, so selecting one selects that step. */
function chainSeed(byId: Map<string, GraphNode>, id: string): string | null {
  const node = byId.get(id)
  if (!node) return null
  if (node.type !== 'ghost') return id
  const step = `s:${id.slice(2)}`
  return byId.has(step) ? step : null
}

/**
 * Newer first, then smaller id.
 *
 * `created_at` is the only time a chain may order by: it is when the row was
 * recorded. `observed_at` is writer-declared, optional and part of the claim
 * fingerprint, so re-stating it is a new assertion and a node would move along
 * the chain. Missing time sorts last so an undated row never wins over a dated
 * one; the id keeps the choice the same on every replay.
 */
function chainOrder(a: GraphNode, b: GraphNode): number {
  const ta = a.orderTime ? Date.parse(a.orderTime) : Number.NaN
  const tb = b.orderTime ? Date.parse(b.orderTime) : Number.NaN
  const ha = !Number.isNaN(ta)
  const hb = !Number.isNaN(tb)
  if (ha && hb && ta !== tb) return tb - ta
  if (ha !== hb) return ha ? -1 : 1
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
}

/**
 * Longest chain reachable from each node in one direction, memoised for a
 * single call. A cycle cannot exist in the ledger (a step may not premise its
 * own output), so the in-progress guard is only there to keep a malformed
 * snapshot from recursing forever.
 */
function chainDepth(adj: Map<string, string[]>, byId: Map<string, GraphNode>) {
  const memo = new Map<string, number>()
  const visiting = new Set<string>()
  const walk = (id: string): number => {
    const hit = memo.get(id)
    if (hit !== undefined) return hit
    if (visiting.has(id)) return 0
    visiting.add(id)
    let best = 0
    for (const next of adj.get(id) ?? []) {
      if (!byId.has(next)) continue
      best = Math.max(best, 1 + walk(next))
    }
    visiting.delete(id)
    memo.set(id, best)
    return best
  }
  return walk
}

/** The neighbour that continues the longest chain; ties go to the newer row. */
function deepestNext(
  id: string,
  adj: Map<string, string[]>,
  byId: Map<string, GraphNode>,
  depth: (id: string) => number,
): string | null {
  let best: string | null = null
  let bestDepth = -1
  for (const next of adj.get(id) ?? []) {
    const node = byId.get(next)
    if (!node) continue
    const d = depth(next)
    if (d > bestDepth) {
      best = next
      bestDepth = d
      continue
    }
    if (d === bestDepth && best) {
      const incumbent = byId.get(best)
      if (incumbent && chainOrder(node, incumbent) < 0) best = next
    }
  }
  return best
}

/**
 * The single longest chain through a node: the deepest provenance above it and
 * the longest output below it, seed in the middle exactly once.
 *
 * This is what "只看主路径" means. A *closure* through a shared hub is a
 * different object: one premise that fifteen directions reuse connects half the
 * ledger, so a closure is the impact surface, never one penetration. The path
 * stays linear — one premise per step, one output per step — because that is
 * the only shape an operator can read as "this led to that".
 *
 * Ties are broken the same way at both ends (newest `created_at`, then smaller
 * id), so the same ledger and the same click always draw the same path.
 */
export function mainPath(
  g: Pick<GraphModel, 'nodes' | 'edges'>,
  id: string | null,
): string[] | null {
  if (!id) return null
  const { byId, up, down } = chainGraph(g)
  const seed = chainSeed(byId, id)
  if (!seed) return null
  const depthUp = chainDepth(up, byId)
  const depthDown = chainDepth(down, byId)
  const seen = new Set<string>([seed])
  const path = [seed]
  let cur: string = seed
  for (;;) {
    const next = deepestNext(cur, up, byId, depthUp)
    if (!next || seen.has(next)) break
    seen.add(next)
    path.unshift(next)
    cur = next
  }
  cur = seed
  for (;;) {
    const next: string | null = deepestNext(cur, down, byId, depthDown)
    if (!next || seen.has(next)) break
    seen.add(next)
    path.push(next)
    cur = next
  }
  return path
}
