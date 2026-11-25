// The graph page body (T6, docs/33 §4.6). Everything the old GraphView rendered
// inside its `<Page>` lives here: canvas, filters, toolbar, legend, goals card,
// hover/pinned inspectors, chat dock, empty states. The shell (GraphView.tsx)
// keeps the rail, the other panels, project chrome, selection + hash deep link
// and the create-intent dialog.
//
// The snapshot itself is owned by the shell (`useGraphSnapshot(graphActive,
// current)`) because it must survive leaving the page without refetching; this
// component receives it as a prop. Leaving the page unmounts GraphPanel, so all
// polls (runs/changes) stop with the map, as before.

import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Crosshair, Download, Plus, RefreshCw } from 'lucide-react'
import { Button, EmptyState, buttonVariants } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { BoardSummary, MetaRule } from '@/components/BoardCounters'
import { CommanderCard } from '@/components/CommanderCard'
import { GoalEditorDialog, type EditableGoal } from '@/components/GoalEditorDialog'
import { GraphCanvas, type GraphControls } from '@/components/GraphCanvas'
import { GraphChatDock } from '@/components/GraphChatDock'
import { Page } from '@/components/Page'
import { ProjectTokens } from '@/components/TokenUsage'
import { cameraStorageKey } from '@/graph/camera'
import { useViewWidth } from '@/graph/view-store'
import type { GraphNode } from '@/graph/model'
import type { LayoutMode } from '@/graph/sim'
import { buildTimelineFromSnapshot, revealedCount, timelineGate, revealedIn } from '@/graph/timeline'
import { chainView, subtree } from '@/graph/chain'
import { fmtTime } from '@/lib/format'
import { hasNewContent, liveReloadDelay, shouldLiveReload } from '@/lib/graph-live'
import { graphSelectionRoot } from '@/lib/graph-pages'
import { cycleSearchIndex, searchGraph } from '@/lib/graph-search'
import { useSessionState } from '@/lib/session-state'
import { api, type ChangePage } from '@/lib/api'
import type { useGraphSnapshot } from '@/lib/graph-data'
import { useDebounced, useLoad, usePoll } from '@/lib/hooks'
import type { BoardResponse, ProjectInfo } from '@/lib/types'
import { GraphFilters } from './GraphFilters'
import { GraphHoverInspector } from './GraphHoverInspector'
import { GraphLegend } from './GraphLegend'
import { GraphListView } from './GraphListView'
import { GraphMinimap } from './GraphMinimap'
import { focusFactLabel } from '@/graph/render'
import { GraphTimeline } from './GraphTimeline'
import { GraphToolbar } from './GraphToolbar'
import { useDockSize } from './DockResize'
import { GraphResizeHandle, useGraphCardResize } from './GraphResize'
import { defaultGraphFilters, useGraphModel, type GraphFiltersState } from './use-graph-model'

/** The ledger snapshot lives in the shell; props stay structural so this file
 *  does not need to re-export T2's hook type. */
export type GraphSnapshot = ReturnType<typeof useGraphSnapshot>

/** The whole axis plays in this long; one click is one replay, no speed control. */
const TIMELINE_PLAY_MS = 60_000

/** Breathing room between a floating card's edge and the map content. */
const OVERLAY_GAP = 12
/** 44rem in CSS px: the same narrow-map breakpoint app.css uses for the cards. */
const NARROW_MAP_PX = 704

// The pinned node panel is ~800 lines of detail/actions. Lazy loading it keeps
// the GraphView chunk inside the P1-c budget; the map is usable before it lands.
// The chunk is warmed as soon as the map mounts (below), so the download is a
// background detail rather than something the first node click waits on.
const GraphNodePanel = lazy(() => import('./GraphNodePanel'))

interface GraphPanelProps {
  current: string
  project?: ProjectInfo
  board?: BoardResponse
  graph: GraphSnapshot
  selected: string | null
  /** Shell-owned selection: writes the hash deep link. */
  onSelect: (id: string | null) => void
  /** Switch the shell to another panel (overview / messages / decisions …). */
  onNavigate: (view: string) => void
  /** Cross-page link: open one run in the run ledger (`?view=runs&run=…`). */
  onOpenRun: (runID: string) => void
  onCreateIntent: () => void
  /** Shell refresh: reload the snapshot and the shared board/projects chrome. */
  onRefresh: () => void
  /** Runs poll errors are part of the page chrome; the shell shows them. */
  onError: (error: string | undefined) => void
}

export function GraphPanel({
  current,
  project,
  board,
  graph,
  selected,
  onSelect,
  onNavigate,
  onOpenRun,
  onCreateIntent,
  onRefresh,
  onError,
}: GraphPanelProps) {
  const canvas = useRef<GraphControls | null>(null)
  // Warm the node inspector's chunk while the operator is still reading the map.
  // A click on a node must open the dock in the same frame; before this, the
  // first pin suspended on a download whose fallback is `null` (see below), so
  // the click looked ignored until the file landed.
  useEffect(() => {
    void import('./GraphNodePanel')
  }, [])
  // The right side of the map (pinned node panel, 下发 dock, hover card) shares
  // one width; the operator sets it by dragging the panel's left edge. It lives
  // here, above all three, because they take turns occupying the same corner.
  // The width is clamped against the *map* (published by the canvas), not the
  // window: the rail and the dock itself leave far less room than innerWidth.
  const stageWidth = useViewWidth()
  const dock = useDockSize(stageWidth)
  const filterCard = useRef<HTMLDivElement | null>(null)
  const filterResize = useGraphCardResize('tw.graph.filterSize', 'bottom-right', filterCard)
  const bottomStack = useRef<HTMLDivElement | null>(null)
  const timelineStrip = useRef<HTMLDivElement | null>(null)
  const [timelineOpen, setTimelineOpen] = useState(false)
  const [insets, setInsets] = useState({ left: 0, bottom: 0 })
  useEffect(() => {
    const filter = filterCard.current
    const stack = bottomStack.current
    const stage = stack?.parentElement
    if (!filter || !stack || !stage) return
    const footer = timelineStrip.current
    // Wide maps have space beside the left stack. Narrow maps keep the map
    // above it so the remaining horizontal band stays usable.
    const read = () => {
      const narrow = stage.clientWidth < NARROW_MAP_PX
      const leftEdge = (el: HTMLDivElement) => el.offsetWidth > 0 ? el.offsetLeft + el.offsetWidth + OVERLAY_GAP : 0
      const filterEdge = leftEdge(filter)
      const bottomOverlay = narrow ? stack : footer
      const left = narrow ? filterEdge : Math.max(filterEdge, leftEdge(stack))
      const bottom = bottomOverlay ? stage.clientHeight - bottomOverlay.offsetTop + OVERLAY_GAP : 0
      setInsets((previous) =>
        previous.left === left && previous.bottom === bottom
          ? previous
          : { left, bottom })
    }
    read()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(read)
    observer.observe(stage)
    observer.observe(filter)
    observer.observe(stack)
    if (footer) observer.observe(footer)
    return () => observer.disconnect()
  }, [timelineOpen])
  // The runs poll is the only source of per-direction warnings, the resolved
  // profile and the model each direction actually ran on. It stops with the
  // page (this component unmounts when the rail moves to another view).
  const runs = usePoll(api.runs, 6000)
  const connection = useLoad(api.settings, [])
  // A graph can change while the operator reads it. The changes probe is a
  // *notice*, never a merge: a new ledger version must be admitted by reloading
  // a fresh snapshot, because pages of two snapshots cannot be mixed. 实时 turns
  // that notice into an automatic whole-snapshot reload (docs/33 §4.6). The
  // probe asks `since=<snapshot seq>`, so run/message bookkeeping (which moves
  // the durable seq without a versions row) does not trigger a reload.
  const changesSince = useRef<number | undefined>(undefined)
  const changes = usePoll(
    () => {
      const since = graph.data?.snapshot_version?.seq
      changesSince.current = since
      // Before the first snapshot lands there is no seq to compare against; a
      // placeholder keeps the poll quiet instead of reading the whole journal.
      return since === undefined
        ? Promise.resolve<ChangePage>({
            changes: [],
            has_more: false,
            high_watermark: { epoch: '', seq: 0 },
            pending: 0,
          })
        : api.changes({ since })
    },
    6000,
    [graph.data?.snapshot_version?.seq],
  )
  // 实时 is on by default: the operator asked to see facts as they land. The
  // switch lets a reader freeze the map while inspecting a large or running
  // engagement; the snapshot contract itself does not change either way.
  const [live, setLive] = useSessionState('graph.live', true)
  const lastLiveReload = useRef(0)

  const [savedFilters, saveFilters] = useSessionState('graph.filters', { ...defaultGraphFilters(), hiddenKinds: [] as string[] })
  // A session saved before a switch existed must not read as "off": merge over the
  // defaults so old persisted filters gain the new drawing switches as on.
  const filters = useMemo<GraphFiltersState>(
    () => ({ ...defaultGraphFilters(), ...savedFilters, hiddenKinds: new Set(savedFilters.hiddenKinds) }),
    [savedFilters],
  )
  const setFilters = (next: GraphFiltersState | ((previous: GraphFiltersState) => GraphFiltersState)) => {
    const value = typeof next === 'function' ? next(filters) : next
    saveFilters({ ...value, hiddenKinds: [...value.hiddenKinds] })
  }
  const [q, setQ] = useSessionState('graph.search', '')
  const query = useDebounced(q, 200)
  const [showLabels, setShowLabels] = useState(false)
  // Layered is the reading view (facts → step → next fact, left to right); the
  // force map stays one click away for "what is connected to what".
  const [layout, setLayout] = useState<LayoutMode>(() => {
    try {
      return localStorage.getItem('tw.graph.layout') === 'force' ? 'force' : 'layered'
    } catch {
      return 'layered'
    }
  })
  // 下发 lives on the 图谱 page's right dock: intervention next to the map. The
  // full conversation record stays in the 通信 page.
  const [chatDock, setChatDock] = useState(false)
  // The inspector asks GraphPanel to open the child dialog for the direction the
  // operator is looking at; the dialog lives here because it belongs to the map,
  // not to the project shell.
  // The 终点 card is the goal's home on this page, so an unmet goal can be
  // redefined here instead of sending the operator back to 概览. The dialog is
  // the same one that page uses: it writes through the operator-scoped goal API
  // (a change to a met goal withdraws its receipt — the dialog says so).
  const [editingGoal, setEditingGoal] = useState<EditableGoal | null>(null)
  // Bumped after a child is created so the inspector's (otherwise static) child
  // list reloads without waiting for the operator to reselect the node.
  const [listView, setListView] = useState(false)
  // Time axis. The cursor is ephemeral on purpose: opening the map means "show me
  // the whole thing", and a half-scrubbed map that survived a reload would hide
  // facts for no stated reason. `null` = latest = everything revealed.
  const [timeCursor, setTimeCursor] = useState<number | null>(null)
  const [timelinePlaying, setTimelinePlaying] = useState(false)
  // The time axis strip is opened from the map toolbar and starts closed: a
  // graph screen pays nothing for a tool the operator reaches for occasionally.
  // 悬停预览：默认开。开着时指针碰到节点/线就高亮并弹右侧卡片，与点击走同一套集合
  // （graph/chain.ts）；工具条上那个开关是用来**关掉**它的，关掉后只有点击才是操作。
  // 键名带 On：旧键 `graph.hoverPreview`（默认关那版）里存过的 false 会盖住新默认值，
  // 不复用。
  const [hoverPreview, setHoverPreview] = useSessionState('graph.hoverPreviewOn', true)
  // 冷热压暗：默认关。默认显示是「当前链路高亮」（chainView 的主干 + 一跳旁支），
  // 这一档只是把链路外再压暗的可选加深；按几何记忆那一类持久化（localStorage）。
  const [coldDim, setColdDim] = useState(() => {
    try {
      return localStorage.getItem('graph.coldDimOn') === '1'
    } catch {
      return false
    }
  })
  const toggleColdDim = (next: boolean) => {
    setColdDim(next)
    try {
      localStorage.setItem('graph.coldDimOn', next ? '1' : '0')
    } catch {
      /* 隐私模式：这一档只是记不住 */
    }
  }

  const chooseLayout = (next: LayoutMode) => {
    setLayout(next)
    try {
      localStorage.setItem('tw.graph.layout', next)
    } catch {
      /* private mode: the choice just does not persist */
    }
  }

  // Switching the drawing mode is an explicit operator action, so it re-frames:
  // the two layouts are different shapes, and keeping the previous camera left
  // the map off-centre at a zoom that belonged to the other mode. Data arrivals
  // still never re-frame (that was 253-256's rule, and it stays true for them).
  const framedLayout = useRef(layout)
  useEffect(() => {
    if (framedLayout.current === layout) return
    framedLayout.current = layout
    const id = requestAnimationFrame(() => canvas.current?.fit({ margin: 0.12, capAtOne: true }))
    return () => cancelAnimationFrame(id)
  }, [layout])

  const defaultModel = connection.data?.model ?? ''

  /**
   * 「只看这棵树」：显式的一次查看动作，不是默认行为——不点的时候图上永远是
   * 全量。集合来自 `subtree`（全部祖先 + 自己 + 向下两跳），只在这里当过滤用，
   * 不进持久化的筛选（那是 JSON，Set 存不住）。
   */
  const [treeIds, setTreeIds] = useState<ReadonlySet<string> | null>(null)
  const modelFilters = useMemo(
    () => (treeIds ? { ...filters, onlyIds: treeIds } : filters),
    [filters, treeIds],
  )
  const model = useGraphModel({ data: graph.data, project, runs: runs.data ?? [], defaultModel }, modelFilters)

  // 这棵树属于一个节点：换了选中就把过滤收掉，否则会出现「点了别的节点还是旧树」。
  useEffect(() => {
    setTreeIds(null)
  }, [selected])

  // 过滤生效后重新适配镜头：树比全图小，留着旧取景等于看一片空白。左右两张浮层
  // （左上筛选卡、右侧面板）都会盖住画布，`fit()` 按实测宽度把树框进它们之间的那条
  // 可见带——窄屏也一样，宁可整棵树小一点，也不能框到卡片底下再让操作员自己拖。
  useEffect(() => {
    if (!treeIds) return
    const id = requestAnimationFrame(() => canvas.current?.fit({ margin: 0.16, capAtOne: true }))
    return () => cancelAnimationFrame(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [treeIds])

  useEffect(() => {
    onError(graph.error || runs.error)
  }, [graph.error, runs.error, onError])

  // Match the canvas's focus chain for screen reader labels and tree actions.
  const chain = useMemo(() => (selected ? chainView(model, selected) : null), [model, selected])

  /** 只看这棵树：把画布换成「全部祖先 + 自己 + 向下两跳」，其余节点不绘制。 */
  const showOnlyTree = () => {
    if (!selected) return
    const ids = subtree(model, selected)
    if (ids) setTreeIds(ids)
  }
  const showWholeGraph = () => setTreeIds(null)

  /*
   * 链上事实的短标签同时进 DOM。画布上的节点文字读屏与自动化都取不到，而
   * “这条链上是哪几条事实”正是读图的入口；可见的短标签仍画在 canvas 上，两边
   * 共用 focusFactLabel，截断规则一致。读屏按画布上的编号顺序读出事实。
   */
  const chainFactLabels = useMemo(() => {
    if (!chain) return []
    const rows: { id: string; label: string }[] = []
    for (const id of chain.main) {
      const node = model.byId.get(id)
      if (node?.type === 'fact') rows.push({ id, label: focusFactLabel(node.label) })
    }
    return rows
  }, [chain, model])

  // Data changes never re-frame: `fit()` frames the whole canvas, which includes
  // the area under the node panel, and on a narrow map a five-hop path fits only
  // by zooming out past legibility. A layout switch does re-frame (see
  // chooseLayout); the toolbar's 适配 stays the operator's own call.

  // Selecting a node never changes what the map draws: the closure stays whole and
  // the chain is drawn on top of it. There is no mode that hides nodes any more —
  // a penetration path is a DAG, and cutting it into one line drops "this fact
  // also feeds another intent".

  // Deep link: a hash naming a node that is not in the snapshot (yet) asks the
  // snapshot for the neighborhood around it. The shell owns the hash; this is
  // just the read side that turns it into a focused page. An edge selection
  // names a panel subject, not a snapshot root: `graphSelectionRoot` keeps it
  // from re-rooting the map at a nonexistent `fact:…->s:…` record.
  useEffect(() => {
    if (!graph.data || graph.loading || !selected) return
    const root = graphSelectionRoot(selected)
    if (root && graph.root !== root && !model.byId.has(selected)) void graph.focus(root)
  }, [graph.data, graph.loading, graph.root, selected, model])

  // The three ledger polls that shaped the old graph are now one snapshot, so
  // "settled" is simply "the snapshot request finished". Settled (not "has
  // data") on purpose: a failed load must still release the camera instead of
  // leaving the map parked at the origin.
  const graphReady = !graph.loading

  // Node search runs over the drawn model (lib/graph-search.ts): the ranking,
  // the keyboard cycle and the canvas highlight all read the same result.
  const search = useMemo(() => searchGraph(model, query), [query, model])
  const matches = search.matches
  // The row the keyboard/pointer is on. The canvas paints it with a stronger
  // ring so ↑↓ gives feedback even before Enter opens the panel.
  const [activeHit, setActiveHit] = useState(-1)
  // The hit Enter opened last; the next Enter advances instead of reopening it.
  const [landedHit, setLandedHit] = useState<string | null>(null)
  useEffect(() => {
    // A new query starts at its strongest hit. `search.hits` belongs to
    // `query`, which is already debounced here.
    setActiveHit(search.hits.length > 0 ? 0 : -1)
    setLandedHit(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query])
  useEffect(() => {
    // A live snapshot can remove rows under a standing query; keep the active
    // row inside the new list instead of pointing past its end.
    setActiveHit((current) => {
      if (search.hits.length === 0) return -1
      if (current < 0) return 0
      return Math.min(current, search.hits.length - 1)
    })
  }, [search.hits.length])
  const activeMatchId = activeHit >= 0 ? search.hits[activeHit]?.node.id ?? null : null

  // An unmet goal has no completed work to stand on, so it is not a map node
  // and owns no edge: the compact 终点 card states the target instead. A met
  // goal becomes a real node attached to its evidence fact (buildGraph).
  //
  // The snapshot keeps goals last and a read can stop short of them (a failed
  // page, or a snapshot the page budget could not finish). The card is therefore
  // not allowed to depend on that page set alone: the board always carries the
  // project's open goals, and is the fallback when the goal was not in this
  // snapshot.
  const openGoals = useMemo(() => {
    const loaded = (graph.data?.goals ?? []).filter((g) => !g.closed && !g.met)
    if (loaded.length > 0) return loaded
    return board?.goals?.open ?? []
  }, [graph.data, board])

  // Selection lookup goes through the model indexes (T2): no linear scans on a
  // 5k-node graph, and an edge id resolves to the direction it stands for.
  const selectedEdge = selected ? model.edgesById.get(selected) : undefined
  const selectedStep = selectedEdge?.intent?.id
    ? model.stepByIntent.get(selectedEdge.intent.id)
    : undefined
  const node: GraphNode | null = selectedEdge?.intent
    ? {
        id: selectedEdge.id,
        type: 'intent',
        label: selectedEdge.intent.title,
        sub: selectedEdge.intent.description,
        status: selectedEdge.intent.status,
        x: 0,
        y: 0,
        vx: 0,
        vy: 0,
        degree: 0,
        w: 0,
        h: 0,
        depth: 1,
        model: selectedStep?.model,
        // Provenance and helper decorations live on the step; carry them so an
        // edge selection opens the same panel a node selection does.
        origin: selectedStep?.origin,
        helpers: selectedStep?.helpers,
        meta: { intent: selectedEdge.intent },
      }
    : selected
      ? (model.byId.get(selected) ?? null)
      : null

  /** The intent a selection names, for the focus overlay. Edges and step/goal
   *  nodes all resolve to the direction whose work they describe. */
  const selectedIntentID = useMemo(() => {
    if (!selected) return null
    // Resolve an edge id before the `s:` prefix: a step→fact edge id also
    // starts with `s:` and would otherwise name a bogus intent.
    const edge = model.edgesById.get(selected)
    if (edge?.intent?.id) return edge.intent.id
    if (selected.startsWith('s:')) return selected.slice(2)
    return model.byId.get(selected)?.meta?.intent?.id ?? null
  }, [selected, model])


  // The axis is a pure function of what is drawn: same model, same order. It is
  // built from the model (not the raw snapshot) so its counts describe the map on
  // screen, including a chain-only view.
  // Built from the snapshot, not the drawn model: the single-chain view and the
  // kind chips change what is drawn, never how many rows the axis has.
  const timeline = useMemo(() => buildTimelineFromSnapshot(graph.data ?? {}), [graph.data])
  const timelineUsable = timeline.order.length > 1
  // A shorter snapshot (chain-only view, a dropped fact) can leave the cursor past
  // the end: fall back to "latest" instead of pinning the map to a dead row.
  useEffect(() => {
    if (timeCursor !== null && timeCursor > timeline.order.length - 1) {
      setTimeCursor(null)
      setTimelinePlaying(false)
    }
  }, [timeCursor, timeline.order.length])

  // Playback: one row per step, the whole axis in 60 s, no speed control. The
  // map paints from the axis rank, so a tick is a cursor change, never a scan.
  useEffect(() => {
    if (!timelinePlaying) return
    const last = timeline.order.length - 1
    if (last <= 0) {
      setTimelinePlaying(false)
      return
    }
    const from = timeCursor ?? 0
    const remaining = Math.max(1, last - from)
    const duration = (remaining / last) * TIMELINE_PLAY_MS
    const started = performance.now()
    const timer = window.setInterval(() => {
      const progress = Math.min(1, (performance.now() - started) / duration)
      const next = from + Math.round(remaining * progress)
      setTimeCursor(next >= last ? null : next)
      if (progress >= 1) setTimelinePlaying(false)
    }, 100)
    return () => window.clearInterval(timer)
    // `timeCursor` is the start row, captured once by the click; re-running on
    // every tick would restart the clock.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [timelinePlaying, timeline.order.length])

  // 门只在时间轴开着时生效：关掉时间轴 = 看完整张图，光标留着也不算数（否则收起
  // 时间轴后图还停在被截断的那一帧，操作员看到的与「已收起」不符）。
  const timeGate = useMemo(
    () => timelineGate(timeline, timelineOpen, timeCursor),
    [timeline, timelineOpen, timeCursor],
  )

  // Locate one search hit: a boundary is a reference the snapshot does not
  // carry, so it expands the neighborhood instead of pinning a missing record;
  // everything else is selected (which opens its panel) and framed inside the
  // strip the panel leaves visible.
  const jumpToHit = useCallback((index: number) => {
    const hit = search.hits[index]
    if (!hit) return
    setActiveHit(index)
    setLandedHit(hit.node.id)
    if (hit.node.boundary) {
      void graph.focus(hit.node.boundary)
      return
    }
    // A row hidden behind the time cursor is not on the map; locating it means
    // revealing the axis (the cursor itself is an explicit stop, so move the
    // slider to latest rather than silently centring on nothing).
    if (timeGate && !revealedIn(timeGate.rank, timeGate.cursor, hit.node.id)) setTimeCursor(null)
    setChatDock(false)
    // On a narrow map the search card and the node panel cannot coexist (the
    // card is hidden whenever a dock is open). Opening the panel here would take
    // the box the operator is typing in away on the first result; locate the
    // node without the dock instead, and let a tap on the located node open it.
    const narrow = stageWidth < NARROW_MAP_PX
    if (!narrow) onSelect(hit.node.id)
    canvas.current?.focusNode(hit.node.id, { rightInset: narrow ? 0 : dock.width })
  }, [search.hits, graph, timeGate, onSelect, dock.width, stageWidth])

  // Enter walks the results: the first press locates the active hit, later
  // presses advance from the one already located (Shift reverses).
  const activateHit = useCallback((backward: boolean) => {
    if (search.hits.length === 0) return
    let target = activeHit >= 0 ? activeHit : 0
    const current = search.hits[target]
    if (current && current.node.id === landedHit) {
      target = cycleSearchIndex(target, search.hits.length, backward ? -1 : 1)
    }
    jumpToHit(target)
  }, [search.hits, activeHit, landedHit, jumpToHit])
  const timelineRevealed = !timelineUsable || !timelineOpen
    ? timeline.order.length
    : timeCursor === null
      ? timeline.order.length
      : revealedCount(timeline, timeCursor)
  const timelineAt = timeline.order.length
    ? timeline.times[Math.min(timeline.order.length - 1, timeCursor ?? timeline.order.length - 1)]
    : ''

  const toggleTimeline = () => {
    if (timelinePlaying) {
      setTimelinePlaying(false)
      return
    }
    if (!timelineUsable) return
    // Play from the cursor, or replay the whole axis when it was already at the end.
    if (timeCursor === null) setTimeCursor(0)
    setTimelinePlaying(true)
  }

  function setTimelineVisible(open: boolean) {
    if (!open) setTimelinePlaying(false)
    setTimelineOpen(open)
  }

  // Hover pulse for the inspector: a running direction shows idle time/context
  // pressure while the pointer rests on it.
  const pulseFor = useCallback(
    (intentID: string) => board?.running_intents.find((item) => item.id === intentID)?.pulse,
    [board],
  )

  // A snapshot page can arrive partial (a cut read, an older server). Counting
  // the rows it did carry would understate the map without saying so, and
  // `.length` on a missing list used to throw; the counter says 未读全 instead.
  const loadedNodes = graph.data
    && Array.isArray(graph.data.facts)
    && Array.isArray(graph.data.intents)
    && Array.isArray(graph.data.goals)
    ? graph.data.facts.length + graph.data.intents.length + graph.data.goals.length
    : undefined
  const totalNodes = graph.data?.total ?? 0
  const snapshotVersion = graph.data?.snapshot_version
  const watermark = changes.data?.high_watermark
  // A stale probe response from an older snapshot cannot speak for the one on
  // screen: only count its pending rows when it was asked with this snapshot's
  // seq, so switching snapshots does not replay an old "new changes" verdict.
  const probeMatchesSnapshot =
    snapshotVersion !== undefined &&
    changesSince.current !== undefined &&
    changesSince.current === snapshotVersion.seq
  const hasNewChanges = hasNewContent(
    snapshotVersion,
    watermark,
    probeMatchesSnapshot ? (changes.data?.pending ?? 0) : 0,
  )

  // 实时: a newer ledger watermark with content rows swaps in a whole new
  // snapshot; it never merges pages of two versions and never touches the
  // operator's camera. The throttle keeps a busy run from re-reading the whole
  // ledger on every poll.
  useEffect(() => {
    if (
      !shouldLiveReload({
        live,
        hidden: document.visibilityState === 'hidden',
        loading: graph.loading,
        hasNewChanges,
      })
    ) {
      return
    }
    const timer = window.setTimeout(() => {
      if (document.visibilityState === 'hidden') return
      lastLiveReload.current = Date.now()
      graph.reload()
    }, liveReloadDelay(Date.now(), lastLiveReload.current))
    return () => window.clearTimeout(timer)
  }, [live, graph.loading, graph.reload, hasNewChanges])

  function refresh() {
    graph.reload()
    onRefresh()
  }

  function handleCanvasSelect(id: string | null) {
    // Opening a node means "show me this", so the chat dock yields the right
    // side instead of sitting on top of the inspector.
    setChatDock(false)
    if (!id) {
      onSelect(null)
      return
    }
    // A boundary node is a not-yet-loaded reference: ask the snapshot for its
    // neighborhood instead of pinning a node the model does not have.
    const picked = model.byId.get(id)
    if (picked?.boundary) {
      void graph.focus(picked.boundary)
      return
    }
    onSelect(id)
  }

  const running = board?.intents.running ?? 0

  return (
    <Page
      title="图谱"
      scroll={false}
      className={`relative tw-graph-stage${timelineOpen ? ' has-timeline' : ''}`}
      meta={
        // 一条状态线：账本计数（收纳成下拉）→ 用量 → 实时。整行 28px 高、垂直居中，
        // 组间只用同一条 1px 竖线（两侧各 10px），不再混字符 `|`；数字全部
        // tabular-nums。计数与快照节点都收在同一个下拉里，窄窗口也不隐藏。
        <div className="flex h-7 items-center text-[11px] leading-4 text-ink-3">
          <BoardSummary board={board} running={running} loadedNodes={loadedNodes} totalNodes={totalNodes} />
          {!live && hasNewChanges && (
            <Button
              size="xs"
              variant="soft"
              className="ml-3 h-7"
              onClick={refresh}
              title="账本已有新变化。刷新会重取一份新快照；旧快照的页面不会被静默拼接。"
            >
              有新变化，点击刷新
            </Button>
          )}
          {graph.root && (
            <Button
              size="xs"
              variant="ghost"
              className="ml-3 h-7"
              onClick={() => {
                onSelect(null)
                void graph.focus('')
              }}
            >
              全图概览
            </Button>
          )}
          <MetaRule className="hidden sm:block" />
          {/* The one place the project token total lives now (docs/34 §3): the
              whole project, including cached input, reported by the model. */}
          <ProjectTokens usage={board?.usage} />
          {/* 实时呼吸灯紧贴 tokens 右侧：同一行、同一高度（h-6，与 tokens 胶囊一致），
              中间用同一条 1px 分隔线，间距与其它组一致；窄屏也不换行、不挤压计数。 */}
          <MetaRule />
          <Button
            size="xs"
            variant="ghost"
            className="h-6 gap-1.5 px-1.5"
            aria-pressed={live}
            onClick={() => setLive((value) => !value)}
            title={
              live
                ? '实时刷新已开：账本出现新变化时自动重取一份完整新快照（相机保留，载入进度不丢）'
                : '实时刷新已关：有新变化时只提示，手动点刷新才换快照'
            }
          >
            <span
              className={`h-1.5 w-1.5 shrink-0 rounded-full ${live ? 'motion-safe:animate-pulse bg-success' : 'bg-ink-3'}`}
            />
            实时
          </Button>
        </div>
      }
      actions={
        // 页头动作共用紧凑按钮尺寸，只有「新建意图」用
        // accent（primary），其余 ghost / outline——一条横幅里只有一个主操作。
        <>
          <a
            className={cn(buttonVariants({ variant: 'outline', size: 'sm' }), 'gap-1.5 px-3')}
            href={`/p/${encodeURIComponent(current)}/api/graph/export`}
            download
            aria-label="导出全图"
            title="导出账本里的全图（不是当前快照页）"
          >
            <Download className="h-4 w-4" />
            <span className="whitespace-nowrap">导出全图</span>
          </a>
          <Button
            size="sm"
            variant="outline"
            className="gap-1.5 px-3"
            aria-label="刷新"
            onClick={refresh}
            title="立即重取最新快照"
          >
            <RefreshCw className="h-4 w-4" />
            <span className="whitespace-nowrap">刷新</span>
          </Button>
        </>
      }
      headerClassName="min-h-10 gap-y-1 py-1.5"
    >
      {/* The canvas is the first and only full-bleed child of the page body. At
          390px the other cards wrap around it instead of taking its place. */}
      <GraphCanvas
        ref={canvas}
        model={model}
        selected={selected}
        matches={matches}
        activeMatch={activeMatchId}
        showLabels={showLabels}
        // The dock (node panel / 下发) covers the right edge; hand its width to
        // the canvas so a label flips to the other side instead of hiding.
        rightInset={chatDock || node ? dock.width : 0}
        leftInset={insets.left}
        bottomInset={insets.bottom}
        layout={layout}
        // One camera per project: a reload or a visit to another panel comes
        // back to the same pan/zoom instead of the opening centre.
        cameraKey={cameraStorageKey(current)}
        ready={graphReady}
        showHelpers={filters.showHelpers}
        timeGate={timeGate}
        hoverPreview={hoverPreview}
        coldDim={coldDim}
        onSelect={handleCanvasSelect}
      />

      <div
        ref={filterCard}
        className="tw-graph-corner-tl tw-graph-filter absolute flex rounded-xl border border-hairline bg-card/95 shadow-lifted backdrop-blur"
        style={{ width: filterResize.size?.w, height: filterResize.size?.h }}
      >
        <GraphFilters
          current={current}
          q={q}
          onQueryChange={setQ}
          hits={search.hits}
          activeIndex={activeHit}
          onActiveIndex={setActiveHit}
          onOpenHit={jumpToHit}
          onActivate={activateHit}
          pending={q !== query}
          counts={board?.facts.by_kind ?? {}}
          filters={filters}
          onFiltersChange={setFilters}
          coldDim={coldDim}
          onToggleColdDim={toggleColdDim}
          onFocusFact={(factID) => void graph.focus(`fact:${factID}`)}
        />
        <GraphResizeHandle resize={filterResize} label="搜索与筛选" testId="graph-filter-resize" />
      </div>

      {chainFactLabels.length > 0 && (
        <div className="sr-only" role="status" aria-live="polite" aria-label="链上事实">
          <p>链上 {chainFactLabels.length} 条事实</p>
          <ul>
            {chainFactLabels.map((row) => (
              <li key={row.id} data-fact-id={row.id}>
                {row.label}
              </li>
            ))}
          </ul>
        </div>
      )}

      <GraphTimeline
        stripRef={timelineStrip}
        open={timelineOpen}
        onClose={() => setTimelineVisible(false)}
        total={timeline.order.length}
        cursor={timeCursor}
        revealed={timelineRevealed}
        pending={timeline.order.length - timelineRevealed}
        untimed={timeline.untimed.length}
        atLabel={timelineAt ? fmtTime(timelineAt) : '—'}
        firstLabel={timelineUsable ? fmtTime(timeline.times[0]) : undefined}
        lastLabel={timelineUsable ? fmtTime(timeline.times[timeline.times.length - 1]) : undefined}
        onCursor={(next) => {
          // Scrubbing is a deliberate stop: leaving playback running would fight
          // the pointer for the same cursor.
          setTimelinePlaying(false)
          setTimeCursor(next)
        }}
        playing={timelinePlaying}
        onTogglePlay={toggleTimeline}
        disabledReason={
          timeline.order.length === 1
            ? '只有一条带入账时间的记录，没有可回放的顺序。'
            : '这个快照里没有可排序的入账时间，所有节点按当前顺序显示。'
        }
      />

      <GraphToolbar
        chatDock={chatDock}
        onToggleChatDock={() => {
          setChatDock((open) => !open)
          onSelect(null)
        }}
        layout={layout}
        onLayout={chooseLayout}
        showLabels={showLabels}
        onToggleLabels={() => setShowLabels((value) => !value)}
        listView={listView}
        onToggleListView={() => setListView((value) => !value)}
        onZoom={(factor) => canvas.current?.zoom(factor)}
        onFit={() => canvas.current?.fit()}
        onRelayout={() => canvas.current?.relayout()}
        timelineOpen={timelineOpen}
        onToggleTimeline={() => setTimelineVisible(!timelineOpen)}
        timelineCounts={
          timelineUsable ? `已揭示 ${timelineRevealed}/${timeline.order.length}` : undefined
        }
        hoverPreview={hoverPreview}
        onToggleHoverPreview={() => setHoverPreview((value) => !value)}
      />

      {/* The bottom-anchored stack scrolls if the open cards outgrow its space. */}
      <div
        ref={bottomStack}
        className="tw-graph-corner-bl tw-graph-dock tw-graph-stack absolute flex flex-col rounded-xl"
      >
        <div className="tw-scroll flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto">
          {/* Order is deliberate: the target and the legend come first; the live
              commander state sits at the bottom where it stays whole and pinned
              while the legend's own body scrolls above it. */}
          {openGoals.length > 0 && (
            <div className="w-full shrink-0 rounded-xl border border-hairline bg-card/95 px-3 py-2 shadow-lifted backdrop-blur">
              <div className="flex items-center gap-1.5">
                <Crosshair className="h-3.5 w-3.5 shrink-0 text-warning" />
                <span className="tw-section-title">终点</span>
                <span className="tw-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
                  未达成 {openGoals.length}
                </span>
              </div>
              <div className="mt-1 space-y-0.5">
                {openGoals.slice(0, 2).map((g) => (
                  <div key={g.id} className="flex items-center gap-1.5">
                    <span
                      className="tw-break min-w-0 flex-1 truncate text-caption text-ink-2"
                      title={g.statement}
                    >
                      {g.statement}
                    </span>
                    <Button
                      size="xs"
                      variant="ghost"
                      className="shrink-0 px-1 text-[11px] text-ink-3 hover:text-ink"
                      onClick={() => setEditingGoal(g)}
                      title="修改这个目标的起点和终点"
                    >
                      管理
                    </Button>
                  </div>
                ))}
              </div>
              {openGoals.length > 2 && (
                <button
                  type="button"
                  className="mt-0.5 rounded text-left text-[10px] text-ink-3 transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  onClick={() => onNavigate('state')}
                  title="打开状态页查看全部未达成目标"
                >
                  另有 {openGoals.length - 2} 个未达成目标，去状态页查看 →
                </button>
              )}
            </div>
          )}

          <GraphLegend />

          <CommanderCard board={board} onOpen={() => onNavigate('decisions')} />
        </div>
      </div>

      {/* The minimap shares its corner with the pinned inspector and chat dock. */}
      <div className="tw-graph-corner-br absolute flex flex-col items-end gap-1.5">
        {!node && !chatDock && !listView && (
          <div className="tw-graph-minimap">
            <GraphMinimap
              model={model}
              layout={layout}
              defaultSide={stageWidth >= 2000 ? 184 : stageWidth >= 1150 ? 160 : undefined}
              live={running > 0 || model.nodes.some((n) => n.status === 'reviewing')}
              matches={matches}
              onCenter={(x, y) => canvas.current?.centerOn(x, y)}
              onFit={() => canvas.current?.fit()}
            />
          </div>
        )}
      </div>

      {!chatDock && node && (
        <Suspense fallback={null}>
          <GraphNodePanel
            node={node}
            facts={graph.data?.facts ?? []}
            runs={runs.data ?? []}
            dock={dock}
            onSelectFact={(id) => {
              if (!model.byId.get(`f:${id}`)?.meta?.fact) void graph.focus(`fact:${id}`)
              onSelect(`f:${id}`)
            }}
            onClose={() => onSelect(null)}
            onChanged={refresh}
            onOpenRun={onOpenRun}
            treeActive={Boolean(treeIds)}
            onOnlyTree={chain ? showOnlyTree : undefined}
            onShowAll={showWholeGraph}
          />
        </Suspense>
      )}

      {/* Hover is published by the canvas (T5's view-store) and subscribed only
          by this inspector: moving the pointer no longer re-renders GraphPanel.
          When a node is pinned the right side belongs to its full NodePanel, so
          hovering other nodes must not cover it with a second card. 悬停预览关着
          时整张卡片也不存在：右侧栏只由点击打开。 */}
      {hoverPreview && !chatDock && !listView && !node && (
        <GraphHoverInspector model={model} selected={selected} pulseFor={pulseFor} width={dock.width} />
      )}

      {chatDock && (
        <GraphChatDock
          dock={dock}
          onClose={() => setChatDock(false)}
          onOpenConversation={() => {
            setChatDock(false)
            onNavigate('messages')
          }}
        />
      )}

      {/* List view is the accessible equivalent of the canvas. It overlays the
          canvas (its own root is absolute inset-0) instead of replacing it, so
          the camera/simulation survive the toggle and Esc can return focus to
          the still-mounted main canvas. */}
      {listView && (
        <GraphListView
          model={model}
          selected={selected}
          matches={matches}
          activeMatch={activeMatchId}
          onSelect={handleCanvasSelect}
          onClose={() => setListView(false)}
        />
      )}

      {/* First open: the whole ledger is being read, so the map is empty by
          definition. Say that instead of flashing the "empty project" card,
          which is only true once a completed read found nothing. */}
      {graph.loading && !graph.data && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center p-4">
          <div
            role="status"
            aria-live="polite"
            className="rounded-full border border-hairline bg-card/90 px-3.5 py-1.5 text-[11px] text-ink-3 shadow-soft backdrop-blur"
          >
            正在载入全图快照…
          </div>
        </div>
      )}

      {!graph.loading && !graph.error && !model.nodes.some((n) => n.type !== 'project') && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center p-4">
          <div className="pointer-events-auto tw-panel w-[340px] rounded-xl">
            <EmptyState
              icon={Crosshair}
              title="这个项目还是空的"
              description="点「开始运行」让指挥官按项目简报推进并派发意图；目标由你在项目创建或「新建目标」里定义，简报和内置约束可在「概览」里补改。"
              action={
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" onClick={() => onNavigate('overview')}>
                    打开概览
                  </Button>
                  <Button size="sm" className="gap-1.5" onClick={onCreateIntent}>
                    <Plus className="h-4 w-4" />
                    新建意图
                  </Button>
                </div>
              }
            />
          </div>
        </div>
      )}

      {model.nodes.some((n) => n.type !== 'project') && (graph.data?.facts ?? []).length === 0 && (
        <div className="pointer-events-none absolute bottom-16 left-1/2 -translate-x-1/2 px-3">
          <div className="rounded-full border border-hairline bg-card/90 px-3.5 py-1.5 text-center text-[11px] text-ink-3 shadow-soft backdrop-blur">
            执行者还没有交接事实：写 `.tw/facts.jsonl` 后，事实会以圆点出现在意图周围
          </div>
        </div>
      )}

      {/* Goal editor opened from the 终点 card. Saving re-reads the snapshot and
          the board so the card and the map agree with the new definition. */}
      <GoalEditorDialog
        goal={editingGoal}
        onClose={() => setEditingGoal(null)}
        onSaved={() => {
          setEditingGoal(null)
          refresh()
        }}
      />

    </Page>
  )
}
