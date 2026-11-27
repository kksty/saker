// The project shell (T1, docs/34 §1/§2): rail + merged view routing + project
// chrome + selection/hash deep link + the create-intent dialog. The 图谱 page
// body itself lives in components/graph/GraphPanel.tsx.
//
// Routing is the URL: `?view=<group>&tab=<tab>[&run=…][&intent=…]`. The old
// one-view-per-page ids (`?view=workers`, `?view=focus`, `?view=traffic` …) are
// aliases and still resolve; the shell normalizes them with `replaceState` so a
// bookmark keeps working but the address bar shows the merged form.
//
// Jumping away and coming back (docs/34 §2): leaving a view records its
// `{ tab, search, hash }` in view-memory; a rail click restores the remembered
// tab/record/graph selection, while explicit deep-link parameters always win.
// Browser Back needs no memory — the history entry already carries its URL.
//
// The ledger snapshot stays here on purpose: `useGraphSnapshot(graphActive,
// current)` must keep its data when the operator visits another panel and comes
// back (T2's contract), which only the always-mounted shell can do. GraphPanel
// receives it as a prop and owns every graph-local poll (runs / changes).

import { Suspense, lazy, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  BookMarked,
  BrainCircuit,
  Bug,
  FileText,
  Gauge,
  Globe,
  LayoutDashboard,
  MessagesSquare,
  Paperclip,
  Pause,
  Play,
  Plus,
  ScrollText,
  Server,
  ShieldCheck,
  Target,
  Waypoints,
} from 'lucide-react'
import {
  Badge,
  Button,
  Popover,
  PopoverContent,
  PopoverTrigger,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  useConfirm,
} from '@cyber/ui'
import { BoardCounters } from '@/components/BoardCounters'
import { ConsoleRail } from '@/components/ConsoleRail'
import { FreshnessPill } from '@/components/FreshnessPill'
import { AppendGoalDialog } from '@/components/AppendGoalDialog'
import { CreateIntentDialog } from '@/components/CreateIntentDialog'
import { SealGoalDialog, type SealGoalRef } from '@/components/SealGoalDialog'
import { ProjectChromeContext } from '@/components/Page'
import { PageSkeleton } from '@/components/PageSkeleton'
import { SideNav } from '@/components/SideNav'
import type { CommanderTab } from '@/views/CommanderView'
import type { EvidenceTab } from '@/views/EvidenceHub'
import type { RuntimeTab } from '@/views/ExecutionEnv'
import type { RunTab } from '@/views/RunCenter'
import type { StateTab } from '@/views/StateCenter'
import { api, gotoProject } from '@/lib/api'
import { projectCloseoutGoal, projectPauseState } from '@/lib/project-controls'
import { inboxBadge } from '@/lib/inbox-summary'
import { useGraphSnapshot } from '@/lib/graph-data'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import type { AsyncState } from '@/lib/hooks'
import { decodeLocationPart, locationPath, navigate } from '@/lib/navigation'
import { pageTitle } from '@/lib/page-title'
import { createPrefetchRegistry } from '@/lib/prefetch'
import type { BoardResponse, ProjectInfo } from '@/lib/types'
import { readViewMemory, writeViewMemory } from '@/lib/view-memory'

// ---------------------------------------------------------------------------
// Lazy chunks. One loader per rail item, used twice: `lazy` renders it and the
// rail's hover prefetch starts the download early. Keeping a single definition
// means the chunk a hover warms is always the chunk the panel imports. The
// merged groups load their TabBar + all child panels as one chunk (T2).
// ---------------------------------------------------------------------------
const PANEL_LOADERS = {
  overview: () => import('@/views/Overview').then((m) => ({ default: m.Overview })),
  graph: () => import('@/components/graph/GraphPanel').then((m) => ({ default: m.GraphPanel })),
  state: () => import('@/views/StateCenter').then((m) => ({ default: m.StateCenter })),
  docs: () => import('@/views/Documents').then((m) => ({ default: m.Documents })),
  messages: () => import('@/views/Messages').then((m) => ({ default: m.Messages })),
  runs: () => import('@/views/RunCenter').then((m) => ({ default: m.RunCenter })),
  access: () => import('@/views/Access').then((m) => ({ default: m.Access })),
  evidence: () => import('@/views/EvidenceHub').then((m) => ({ default: m.EvidenceHub })),
  vulns: () => import('@/views/Vulns').then((m) => ({ default: m.Vulns })),
  playbooks: () => import('@/views/Playbooks').then((m) => ({ default: m.Playbooks })),
  reports: () => import('@/views/Reports').then((m) => ({ default: m.Reports })),
  commander: () => import('@/views/CommanderView').then((m) => ({ default: m.CommanderView })),
  runtime: () => import('@/views/ExecutionEnv').then((m) => ({ default: m.ExecutionEnv })),
}

type PanelID = keyof typeof PANEL_LOADERS

// Module scope on purpose: the registry must survive re-renders so a second
// hover on the same row does not start a second download.
const prefetchRegistry = createPrefetchRegistry()
for (const id of Object.keys(PANEL_LOADERS) as PanelID[]) {
  prefetchRegistry.register(id, PANEL_LOADERS[id])
}

const Overview = lazy(PANEL_LOADERS.overview)
const GraphPanel = lazy(PANEL_LOADERS.graph)
const StateCenter = lazy(PANEL_LOADERS.state)
const Documents = lazy(PANEL_LOADERS.docs)
const Messages = lazy(PANEL_LOADERS.messages)
const RunCenter = lazy(PANEL_LOADERS.runs)
const Access = lazy(PANEL_LOADERS.access)
const EvidenceHub = lazy(PANEL_LOADERS.evidence)
const Vulns = lazy(PANEL_LOADERS.vulns)
const Playbooks = lazy(PANEL_LOADERS.playbooks)
const Reports = lazy(PANEL_LOADERS.reports)
const CommanderView = lazy(PANEL_LOADERS.commander)
const ExecutionEnv = lazy(PANEL_LOADERS.runtime)

// ---------------------------------------------------------------------------
// Navigation model (docs/34 §1.1). 12 rail items in 3 groups; five of them are
// merged pages whose tabs live in `?tab=`.
// ---------------------------------------------------------------------------
const NAV = [
  { id: 'overview', label: '概览', icon: LayoutDashboard },
  { id: 'graph', label: '图谱', icon: Waypoints },
  { id: 'state', label: '状态', icon: Gauge },
  { id: 'docs', label: '项目文档', icon: ScrollText },
  { id: 'messages', label: '通信', icon: MessagesSquare },
  { id: 'runs', label: '运行', icon: Activity },
  { id: 'access', label: '访问与交接', icon: ShieldCheck },
  { id: 'evidence', label: '证据', icon: Paperclip },
  { id: 'vulns', label: '漏洞', icon: Bug },
  { id: 'playbooks', label: '战场知识', icon: BookMarked },
  { id: 'reports', label: '报告', icon: FileText },
  { id: 'commander', label: '决策', icon: BrainCircuit },
  { id: 'runtime', label: '执行环境', icon: Server },
] as const

type ViewID = (typeof NAV)[number]['id']

const NAV_GROUPS: { label: string; items: readonly ViewID[] }[] = [
  { label: '项目现场', items: ['overview', 'graph', 'state', 'docs', 'messages', 'runs', 'access'] },
  { label: '发现与证据', items: ['evidence', 'vulns', 'playbooks', 'reports'] },
  { label: '指挥与运行', items: ['commander', 'runtime'] },
]

const GROUP_IDS = ['runs', 'runtime', 'commander', 'evidence', 'state'] as const
type GroupID = (typeof GROUP_IDS)[number]

// The tab unions come from the containers T2 owns: importing them (type-only,
// erased at build time) makes a renamed tab a compile error here instead of a
// dead link at runtime.
type TabID = RunTab | RuntimeTab | CommanderTab | EvidenceTab | StateTab

const RUN_TABS = ['runs', 'workers', 'actions'] as const satisfies readonly RunTab[]
const RUNTIME_TABS = ['nodes', 'terminal', 'repl', 'toolbox'] as const satisfies readonly RuntimeTab[]
const COMMANDER_TABS = ['decisions', 'memory'] as const satisfies readonly CommanderTab[]
const EVIDENCE_TABS = ['evidence', 'traffic', 'assets'] as const satisfies readonly EvidenceTab[]
const STATE_TABS = ['state', 'focus'] as const satisfies readonly StateTab[]

const GROUP_TABS: Record<GroupID, { tabs: readonly TabID[]; default: TabID }> = {
  runs: { tabs: RUN_TABS, default: 'runs' },
  runtime: { tabs: RUNTIME_TABS, default: 'nodes' },
  commander: { tabs: COMMANDER_TABS, default: 'decisions' },
  evidence: { tabs: EVIDENCE_TABS, default: 'evidence' },
  state: { tabs: STATE_TABS, default: 'state' },
}

// Old `?view=` ids → merged group + tab (docs/34 §1.1). Plain pages map to
// themselves, so this table is also the single list of accepted view ids.
const VIEW_ALIASES: Record<string, { view: ViewID; tab?: TabID }> = {
  overview: { view: 'overview' },
  graph: { view: 'graph' },
  state: { view: 'state' },
  docs: { view: 'docs' },
  focus: { view: 'state', tab: 'focus' },
  messages: { view: 'messages' },
  runs: { view: 'runs' },
  workers: { view: 'runs', tab: 'workers' },
  actions: { view: 'runs', tab: 'actions' },
  access: { view: 'access' },
  evidence: { view: 'evidence' },
  traffic: { view: 'evidence', tab: 'traffic' },
  assets: { view: 'evidence', tab: 'assets' },
  vulns: { view: 'vulns' },
  playbooks: { view: 'playbooks' },
  reports: { view: 'reports' },
  commander: { view: 'commander' },
  decisions: { view: 'commander', tab: 'decisions' },
  memory: { view: 'commander', tab: 'memory' },
  runtime: { view: 'runtime' },
  nodes: { view: 'runtime', tab: 'nodes' },
  terminal: { view: 'runtime', tab: 'terminal' },
  repl: { view: 'runtime', tab: 'repl' },
  toolbox: { view: 'runtime', tab: 'toolbox' },
}

// Record params name a row inside one view. They belong to the view that renders
// them, so switching away must not leave a stale pointer behind, and switching
// back may restore it from memory.
//
// `id`, `goal` and `fact` are the records that had no address of their own: an
// authorization request (Access), a stale goal receipt (状态) and one fact's
// claim/review/evidence panel (图谱). All are the same param-on-existing-view
// pattern as `run`/`intent` — no new route.
const RECORD_KEYS = ['run', 'run_group', 'intent', 'vuln', 'call', 'session', 'id', 'goal', 'fact', 'report'] as const
type RecordKey = (typeof RECORD_KEYS)[number]

const RECORD_OWNERS: Record<RecordKey, ViewID> = {
  run: 'runs',
  run_group: 'runs',
  intent: 'state',
  vuln: 'vulns',
  // One exact call to point at, and the session that owns it when the run id is
  // not known yet (an approval names its session, not its run).
  call: 'runs',
  session: 'runs',
  // One authorization request, opened on the page that owns access decisions.
  id: 'access',
  // One goal's receipt, opened on the 状态 page.
  goal: 'state',
  // One fact's claim version, reviews and original evidence, opened on the map.
  fact: 'graph',
  // One report's preview sheet, opened on the 报告 page. It is the return point
  // of the trace: leaving a report for its fact keeps this param on the history
  // entry, so Back reopens the same report instead of the bare list.
  report: 'reports',
}

/** `#new` opens the create-intent dialog; it is not a node selection. */
const DIALOG_HASH = '#new'

interface ViewMemory {
  tab?: TabID
  search?: string
  hash?: string
}

interface ViewTarget {
  view: ViewID
  tab?: TabID
}

function isGroup(view: ViewID): view is GroupID {
  return (GROUP_IDS as readonly string[]).includes(view)
}

function validTab(view: ViewID, tab: string | null | undefined): TabID | undefined {
  if (!tab || !isGroup(view)) return undefined
  return GROUP_TABS[view].tabs.includes(tab as TabID) ? (tab as TabID) : undefined
}

function resolveView(raw: string | null | undefined): ViewTarget | null {
  if (!raw) return null
  return VIEW_ALIASES[raw] ?? null
}

function ownsRecord(view: ViewID, key: RecordKey): boolean {
  return RECORD_OWNERS[key] === view
}

function restorableHash(hash: string | undefined): string {
  if (!hash || hash === '#' || hash === DIALOG_HASH) return ''
  return hash
}

/** Canonical URL: `view` first, `tab` only for merged groups, everything else kept. */
function buildViewURL(view: ViewID, tab: TabID | '', params: URLSearchParams, hash: string): string {
  const next = new URLSearchParams()
  next.set('view', view)
  if (tab) next.set('tab', tab)
  params.forEach((value, key) => {
    if (key === 'view' || key === 'tab') return
    next.append(key, value)
  })
  const search = next.toString()
  return `${window.location.pathname}${search ? `?${search}` : ''}${hash}`
}

interface ViewRoute {
  view: ViewID
  tab: TabID | ''
  params: URLSearchParams
  search: string
  hash: string
  /** The canonical form of the URL this route describes. */
  canonical: string
}

/** A record param already names the tab that renders it: `?view=runs&run=…`
 *  must open 运行记录 even when another tab of that group was visited last. */
function recordTab(view: ViewID, params: URLSearchParams): TabID | undefined {
  if (view === 'runs' && (params.get('run') || params.get('run_group'))) return 'runs'
  if (view === 'state' && (params.get('intent') || params.get('goal'))) return 'state'
  return undefined
}

/** Resolve the URL into the view the shell renders (alias + memory aware). */
function resolveRoute(location: URL, scope: string): ViewRoute {
  const params = new URLSearchParams(location.search)
  // `?vuln=<id>` alone still lands on the vulnerability library, otherwise a
  // shared detail link would stop on the overview page that never renders it.
  const raw = params.get('view') ?? (params.get('vuln') ? 'vulns' : '')
  const base = resolveView(raw) ?? { view: 'overview' as ViewID }
  const resolved = base.view
  // Explicit `?tab=` (or the alias's own tab) beats memory; memory beats the
  // group's default (docs/34 §1.1). A deep-linked record implies its own tab
  // before memory does, so `?view=runs&run=<id>` never lands on another tab.
  const tab: TabID | '' = isGroup(resolved)
    ? (validTab(resolved, base.tab) ??
      validTab(resolved, params.get('tab')) ??
      recordTab(resolved, params) ??
      validTab(resolved, readViewMemory<ViewMemory>(scope, resolved)?.tab) ??
      GROUP_TABS[resolved].default)
    : ''
  const hash = location.hash
  return {
    view: resolved,
    tab,
    params,
    search: location.search,
    hash,
    canonical: buildViewURL(resolved, tab, params, hash),
  }
}

interface Props {
  current: string
  projects: ProjectInfo[]
  board?: BoardResponse
  /** The shell's board poll. Pages render it and refresh through reload(); no
   *  panel polls /api/board on its own (that sent two requests per period). */
  boardState: AsyncState<BoardResponse>
  loadError?: string
  onRefresh: () => void
}

export function GraphView({ current, projects, board, boardState, onRefresh, loadError }: Props) {
  const confirm = useConfirm()
  const location = useConsoleLocation()
  const scope = `project:${current}`
  const route = useMemo(() => resolveRoute(location, scope), [location, scope])
  const view = route.view
  const tab = route.tab

  // The snapshot itself lives in the shell so leaving the map does not refetch
  // it; the map's own polls (runs / changes, and the 实时 reload) live in
  // GraphPanel and stop when the panel unmounts.
  const graphActive = view === 'graph'
  // The rail badge is the backend inbox summary, not a count of one category:
  // this one read (GET /api/operator-inbox) is the shell's only inbox poll and
  // feeds the badge, the overview card and the access page, so the numbers
  // cannot drift apart and one period is one request. When any category is
  // unknown — tool approval never asked, or the host unreachable — no exact
  // number is printed: 未查询 / 不可用 are answers too, and a badge reading 0
  // because nobody asked is a lie the operator acts on.
  const inbox = usePoll(api.operatorInbox, 6000)
  const badge = inboxBadge(inbox.data?.summary)
  const projectName = projects.find((p) => p.id === current)?.name
  const project = projects.find((p) => p.id === current)

  // Old bookmarks/aliases: rewrite the address bar to the merged form without
  // adding a history entry (docs/34 §2). Explicit record params and the graph
  // hash are preserved by the canonical builder; only `view`/`tab` change.
  useEffect(() => {
    if (route.canonical !== locationPath()) navigate(route.canonical, true)
  }, [route.canonical])

  // Leaving a view records the tab, record params and graph selection, so a
  // rail click can put the operator back where they were (docs/34 §2). Tab
  // changes inside one group are not a leave.
  const previousRoute = useRef<ViewRoute | null>(null)
  useEffect(() => {
    const previous = previousRoute.current
    if (previous && previous.view !== route.view) {
      writeViewMemory<ViewMemory>(scope, previous.view, {
        tab: previous.tab || undefined,
        search: previous.search,
        hash: previous.hash,
      })
    }
    previousRoute.current = route
  }, [route, scope])

  // The tab names the engagement and the panel: several projects/panels open in
  // parallel are otherwise indistinguishable tabs.
  useEffect(() => {
    document.title = pageTitle([projectName, NAV.find((n) => n.id === view)?.label])
  }, [projectName, view])

  const hash = route.hash ? decodeLocationPart(route.hash.replace(/^#/, '')) : ''
  // A fact record is addressed by `?fact=`; the hash stays the map's own node /
  // edge picker. Both produce the same selection (`f:<id>`), so the panel does
  // not care which one arrived.
  const factParam = route.params.get('fact') ?? ''
  const fromURL = hash && hash !== 'new' ? hash : factParam ? `f:${factParam}` : ''
  const [creating, setCreating] = useState(hash === 'new')
  const [selected, setSelected] = useState<string | null>(fromURL || null)
  useEffect(() => {
    setSelected(hash && hash !== 'new' ? hash : factParam ? `f:${factParam}` : null)
    setCreating(hash === 'new')
  }, [hash, factParam])
  const [deciding, setDeciding] = useState(false)
  const [pausing, setPausing] = useState(false)
  // The explicit "append a goal and continue" action (not chat): see
  // internal/api/goal_continue.go. goalRefreshSeq lets the always-mounted
  // overview reload its own goals poll after a successful append.
  const [appendGoalOpen, setAppendGoalOpen] = useState(false)
  // The pause banner's "认可达成并收口" opens the same seal dialog the overview
  // goal card uses. It is separate from "append a new goal": sealing the open
  // goal does not create a second one and does not resume the project.
  const [sealGoal, setSealGoal] = useState<SealGoalRef | null>(null)
  const [goalRefreshSeq, setGoalRefreshSeq] = useState(0)
  const [actionError, setActionError] = useState('')
  // The runs poll lives in GraphPanel (it only feeds the map model); its error
  // is still part of the page chrome, so the panel reports it up.
  const [runsError, setRunsError] = useState<string>()

  const graph = useGraphSnapshot(graphActive, current)

  // Every rail panel is a separate chunk. Hovering a row already starts its
  // download, but the first visit on a cold row (touch input, a fast click, a
  // freshly opened tab) still waited for the file before anything could render.
  // Once the shell has painted, warm the remaining chunks one at a time so the
  // first switch to each panel swaps content instead of showing a skeleton. The
  // files are small and served by the local control plane, and the delay keeps
  // the warm-up out of the first frame and off the initial requests.
  useEffect(() => {
    const ids = Object.keys(PANEL_LOADERS) as PanelID[]
    let index = 0
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const warm = () => {
      timer = undefined
      if (cancelled || index >= ids.length) return
      // A background tab keeps the queue: it resumes when the operator looks.
      if (document.visibilityState === 'hidden') {
        timer = setTimeout(warm, 1500)
        return
      }
      prefetchRegistry.prefetch(ids[index])
      index += 1
      timer = setTimeout(warm, 150)
    }
    timer = setTimeout(warm, 800)
    return () => {
      cancelled = true
      if (timer !== undefined) clearTimeout(timer)
    }
  }, [])

  /**
   * Selection is deep-linkable (the hash names the node) and it must be
   * instant: `navigate` commits synchronously, so this click updates React and
   * the URL in one task and the dock is painted on the next frame. The map
   * itself is untouched — the pin is an overlay, not a route change, so the
   * camera and the simulation never move.
   */
  function select(id: string | null) {
    setSelected(id)
    // A node pick replaces the fact record address: leaving `?fact=` beside a
    // hash would be two addresses for one selection.
    const params = new URLSearchParams(window.location.search)
    const hadFact = params.has('fact')
    if (hadFact) params.delete('fact')
    const search = hadFact ? (params.toString() ? `?${params.toString()}` : '') : window.location.search
    const next = id
      ? `${window.location.pathname}${search}#${encodeURIComponent(id)}`
      : `${window.location.pathname}${search}`
    navigate(next, true)
  }

  interface OpenViewOptions {
    /** Explicit tab (old alias or a tab click): wins over memory and default. */
    tab?: TabID
    /** Record params this navigation sets; `''` clears one. */
    params?: Partial<Record<RecordKey, string>>
    /** `''` clears the hash; `undefined` lets the graph memory decide. */
    hash?: string
    replace?: boolean
  }

  /**
   * Open a rail view. Precedence for the tab, the view's own record params and
   * the graph hash: explicit URL argument → view memory → group default. Only
   * the URL of the *current* view counts as explicit for the target (a stale
   * `?run=` from the page we are leaving must not follow us).
   */
  function openView(target: ViewID, options: OpenViewOptions = {}) {
    const group = isGroup(target)
    const memory = readViewMemory<ViewMemory>(scope, target)
    // Memory is for *returning* to a view. Staying inside it (re-clicking the
    // active rail row, switching its tabs) must read the live URL only, or an
    // old selection kept in memory would jump back in.
    const returning = route.view !== target

    const explicitTab = group
      ? (validTab(target, options.tab) ??
        (route.view === target ? validTab(target, route.tab) : undefined))
      : undefined
    const tab: TabID | '' = group
      ? (explicitTab ??
        (returning ? validTab(target, memory?.tab) : undefined) ??
        GROUP_TABS[target].default)
      : ''

    // Keep the operator's unrelated params (`case=` …); every record param is
    // rebuilt below from the target's point of view.
    const params = new URLSearchParams(window.location.search)
    for (const key of RECORD_KEYS) params.delete(key)

    const remembered = returning ? new URLSearchParams(memory?.search ?? '') : null
    for (const key of RECORD_KEYS) {
      if (!ownsRecord(target, key)) continue
      // `call`/`session` point at one exact approval for one visit. Returning to
      // the panel later must not re-open an approval the operator already read.
      // `fact` is the same: a report/premise link names one record to read now,
      // and a remembered `?fact=` would reopen it after the operator moved on.
      if (key === 'call' || key === 'session' || key === 'fact') continue
      const value = (route.view === target ? route.params.get(key) : null) ?? remembered?.get(key)
      if (value) params.set(key, value)
    }
    if (options.params) {
      for (const [key, value] of Object.entries(options.params) as [RecordKey, string | undefined][]) {
        if (!value) params.delete(key)
        else params.set(key, value)
      }
    }

    let hash = ''
    if (options.hash !== undefined) {
      // Explicit hashes are node ids (`f:<fact>`); the URL builder owns the `#`.
      // Callers may pass either the raw id or an already-prefixed hash.
      const raw = options.hash.replace(/^#/, '')
      hash = raw ? `#${encodeURIComponent(raw)}` : ''
    } else if (target === 'graph') {
      // Staying on / re-clicking the map keeps the selection in the URL; only a
      // return from another view pulls the remembered selection back.
      hash = returning ? restorableHash(memory?.hash) : route.hash
    }

    navigate(buildViewURL(target, tab, params, hash), options.replace ?? false)
  }

  /** Rail clicks and alias navigation: `?view` may be an old view id. */
  function selectView(next: string) {
    const target = resolveView(next)
    if (target) openView(target.view, { tab: target.tab })
  }

  /** Tab clicks inside a merged group: same view, new tab (push). */
  function selectTab(next: TabID) {
    if (!isGroup(view) || !GROUP_TABS[view].tabs.includes(next)) return
    // The tab is part of the URL, so this panel swap is one `navigate` call: the
    // URL and the React state commit together, and nothing animates the swap.
    openView(view, { tab: next })
  }

  /** Cross-page link: one direction on the state page (`?intent=`). */
  function openIntent(intentID: string) {
    openView('state', { tab: 'state', params: { intent: intentID } })
  }

  /** Cross-page link: one run in the run ledger (`?run=`). */
  function openRun(runID: string) {
    // A specific run pins the history row; a remembered group key would point at
    // another group, so clear it explicitly.
    openView('runs', { tab: 'runs', params: { run: runID, run_group: undefined } })
  }

  /** Cross-page link: one authorization request on the access page (`?id=`). */
  function openAuthorization(requestID: string) {
    openView('access', { params: { id: requestID } })
  }

  /** Cross-page link: one goal's receipt on the 状态 page (`?goal=`). */
  function openGoal(goalID: string) {
    openView('state', { tab: 'state', params: { goal: goalID } })
  }

  /** Cross-page link: one fact's claim version, reviews and evidence (`?fact=`). */
  function openFact(factID: string) {
    // The hash is cleared: `f:<id>` came from a previous map pick and would win
    // over the record param (`?fact=`) in the selection below.
    openView('graph', { params: { fact: factID }, hash: '' })
  }

  /** Selection is a graph affordance: select the node (`f:`/`s:`/edge id). */
  function openGraphNode(nodeID: string) {
    // One navigation, not two: `selectView()` + `select()` would emit two
    // history entries and re-render the rail for a change that is a single
    // view + hash.
    openView('graph', { hash: nodeID })
  }

  function refresh() {
    // Board/projects and the inbox are the shell's shared chrome, so they always
    // reload. The ledger is only pulled when the map is on screen: reopening 图谱
    // revalidates it immediately, so refreshing another panel does not download
    // it twice.
    if (graphActive) {
      graph.reload()
    }
    inbox.reload()
    onRefresh()
  }

  async function decideRound() {
    setDeciding(true)
    setActionError('')
    try {
      await api.decide('operator')
      onRefresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setDeciding(false)
    }
  }

  /** The whole-project switch: stop deciding *and* stop the executors. The
   *  canceled directions go back to pending, so resuming re-dispatches them. */
  async function toggleProject(resumeGoalID?: string) {
    if (!board) return
    const projectStopped = Boolean(board.project_paused || board.decider.paused)
    if (!projectStopped) {
      const ok = await confirm({
        title: '暂停项目',
        description:
          running > 0
            ? `指挥官会停止决策，当前 ${running} 个正在跑的 run 会被取消（已写出的证据保留）。继续后指挥官会重新派发待办意图。`
            : '指挥官会停止决策，直到你点「继续项目」。',
        confirmLabel: '暂停项目',
      })
      if (!ok) return
    }
    setPausing(true)
    try {
      if (projectStopped) await api.resumeProject(resumeGoalID)
      else await api.pauseProject()
      refresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setPausing(false)
    }
  }

  function switchProject(id: string) {
    if (id !== current && projects.some(p => p.id === id)) gotoProject(id)
  }

  const running = board?.intents.running ?? 0

  const panel =
    view === 'overview' ? (
      <Overview
        current={current}
        projects={projects}
        board={board}
        inbox={inbox}
        onRefresh={refresh}
        onNavigate={selectView}
        onOpenNode={openGraphNode}
        // A todo row opens the record it names, not just the page that hosts
        // records: `?intent=` is the direction's own detail, `?run=` its event
        // stream. The overview card keeps the view-level link for anything that
        // has no record URL.
        onOpenIntent={openIntent}
        onOpenRun={openRun}
        onOpenAuthorization={openAuthorization}
        onOpenGoal={openGoal}
        onLocateGuardrail={(target) =>
          // The run id is unknown at the click: an approval names its session.
          // The runs panel resolves it and reports when the call is not there.
          openView('runs', { tab: 'runs', params: { run: undefined, session: target.sessionID, call: target.callID } })
        }
        refreshGoalsSeq={goalRefreshSeq}
      />
    ) : view === 'state' ? (
      <StateCenter
        tab={tab as StateTab}
        onTab={selectTab}
        onNavigate={selectView}
        boardState={boardState}
        // ProjectState's 图谱 entry opens the direction on the map (docs/34
        // §1.2 contract: "ProjectState: 图谱"), unlike RunCenter's 状态 entry.
        onOpenIntent={(id) => openGraphNode(`s:${id}`)}
        onOpenRun={openRun}
        // A fact is a record with an address now (`?fact=`), not a hash-only
        // affordance: the report/premise links and this page all use one form.
        onOpenFact={openFact}
      />
    ) : view === 'evidence' ? (
      <EvidenceHub tab={tab as EvidenceTab} onTab={selectTab} />
    ) : view === 'vulns' ? (
      <Vulns lockedProject={current} />
    ) : view === 'runs' ? (
      <RunCenter
        tab={tab as RunTab}
        onTab={selectTab}
        onOpenIntent={openIntent}
        onOpenGraphIntent={(id) => openGraphNode(`s:${id}`)}
        onOpenRun={openRun}
      />
    ) : view === 'messages' ? (
      <Messages boardState={boardState} />
    ) : view === 'docs' ? (
      <Documents />
    ) : view === 'access' ? (
      <Access inbox={inbox} />
    ) : view === 'commander' ? (
      <CommanderView tab={tab as CommanderTab} onTab={selectTab} board={board} />
    ) : view === 'playbooks' ? (
      <Playbooks />
    ) : view === 'reports' ? (
      <Reports onGoVerify={() => selectView('vulns')} onOpenFact={openFact} />
    ) : view === 'runtime' ? (
      <ExecutionEnv tab={tab as RuntimeTab} onTab={selectTab} />
    ) : null

  // One verdict for the buttons, from the backend action contract
  // (internal/api/project_actions.go): the resume handler checks the same
  // function, so a button that looks available and a request that comes back
  // 409 can no longer disagree. 「已收口」 is that verdict too — "paused and every
  // goal happens to be met" cannot tell a completion stand-by from a manual
  // pause, which is why the shell no longer derives it. The old derivation stays
  // only as the fallback for a board that predates the contract.
  const contract = board?.project_actions
  const resumeAction = contract?.actions.find((action) => action.id === 'resume')
  const pauseAction = contract?.actions.find((action) => action.id === 'pause')
  const paused = contract ? contract.state.paused : Boolean(board?.project_paused || board?.decider.paused)
  // Say why it is paused instead of leaving the operator to guess: a completed
  // goal, a provider failure, the operator's own pause, or a commander stop.
  const pauseReason = paused ? (contract?.state.reason ?? board?.decider.paused_reason ?? '') : ''
  const pauseKind = contract?.state.kind ?? board?.project_pause_kind ?? ''
  const finished = pauseKind === 'completion_standby' || pauseReason.includes('目标已完成')
  // The commander's stop request is a machine code, not prose: it must stay
  // visible in the banner and still be undone only by an explicit 「继续项目」.
  const currentGoal = board?.goals.open?.[0]
  const closeoutGoal = projectCloseoutGoal(board)
  const legacy = projectPauseState(board, finished)
  const commanderStoodBy =
    paused && Boolean(currentGoal) && (pauseKind ? pauseKind === 'commander_standby' : legacy.commanderStoodBy)
  const projectClosed = paused
    ? resumeAction
      ? resumeAction.reason_code === 'nothing_to_continue'
      : legacy.projectClosed
    : false
  const completionParked = paused
    ? resumeAction
      ? resumeAction.reason_code === 'nothing_to_continue' || pauseKind === 'completion_standby'
      : legacy.completionParked
    : false
  // A closeout in flight is not "parked": resume resumes the closeout's outcome.
  const resumeBlocked = paused && resumeAction ? !resumeAction.enabled : false

  // The board counters are a quiet meta line, not chips: label at 11px ink-3,
  // the number mono/tabular at ink-2, groups separated by a hairline. They sit
  // on the header's lower row next to the snapshot/live/token meta, so the top
  // row stays "what can I do" and never competes with the title.
  //
  // 图谱页例外：那一页自带「快照已载 / 实时 / tokens」同一条状态线，计数由
  // GraphPanel 用同一个 BoardCounters 画在同一个容器里——分开注入会在 在跑 与
  // 快照已载 之间留下一条对不齐的缝。
  const stats = view === 'overview' || view === 'graph' ? null : (
    <BoardCounters board={board} running={running} />
  )

  // The project-wide controls ride in every page banner (ProjectChromeContext),
  // so the kill switch is never buried in one view. One compact size (h-8) and
  // one hierarchy: a single primary CTA (新建意图) on the right, the rest
  // outline/ghost so the header has one thing to click first.
  const controls = (
    <>
      {commanderStoodBy && (
        <Badge size="sm" variant="warning" title={pauseReason} className="shrink-0">
          指挥官请求停止 · 目标仍未达成
        </Badge>
      )}
      {projectClosed && (
        <Badge size="sm" variant="success" title={pauseReason} className="shrink-0">
          已收口
        </Badge>
      )}
      {finished && (
        <Badge size="sm" variant="success" title={pauseReason} className="shrink-0">
          已完成
        </Badge>
      )}
      {board && (
        <Button
          size="sm"
          variant={completionParked ? 'ghost' : 'outline'}
          className="gap-1.5 px-3"
          aria-label={paused ? (commanderStoodBy ? '继续当前目标' : '继续项目') : '暂停项目'}
          onClick={() => toggleProject(commanderStoodBy ? currentGoal?.id : undefined)}
          disabled={pausing || resumeBlocked || (!paused && pauseAction?.enabled === false)}
          title={
            commanderStoodBy
              ? `恢复项目并唤醒指挥官，继续当前目标「${currentGoal?.statement ?? ''}」，不新增目标`
              : paused
                ? (resumeAction?.reason ?? (pauseReason ? `${pauseReason}；点「继续项目」恢复决策` : '继续项目：指挥官恢复决策，并重新派发待办意图'))
                : (pauseAction?.reason ?? '暂停项目：停止指挥官，并取消所有正在跑的 run')
          }
        >
          {paused ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
          <span className="whitespace-nowrap">
            {paused ? (commanderStoodBy ? '继续当前目标' : '继续项目') : '暂停项目'}
          </span>
        </Button>
      )}
      {/* Occasional controls sit behind one grouped entry: close out, append a
          goal after a closed engagement, run one round. None is a daily click,
          and each keeps its state and validation on its own button. */}
      {(closeoutGoal ||
        (paused && (projectClosed || finished) && (board?.goals.total ?? 0) > 0) ||
        (!paused && board?.decider.enabled)) && (
        <Popover>
          <PopoverTrigger asChild>
            <Button size="sm" variant="ghost" className="gap-1.5 px-3" aria-label="更多项目操作" title="更多项目操作">
              <span className="whitespace-nowrap">更多</span>
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="w-60 p-1.5">
            {closeoutGoal && (
              <Button
                size="sm"
                variant="ghost"
                className="w-full justify-start"
                onClick={() => setSealGoal(closeoutGoal)}
                title="让指挥官核对已有事实与证据并整理收口；项目保持暂停"
              >
                <Target className="h-4 w-4" /> 收口项目并完成目标
              </Button>
            )}
            {paused && (projectClosed || finished) && (board?.goals.total ?? 0) > 0 && (
              <Button
                size="sm"
                variant="ghost"
                className="w-full justify-start"
                onClick={() => setAppendGoalOpen(true)}
                title="追加一个新的独立目标，并按这次显式操作恢复派发；旧目标的完成收据保持不变。普通消息不会自动恢复暂停的项目。"
              >
                <Target className="h-4 w-4" /> 追加目标并继续
              </Button>
            )}
            {!paused && board?.decider.enabled && (
              <Button
                size="sm"
                variant="ghost"
                className="w-full justify-start"
                onClick={decideRound}
                disabled={deciding}
                title="让指挥官立刻跑一轮"
              >
                <Play className="h-4 w-4" /> 跑一轮
              </Button>
            )}
          </PopoverContent>
        </Popover>
      )}
      {/* One saturated action per page: the graph page itself has no other
          primary, so 新建意图 owns the accent there. Other pages have their own
          page-level CTA (新建目标 / 下发 …), so the shared chrome stays out of
          their header. */}
      {view === 'graph' && (
        <Button
          size="sm"
          className="gap-1.5 px-3"
          aria-label="新建意图"
          title="新建意图"
          onClick={() => setCreating(true)}
        >
          <Plus className="h-4 w-4" />
          <span className="whitespace-nowrap">新建意图</span>
        </Button>
      )}
    </>
  )

  return (
    <div className="flex h-full w-full flex-col overflow-hidden lg:flex-row">
      {/* ---- left rail ---- */}
      <ConsoleRail
        title="项目工作区"
        active={view}
        context={
          <Select value={current} onValueChange={switchProject}>
            <SelectTrigger
              className="h-9 w-full border-hairline bg-surface-2/60 text-[12px] text-ink-2"
              aria-label="切换项目"
              title={projects.find((p) => p.id === current)?.scope || '范围未指定'}
            >
              <SelectValue placeholder="项目" />
            </SelectTrigger>
            <SelectContent>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id} className="text-[12px]">
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        footer={
          <div className="space-y-1.5">
            <FreshnessPill state={boardState} intervalMs={5000} label="项目数据" />
            <Button
              size="sm"
              variant="ghost"
              className="w-full justify-start gap-2 text-ink-2"
              onClick={() => window.location.assign('/g/')}
              title="项目、全局漏洞库、POC 库与模型设置"
            >
              <Globe className="h-4 w-4 text-ink-3" />
              全局控制台
            </Button>
          </div>
        }
      >
        <nav aria-label="项目导航" className="min-h-0 flex-1 space-y-5 overflow-y-auto px-3 pb-4">
          {NAV_GROUPS.map((group) => (
            <div key={group.label}>
              <div className="saker-eyebrow mb-2 px-3">{group.label}</div>
              <div className="space-y-0.5">
                {group.items.map((id) => {
                  const item = NAV.find((entry) => entry.id === id)!
                  return (
                    <SideNav
                      key={item.id}
                      active={view === item.id}
                      icon={item.icon}
                      label={item.label}
                      badge={item.id === 'overview' ? badge?.text : undefined}
                      badgeTitle={item.id === 'overview' ? badge?.title : undefined}
                      onClick={() => selectView(item.id)}
                      onIntent={() => prefetchRegistry.prefetch(item.id)}
                    />
                  )
                })}
              </div>
            </div>
          ))}
        </nav>
      </ConsoleRail>

      {/* ---- main ---- */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <ProjectChromeContext.Provider
          value={{
            stats,
            controls,
            projectName,
            error: actionError || loadError || (graphActive ? graph.error || runsError : undefined),
          }}
        >
          {view === 'graph' ? (
            <Suspense fallback={<PageSkeleton />}>
              <GraphPanel
                current={current}
                project={project}
                board={board}
                graph={graph}
                selected={selected}
                onSelect={select}
                onNavigate={selectView}
                onOpenRun={openRun}
                onCreateIntent={() => setCreating(true)}
                onRefresh={refresh}
                onError={setRunsError}
              />
            </Suspense>
          ) : (
            <div className="min-h-0 flex-1 overflow-hidden">
              <Suspense fallback={<PageSkeleton />}>{panel}</Suspense>
            </div>
          )}
        </ProjectChromeContext.Provider>
      </div>

      <CreateIntentDialog open={creating} onOpenChange={setCreating} onCreated={(id) => { refresh(); openIntent(id) }} />
      <AppendGoalDialog
        projectID={current}
        open={appendGoalOpen}
        onOpenChange={setAppendGoalOpen}
        paused={paused}
        pauseReason={pauseReason}
        onPartial={() => { setGoalRefreshSeq((n) => n + 1); refresh() }}
        onCompleted={() => {
          // The goal exists now; the board (pause state) and the overview's own
          // goals poll must both re-read, so "追加目标并继续" shows the result.
          setGoalRefreshSeq((n) => n + 1)
          refresh()
        }}
      />
      <SealGoalDialog
        goal={sealGoal}
        open={!!sealGoal}
        paused={paused}
        onOpenChange={(open) => { if (!open) setSealGoal(null) }}
        onSealed={() => {
          // The board now says the goal is met; the banner switches to 已收口.
          // The project deliberately stays paused: sealing is not resuming.
          setSealGoal(null)
          setGoalRefreshSeq((n) => n + 1)
          refresh()
        }}
      />
    </div>
  )
}
