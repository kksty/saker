import { forwardRef, useEffect, useId, useImperativeHandle, useRef } from 'react'
import { edgeFocus, edgeKindLabel, focusTarget, type GraphEdge, type GraphModel, type GraphNode } from '@/graph/model'
import { chainView } from '@/graph/chain'
import {
  ForceSim,
  createMoveCoalescer,
  radiusOf,
  warmTicks,
  warmWithin,
  type LayoutMode,
  type MoveCoalescer,
} from '@/graph/sim'
import { PILL_MAX_LINES, PILL_TEXT_W, fitText, halfExtentX, paint, wrapText, type Camera } from '@/graph/render'
import type { GraphPalette } from '@/graph/palette'
import { layoutBounds, openCamera, parseCamera, focusCamera, type NodeExtent } from '@/graph/camera'
import { focusFadeMix, initialFocusFade } from '@/graph/focus-fade'
import { GraphIndex, edgeHitTest, pickAt as pickLinear, type GraphPick } from '@/graph/hit'
import {
  createFramePublisher,
  setHovered,
  setView,
  type FramePublisher,
  type GraphView,
} from '@/graph/view-store'

export interface GraphControls {
  /**
   * Frame the whole graph. `margin` is a fraction of each axis reserved as empty
   * space (the toolbar's 适配 uses the default fixed padding); `capAtOne` keeps a
   * layout switch from landing at a zoom above 1: a fit that would enlarge a small
   * map instead settles one notch out at 0.9.
   */
  fit: (options?: {
    margin?: number
    capAtOne?: boolean
    /** CSS px covered by the right dock; framing stays left of it. */
    rightInset?: number
    /** CSS px covered by the left filter card; framing stays right of it. */
    leftInset?: number
    /** CSS px covered by the bottom-left card stack; framing stays above it. */
    bottomInset?: number
  }) => void
  /** Restore the computed layout, discarding manually dropped nodes. */
  relayout: () => void
  zoom: (factor: number) => void
  /** Centre the camera on a graph-space point (minimap click/drag). */
  centerOn: (x: number, y: number) => void
  /**
   * Frame one node for a search hit: centre it at a readable zoom, inside the
   * strip the open search card and the right dock leave visible. A no-op when
   * the node is not in the current simulation.
   */
  focusNode: (id: string, options?: { rightInset?: number }) => void
  /** Set the zoom around the current viewport centre (minimap/toolbar). */
  zoomTo: (k: number) => void
}

/** Screen-space pointer slop when picking a node (graph units at zoom 1). */
const HIT_PAD_PX = 6
/** Screen-space tolerance when picking an edge curve. */
const HIT_EDGE_TOL_PX = 12
/** Fingers are fatter and less precise than a cursor (also used for coarse pointers). */
const TOUCH_HIT_SCALE = 2.2
/** Redraw interval for a running edge over a settled layout (30fps). */
const BREATHING_FRAME_MS = 33
/** How much of a frame the settle loop may spend ticking before it paints. */
const TICK_FRAME_BUDGET_MS = 12
/** Hard cap so a fast tick cannot starve the paint on small graphs. */
const MAX_TICKS_PER_FRAME = 8
/** How long a node that just appeared scales/ripples in. */
const RIPPLE_MS = 1100
/**
 * Bounded synchronous settle used when reduced motion is on. The map still has
 * to look like a graph, so it settles once in a stall instead of animating for
 * a second; `REDUCED_SETTLE_TICKS` caps the O(n²) force pass on a huge map.
 */
const REDUCED_SETTLE_MS = 120
const REDUCED_SETTLE_TICKS = 600

interface Props {
  model: GraphModel
  palette?: GraphPalette
  selected: string | null
  matches: Set<string> | null
  /** The hit the search list is on: painted with a stronger, doubled ring. */
  activeMatch?: string | null
  /** Show every intent label, not just the focused/running ones. */
  showLabels: boolean
  /** Force map or left-to-right penetration layers. */
  layout: LayoutMode
  /** Drawing-only: draw the derived 发起边 provenance lines. */
  /** Drawing-only: draw the native-subagent satellites on step pills. */
  showHelpers?: boolean
  /**
   * Width of the right dock (node panel / 下发) in CSS px, 0 when it is closed.
   * Labels use it as a drawing inset so they never run under the panel.
   */
  rightInset?: number
  /**
   * Width of the left filter card in CSS px, 0 when it is not drawn (collapsed
   * or hidden on a narrow map). Same contract as `rightInset`: framing and
   * labels stay inside the strip the two cards leave visible.
   */
  leftInset?: number
  /**
   * Height of the bottom-left card stack (终点 / 图例 / 指挥官) in CSS px that
   * `fit()` must frame above, 0 when it is not in the way. Only the narrow
   * layout reports it: on a wide map the stack sits left of the framed tree.
   */
  bottomInset?: number
  /** localStorage key for the camera. With one, the operator's pan/zoom survives
   *  a reload and a visit to another panel; without one the view is per-instance. */
  cameraKey?: string
  /** False while the ledger snapshot is still loading: the opening centre must
   *  not frame a half-built layout (the saved camera, if any, is unaffected). */
  ready?: boolean

  /** Time axis gate (graph/timeline.ts): rows ranked beyond the cursor are not
   *  drawn. Null = the axis is at "latest" (everything revealed). */
  timeGate?: { rank: ReadonlyMap<string, number>; cursor: number } | null
  /**
   * 悬停预览（默认开，工具条上的开关用来关掉）。开：指针碰到节点/线就高亮并弹右侧
   * 卡片，与点击用同一套集合（graph/chain.ts 的整条主干 + 一跳旁支）；关：指针移动
   * 既不做命中测试也不发布 hover，只有点击才是操作。
   */
  hoverPreview?: boolean
  /**
   * 冷热压暗（默认关）：开则把链路外的节点压到 FOCUS_COLD_ALPHA。关着时链路高亮
   * 照常生效（主干边加粗、节点编号），两者互不覆盖。
   */
  coldDim?: boolean
  onSelect: (id: string | null) => void
}

/** 画布要画的焦点：主干、一跳旁支、主干边。 */
interface DisplayFocus {
  /** 主干节点（满亮）。 */
  focus: Set<string> | null
  near: ReadonlySet<string> | null
  edges: ReadonlySet<string> | null
}

interface DragState {
  mode: 'pan' | 'node' | null
  id?: string
  sx: number
  sy: number
  cx: number
  cy: number
  moved: boolean
  /** The press landed on a ghost ring: a click there must do nothing at all,
   *  not even clear the current selection. */
  inert?: boolean
  /** The relation this press selected. A release that drifts off a 1.5px curve
   *  must not undo the click: the press already made the choice. */
  edge?: string
}

const NO_DRAG: DragState = { mode: null, sx: 0, sy: 0, cx: 0, cy: 0, moved: false }

/** The edge list one frame may draw/hit-test. The 发起线 switch is a drawing
 *  filter over a derived view, never an edit of the ledger model. */
/**
 * One canvas, one camera: the engagement graph.
 *
 * Interaction state lives in refs, not React state, for two reasons: a hover or
 * a pan must not re-render GraphView (the inspector/minimap subscribe to the
 * view-store instead), and the canvas is repainted from an animation-frame loop
 * that cannot wait for a React render. Props are mirrored into a ref each render
 * so the frame loop always paints the current selection.
 */
export const GraphCanvas = forwardRef<GraphControls, Props>(function GraphCanvas(
  { model, palette, selected, matches, activeMatch = null, showLabels, layout, cameraKey, ready = true, timeGate, hoverPreview = true, onSelect, showHelpers = true, rightInset = 0, leftInset = 0, bottomInset = 0, coldDim = false },
  ref,
) {
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const simRef = useRef<ForceSim | null>(null)
  const camRef = useRef<Camera>({ x: 0, y: 0, k: 1 })
  // The animation-frame loop that settles the simulation / animates ripples.
  const rafRef = useRef(0)
  // A pan (or a zoom step) only needs one paint per frame; pointermove schedules
  // it, and several moves in the same frame share it.
  const paintRafRef = useRef(0)
  // Camera/size publishing to the minimap is coalesced the same way: one publish
  // per frame, latest camera wins.
  const publishRef = useRef<FramePublisher<GraphView> | null>(null)
  const layoutRevision = useRef(0)
  // When the loop last painted idle breathing, for the 30fps cap.
  const lastPaintRef = useRef(0)
  // 绘制耗时（验收钩子）：最近 180 帧、首帧、上次写 DOM 的时刻。
  const perfFrames = useRef<number[]>([])
  const perfTotals = useRef<number[]>([])
  const simMsRef = useRef(0)
  const perfPublishedAt = useRef(0)
  const firstPaintMs = useRef(0)
  const firstTotalMs = useRef(0)
  // The saved camera (or, on a first visit, the fixed-zoom centre) is decided
  // once. After that the camera belongs to the operator: polls, resizes and
  // layout switches must not move it (no fit-to-canvas).
  const centeredRef = useRef(false)
  const sizeRef = useRef({ w: 0, h: 0, dpr: 1 })
  /**
   * The canvas box in client coordinates. Every pointer/hover hit test needs it
   * and `getBoundingClientRect()` forces a layout read; the box only moves when
   * the canvas is resized (the graph body never scrolls), so one measurement per
   * resize serves the whole gesture — no per-pointer layout thrash.
   */
  const rectRef = useRef<{ left: number; top: number } | null>(null)
  // Resize sources (ResizeObserver, window resize, visual viewport, dpr media
  // query) can all fire in one frame; they share one coalesced sync.
  const syncRafRef = useRef(0)
  // Off-screen / backgrounded: the frame loop is paused, not merely idle.
  const pausedRef = useRef(false)
  // A reduced-motion settle is owed to the next frame (set on rebuild / reheat).
  const settlePendingRef = useRef(false)
  // The canvas cursor is written through the style attribute; only write when it
  // actually changes, instead of re-styling the element on every painted frame.
  const cursorRef = useRef('')
  // Polite live region: the canvas ring moves with the arrow keys, and a screen
  // reader user needs to hear which node they landed on.
  const liveRef = useRef<HTMLDivElement | null>(null)
  const saveTimerRef = useRef(0)
  // A throttle is pending: the view changed but localStorage has not caught up.
  const saveDirtyRef = useRef(false)
  // Node id → when it first appeared on this canvas. New facts/steps ripple and
  // scale in for a second, then the map is pruned.
  const appearRef = useRef(new Map<string, number>())
  // Eased selection focus: the cold alpha mixes in instead of snapping. Only a
  // node selection eases; hover is immediate feedback, and a selected edge is a
  // panel subject whose focus may follow the pointer. The state remembers the
  // dim that is actually on screen, so a click on an already-hovered subject
  // does not flash the map back to full brightness (graph/focus-fade.ts).
  const focusFadeRef = useRef(initialFocusFade())
  // performance.now() at which the focus fade ends; the frame loop must keep
  // painting until then (a settled canvas otherwise stops on its own).
  const focusFadeUntilRef = useRef(0)
  // performance.now() at which the last ripple ends; lets the loop skip walking
  // the appearance map every frame.
  const rippleUntilRef = useRef(0)
  // Honours prefers-reduced-motion: false disables comets, pulses and ripples.
  const motionRef = useRef(true)
  // A coarse pointer / touch pointer widens the hit slop.
  const coarseRef = useRef(false)
  const lastPointerTypeRef = useRef('mouse')
  // Pointer is over (or dragging) this node/edge id; published to the view-store.
  const hoverRef = useRef<string | null>(null)
  // Arrow-key focus node; drawn as a ring by the renderer, never a React state.
  const keyboardFocusRef = useRef<string | null>(null)
  const dragRef = useRef<DragState>(NO_DRAG)
  // Nodes the operator dragged off the chain tree: id → dropped graph position.
  // The layered tick springs back to anchors, and every poll rebuilds the sim
  // from the deterministic layout, so a drop has to be re-applied to each new
  // sim. Only 重新布局 (or a layout switch) clears it.
  const manualRef = useRef(new Map<string, { x: number; y: number }>())
  const stateRef = useRef({ palette, selected, matches, activeMatch, showLabels, showHelpers, timeGate, hoverPreview, rightInset, leftInset, bottomInset, coldDim })
  stateRef.current = { palette, selected, matches, activeMatch, showLabels, showHelpers, timeGate, hoverPreview, rightInset, leftInset, bottomInset, coldDim }
  // The 发起边 drawing switch filters sim.edges (so paint AND hit-testing agree),
  // but the unfiltered model is the source of truth when it is toggled back on.
  const modelRef = useRef(model)
  modelRef.current = model
  // The flag the current sim was built/filtered with; toggling re-filters once.

  // Multi-touch pan/zoom: live touches and the pinch anchor (world point under
  // the midpoint of the two fingers when the gesture started).
  const touchesRef = useRef(new Map<number, { x: number; y: number }>())
  const pinchRef = useRef<{ dist: number; worldX: number; worldY: number; baseK: number } | null>(null)

  // A pointermove fires several times per frame. Hit-testing every sample (every
  // node, then every intent/brief edge) was the hot path while hovering a large
  // map; one test per painted frame sees the same picture.
  const hoverMoveRef = useRef<MoveCoalescer | null>(null)
  // Derived from the current simulation, but pure: id → node and the shown chain
  // change only when the sim, the selection or the hover changes. Rebuilding them
  // on every animation frame was pure garbage.
  const derivedRef = useRef<{
    sim: ForceSim | null
    byId: Map<string, GraphNode>
    /** Which subject the cached display belongs to (null = nothing lit). */
    focusID: string | null
    /** 主干 + 旁支 + 编号：点选与悬停走同一个函数（graph/chain.ts）。 */
    display: DisplayFocus | null
  }>({ sim: null, byId: new Map(), focusID: null, display: null })
  // Pill widths are a constant of (font, text); a poll keeps almost every label,
  // so re-shaping all of them on every rebuild was wasted main-thread time.
  const measureRef = useRef(new Map<string, { w: number; h: number; lines: string[] }>())
  // Lazily-built spatial index for the settled layout (docs/33 §4.5). Null while
  // the simulation runs or a node is dragged: positions move every frame then, so
  // hit-testing falls back to the linear scan.
  const indexRef = useRef<{ sim: ForceSim; index: GraphIndex } | null>(null)
  // Cached "does this sim have a running edge" flag; edges are fixed per sim.
  const breathingRef = useRef<{ sim: ForceSim | null; value: boolean }>({ sim: null, value: false })
  // Cached "does this sim have a direction in review" flag; the verification
  // frame is time-based, so the frame loop has to stay alive for it.
  const reviewingRef = useRef<{ sim: ForceSim | null; value: boolean }>({ sim: null, value: false })
  // Cached "does this sim have a live native-subagent helper" flag; its satellite
  // pulse is time-based too, so the loop must keep painting while it breathes.
  const helperPulseRef = useRef<{ sim: ForceSim | null; show: boolean; value: boolean }>({
    sim: null,
    show: true,
    value: false,
  })
  // Keyboard traversal order for the current sim (depth, y, x): deterministic.
  const keyboardOrderRef = useRef<{ sim: ForceSim | null; nodes: GraphNode[] }>({ sim: null, nodes: [] })

  // One coalescer for the canvas lifetime: it owns the pending pointer frame.
  if (hoverMoveRef.current === null) {
    hoverMoveRef.current = createMoveCoalescer((x, y) => applyHover(x, y))
  }
  // One publisher for the canvas lifetime: it owns the pending camera publish.
  if (publishRef.current === null) {
    publishRef.current = createFramePublisher<GraphView>((view) => setView(view))
  }

  // Rebuild the simulation when the ledger or the layout changes, keeping node
  // positions so a poll does not make the picture jump. A layout switch resets
  // them: the new anchors are the layout.
  useEffect(() => {
    const prev = simRef.current
    const prevNodes = prev && prev.mode === layout ? prev.nodes : null
    // Picking another layout is an explicit re-lay: manual drops belong to the
    // picture they were made on, not to the new one.
    if (prev && prev.mode !== layout) manualRef.current.clear()
    measureNodes(model)
    // The chain-tree view derives every x/y from the model (placeLayered), so a
    // poll has to rebuild its anchors instead of pinning yesterday's coordinates
    // on top of them. The force map's positions are free, and those are the ones
    // worth carrying across a poll so the picture does not jump.
    const prior = prevNodes ? new Set(prevNodes.map((n) => n.id)) : null
    const kept = new Map<string, { x: number; y: number }>()
    if (prevNodes && layout === 'force') {
      for (const n of prevNodes) kept.set(n.id, { x: n.x, y: n.y })
    }
    const sim = new ForceSim(model, { mode: layout })
    sim.edges = model.edges
    if (kept.size > 0) {
      for (const n of sim.nodes) {
        const old = kept.get(n.id)
        if (old) {
          n.x = old.x
          n.y = old.y
          n.vx = 0
          n.vy = 0
        }
      }
    }
    // A poll rebuilds the chain tree from scratch; put the operator's drops back
    // on top of it so a refresh does not silently undo their arrangement.
    if (layout === 'layered') {
      for (const [id, at] of manualRef.current) {
        if (model.byId.has(id)) sim.holdAt(id, at.x, at.y)
      }
    }
    // Only nodes this poll introduced have to be pulled into place; everything
    // else already sits where the operator saw it.
    const added = prior
      ? sim.nodes.reduce((count, n) => count + (prior.has(n.id) ? 0 : 1), 0)
      : sim.nodes.length
    // Record new node appearances; the very first paint marks everything as old
    // so opening a project does not ripple all of its history.
    const now = performance.now()
    const seen = appearRef.current
    const next = new Map<string, number>()
    let rippleUntil = 0
    for (const n of model.nodes) {
      const at = prev ? seen.get(n.id) ?? now : 0
      next.set(n.id, at)
      if (at > 0 && at + RIPPLE_MS > rippleUntil) rippleUntil = at + RIPPLE_MS
    }
    appearRef.current = next
    rippleUntilRef.current = rippleUntil

    // A rebuilt sim means every cached hit/adjacency/keyboard derivation is for
    // the old layout.
    simRef.current = sim
    invalidateIndex()
    derivedRef.current = { sim: null, byId: new Map(), focusID: null, display: null }
    breathingRef.current = { sim: null, value: false }
    reviewingRef.current = { sim: null, value: false }
    keyboardOrderRef.current = { sim: null, nodes: [] }

    // Settle the layout before the first paint: a background tab may never get
    // animation frames, and the picture must already look like a graph. The old
    // fixed warm blocked the main thread for hundreds of milliseconds on a large
    // map; the warm is now a bounded stall (docs/33 §4.4) and the frame loop
    // finishes whatever the budget could not.
    // A rebuild that only changed the highlight (a different selection, the same
    // nodes) introduces no node: alpha stays 0, so the settled picture is not
    // pulled apart under the pointer. Only genuinely new nodes get
    // a warm, and everything that survives keeps the position the model carried
    // over (lib/graph-model-cache.ts carryNodePositions).
    const highlightOnly = prior !== null && added === 0
    if (layout === 'layered' || highlightOnly) {
      // The chain tree is deterministic, and a poll that added nothing must not
      // animate a settled picture. warmTicks() also returns 0 for that case.
      sim.alpha = 0
    } else {
      const warm = warmTicks(prev, model, layout)
      if (warm <= 0) sim.alpha = 0
      else warmWithin(sim, { maxTicks: warm })
    }
    // Reduced motion: finish the settle now, in one bounded stall, so the first
    // paint is already the final picture. A poll while the operator asked for
    // less motion must not start a second of drifting nodes.
    if (!motionRef.current) {
      settlePendingRef.current = true
      settleWithoutMotion()
    }

    // Opening view only. A later rebuild came from a poll or the operator
    // switching layout, and either way the camera stays where it was.
    if (!centeredRef.current) centeredRef.current = openingCamera()
    start()
    requestPublish()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model, layout])

  // A queued hover test must not fire after the canvas is gone, and the frame
  // loop/paint publishes must not outlive it either.
  useEffect(
    () => () => {
      hoverMoveRef.current?.cancel()
      cancelAnimationFrame(rafRef.current)
      rafRef.current = 0
      if (paintRafRef.current) cancelAnimationFrame(paintRafRef.current)
      paintRafRef.current = 0
      if (syncRafRef.current) cancelAnimationFrame(syncRafRef.current)
      syncRafRef.current = 0
      publishRef.current?.cancel()
      // Nobody is hovering a canvas that no longer exists.
      if (hoverRef.current !== null) {
        hoverRef.current = null
        setHovered(null)
      }
    },
    [],
  )

  // prefers-reduced-motion is an accessibility setting, not a style choice: the
  // graph keeps all of its information, it just stops moving.
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => {
      motionRef.current = !mq.matches
      if (mq.matches) {
        // Turning the setting on mid-settle must stop the picture now, not at
        // the end of the animation.
        settlePendingRef.current = true
        settleWithoutMotion()
      }
      paintOnce()
    }
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // A touch/coarse pointer gets a larger hit slop.
  useEffect(() => {
    const mq = window.matchMedia?.('(pointer: coarse)')
    if (!mq) return
    const sync = () => {
      coarseRef.current = mq.matches
    }
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    // Browser zoom changes the CSS size *and* devicePixelRatio without
    // necessarily firing one clean resize, so every path that can change the
    // canvas geometry funnels here: ResizeObserver, window resize, visual
    // viewport (pinch) and the dpr media query. They share one measurement per
    // animation frame — a burst of four events is one `getBoundingClientRect()`
    // and one paint, not four.
    const sync = () => scheduleSizeSync()
    const ro = new ResizeObserver(sync)
    ro.observe(el)
    window.addEventListener('resize', sync)
    window.visualViewport?.addEventListener('resize', sync)
    let mq: MediaQueryList | null = null
    const watchDpr = () => {
      mq?.removeEventListener('change', watchDpr)
      mq = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`)
      mq.addEventListener('change', watchDpr)
      // The resolution actually changed: measure immediately so the backing
      // store is never painted at the wrong dpr.
      syncSize()
    }
    watchDpr()
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', sync)
      window.visualViewport?.removeEventListener('resize', sync)
      mq?.removeEventListener('change', watchDpr)
      if (syncRafRef.current) cancelAnimationFrame(syncRafRef.current)
      syncRafRef.current = 0
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // A backgrounded tab or a canvas the layout has scrolled/covered must not keep
  // a 30fps pulse loop alive. rAF already pauses in a hidden tab, but the loop
  // also has to stop when this canvas is off-screen and resume *just in time*
  // when it comes back (the documented IntersectionObserver fallback for
  // `content-visibility`, which must not be applied to above-the-fold content).
  useEffect(() => {
    const el = wrapRef.current
    const setPaused = (paused: boolean) => {
      if (pausedRef.current === paused) return
      pausedRef.current = paused
      if (paused) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = 0
        return
      }
      // Back on screen: paint the current state, then let the loop decide
      // whether anything is still animating.
      paintOnce()
      start()
    }
    const onVisibility = () => setPaused(document.visibilityState === 'hidden')
    document.addEventListener('visibilitychange', onVisibility)
    onVisibility()
    let observer: IntersectionObserver | null = null
    if (el && typeof IntersectionObserver !== 'undefined') {
      observer = new IntersectionObserver((entries) => {
        const entry = entries[entries.length - 1]
        if (entry) setPaused(!entry.isIntersecting)
      })
      observer.observe(el)
    }
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      observer?.disconnect()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The camera outlives this canvas: opening another panel unmounts it, and a
  // reload closes the page. Flush the last view on either way out, so coming
  // back lands exactly where the operator left off.
  useEffect(() => {
    const flush = () => {
      if (saveTimerRef.current) {
        window.clearTimeout(saveTimerRef.current)
        saveTimerRef.current = 0
      }
      // Only an un-flushed operator move is worth writing: an untouched canvas
      // must not freeze its opening centre over a layout that has since grown.
      if (saveDirtyRef.current) saveCamera()
    }
    window.addEventListener('pagehide', flush)
    return () => {
      window.removeEventListener('pagehide', flush)
      flush()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraKey])

  // The last poll can settle without changing the model (a failed goals fetch,
  // for example), so readiness alone may be what unblocks the opening camera.
  useEffect(() => {
    if (!ready || centeredRef.current) return
    centeredRef.current = openingCamera()
    paintOnce()
    requestPublish()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready])

  // A switch the operator flips: the sim's edge list feeds paint, hit-testing and
  // the (already origin-free) layout, so filtering it once is enough.


  // Props that change what is drawn (but not the layout) repaint directly; hover
  // is handled internally and does not go through React at all.
  useEffect(() => {
    paintOnce()
    // A fresh selection starts the eased dim: paintOnce alone would freeze it
    // on a settled canvas.
    if (focusFadeUntilRef.current > performance.now()) start()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [palette, selected, matches, activeMatch, showLabels, showHelpers, timeGate, hoverPreview, rightInset, leftInset, coldDim])

  /** One coalesced size measurement per frame (see the resize effect). */
  function scheduleSizeSync() {
    if (syncRafRef.current) return
    syncRafRef.current = requestAnimationFrame(() => {
      syncRafRef.current = 0
      syncSize()
    })
  }

  /**
   * Resize the backing store to the CSS box at the current dpr. Keeps the view
   * centred when the geometry changes (browser zoom), so the picture grows with
   * the page instead of drifting off the canvas.
   *
   * The CSS box comes from the layout (`absolute inset-0`), so only the backing
   * store is written here — and only when its pixel size actually changed. A
   * redundant `canvas.width` write reallocates the bitmap and clears it, which
   * is a full repaint for nothing.
   */
  function syncSize() {
    const el = wrapRef.current
    const c = canvasRef.current
    if (!el || !c) return
    const rect = el.getBoundingClientRect()
    // Cache the client box for the pointer paths: one layout read per resize.
    rectRef.current = { left: rect.left, top: rect.top }
    if (rect.width < 1 || rect.height < 1) return
    // Cap only for pathological dpr; 3 keeps 300% browser zoom crisp.
    const dpr = Math.max(1, Math.min(window.devicePixelRatio || 1, 3))
    const prev = sizeRef.current
    const bw = Math.max(1, Math.round(rect.width * dpr))
    const bh = Math.max(1, Math.round(rect.height * dpr))
    sizeRef.current = { w: rect.width, h: rect.height, dpr }
    if (c.width !== bw || c.height !== bh) {
      c.width = bw
      c.height = bh
    }
    if (prev.w > 0 && (prev.w !== rect.width || prev.h !== rect.height)) {
      // Keep whatever was at the viewport centre at the centre.
      camRef.current.x += (rect.width - prev.w) / 2
      camRef.current.y += (rect.height - prev.h) / 2
      saveCameraSoon()
    }
    // A resize never reframes the graph: the centre-preserving shift above is
    // the whole response, so browser zoom does not undo the operator's view.
    if (!centeredRef.current) centeredRef.current = openingCamera()
    paintOnce()
    requestPublish()
  }

  /**
   * Reduced motion: run the remaining settle synchronously in one bounded stall,
   * then freeze the simulation. Leaving alpha above the settle threshold would
   * keep the frame loop alive, which is exactly the motion the operator opted
   * out of. Bounded so a 4k-node force tick cannot hold the main thread.
   */
  function settleWithoutMotion() {
    settlePendingRef.current = false
    const sim = simRef.current
    if (!sim || !sim.running) return
    warmWithin(sim, { budgetMs: REDUCED_SETTLE_MS, maxTicks: REDUCED_SETTLE_TICKS })
    sim.alpha = 0
    invalidateIndex()
  }

  /** id → node for the current simulation, rebuilt only when the sim is. */
  function indexOf(sim: ForceSim): Map<string, GraphNode> {
    const derived = derivedRef.current
    if (derived.sim !== sim) {
      derived.sim = sim
      derived.byId = new Map(sim.nodes.map((n) => [n.id, n]))
      derived.focusID = null
      derived.display = null
    }
    return derived.byId
  }

  /**
   * 这一帧要亮什么：主干 + 一跳旁支 + 主干边 + 编号。
   *
   * 点选与悬停走**同一个函数**（`chainView`），所以「碰一次」和「点一次」给出的
   * 集合在定义上就是同一份——这正是链路树之前两套集合、看起来亮得不一样的根因。
   * 选中一条线时线本身是主语：只亮两端（edgeFocus），不铺整条链。
   * 结果按 subject 缓存：动画帧只做一次字符串比较。
   */
  function focusDisplay(
    sim: ForceSim,
    selectedID: string | null,
    hoveredID: string | null,
  ): DisplayFocus | null {
    const derived = derivedRef.current
    indexOf(sim)
    const model = modelRef.current
    const selectedEdge = selectedID ? model.edgesById.get(selectedID) : undefined
    const subject = selectedEdge ? selectedID : focusTarget(selectedID, hoveredID)
    const hoveredEdge = !selectedEdge && subject && subject !== selectedID ? model.edgesById.get(subject) : undefined
    const key = selectedEdge ? `edge:${subject}` : hoveredEdge ? `hover:${subject}` : `chain:${subject ?? ''}`
    if (derived.focusID !== key || derived.sim !== sim) {
      derived.focusID = key
      if (selectedEdge || hoveredEdge) {
        derived.display = {
          focus: edgeFocus((selectedEdge ?? hoveredEdge)!),
          near: null,
          edges: null,
        }
      } else {
        const chain = subject ? chainView(model, subject) : null
        derived.display = chain
          ? { focus: new Set(chain.main), near: chain.near, edges: chain.mainEdges }
          : null
      }
    }
    return derived.display
  }

  /**
   * The eased selection-focus mix for this frame. Hover keeps the instant
   * behaviour — it is feedback, not a transition — while a selection (a node or
   * a line) fades its dim in from whatever is already painted.
   */
  function selectionFocusMix(
    target: Set<string> | null,
    eases: boolean,
    now: number,
  ): { mix: number; settledAt: number } {
    return focusFadeMix(focusFadeRef.current, target, eases, now)
  }

  /** Does this layout contain a running intent edge? (Edges are fixed per sim.) */
  function hasRunningEdge(sim: ForceSim): boolean {
    const cached = breathingRef.current
    if (cached.sim === sim) return cached.value
    const value = sim.edges.some((e) => e.kind === 'intent' && e.status === 'running')
    breathingRef.current = { sim, value }
    return value
  }

  /** Does this layout contain a direction waiting on independent review? The
   *  canvas paints a slow verification frame for it, which needs frames even
   *  when the tree itself is settled. */
  function hasReviewingNode(sim: ForceSim): boolean {
    const cached = reviewingRef.current
    if (cached.sim === sim) return cached.value
    const value = sim.nodes.some((n) => n.status === 'reviewing')
    reviewingRef.current = { sim, value }
    return value
  }

  /** Does a step/ghost have a live native-subagent helper? Its satellite pulse is
   *  time-based, so the frame loop must stay alive (30fps) while it breathes. */
  function hasHelperPulse(sim: ForceSim): boolean {
    const show = stateRef.current.showHelpers
    const cached = helperPulseRef.current
    if (cached.sim === sim && cached.show === show) return cached.value
    const value = show && sim.nodes.some((n) => (n.helpers?.active ?? 0) > 0)
    helperPulseRef.current = { sim, show, value }
    return value
  }

  /** Which time-based animations are currently running (reduced motion: none). */
  function animations(now: number): {
    breathing: boolean
    reviewing: boolean
    helpers: boolean
    rippling: boolean
  } {
    const sim = simRef.current
    if (!sim || !motionRef.current) return { breathing: false, reviewing: false, helpers: false, rippling: false }
    return {
      breathing: hasRunningEdge(sim),
      reviewing: hasReviewingNode(sim),
      helpers: hasHelperPulse(sim),
      rippling: now < rippleUntilRef.current,
    }
  }

  /**
   * True while the layout and the pointer are at rest: geometry can be cached,
   * the spatial index is valid, and the renderer may reuse its labels. A pan is
   * "settled" (nodes do not move); a node drag is not.
   */
  function isSettled(): boolean {
    const sim = simRef.current
    if (!sim) return false
    const { breathing, rippling } = animations(performance.now())
    return !sim.running && dragRef.current.mode === null && !breathing && !rippling
  }

  function invalidateIndex() {
    indexRef.current = null
    layoutRevision.current++
    requestPublish()
  }

  /** Build the spatial index for a settled layout on first use (never per frame). */
  function ensureIndex(sim: ForceSim): GraphIndex {
    const cached = indexRef.current
    if (cached && cached.sim === sim) return cached.index
    const index = new GraphIndex(sim.nodes, sim.edges, { layered: sim.mode === 'layered' })
    indexRef.current = { sim, index }
    return index
  }

  function paintOnce() {
    const canvas = canvasRef.current
    const ctx = canvas?.getContext('2d')
    const sim = simRef.current
    if (!canvas || !ctx || !sim) return
    const { w, h, dpr } = sizeRef.current
    if (w === 0 || h === 0) return
    const now = performance.now()
    const { breathing, rippling } = animations(now)
    const state = stateRef.current
    // A settled frame lets the renderer reuse edge geometry and label buckets;
    // while the sim runs, a node is dragged or an animation is playing, it
    // rebuilds them (the canvas only reports the state, render decides).
    const settled = !sim.running && dragRef.current.mode === null && !breathing && !rippling
    // A style write invalidates style/layout for the element; the cursor only
    // changes on a hover/drag transition, so compare before writing.
    const cursor =
      dragRef.current.mode === 'pan' ? 'grabbing' : hoverRef.current ? 'pointer' : 'grab'
    if (cursor !== cursorRef.current) {
      cursorRef.current = cursor
      canvas.style.cursor = cursor
    }
    // A selection — a node or a line — eases its dim in; hover stays instant.
    const display = focusDisplay(sim, state.selected, hoverRef.current)
    const focus = display?.focus ?? null
    const focusFade = selectionFocusMix(focus, Boolean(state.selected), now)
    focusFadeUntilRef.current = focusFade.settledAt
    const paintStart = performance.now()
    paint({
      ctx,
      palette: state.palette,
      width: w,
      height: h,
      dpr,
      cam: camRef.current,
      nodes: sim.nodes,
      edges: sim.edges,
      byId: indexOf(sim),
      selected: state.selected,
      hovered: hoverRef.current,
      matches: state.matches,
      activeMatch: state.activeMatch,
      focus,
      near: display?.near ?? null,
      chainEdges: display?.edges ?? null,
      coldDim: state.coldDim,
      focusMix: focusFade.mix,
      showLabels: state.showLabels,
      time: now,
      layered: sim.mode === 'layered',
      motion: motionRef.current,
      appear: appearRef.current,
      bands: sim.depthBands,
      settled,
      keyboardFocus: keyboardFocusRef.current,
      showHelpers: state.showHelpers,
      timeGate: state.timeGate,
      rightInset: state.rightInset,
      leftInset: state.leftInset,
    })
    // 验收钩子：这一帧的绘制耗时写进 data-frame-ms（每 500ms 一次，不全量写 DOM）。
    // 星图观感是纯绘制改动，成本必须能从页面直接读到，不用另开 profiler。
    const cost = performance.now() - paintStart
    if (firstPaintMs.current === 0) firstPaintMs.current = cost
    // 整帧成本 = 本次模拟步进 + 绘制，和 2D 基线的「稳态每帧」同口径；绘制单独
    // 的耗时分开放，用来判断新动效本身贵不贵。跨帧的等待不计入。
    const frameCost = cost + simMsRef.current
    simMsRef.current = 0
    if (firstTotalMs.current === 0) firstTotalMs.current = frameCost
    const perf = perfFrames.current
    perf.push(cost)
    if (perf.length > 180) perf.shift()
    const totals = perfTotals.current
    totals.push(frameCost)
    if (totals.length > 180) totals.shift()
    if (now - perfPublishedAt.current > 500) {
      perfPublishedAt.current = now
      const at = (list: number[], q: number) => {
        const sorted = [...list].sort((a, b) => a - b)
        return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * q))]
      }
      canvas.dataset.frameMs = JSON.stringify({
        n: perf.length,
        firstPaint: Math.round(firstPaintMs.current * 100) / 100,
        firstTotal: Math.round(firstTotalMs.current * 100) / 100,
        paintP50: Math.round(at(perf, 0.5) * 100) / 100,
        paintP95: Math.round(at(perf, 0.95) * 100) / 100,
        totalP50: Math.round(at(totals, 0.5) * 100) / 100,
        totalP95: Math.round(at(totals, 0.95) * 100) / 100,
      })
    }
  }

  /** Publish cam + size to the view-store, at most once per animation frame. */
  function requestPublish() {
    publishRef.current?.publish({
      camera: camRef.current,
      width: sizeRef.current.w,
      height: sizeRef.current.h,
      layoutRevision: layoutRevision.current,
    })
  }

  /** One coalesced paint, for pointer paths that must not paint per event. */
  function schedulePaint() {
    if (paintRafRef.current) return
    paintRafRef.current = requestAnimationFrame(() => {
      paintRafRef.current = 0
      paintOnce()
    })
  }

  function start() {
    // Paused is not idle: scheduling a frame now would paint a covered canvas
    // and, worse, restart the loop that visibilitychange just cancelled.
    if (pausedRef.current) return
    cancelAnimationFrame(rafRef.current)
    rafRef.current = requestAnimationFrame(loop)
  }

  function loop() {
    rafRef.current = 0
    const sim = simRef.current
    if (!sim || pausedRef.current) return
    // Reduced motion owes a one-shot settle (after a rebuild or a drag reheat);
    // consume it here, before the animation flags are read.
    if (!motionRef.current && settlePendingRef.current) settleWithoutMotion()
    const now = performance.now()
    simMsRef.current = 0
    const { breathing, reviewing, helpers, rippling } = animations(now)
    const dragging = dragRef.current.mode === 'node'
    // The eased focus fade is the only drawing-only effect that needs extra
    // frames; every other animation is either the simulation running or a
    // bounded one-shot.
    const focusFading = now < focusFadeUntilRef.current
    if (!sim.running && !dragging && !breathing && !reviewing && !helpers && !rippling && !focusFading) {
      paintOnce()
      return
    }
    if (sim.running || dragging) {
      // Settle with a per-frame budget instead of exactly one tick: a fast graph
      // (small n) converges in a few hundred ms, a 4k-node Barnes–Hut tick is
      // ~8ms so it still runs one or two per frame. The paint always happens, so
      // the operator sees the layout relax rather than a frozen first frame.
      const tickStart = performance.now()
      const deadline = tickStart + TICK_FRAME_BUDGET_MS
      let ticks = 0
      while (sim.running && ticks < MAX_TICKS_PER_FRAME && performance.now() < deadline) {
        sim.tick()
        ticks++
      }
      // A frozen (reduced-motion) or layered sim is not running: do not force a
      // pointless tick that would only spring a node and invalidate the index.
      if (ticks === 0 && sim.running) sim.tick()
      simMsRef.current = performance.now() - tickStart
      // Positions moved: the settled-layout spatial index is stale.
      invalidateIndex()
    }
    // A running edge on a settled map is a pulse, not motion: 30fps is plenty,
    // and it keeps a 5k-node paint from eating the whole frame every frame.
    const idlePulse = !sim.running && !dragging && !rippling
    if (idlePulse && now - lastPaintRef.current < BREATHING_FRAME_MS) {
      rafRef.current = requestAnimationFrame(loop)
      return
    }
    lastPaintRef.current = now
    paintOnce()
    rafRef.current = requestAnimationFrame(loop)
  }

  /**
   * Pill boxes come from the real text, wrapped rather than clipped: a long CJK
   * title becomes 2–3 lines inside a compact pill, so the chain-tree columns
   * stay close together and no label is cut at the pill edge. Height is derived
   * from the line count; the layered layout already sizes rows from node h.
   */
  function measureNodes(model: GraphModel) {
    const ctx = canvasRef.current?.getContext('2d')
    if (!ctx) return
    const cache = measureRef.current
    // The label set is bounded by the engagement's history, but a cache miss is
    // always cheaper than unbounded growth: drop it and re-measure next rebuild.
    if (cache.size > 4096) cache.clear()
    const TITLE_FONT = `600 12px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    const MODEL_FONT = `500 11px "JetBrains Mono","Cascadia Code",monospace`
    const LINE_H = 16
    const MODEL_H = 15
    // Vertical breathing room inside the pill: the model line used to sit a
    // couple of pixels above the border, which read as one continuous block
    // once several pills were stacked.
    const V_PAD = 16
    for (const n of model.nodes) {
      if (n.type === 'fact' || n.type === 'ghost') continue
      // A pill's box depends on its text and, on a step, on the model line.
      const key = `${n.type}\u0000${n.label}\u0000${n.type === 'step' ? (n.model ?? '') : ''}`
      const cached = cache.get(key)
      if (cached) {
        n.w = cached.w
        n.h = cached.h
        n.labelLines = cached.lines
        continue
      }
      // Wrap only the drawn box types; a force-map step is a diamond and has no
      // label inside it.
      ctx.font = TITLE_FONT
      const wraps = n.type === 'step' || n.type === 'intent' || n.type === 'goal' || n.type === 'project'
      const lines = wraps ? wrapText(ctx, n.label, PILL_TEXT_W, PILL_MAX_LINES) : [n.label]
      let maxLineW = 0
      for (const line of lines) maxLineW = Math.max(maxLineW, ctx.measureText(line).width)
      const hasModel = n.type === 'step' && Boolean(n.model)
      if (hasModel) {
        ctx.font = MODEL_FONT
        maxLineW = Math.max(maxLineW, ctx.measureText(fitText(ctx, n.model!, PILL_TEXT_W)).width)
      }
      if (n.type === 'step') {
        // The chain-tree layout draws a step as a labelled pill: title lines plus
        // the model line. The force map ignores w/h for steps and keeps the
        // diamond, so measuring the wider two-line box only widens the columns.
        n.w = Math.max(40, Math.min(maxLineW / 2 + 12, 140))
        n.h = (lines.length * LINE_H + (hasModel ? MODEL_H : 0) + V_PAD) / 2
      } else {
        n.w = Math.max(46, Math.min(maxLineW / 2 + 12, 140))
        n.h = (lines.length * LINE_H + V_PAD) / 2
      }
      n.labelLines = lines
      cache.set(key, { w: n.w, h: n.h, lines })
    }
  }

  /** Drawn footprint of every node, label margin included: what framing and the
   *  opening camera measure. */
  function nodeExtents(nodes: GraphNode[], layered: boolean): NodeExtent[] {
    return nodes.map((n) => {
      const halfW = Math.max(radiusOf(n), halfExtentX(n, layered))
      // Facts and ghosts are dots; everything else draws a box of half-height h.
      const halfH = Math.max(radiusOf(n), n.type === 'fact' || n.type === 'ghost' ? radiusOf(n) : n.h)
      return { x: n.x, y: n.y, halfW: halfW + 30, halfH: halfH + 30 }
    })
  }

  /**
   * Place the camera for the first layout that has a canvas to live in: layout
   * centre at the viewport centre, at the fixed opening zoom. Returns false
   * while either the layout or the canvas size is still missing, so the caller
   * can retry when it arrives.
   */
  function centerOnOpen(): boolean {
    const sim = simRef.current
    const { w, h } = sizeRef.current
    if (!sim) return false
    const cam = openCamera(nodeExtents(sim.nodes, sim.mode === 'layered'), w, h)
    if (!cam) return false
    camRef.current = cam
    return true
  }

  /**
   * The camera this canvas opens with: the operator's saved view for this
   * project when there is one, otherwise the fixed-zoom centre. Called once;
   * returns false while it still has to wait for data or a canvas size.
   *
   * The opening centre is not persisted: it is derived from the layout, and a
   * reload should derive it from the same complete layout rather than freeze a
   * half-loaded one. Only the operator's own moves are saved.
   */
  function openingCamera(): boolean {
    const saved = readSavedCamera()
    if (saved) {
      camRef.current = saved
      return true
    }
    if (!ready) return false
    return centerOnOpen()
  }

  function readSavedCamera(): Camera | null {
    if (!cameraKey) return null
    try {
      return parseCamera(window.localStorage.getItem(cameraKey))
    } catch {
      /* storage blocked: nothing was saved, fall back to the opening centre */
      return null
    }
  }

  function saveCamera() {
    // Nothing to remember until this canvas has an opening camera: before that
    // camRef is still the placeholder and a leave-before-data would freeze it.
    if (!cameraKey || !centeredRef.current) return
    saveDirtyRef.current = false
    try {
      window.localStorage.setItem(cameraKey, JSON.stringify(camRef.current))
    } catch {
      /* storage unavailable or full: the view just will not survive a reload */
    }
  }

  /** Wheel and resize fire many times per second; write once they pause. */
  function saveCameraSoon() {
    if (!cameraKey || !centeredRef.current) return
    saveDirtyRef.current = true
    if (saveTimerRef.current) window.clearTimeout(saveTimerRef.current)
    saveTimerRef.current = window.setTimeout(() => {
      saveTimerRef.current = 0
      saveCamera()
    }, 180)
  }

  /**
   * Explicit fit: the toolbar's 适配画布 button, and the layout switch (which
   * passes a wider margin and caps the result at 1). Data refreshes and
   * double-clicks never call it: they must not move or resize the map under the
   * operator.
   */
  function fit(
    options: {
      margin?: number
      capAtOne?: boolean
      rightInset?: number
      leftInset?: number
      bottomInset?: number
    } = {},
  ) {
    const sim = simRef.current
    const { w, h } = sizeRef.current
    if (!sim || w === 0) return
    const b = layoutBounds(nodeExtents(sim.nodes, sim.mode === 'layered'))
    if (!b) return
    const margin = Math.min(Math.max(options.margin ?? 0, 0), 0.3)
    // Both floating cards cover the canvas edges — the left filter card and the
    // right dock (node panel / 下发). Frame into the strip that stays visible
    // instead of centring under a card. An inset the caller does not state comes
    // from the live props, so the toolbar's 适配 and a layout switch reserve the
    // same strip as the tree filter: one rule, no per-caller guesswork.
    const right = Math.min(Math.max(options.rightInset ?? stateRef.current.rightInset, 0), w * 0.6)
    const left = Math.min(Math.max(options.leftInset ?? stateRef.current.leftInset, 0), w * 0.6)
    const bottom = Math.min(Math.max(options.bottomInset ?? stateRef.current.bottomInset, 0), h * 0.6)
    const band = Math.max(1, w - left - right)
    const bandH = Math.max(1, h - bottom)
    // Reserve the same fraction of each axis on both sides; the default keeps the
    // toolbar's long-standing 60px total padding. The cards already ate their
    // share of the width, so the fraction applies to what is left.
    const availW = Math.max(1, margin > 0 ? band * (1 - margin * 2) : band - 60)
    const availH = Math.max(1, margin > 0 ? bandH * (1 - margin * 2) : bandH - 60)
    const k = Math.min(availW / Math.max(b.maxX - b.minX, 1), availH / Math.max(b.maxY - b.minY, 1), 2)
    let kk = Math.max(0.12, k)
    // A switched-to layout that would be enlarged is shown one notch out instead:
    // filling the canvas edge to edge loses the sense of where the map continues.
    if (options.capAtOne && kk > 1) kk = 0.9
    camRef.current = {
      k: kk,
      // Centre of the visible band, not of the canvas: half of [left, w - right]
      // across, half of [0, h - bottom] down.
      x: (left + w - right) / 2 - ((b.minX + b.maxX) / 2) * kk,
      y: bandH / 2 - ((b.minY + b.maxY) / 2) * kk,
    }
    paintOnce()
    saveCamera()
    requestPublish()
  }

  /** Zoom by `factor` keeping the viewport centre fixed. */
  function zoomAroundCentre(factor: number) {
    const { w, h } = sizeRef.current
    const cam = camRef.current
    applyZoom(cam, w / 2, h / 2, factor)
  }

  /** Zoom by `factor` keeping screen point (mx,my) fixed. */
  function applyZoom(cam: Camera, mx: number, my: number, factor: number) {
    const k = Math.min(4, Math.max(0.08, cam.k * factor))
    cam.x = mx - ((mx - cam.x) / cam.k) * k
    cam.y = my - ((my - cam.y) / cam.k) * k
    cam.k = k
  }

  useImperativeHandle(ref, () => ({
    fit,
    relayout: () => {
      // Explicit 重新布局: the operator asks for the computed tree again, so the
      // manual drops are discarded rather than springs that now point at them.
      manualRef.current.clear()
      simRef.current?.relayout()
      invalidateIndex()
      start()
    },
    zoom: (factor: number) => {
      zoomAroundCentre(factor)
      paintOnce()
      saveCamera()
      requestPublish()
    },
    // Minimap: centre a world point without changing the zoom.
    centerOn: (x: number, y: number) => {
      const { w, h } = sizeRef.current
      if (w === 0 || h === 0) return
      const cam = camRef.current
      cam.x = w / 2 - x * cam.k
      cam.y = h / 2 - y * cam.k
      paintOnce()
      saveCamera()
      requestPublish()
    },
    // Search hit: frame the node in the band the floating cards leave visible.
    // A hit found while zoomed far out must not stay a speck, so the zoom is at
    // least 1; an already closer view is kept (the operator's zoom wins).
    focusNode: (id: string, options: { rightInset?: number } = {}) => {
      const sim = simRef.current
      const { w, h } = sizeRef.current
      if (!sim || w === 0 || h === 0) return
      const node = indexOf(sim).get(id)
      if (!node) return
      const cam = focusCamera(node, w, h, camRef.current.k, {
        left: stateRef.current.leftInset,
        right: options.rightInset ?? stateRef.current.rightInset,
        bottom: stateRef.current.bottomInset,
      })
      if (!cam) return
      camRef.current = cam
      paintOnce()
      saveCamera()
      requestPublish()
    },
    // Minimap/toolbar: absolute zoom around the viewport centre.
    zoomTo: (k: number) => {
      const { w, h } = sizeRef.current
      if (w === 0 || h === 0) return
      const cam = camRef.current
      const target = Math.min(4, Math.max(0.08, k))
      cam.x = w / 2 - ((w / 2 - cam.x) / cam.k) * target
      cam.y = h / 2 - ((h / 2 - cam.y) / cam.k) * target
      cam.k = target
      paintOnce()
      saveCamera()
      requestPublish()
    },
  }))

  /** The cached client box of the canvas (falls back to one measurement). */
  function canvasBox(): { left: number; top: number } {
    const cached = rectRef.current
    if (cached) return cached
    const rect = canvasRef.current?.getBoundingClientRect()
    const box = rect ? { left: rect.left, top: rect.top } : { left: 0, top: 0 }
    rectRef.current = box
    return box
  }

  function toWorld(clientX: number, clientY: number) {
    const rect = canvasBox()
    const cam = camRef.current
    return {
      x: (clientX - rect.left - cam.x) / cam.k,
      y: (clientY - rect.top - cam.y) / cam.k,
    }
  }

  /** Touch/coarse pointers are less precise: widen the pick radius. */
  function hitScale(): number {
    return coarseRef.current || lastPointerTypeRef.current === 'touch' ? TOUCH_HIT_SCALE : 1
  }

  /** Screen-space edge tolerance in graph units at the current zoom. */
  function edgeTolerance(): number {
    return (HIT_EDGE_TOL_PX / Math.max(camRef.current.k, 0.4)) * hitScale()
  }

  /**
   * What the pointer is on, in the order the map defines it: a node, then a
   * ghost ring, then an edge. A ghost is a dead zone, not a miss — the intent
   * edge is drawn up to the ring, so falling through to the edge would open the
   * direction's panel from a ghost.
   */
  function pickAt(clientX: number, clientY: number): GraphPick {
    const sim = simRef.current
    if (!sim) return { node: null, ghost: null, edge: null }
    const p = toWorld(clientX, clientY)
    const pad = (HIT_PAD_PX / camRef.current.k) * hitScale()
    // While the layout is at rest the grid is valid; while it runs or a node is
    // dragged the positions move every frame, so scanning is cheaper than
    // rebuilding an index that would be stale by the next pointer sample.
    if (isSettled()) return ensureIndex(sim).pickAt(p.x, p.y, pad, edgeTolerance())
    return pickLinear(sim.nodes, sim.edges, indexOf(sim), p.x, p.y, pad, edgeTolerance(), sim.mode === 'layered')
  }

  /** Nearest selectable/hoverable edge, sampled on the same curve the renderer draws. */
  function edgeAt(clientX: number, clientY: number): GraphEdge | null {
    const sim = simRef.current
    if (!sim) return null
    const p = toWorld(clientX, clientY)
    if (isSettled()) return ensureIndex(sim).edgeAt(p.x, p.y, edgeTolerance())
    return edgeHitTest(sim.edges, indexOf(sim), p.x, p.y, edgeTolerance(), sim.mode === 'layered')
  }

  function onWheel(e: React.WheelEvent) {
    // Ctrl/Cmd + wheel is the browser's zoom gesture. Swallowing it here is why
    // zooming with the cursor over the graph did nothing; let the page zoom and
    // the sync above repaints at the new dpr.
    if (e.ctrlKey || e.metaKey) return
    e.preventDefault()
    const rect = canvasBox()
    applyZoom(camRef.current, e.clientX - rect.left, e.clientY - rect.top, Math.exp(-e.deltaY * 0.0015))
    // Same coalescing rule as pan and pinch: a trackpad delivers many wheel
    // events per frame, and every one of them used to repaint a full frame.
    schedulePaint()
    saveCameraSoon()
    requestPublish()
  }

  function onPointerDown(e: React.PointerEvent<HTMLCanvasElement>) {
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      /* capture is best-effort (some browsers throw for a released pointer) */
    }
    e.currentTarget.focus({ preventScroll: true })
    const touch = e.pointerType === 'touch'
    lastPointerTypeRef.current = e.pointerType || 'mouse'
    if (touch) {
      touchesRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      if (touchesRef.current.size === 2) {
        // Second finger: this is a pinch, not a node drag or a pan.
        dragRef.current = NO_DRAG
        hoverMoveRef.current?.cancel()
        beginPinch()
        return
      }
      if (touchesRef.current.size > 2) return
    }
    const pick = pickAt(e.clientX, e.clientY)
    // The edge is the intent. Select it on press, not on release: a click on a
    // thin curve is otherwise easy to lose to a one-pixel pointer drift. A ghost
    // ring is never an edge target: its press is inert (see `pickAt`). An edge
    // without a direction (goal support) is readable on hover, not selectable.
    if (pick.edge?.intent) onSelect(pick.edge.id === stateRef.current.selected ? null : pick.edge.id)
    if (pick.node) {
      const node = pick.node
      dragRef.current = {
        mode: 'node',
        id: node.id,
        sx: e.clientX,
        sy: e.clientY,
        cx: node.x,
        cy: node.y,
        moved: false,
      }
      // The node moves off its settled position: the grid is stale from now on.
      invalidateIndex()
      simRef.current?.reheat(0.35)
      // Reduced motion: the reheat must not animate; the loop freezes it once.
      if (!motionRef.current) settlePendingRef.current = true
    } else {
      dragRef.current = {
        mode: 'pan',
        sx: e.clientX,
        sy: e.clientY,
        cx: camRef.current.x,
        cy: camRef.current.y,
        moved: false,
        // A ghost ring still pans like empty canvas, but releasing without a
        // move must leave the map exactly as it was.
        inert: Boolean(pick.ghost),
        // Selecting the relation happened on press (line above): remember it so
        // the release does not read as a click on empty canvas.
        edge: pick.edge?.intent ? pick.edge.id : undefined,
      }
    }
    paintOnce()
    start()
  }

  function beginPinch() {
    const points = [...touchesRef.current.values()]
    if (points.length < 2 || !canvasRef.current) return
    const rect = canvasBox()
    const [a, b] = points
    const mx = (a.x + b.x) / 2 - rect.left
    const my = (a.y + b.y) / 2 - rect.top
    const cam = camRef.current
    pinchRef.current = {
      dist: Math.max(Math.hypot(a.x - b.x, a.y - b.y), 1),
      // The world point under the midpoint stays under the midpoint while the
      // fingers zoom: that is what makes pinch feel attached to the map.
      worldX: (mx - cam.x) / cam.k,
      worldY: (my - cam.y) / cam.k,
      baseK: cam.k,
    }
  }

  /** Apply the current pinch geometry; returns false when no pinch is active. */
  function updatePinch(): boolean {
    const pinch = pinchRef.current
    if (!pinch || !canvasRef.current) return false
    const points = [...touchesRef.current.values()]
    if (points.length < 2) return false
    const rect = canvasBox()
    const [a, b] = points
    const mx = (a.x + b.x) / 2 - rect.left
    const my = (a.y + b.y) / 2 - rect.top
    const dist = Math.max(Math.hypot(a.x - b.x, a.y - b.y), 1)
    const cam = camRef.current
    cam.k = Math.min(4, Math.max(0.08, (pinch.baseK * dist) / pinch.dist))
    cam.x = mx - pinch.worldX * cam.k
    cam.y = my - pinch.worldY * cam.k
    schedulePaint()
    requestPublish()
    return true
  }

  function onPointerMove(e: React.PointerEvent<HTMLCanvasElement>) {
    const touch = e.pointerType === 'touch'
    if (touch) {
      if (touchesRef.current.has(e.pointerId)) {
        touchesRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      }
      if (pinchRef.current && updatePinch()) return
    }
    lastPointerTypeRef.current = e.pointerType || 'mouse'
    const d = dragRef.current
    if (d.mode === 'node' && d.id) {
      const sim = simRef.current
      const node = sim ? indexOf(sim).get(d.id) : undefined
      if (node) {
        const cam = camRef.current
        // Only the target position changes here; the main loop paints the frame.
        node.x = d.cx + (e.clientX - d.sx) / cam.k
        node.y = d.cy + (e.clientY - d.sy) / cam.k
        node.vx = 0
        node.vy = 0
        d.moved = true
        invalidateIndex()
        // The frame loop is frozen under reduced motion, so the drag has to
        // schedule its own coalesced paint.
        if (!motionRef.current) schedulePaint()
      }
      return
    }
    if (d.mode === 'pan') {
      camRef.current.x = d.cx + (e.clientX - d.sx)
      camRef.current.y = d.cy + (e.clientY - d.sy)
      if (Math.abs(e.clientX - d.sx) + Math.abs(e.clientY - d.sy) > 2) d.moved = true
      // One coalesced paint + publish per frame; the camera is saved on pointerup.
      schedulePaint()
      requestPublish()
      return
    }
    // Hover is not urgent: hand the position to the coalescer, which runs the
    // node/edge hit test once for this frame with the latest position. Copy the
    // coordinates now — React recycles the synthetic event after this handler.
    // 悬停预览关着时整条路径都不走：不命中、不建索引、不发布 hover。
    if (!stateRef.current.hoverPreview) return
    hoverMoveRef.current?.move(e.clientX, e.clientY)
  }

  // 悬停预览关掉的那一刻，指针可能正停在一个节点上：立刻收起那层高亮与右侧
  // 悬停卡片，别让「关掉之后还亮着」变成新的不一致。排队中的命中测试一起取消。
  useEffect(() => {
    if (hoverPreview) return
    hoverMoveRef.current?.cancel()
    if (hoverRef.current !== null) {
      hoverRef.current = null
      setHovered(null)
      paintOnce()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hoverPreview])

  /** Hit-test the last position of a frame and publish the hover only on change. */
  function applyHover(clientX: number, clientY: number) {
    // 开关关着时不做命中测试：悬停只是指针移动，不该有任何集合变化。
    if (!stateRef.current.hoverPreview) return
    const pick = pickAt(clientX, clientY)
    // A relation edge without an intent (goal support) is still worth inspecting:
    // the inspector names the relation even when there is no direction panel.
    // A ghost ring is decorative: hovering it highlights nothing.
    const id = pick.node?.id ?? pick.edge?.id ?? null
    if (id === hoverRef.current) return
    hoverRef.current = id
    // The inspector/minimap subscribe here; GraphView itself does not re-render.
    setHovered(id)
    paintOnce()
  }

  function onPointerUp(e: React.PointerEvent<HTMLCanvasElement>) {
    const touch = e.pointerType === 'touch'
    if (touch) {
      touchesRef.current.delete(e.pointerId)
      if (pinchRef.current) {
        pinchRef.current = null
        // A pinch changes the camera; persist the gesture, not every frame.
        saveCamera()
        requestPublish()
        // One finger may still be down: continue that finger as a pan from
        // where it is, so the map keeps following it instead of jumping when
        // its next pointermove arrives.
        const rest = touchesRef.current.values().next().value
        if (rest) {
          dragRef.current = {
            mode: 'pan',
            sx: rest.x,
            sy: rest.y,
            cx: camRef.current.x,
            cy: camRef.current.y,
            moved: true,
          }
          return
        }
      }
      if (touchesRef.current.size > 0) return
    }
    const d = dragRef.current
    dragRef.current = NO_DRAG
    // A pan changes the camera; a click or node drag does not. Persist on the
    // released gesture instead of on every pointermove.
    if (d.mode === 'pan' && d.moved) {
      saveCamera()
      requestPublish()
    }
    if (d.mode === 'pan' && !d.moved) {
      // Edge selection already happened on press; a click on empty canvas only
      // clears the current selection. A press that started on a ghost ring is
      // inert, and one that started on a curve keeps what the press selected —
      // the release point can drift a couple of pixels off a thin line.
      if (!d.inert && !d.edge && !edgeAt(e.clientX, e.clientY)) onSelect(null)
    }
    if (d.mode === 'node' && d.id && !d.moved) {
      onSelect(d.id === stateRef.current.selected ? null : d.id)
    }
    if (d.mode === 'node' && d.id && d.moved) {
      // A drop is the operator placing the node: hold it there instead of letting
      // the layered spring (or the next poll) reset it.
      const sim = simRef.current
      const node = sim ? indexOf(sim).get(d.id) : undefined
      if (sim && node && sim.mode === 'layered') {
        sim.holdAt(d.id, node.x, node.y)
        manualRef.current.set(d.id, { x: node.x, y: node.y })
      }
    }
    paintOnce()
  }

  function onPointerCancel(e: React.PointerEvent<HTMLCanvasElement>) {
    if (e.pointerType === 'touch') {
      touchesRef.current.delete(e.pointerId)
      if (pinchRef.current && touchesRef.current.size < 2) {
        pinchRef.current = null
        // A cancelled pinch still moved the camera: keep where the operator is.
        saveCamera()
        requestPublish()
      }
    }
    dragRef.current = NO_DRAG
    paintOnce()
  }

  function onPointerLeave(e: React.PointerEvent<HTMLCanvasElement>) {
    // Touch pointers leave after their up; only the ended finger goes away (the
    // other finger of a pinch is still down).
    if (e.pointerType === 'touch') {
      touchesRef.current.delete(e.pointerId)
      if (touchesRef.current.size < 2) pinchRef.current = null
      return
    }
    // Drop a hover test still queued for this frame, or it would re-hover the
    // node the pointer just left.
    hoverMoveRef.current?.cancel()
    if (hoverRef.current !== null) {
      hoverRef.current = null
      setHovered(null)
      paintOnce()
    }
  }

  /** Keyboard order: depth, then y, then x, then id — deterministic and stable. */
  function keyboardOrder(sim: ForceSim): GraphNode[] {
    const cached = keyboardOrderRef.current
    if (cached.sim === sim) return cached.nodes
    const nodes = sim.nodes
      .filter((n) => n.type !== 'ghost')
      .sort((a, b) => {
        if (a.depth !== b.depth) return a.depth - b.depth
        if (a.y !== b.y) return a.y - b.y
        if (a.x !== b.x) return a.x - b.x
        return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
      })
    keyboardOrderRef.current = { sim, nodes }
    return nodes
  }

  /**
   * Announce where the keyboard ring landed. The ring is painted on the canvas,
   * so a screen-reader user would otherwise move it silently; the polite live
   * region is the only channel that reports it.
   */
  function announceKeyboardFocus(id: string | null, order?: GraphNode[]) {
    const region = liveRef.current
    if (!region) return
    const sim = simRef.current
    const node = id && sim ? indexOf(sim).get(id) : undefined
    if (!node) {
      region.textContent = ''
      return
    }
    const at = order ? order.findIndex((n) => n.id === id) + 1 : 0
    const where = order && at > 0 ? `第 ${at}/${order.length} 个` : ''
    region.textContent = `键盘定位${where}：${node.label || '（无标题）'}。Enter ${
      stateRef.current.selected === node.id ? '收起' : '打开'
    }，Escape 清除选择。`
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLCanvasElement>) {
    const sim = simRef.current
    if (!sim) return
    if (e.key === 'Escape') {
      const hadRing = keyboardFocusRef.current !== null
      keyboardFocusRef.current = null
      onSelect(null)
      if (hadRing) announceKeyboardFocus(null)
      paintOnce()
      e.preventDefault()
      return
    }
    if (e.key === 'Enter') {
      const id = keyboardFocusRef.current
      if (!id) return
      e.preventDefault()
      onSelect(id === stateRef.current.selected ? null : id)
      announceKeyboardFocus(id, keyboardOrder(sim))
      return
    }
    if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault()
      const order = keyboardOrder(sim)
      if (order.length === 0) return
      keyboardFocusRef.current = (e.key === 'Home' ? order[0] : order[order.length - 1]).id
      announceKeyboardFocus(keyboardFocusRef.current, order)
      paintOnce()
      return
    }
    const forward = e.key === 'ArrowRight' || e.key === 'ArrowDown'
    const backward = e.key === 'ArrowLeft' || e.key === 'ArrowUp'
    if (!forward && !backward) return
    e.preventDefault()
    const order = keyboardOrder(sim)
    if (order.length === 0) return
    const current = keyboardFocusRef.current
    const at = current ? order.findIndex((n) => n.id === current) : -1
    let next = at < 0 ? 0 : at + (forward ? 1 : -1)
    if (next < 0) next = 0
    if (next >= order.length) next = order.length - 1
    keyboardFocusRef.current = order[next].id
    announceKeyboardFocus(keyboardFocusRef.current, order)
    paintOnce()
  }

  function onBlur() {
    // The ring belongs to keyboard focus on the canvas, not to the selection.
    if (keyboardFocusRef.current === null) return
    keyboardFocusRef.current = null
    announceKeyboardFocus(null)
    paintOnce()
  }

  const selectedNode = selected ? model.byId.get(selected) : null
  const selectedEdge = !selectedNode && selected ? model.edgesById?.get(selected) ?? null : null
  const selectionText = selectedNode
    ? `当前选中：${selectedNode.label}。`
    : selectedEdge
      ? `当前选中关系：${edgeKindLabel(selectedEdge.kind)}。`
      : '当前未选中节点。'
  // The label is the canvas's *name*; the shortcut help is a description so a
  // screen reader can skip it after the first visit.
  const ariaLabel = `${model.nodes.length} 个节点，${model.edges.length} 条边。${selectionText}`
  const helpID = useId()

  return (
    <div ref={wrapRef} className="relative h-full w-full">
      {/* Shortcut help + the keyboard-ring announcer. Both are visual-only off:
          the canvas is the interactive surface, these two strings are its
          accessible instructions and status channel. */}
      <p id={helpID} className="sr-only">
        图谱画布。方向键在节点间移动（Home / End 到首尾），Enter 打开或收起键盘定位的节点，Escape
        清除选择；滚轮缩放，拖拽平移，拖拽节点保留位置。等价的纯文本入口是工具栏的「节点列表」。
      </p>
      <div ref={liveRef} className="sr-only" aria-live="polite" aria-atomic="true" />
      <canvas
        ref={canvasRef}
        data-testid="graph-canvas"
        tabIndex={0}
        role="application"
        aria-roledescription="交互式图谱"
        aria-label={ariaLabel}
        aria-describedby={helpID}
        aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight Home End Enter Escape"
        className="absolute inset-0 touch-none outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring/70 focus-visible:outline-offset-2"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onPointerLeave={onPointerLeave}
        onKeyDown={onKeyDown}
        onBlur={onBlur}
      />
    </div>
  )
})
