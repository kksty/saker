// Painting the engagement graph.
//
// This module owns what a frame costs, not what the graph means. Positions come
// from the simulation, relations from the ledger, and docs/33 §4.3 keeps the
// budget honest here:
//
//   viewport clipping — a node/edge/label outside the camera rectangle is not
//                       drawn (clipping is a draw filter: the model is untouched)
//   LOD               — at low zoom the map is dots and short lines; floating
//                       labels obey a per-frame budget, emphasis labels never do
//   geometry cache    — while the layout is settled the Bézier of every edge is
//                       reused by edge id; a new picture (edge set change) drops it
//   label occupancy   — collision boxes live in a 64 world-unit grid, not a list
//
// Everything here must stay loadable without a DOM: node tests import this
// module (and the value imports below carry their .ts extension for Node ESM),
// so `document` is only touched lazily inside `paint`.

import type { GraphEdge, GraphNode, IntentOrigin } from './model.ts'
import { edgeKindLabel } from './model.ts'
import { revealedIn } from './timeline.ts'
import { radiusOf, type DepthBand } from './sim.ts'

export interface Camera {
  x: number
  y: number
  k: number
}

/**
 * The circle drawn for one node at dot-level zoom.
 *
 * `radiusOf` is the collision circle: for a pill it scales with the label
 * footprint, which is right while the pill is drawn and wrong the moment the
 * zoom drops to dots — a 200px goal statement became a ball twenty times the
 * size of a fact. A dot represents the node, not its text, so pills are capped;
 * the screen-space floor keeps every dot visible when zoomed far out.
 */
export function dotRadius(n: GraphNode, camK: number): number {
  const floor = 2 / Math.max(Number.isFinite(camK) && camK > 0 ? camK : 1, 0.001)
  const base =
    n.type === 'intent' || n.type === 'goal' || n.type === 'project'
      ? Math.min(radiusOf(n), 11)
      : radiusOf(n)
  return Math.max(base, floor)
}

export interface PaintOptions {
  ctx: CanvasRenderingContext2D
  width: number
  height: number
  dpr: number
  cam: Camera
  nodes: GraphNode[]
  edges: GraphEdge[]
  byId: Map<string, GraphNode>
  selected: string | null
  hovered: string | null
  matches: Set<string> | null
  /** The search row the list is on: a doubled ring, distinct from a plain match. */
  activeMatch?: string | null
  focus: Set<string> | null
  /**
   * 一跳旁支：主干之外留在图上、但次一档亮的节点（渗透链路，graph/chain.ts）。
   * 主干（`focus`）满亮、旁支在满亮与压暗之间、其余压暗——三级都看得见。
   */
  near?: ReadonlySet<string> | null
  /** 主干上的边：比同层其它边粗一档，顺序才读得出来。 */
  chainEdges?: ReadonlySet<string> | null
  /**
   * 冷热压暗（工具条「画什么」里的开关，默认关）：开时链外节点压到
   * `FOCUS_COLD_ALPHA`、一跳旁支半亮；关时链外节点照常不透明度，链路靠加粗的
   * 主干边和节点编号读出来。两种状态下「当前链路」都是同一份 `chainView` 集合。
   */
  coldDim?: boolean
  /** Operator toggle: show every intent label, not only the focused/running ones. */
  showLabels: boolean
  time: number
  /** Layered layout draws depth bands; the force map stays paper-like. */
  layered: boolean
  /** False honours `prefers-reduced-motion`: no comets, pulses or ripples. */
  motion: boolean
  /** Node id → performance.now() when it first appeared (scale-in + ripple). */
  appear: Map<string, number>
  /** Depth columns, computed by the layered simulation. */
  bands: DepthBand[]
  /** True = layout and drags are at rest: edge geometry and label buckets may be
   *  reused. False = rebuild the geometry every frame (nodes are still moving). */
  settled: boolean
  /** 0..1 eased focus dim (selection transition). undefined/1 = the cold alpha
   *  applies in full; a smaller value fades the dim in without moving anything. */
  focusMix?: number
  /** Keyboard focus node (canvas arrow-key selection); undefined = none. */
  keyboardFocus?: string | null
  /** Operator toggle (GraphFilters): draw the native-subagent satellites. A
   *  drawing-only switch; the panel aggregate is never hidden. Default true. */
  showHelpers?: boolean
  /** Time axis gate: rows ranked beyond `cursor` are not drawn. Untimed ids are
   *  absent from `rank` and always pass (graph/timeline.ts). A Map lookup per
   *  node keeps the frame O(drawn), not O(model): the cursor advances an index
   *  into a pre-sorted order instead of re-filtering the nodes every frame. */
  timeGate?: { rank: ReadonlyMap<string, number>; cursor: number } | null
  /**
   * Right-edge dock width in CSS px (node panel / 下发), 0 when closed. Labels
   * treat `width - rightInset` as the usable edge: a label that would run under
   * the panel flips to the node's other side instead of disappearing behind it.
   */
  rightInset?: number
  /** Left-edge card width in CSS px (the filter card), 0 when it is not drawn.
   *  Labels keep right of it for the same reason as the dock. */
  leftInset?: number
}

// ---------------------------------------------------------------------------
// Palette. The canvas paints the same dark console the DOM does, so its colours
// are the app.css token values: canvas #0a0c10, surface #11151c, hairline
// #1b2129, ink #e7eaf0/#a9b2c1/#788294, primary #f2554d, success #3dd68c,
// warning #e5b53c, caution #f0883e, info #6cb6ff, review #2dd4bf, ai #b388ff,
// stopped #de93b2.
// `nodeColor` and `edgeColor` stay the only place a colour is chosen, and the
// legend imports the same tables instead of re-declaring hexes that drift from
// the brush.
// ---------------------------------------------------------------------------
export const CANVAS = {
  background: '#0a0c10',
  surface: '#11151c',
  hairline: '#1b2129',
  ink: '#e7eaf0',
  ink2: '#a9b2c1',
  ink3: '#788294',
  primary: '#f2554d',
  success: '#3dd68c',
  warning: '#e5b53c',
  caution: '#f0883e',
  info: '#6cb6ff',
  review: '#2dd4bf',
  ai: '#b388ff',
  stopped: '#de93b2',
} as const

// Solid red + marching dashes + a travelling comet is the live execution hue.
// Teal + a slow breathing frame is "independent verification in progress": it
// must not borrow the blue hard-gate hue. Blue is the gate hue, and only as a
// *solid* line: a blocked premise is dotted red (gate currently unsatisfied),
// and a reviewing direction's produced-fact edge is dashed teal. A failed intent
// is not executing anything, so it gets the ai hue instead of the live red.
export const FACT_COLORS: Record<string, string> = {
  vuln: CANVAS.primary,
  cred: CANVAS.ai,
  host: CANVAS.info,
  service: CANVAS.success,
  loot: CANVAS.warning,
  artifact: CANVAS.caution,
  note: CANVAS.ink3,
}

export const INTENT_COLORS: Record<string, string> = {
  running: CANVAS.primary,
  // Pre-run / waiting family: pending, claimed and waiting have no live work
  // yet, so they share amber. They are separated by the status label and by
  // the dashed produced edge; review_failed keeps its own orange.
  claimed: CANVAS.warning,
  pending: CANVAS.warning,
  waiting: CANVAS.warning,
  // Reviewing is the verification hue (teal), deliberately not the premise
  // gate's blue. Its intent edges are dashed, so a solid blue line still only
  // means 必要前提.
  reviewing: CANVAS.review,
  done: CANVAS.success,
  failed: CANVAS.ai,
  review_failed: CANVAS.caution,
  parked: CANVAS.ink3,
  // Not a ledger status: `failed`/`pending` with hold=true, i.e. an explicit
  // stop. It gets its own hue (and the 已停止 label) so a policy stop never
  // reads as a failed attempt.
  stopped: CANVAS.stopped,
}

// Green is the "reading choice" family: a reference is a dashed green line, a
// pin is the same hue as a solid double line, so the operator never has to guess
// whether a relation is a gate (blue premise) or context they asked to see.
export const PIN_COLOR = 'rgba(61,214,140,0.95)'
export const REFERENCE_COLOR = 'rgba(61,214,140,0.82)'
/**
 * 选中后的“冷”不透明度：0.06 时非邻域只剩一层几乎看不见的底纹，选中的链满亮、
 * 一跳旁支半亮——三档一眼分得开。0.38 那版“被压暗”但仍会抢注意力，0.12 那版
 * 与旁支拉不开（旁支 = cold + (1-cold)*0.55 ≈ 0.52），所以再压一档。
 * 节点、边、标签都走同一处 `dim()`/`focusAlpha()`，不会只压点不压线。
 */
export const FOCUS_COLD_ALPHA = 0.06

/** 链上事实的短标签：够分辨两条事实，又不至于在地图上铺成一片。 */
export const FOCUS_FACT_LABEL_CHARS = 6
/** 闭包超过这个规模就不再逐条命名：链路太长时标签只会互相压住。 */
export const FOCUS_LABEL_MAX = 40

/**
 * 一条事实在焦点链上的短标签。画布与页面那份 DOM 清单共用它，否则肉眼看到的
 * 和读屏/自动化读到的是两套截断规则。
 *
 * 冒号前一般是分类前缀（示例／事实／主机），冒号后才是这条事实的名字：直接截前
 * 6 个字会让同前缀的事实全叫一个名字（实测 5 条链上事实都读成「示例：网关…」）。
 * 没有冒号、或冒号后不足两个字，才退回整串的前 6 个字。
 */
export function focusFactLabel(label: string): string {
  const text = (label ?? '').trim()
  const at = text.search(/[：:]/)
  const tail = at >= 0 ? text.slice(at + 1).trim() : ''
  return clip(tail.length >= 2 ? tail : text, FOCUS_FACT_LABEL_CHARS)
}

/**
 * 标签该不该画到节点左边。
 *
 * 左侧筛选卡与右侧面板（节点面板 / 下发 dock）都会盖住画布边：可用边界是
 * `[leftInset, canvasWidth - rightInset]`。标签会伸到面板底下就翻到左边；左边
 * 也放不下才留在右边——宁可被压住一点，也不能跑到卡片或画布外。
 */
export function factLabelFlip(
  screenX: number,
  textWidth: number,
  radius: number,
  canvasWidth: number,
  rightInset: number,
  leftInset = 0,
): boolean {
  const usableRight = canvasWidth - Math.max(0, rightInset)
  const usableLeft = Math.max(0, leftInset)
  const overflows = screenX + radius + 2 + textWidth + 8 > usableRight
  if (!overflows) return false
  return screenX - radius - 2 - textWidth - 8 >= usableLeft
}

/**
 * Eased focus dim. `mix` 0 = no dimming (the frame before a selection fades in),
 * 1 = the full cold alpha. Exported so the canvas and its tests share one rule.
 */
export function focusColdAlpha(mix: number): number {
  const m = Math.min(1, Math.max(0, Number.isFinite(mix) ? mix : 1))
  // Exact endpoints: the settled frame must be exactly the documented cold alpha
  // (not 0.17999999999999994), and the pre-transition frame must be fully opaque.
  if (m >= 1) return FOCUS_COLD_ALPHA
  if (m <= 0) return 1
  return 1 - (1 - FOCUS_COLD_ALPHA) * m
}

export const KEYBOARD_FOCUS_RING = 'rgba(108,182,255,0.95)'
export const REVIEW_RING = 'rgba(229,181,60,0.9)'
export const MATCH_RING = 'rgba(242,85,77,0.7)'
/** The active search hit: full-alpha, doubled, so ↑↓ has a visible target. */
export const ACTIVE_MATCH_RING = 'rgba(242,85,77,0.95)'
export const HOVER_RING = 'rgba(231,234,240,0.42)'
/** The standing marker on the selected node. Full alpha, drawn last: the panel
 *  says what is selected, but the map has to keep showing *where* it is after
 *  the pointer moves away. Shape as well as colour, so it survives colour-blind
 *  viewing, the dark theme and the narrow layout. */
export const SELECTION_RING = 'rgba(236,240,248,0.95)'

/** Which nodes dim, and by how much.
 *
 *  A selected node dims everything outside its focus set, but a **search hit
 *  never dims**: the operator asked to find it, and letting the selection hide
 *  the result meant the hit ring was painted at 38% and effectively lost. Pure,
 *  so the coexistence of `matches` and `focus` can be asserted without a canvas. */
export function focusAlpha(
  id: string,
  o: {
    focus?: ReadonlySet<string> | null
    matches?: ReadonlySet<string> | null
    focusMix?: number
  },
): number {
  if (!o.focus) return 1
  if (o.matches?.has(id)) return 1
  return o.focus.has(id) ? 1 : focusColdAlpha(o.focusMix ?? 1)
}

/**
 * 一条链的高亮口径（力导向和链路树共用 `graph/chain.ts` 的同一份集合）：
 * 主干满亮、一跳旁支次亮、其余按「冷热压暗」开关。
 *
 * 开关关掉（默认）时链外节点不压暗：默认显示就是「当前链路高亮」——主干边加粗、
 * 节点按顺序编号，冷压暗只是可选加深。搜索命中仍满亮（focusAlpha 的既有规则）。
 */
export function chainDimAlpha(
  id: string,
  o: {
    focus?: ReadonlySet<string> | null
    near?: ReadonlySet<string> | null
    matches?: ReadonlySet<string> | null
    focusMix?: number
    coldDim?: boolean
  },
): number {
  if (o.focus?.has(id)) return 1
  const cold = o.coldDim ? focusColdAlpha(o.focusMix ?? 1) : 1
  if (o.near?.has(id)) return Math.min(1, cold + (1 - cold) * 0.55)
  return focusAlpha(id, { focus: o.focus, matches: o.matches, focusMix: o.coldDim ? o.focusMix : 0 })
}

/** The relation colours the legend has to explain, in one place. */
export const EDGE_COLORS = {
  produced: 'rgba(242,85,77,0.45)',
  brief: 'rgba(120,130,148,0.65)',
  host: 'rgba(120,130,148,0.5)',
  goal: 'rgba(229,181,60,0.62)',
  // A hard prerequisite is context, not the subject: faded (and drawn before
  // the intent chain) so its blue reads as scaffolding instead of covering the
  // other lines. A blocked gate keeps its stronger red on purpose.
  premise: 'rgba(108,182,255,0.72)',
  // A premise whose source fact has not passed a review is a blocked gate, not
  // a normal prerequisite. Red is otherwise the live-execution hue, so this is
  // a dotted red: "hard gate, currently unsatisfied", not "running".
  premiseBlocked: 'rgba(242,85,77,0.85)',
  reference: REFERENCE_COLOR,
  pin: PIN_COLOR,
  supports: 'rgba(229,181,60,0.7)',
} as const

/** Boundary leaves keep a glyph per reason they are not on the map. */
export const BOUNDARY_COLORS = {
  not_loaded: CANVAS.ink3,
  missing: 'rgba(242,85,77,0.72)',
  rejected: 'rgba(179,136,255,0.72)',
  withdrawn: 'rgba(229,181,60,0.72)',
} as const

export const PROJECT_COLOR = CANVAS.ink3
export const GOAL_COLORS = { met: CANVAS.success, open: CANVAS.warning } as const

// 发起者 is the graph's *second* channel: status colour stays the first one, so
// these marks are shape-only and neutral — reading them never depends on hue.
// The one outlier (unknown/legacy) draws nothing: an empty created_by is not a
// fourth role, and a "?" on every historical row would be noise, not information.
export const ORIGIN_MARKER_COLOR = 'rgba(231,234,240,0.9)'
/** Native-subagent satellites: neutral by design (temporary helper, not work). */
export const HELPER_MARKER_COLOR = 'rgba(231,234,240,0.8)'

/** The shape drawn for one provenance class; null = no mark at all. */
export type OriginMarker = 'diamond' | 'circle' | 'double-ring' | null

export function originMarker(origin: IntentOrigin | undefined): OriginMarker {
  switch (origin) {
    case 'commander':
      return 'diamond' // 实心菱形
    case 'executor':
      return 'circle' // 空心圆
    case 'operator':
      return 'double-ring' // 双环
    default:
      return null // unknown / legacy: nothing is claimed
  }
}

export function nodeColor(n: GraphNode): string {
  if (n.type === 'ghost' || n.type === 'step' || n.type === 'intent') {
    if (n.stopped) return INTENT_COLORS.stopped
    return INTENT_COLORS[n.status ?? ''] ?? CANVAS.ink3
  }
  if (n.type === 'goal') return n.status === 'met' ? GOAL_COLORS.met : GOAL_COLORS.open
  // A boundary leaf is not a fact: its colour says why it is not on the map.
  // not_loaded is expandable; missing is gone from the ledger; rejected exists
  // but was explicitly negated. The same grey for all three hid the difference.
  if (n.type === 'boundary') {
    if (n.boundaryState === 'missing') return BOUNDARY_COLORS.missing
    if (n.boundaryState === 'rejected') return BOUNDARY_COLORS.rejected
    if (n.boundaryState === 'withdrawn') return BOUNDARY_COLORS.withdrawn
    return BOUNDARY_COLORS.not_loaded
  }
  return FACT_COLORS[n.kind ?? ''] ?? CANVAS.ink3
}

function edgeColor(e: GraphEdge): string {
  const kind = e.kind
  if (kind === 'intent') {
    if (e.stopped) return INTENT_COLORS.stopped
    return INTENT_COLORS[e.status ?? ''] ?? CANVAS.ink3
  }
  switch (kind) {
    case 'brief':
      return EDGE_COLORS.brief
    case 'host':
      return EDGE_COLORS.host
    case 'goal':
      return EDGE_COLORS.goal
    // Solid blue = a hard prerequisite; a reference must never borrow that
    // colour. A blocked gate (unreviewable/unverified/missing source) is dotted
    // red — never the live-execution red.
    case 'premise':
      return e.blocked ? EDGE_COLORS.premiseBlocked : EDGE_COLORS.premise
    // Green dashed = a reading choice (reference); green solid double = a pin.
    case 'reference':
      return REFERENCE_COLOR
    case 'pin':
      return PIN_COLOR
    // Gold = a fact that backs an achieved goal's evidence.
    case 'supports':
      return EDGE_COLORS.supports
    // Unknown/legacy relation kind: keep the historical quiet produced stroke.
    // The console never invents a new relation colour for a kind it does not know.
    default:
      return EDGE_COLORS.produced
  }
}

/** The static visual encoding of one relation: colour plus whether the line
 *  itself is dashed. `running` intent edges are not `dashed` here: their dashes
 *  march (animated in the paint loop), which is the live-execution signal. */
export interface EdgeStyle {
  color: string
  dashed: boolean
}

/**
 * A relation's colour and line type, as one contract the painter and the legend
 * both describe. The rules that matter:
 *   - solid blue is reserved for a necessary premise (the only hard-gate style);
 *   - a blocked premise is dotted red (admission would refuse it);
 *   - an intent that has not started (pending/claimed/waiting/parked) or is
 *     waiting for review draws a dashed produced edge;
 *   - a reviewing direction's produced-fact edge is dashed and teal, never the
 *     premise blue, so a colour never has to be decoded from the shape;
 *   - references and goal support are dashed families of their own.
 */
export function edgeStyle(e: GraphEdge): EdgeStyle {
  const intentEdge = e.kind === 'intent'
  const dashed =
    (intentEdge &&
      (e.status === 'pending' ||
        e.status === 'claimed' ||
        e.status === 'waiting' ||
        e.status === 'parked' ||
        e.status === 'reviewing')) ||
    (e.kind === 'premise' && Boolean(e.blocked)) ||
    e.kind === 'reference' ||
    e.kind === 'supports'
  return { color: edgeColor(e), dashed }
}

/** Horizontal half-extent of the drawn shape: where an edge may touch it. */
export function halfExtentX(n: GraphNode, layered: boolean): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  // In the chain-tree view a step is a labelled pill; the force map keeps the
  // small diamond so springs still read as a force picture.
  if (n.type === 'step') return layered ? Math.max(n.w, 8) : 8
  return Math.max(n.w, radiusOf(n))
}

/** Distance from the centre to the drawn boundary in a given direction. */
function boundaryAlong(n: GraphNode, ux: number, uy: number): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  if (n.type === 'step') return 8
  const tx = Math.abs(ux) > 1e-6 ? n.w / Math.abs(ux) : Infinity
  const ty = Math.abs(uy) > 1e-6 ? n.h / Math.abs(uy) : Infinity
  return Math.min(tx, ty)
}

/** A cubic Bézier in graph coordinates, shared by the painter and hit-testing. */
export interface EdgeCurve {
  sx: number
  sy: number
  c1x: number
  c1y: number
  c2x: number
  c2y: number
  tx: number
  ty: number
}

/**
 * Edge geometry. In the chain-tree view every hop goes right, so an edge leaves
 * the right side of its source and enters the left side of its target with
 * horizontal tangents: a fan of children nests instead of crossing, and a line
 * never runs through the pill it starts from. The force map keeps the radial,
 * slightly bent spoke.
 */
export function edgeCurve(
  s: GraphNode,
  t: GraphNode,
  layered: boolean,
  kind: GraphEdge['kind'],
): EdgeCurve {
  if (layered) {
    const sx = s.x + halfExtentX(s, true)
    const sy = s.y
    const tx = t.x - halfExtentX(t, true)
    const ty = t.y
    const dx = tx - sx
    if (dx > 16) {
      return {
        sx,
        sy,
        c1x: sx + dx * 0.45,
        c1y: sy,
        c2x: tx - dx * 0.45,
        c2y: ty,
        tx,
        ty,
      }
    }
    // A cross-link pointing back (host edge between two fact columns, a retry
    // lineage, …) is not a forward hop: draw it as the ordinary curved spoke so
    // it visibly differs from the chain.
  }
  const dx = t.x - s.x
  const dy = t.y - s.y
  const d = Math.max(Math.hypot(dx, dy), 0.001)
  const ux = dx / d
  const uy = dy / d
  const r1 = boundaryAlong(s, ux, uy)
  const r2 = boundaryAlong(t, ux, uy)
  const sx = s.x + ux * r1
  const sy = s.y + uy * r1
  const tx = t.x - ux * r2
  const ty = t.y - uy * r2
  const bend = kind === 'brief' ? 0.05 : 0.08
  const mx = (sx + tx) / 2
  const my = (sy + ty) / 2
  const ctrlX = mx - (ty - sy) * bend
  const ctrlY = my + (tx - sx) * bend
  return {
    sx,
    sy,
    c1x: sx + (2 / 3) * (ctrlX - sx),
    c1y: sy + (2 / 3) * (ctrlY - sy),
    c2x: tx + (2 / 3) * (ctrlX - tx),
    c2y: ty + (2 / 3) * (ctrlY - ty),
    tx,
    ty,
  }
}

/** Point on a cubic at t ∈ [0,1]; the live comet rides this. */
export function pointOnCurve(c: EdgeCurve, t: number): { x: number; y: number } {
  const inv = 1 - t
  const a = inv * inv * inv
  const b = 3 * inv * inv * t
  const d = 3 * inv * t * t
  const e = t * t * t
  return {
    x: a * c.sx + b * c.c1x + d * c.c2x + e * c.tx,
    y: a * c.sy + b * c.c1y + d * c.c2y + e * c.ty,
  }
}

// ---------------------------------------------------------------------------
// Viewport clipping (docs/33 §4.3). These are pure helpers on purpose: the node
// tests assert the draw filter with no canvas involved.
// ---------------------------------------------------------------------------

export interface Viewport {
  x0: number
  y0: number
  x1: number
  y1: number
}

/** World-space margin around the visible rectangle, so a node straddling the
 *  canvas edge still draws. */
export const VIEWPORT_MARGIN = 80

/** The world rectangle the camera can currently see (plus the margin). */
export function viewportOf(cam: Camera, width: number, height: number): Viewport {
  const k = Math.max(cam.k, 0.001)
  return {
    x0: -cam.x / k - VIEWPORT_MARGIN,
    y0: -cam.y / k - VIEWPORT_MARGIN,
    x1: (width - cam.x) / k + VIEWPORT_MARGIN,
    y1: (height - cam.y) / k + VIEWPORT_MARGIN,
  }
}

function pointInViewport(x: number, y: number, vp: Viewport): boolean {
  return x >= vp.x0 && x <= vp.x1 && y >= vp.y0 && y <= vp.y1
}

/** Half extents of a node's drawn footprint (labels excluded; a label's own box
 *  is what the label pass measures). */
function nodeHalfW(n: GraphNode): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  return Math.max(n.w, radiusOf(n))
}

function nodeHalfH(n: GraphNode): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  return Math.max(n.h, radiusOf(n))
}

/** Is any part of the node's glyph inside the viewport? */
export function isNodeVisible(n: GraphNode, vp: Viewport): boolean {
  return (
    n.x + nodeHalfW(n) >= vp.x0 &&
    n.x - nodeHalfW(n) <= vp.x1 &&
    n.y + nodeHalfH(n) >= vp.y0 &&
    n.y - nodeHalfH(n) <= vp.y1
  )
}

/** Conservative curve-vs-rectangle test: the Bézier lies inside the convex hull
 *  of its four control points, so a hull that misses the viewport misses the
 *  viewport too. */
function curveInViewport(c: EdgeCurve, vp: Viewport): boolean {
  const minX = Math.min(c.sx, c.c1x, c.c2x, c.tx)
  const maxX = Math.max(c.sx, c.c1x, c.c2x, c.tx)
  if (maxX < vp.x0 || minX > vp.x1) return false
  const minY = Math.min(c.sy, c.c1y, c.c2y, c.ty)
  const maxY = Math.max(c.sy, c.c1y, c.c2y, c.ty)
  return !(maxY < vp.y0 || minY > vp.y1)
}

/** Is the drawn curve inside the viewport? Uses the control-point hull, so an
 *  edge that crosses the screen between two off-screen nodes still draws. */
export function isEdgeVisible(
  s: GraphNode,
  t: GraphNode,
  kind: GraphEdge['kind'],
  layered: boolean,
  vp: Viewport,
): boolean {
  return curveInViewport(edgeCurve(s, t, layered, kind), vp)
}

/** The nodes the paint pass may draw: clipping is a filter over the same array,
 *  never a mutation of the model. Exported so the test can count the draws. */
export function clipNodes(nodes: readonly GraphNode[], vp: Viewport): GraphNode[] {
  return clipInto(nodes, vp, [])
}

/** `clipNodes` into a caller-owned buffer, so a frame can reuse one array. */
function clipInto(nodes: readonly GraphNode[], vp: Viewport, out: GraphNode[]): GraphNode[] {
  out.length = 0
  for (const n of nodes) if (isNodeVisible(n, vp)) out.push(n)
  return out
}

// The canvas repaints the same graph frame after frame; reusing one scratch
// array keeps the clip pass allocation-free (paint is not reentrant).
const visibleBuffer: GraphNode[] = []

// ---------------------------------------------------------------------------
// Level of detail (docs/33 §4.3): at low zoom a frame must cost a dot, not a
// pill plus a measured label.
// ---------------------------------------------------------------------------

/** Below this zoom the force map drops pills and curves for dots and lines. */
export const LOD_SHAPES_K = 0.22
/** Above this zoom fact names are worth their measured box. */
export const LOD_FACT_LABEL_K = 1.25
/** Force-map step titles: same threshold the painter always used. */
export const LOD_STEP_LABEL_K = 1.05

export interface Lod {
  /** Draw full pills/paths; false = dots and short straight lines only. */
  shapes: boolean
  /** Draw unselected fact labels at this zoom. */
  factLabels: boolean
  /** Draw background step labels at this zoom (showLabels forces them on). */
  stepLabels: boolean
  /** Background floating-label budget for one frame (sel/hover/hits exempt). */
  maxLabels: number
}

export function lod(camK: number, flags: { layered: boolean; showLabels: boolean }): Lod {
  const k = Number.isFinite(camK) && camK > 0 ? camK : 1
  // The chain tree is read through its pill text, so it keeps its shapes a
  // little longer than the paper-like force map when the operator zooms out.
  const shapesFrom = flags.layered ? LOD_SHAPES_K * 0.7 : LOD_SHAPES_K
  const shapes = k >= shapesFrom
  const factLabels = k >= LOD_FACT_LABEL_K
  const stepLabels = flags.showLabels || k >= LOD_STEP_LABEL_K
  const maxLabels = k >= 1.6 ? 160 : k >= 1.1 ? 90 : k >= 0.7 ? 48 : k >= 0.4 ? 24 : 12
  return { shapes, factLabels, stepLabels, maxLabels }
}

// ---------------------------------------------------------------------------
// Label occupancy grid (docs/33 §4.3). Boxes are inserted into 64 world-unit
// buckets; a query only walks the buckets its own box (plus the collision pad)
// touches. `overlaps` is equivalent to a linear scan by construction (widened
// boxes always share a bucket) and the test asserts that equivalence.
// ---------------------------------------------------------------------------

export interface Box {
  x1: number
  y1: number
  x2: number
  y2: number
}

export const BOX_CELL = 64
/** A box covering more than this many cells is kept on a short "wide" list and
 *  tested linearly: a label that spans half the map must not allocate thousands
 *  of bucket arrays. */
const MAX_BUCKET_CELLS = 256
/** A query covering more than this many cells scans every box instead. */
const MAX_QUERY_CELLS = 1024

export class BoxGrid {
  private readonly cell: number
  private readonly buckets = new Map<string, Box[]>()
  private readonly wide: Box[] = []
  private readonly all: Box[] = []

  constructor(cell = BOX_CELL) {
    this.cell = Number.isFinite(cell) && cell > 0 ? cell : BOX_CELL
  }

  /** Number of boxes inserted since the last clear. */
  get size(): number {
    return this.all.length
  }

  clear(): void {
    this.buckets.clear()
    this.wide.length = 0
    this.all.length = 0
  }

  insert(box: Box): void {
    this.all.push(box)
    const c = this.cell
    if (
      !Number.isFinite(box.x1) ||
      !Number.isFinite(box.y1) ||
      !Number.isFinite(box.x2) ||
      !Number.isFinite(box.y2)
    ) {
      this.wide.push(box)
      return
    }
    const cx0 = Math.floor(box.x1 / c)
    const cx1 = Math.floor(box.x2 / c)
    const cy0 = Math.floor(box.y1 / c)
    const cy1 = Math.floor(box.y2 / c)
    if ((cx1 - cx0 + 1) * (cy1 - cy0 + 1) > MAX_BUCKET_CELLS) {
      this.wide.push(box)
      return
    }
    for (let cx = cx0; cx <= cx1; cx++) {
      for (let cy = cy0; cy <= cy1; cy++) {
        const key = `${cx},${cy}`
        const list = this.buckets.get(key)
        if (list) list.push(box)
        else this.buckets.set(key, [box])
      }
    }
  }

  /** Would `box` overlap any inserted box, with `pad` world units of slack? */
  overlaps(box: Box, pad = 0): boolean {
    if (this.all.length === 0) return false
    const c = this.cell
    const qx0 = box.x1 - pad
    const qy0 = box.y1 - pad
    const qx1 = box.x2 + pad
    const qy1 = box.y2 + pad
    if (!Number.isFinite(qx0) || !Number.isFinite(qy0) || !Number.isFinite(qx1) || !Number.isFinite(qy1)) {
      return this.linear(box, pad)
    }
    const cx0 = Math.floor(qx0 / c)
    const cx1 = Math.floor(qx1 / c)
    const cy0 = Math.floor(qy0 / c)
    const cy1 = Math.floor(qy1 / c)
    if ((cx1 - cx0 + 1) * (cy1 - cy0 + 1) > MAX_QUERY_CELLS) return this.linear(box, pad)
    for (let cx = cx0; cx <= cx1; cx++) {
      for (let cy = cy0; cy <= cy1; cy++) {
        const list = this.buckets.get(`${cx},${cy}`)
        if (!list) continue
        for (const b of list) if (boxesOverlap(box, b, pad)) return true
      }
    }
    for (const b of this.wide) if (boxesOverlap(box, b, pad)) return true
    return false
  }

  private linear(box: Box, pad: number): boolean {
    for (const b of this.all) if (boxesOverlap(box, b, pad)) return true
    return false
  }
}

function boxesOverlap(a: Box, b: Box, pad: number): boolean {
  return a.x1 < b.x2 + pad && a.x2 > b.x1 - pad && a.y1 < b.y2 + pad && a.y2 > b.y1 - pad
}

// ---------------------------------------------------------------------------
// Edge geometry cache (docs/33 §4.3). A settled frame reuses last frame's
// curves by edge id; `settled === false` clears them because the nodes moved.
// The identity string folds the edge set in, so a poll that added/removed any
// edge cannot hand a stale curve to a new picture.
// ---------------------------------------------------------------------------

/** Generic identity-scoped map: the picture key decides whether entries live. */
export class GeometryCache<V> {
  private readonly values = new Map<string, V>()
  private identity = ''

  get size(): number {
    return this.values.size
  }

  /** The identity of the picture the current entries belong to. */
  get currentIdentity(): string {
    return this.identity
  }

  has(key: string): boolean {
    return this.values.has(key)
  }

  get(key: string): V | undefined {
    return this.values.get(key)
  }

  set(key: string, value: V): void {
    this.values.set(key, value)
  }

  clear(): void {
    this.values.clear()
  }

  /** Start a picture. Returns true (and drops every entry) when the identity
   *  changed, i.e. the operator is looking at a different edge set now. */
  begin(identity: string): boolean {
    if (identity === this.identity) return false
    this.identity = identity
    this.values.clear()
    return true
  }
}

// Hashing the whole edge list every frame is avoidable: the simulation keeps the
// same edge array until it rebuilds the model, so the array reference is the key.
const identityMemo = new WeakMap<readonly GraphEdge[], string>()

function hashEdges(edges: readonly GraphEdge[]): string {
  let h = 2166136261
  for (const e of edges) {
    const id = e.id
    for (let i = 0; i < id.length; i++) {
      h ^= id.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
    // The id already carries source/target/kind/intent, but mixing the kind in
    // keeps an unchanged id with a changed meaning a distinct identity.
    const kind = e.kind
    for (let i = 0; i < kind.length; i++) {
      h ^= kind.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
  }
  return (h >>> 0).toString(36)
}

/** Identity of "the edge picture": edge count + id/kind hash + layout mode. */
export function edgeSetIdentity(edges: readonly GraphEdge[], layered: boolean): string {
  let base = identityMemo.get(edges)
  if (base === undefined) {
    base = `${edges.length}:${hashEdges(edges)}`
    identityMemo.set(edges, base)
  }
  return `${layered ? 'layered' : 'force'}:${base}`
}

/** The curve for one edge, from the cache when the frame may reuse geometry. */
export function cachedEdgeCurve(
  cache: GeometryCache<EdgeCurve>,
  edge: GraphEdge,
  source: GraphNode,
  target: GraphNode,
  layered: boolean,
  reuse: boolean,
): EdgeCurve {
  if (!reuse) return edgeCurve(source, target, layered, edge.kind)
  const hit = cache.get(edge.id)
  if (hit) return hit
  const curve = edgeCurve(source, target, layered, edge.kind)
  cache.set(edge.id, curve)
  return curve
}

const curveCache = new GeometryCache<EdgeCurve>()

// ---------------------------------------------------------------------------
// Background: one 26×26 offscreen tile + createPattern instead of a double
// fillRect loop per frame. Built lazily per context: the module top level must
// not touch `document`, because the node tests import this file.
// ---------------------------------------------------------------------------

const BACKGROUND_GAP = 26
const patternCache = new WeakMap<CanvasRenderingContext2D, CanvasPattern | null>()

function dotPattern(ctx: CanvasRenderingContext2D): CanvasPattern | null {
  if (patternCache.has(ctx)) return patternCache.get(ctx) ?? null
  let pattern: CanvasPattern | null = null
  try {
    const doc = typeof document !== 'undefined' ? document : null
    if (doc) {
      const tile = doc.createElement('canvas')
      tile.width = BACKGROUND_GAP
      tile.height = BACKGROUND_GAP
      const tileCtx = tile.getContext('2d')
      if (tileCtx) {
        tileCtx.fillStyle = 'rgba(169,178,193,0.10)'
        tileCtx.fillRect(0, 0, 1, 1)
        pattern = ctx.createPattern(tile, 'repeat')
      }
    }
  } catch {
    pattern = null
  }
  patternCache.set(ctx, pattern)
  return pattern
}

function paintBackground(
  ctx: CanvasRenderingContext2D,
  width: number,
  height: number,
  cam: Camera,
  _motion: boolean,
): void {
  // 背景回到星云之前的样子：底色 + 点阵。星图观感只留在力导向的节点、光晕与航道里。
  const ox = cam.x % BACKGROUND_GAP
  const oy = cam.y % BACKGROUND_GAP
  const pattern = dotPattern(ctx)
  if (pattern) {
    ctx.save()
    ctx.fillStyle = pattern
    // The tile's dot sits at its origin; scrolling the pattern origin with the
    // camera keeps the paper still under the graph.
    ctx.translate(ox, oy)
    ctx.fillRect(-BACKGROUND_GAP, -BACKGROUND_GAP, width + BACKGROUND_GAP * 2, height + BACKGROUND_GAP * 2)
    ctx.restore()
    return
  }
  // No DOM (node tests never paint; this is for exotic 2d implementations).
  ctx.fillStyle = 'rgba(169,178,193,0.10)'
  for (let x = ox; x < width; x += BACKGROUND_GAP) {
    for (let y = oy; y < height; y += BACKGROUND_GAP) {
      ctx.fillRect(x, y, 1, 1)
    }
  }
}

function hashString(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0) / 4294967295
}

const TAU = Math.PI * 2
/** A screen-space-ish pad in world units: rings stay visible when zoomed out. */
function screenPad(camK: number, pixels: number): number {
  return pixels / Math.max(camK, 0.35)
}

/** A rejected/withdrawn fact keeps its kind colour but gets a true × cut-out.
 *  It must be both diagonals: the older single slash read as a rendering
 *  artifact and did not match the legend's ×. */
function drawStatusCross(
  ctx: CanvasRenderingContext2D,
  n: GraphNode,
  radius: number,
  camK: number,
): void {
  const d = radius * 0.6
  ctx.beginPath()
  ctx.moveTo(n.x - d, n.y - d)
  ctx.lineTo(n.x + d, n.y + d)
  ctx.moveTo(n.x + d, n.y - d)
  ctx.lineTo(n.x - d, n.y + d)
  ctx.strokeStyle = CANVAS.background
  ctx.lineWidth = 1.6 / Math.max(camK, 0.5)
  ctx.stroke()
}

/** A ring around a node's drawn shape, following the pill for boxed nodes. */
function nodeRing(ctx: CanvasRenderingContext2D, n: GraphNode, layered: boolean, pad: number): void {
  ctx.beginPath()
  if (n.type === 'fact' || n.type === 'ghost') {
    ctx.arc(n.x, n.y, radiusOf(n) + pad, 0, TAU)
    return
  }
  if (n.type === 'step' && !layered) {
    ctx.arc(n.x, n.y, Math.max(radiusOf(n), Math.hypot(n.w, n.h)) + pad, 0, TAU)
    return
  }
  roundRect(ctx, n.x - n.w - pad, n.y - n.h - pad, 2 * n.w + 2 * pad, 2 * n.h + 2 * pad, 9)
}

/** Trace one curve into the current path; the caller strokes it. */
function traceCurve(ctx: CanvasRenderingContext2D, c: EdgeCurve, dx = 0, dy = 0): void {
  ctx.beginPath()
  ctx.moveTo(c.sx + dx, c.sy + dy)
  ctx.bezierCurveTo(c.c1x + dx, c.c1y + dy, c.c2x + dx, c.c2y + dy, c.tx + dx, c.ty + dy)
}

// ---------------------------------------------------------------------------
// Text fitting cache. Measuring a long title character by character every frame
// showed up on large maps; the result is a pure function of (font, text, maxW),
// so it can be reused until the bounded cache is dropped.
// ---------------------------------------------------------------------------

const FIT_CACHE_LIMIT = 2048
const fitCache = new Map<string, string>()

/** Shorten to what fits the pill, measured rather than guessed. */
export function fitText(ctx: CanvasRenderingContext2D, text: string, maxW: number): string {
  const key = `${ctx.font}\u0000${maxW}\u0000${text}`
  const cached = fitCache.get(key)
  if (cached !== undefined) return cached
  let out = text
  if (ctx.measureText(text).width > maxW) {
    let s = text
    while (s.length > 1 && ctx.measureText(`${s}…`).width > maxW) s = s.slice(0, -1)
    out = `${s}…`
  }
  // Bounded: a cache miss is always cheaper than unbounded growth on a long run.
  if (fitCache.size >= FIT_CACHE_LIMIT) fitCache.clear()
  fitCache.set(key, out)
  return out
}

// ---------------------------------------------------------------------------
// Multi-line pill labels. A pill is read, not skimmed: a 300px single line of
// CJK forces the reader to pan, and it pushes the chain-tree columns apart. Two
// to three wrapped lines keep the same text in a quarter of the horizontal
// footprint. The layout already derives row heights from node h and column
// widths from node w (sim.placeLayered), so the wrapped box feeds straight back
// into placement without a second layout pass.
// ---------------------------------------------------------------------------

/** Text width a pill may use; the box adds the horizontal padding on top. */
export const PILL_TEXT_W = 232
/** A box never shows more than this many title lines (then it ellipsises). */
export const PILL_MAX_LINES = 3

const WRAP_BREAK_BEFORE = /[，。；：、！？）】」』》]/u

/**
 * Wrap `text` to `maxW`, breaking Latin words at spaces and CJK at any
 * character. Returns at most `maxLines`; the last line is ellipsised with
 * {@link fitText} so no text is silently dropped.
 */
export function wrapText(
  ctx: CanvasRenderingContext2D,
  text: string,
  maxW: number,
  maxLines: number,
): string[] {
  const trimmed = text.trim()
  if (!trimmed) return ['']
  if (ctx.measureText(trimmed).width <= maxW) return [trimmed]

  // Tokens: keep Latin words together, CJK characters on their own, and
  // punctuation attached to the preceding character so a line never starts
  // with a comma or a closing bracket.
  const tokens = trimmed.match(/[\u3000-\u303f\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]|[^\s\u3000-\u303f\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]+|\s+/gu) ?? [trimmed]
  const lines: string[] = []
  let line = ''
  const flush = () => {
    if (line.trim()) lines.push(line.trim())
    line = ''
  }
  const pushChars = (token: string) => {
    // A single token wider than the line (a long URL, a run id): break it by
    // character instead of overflowing the pill.
    for (const ch of token) {
      if (line && ctx.measureText(line + ch).width > maxW) flush()
      line += ch
    }
  }
  for (const token of tokens) {
    if (/^\s+$/u.test(token)) {
      if (line) line += ' '
      continue
    }
    if (ctx.measureText(line + token).width <= maxW) {
      line += token
      continue
    }
    if (ctx.measureText(token).width > maxW) {
      if (line) flush()
      pushChars(token)
      continue
    }
    if (WRAP_BREAK_BEFORE.test(token)) {
      // Hang closing punctuation off the previous line when it still fits.
      if (line && ctx.measureText(line + token).width <= maxW) {
        line += token
        continue
      }
    }
    flush()
    line = token
  }
  flush()

  if (lines.length <= maxLines) return lines
  const kept = lines.slice(0, maxLines)
  kept[maxLines - 1] = fitText(ctx, lines.slice(maxLines - 1).join(''), maxW)
  return kept
}

/**
 * Context lines are painted before the chain: a faded blue premise under a
 * bright intent edge reads as scaffolding, while the same stroke painted last
 * covers the chain it explains. Unknown kinds keep the middle position.
 * `rankFor` is a pure lookup; the sort is cached against the edges array
 * identity, so a settled frame reuses the order (no per-frame allocation).
 */
const EDGE_DRAW_RANK: Record<string, number> = {
  premise: 0,
  premiseBlocked: 0,
  reference: 0,
  supports: 0,
  origin: 0,
  host: 0,
  goal: 0,
  brief: 0,
}
let orderedSrc: readonly GraphEdge[] | null = null
let orderedEdges: GraphEdge[] = []
function edgesInDrawOrder(edges: readonly GraphEdge[]): GraphEdge[] {
  if (orderedSrc === edges) return orderedEdges
  orderedSrc = edges
  orderedEdges = [...edges].sort(
    (a, b) => (EDGE_DRAW_RANK[a.kind] ?? 1) - (EDGE_DRAW_RANK[b.kind] ?? 1),
  )
  return orderedEdges
}

export function paint(o: PaintOptions) {
  const { ctx, width, height, dpr, cam, nodes, edges, byId } = o
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, height)
  paintBackground(ctx, width, height, cam, o.motion)

  const vp = viewportOf(cam, width, height)
  const level = lod(cam.k, { layered: o.layered, showLabels: o.showLabels })

  ctx.save()
  ctx.translate(cam.x, cam.y)
  ctx.scale(cam.k, cam.k)

  // The selection transition eases the cold alpha instead of snapping it: the
  // information (who is in the neighbourhood) is unchanged, only how fast the
  // rest of the map recedes. The cold dim itself is a switch (default off).
  const coldDimOn = o.coldDim === true
  const dim = (id: string): number =>
    chainDimAlpha(id, {
      focus: o.focus,
      near: o.near,
      matches: o.matches,
      focusMix: o.focusMix,
      coldDim: coldDimOn,
    })
  const fadeIn = (id: string) => {
    if (!o.motion) return 1
    const t = o.appear.get(id)
    if (t === undefined) return 1
    return Math.min(1, Math.max(0, (o.time - t) / 380))
  }

  // Clipping is a draw filter only: the model, search and focus keep everything.
  const visible = clipInto(nodes, vp, visibleBuffer)
  // The time axis is a second draw filter, applied in place on the same buffer
  // so the frame never allocates. Undated nodes (the brief, boundary leaves) are
  // not in the rank map and stay visible: the axis may only hide what it can
  // actually order.
  if (o.timeGate) {
    const gate = o.timeGate
    let kept = 0
    for (const n of visible) if (revealedIn(gate.rank, gate.cursor, n.id)) visible[kept++] = n
    visible.length = kept
  }

  // ---- depth bands (layered mode only) ----
  if (o.layered && o.bands.length > 0) {
    // Bands span the visible world (not just the nodes) so the columns read as
    // lanes. They tile the whole width — a column owns the space up to the
    // midpoint of the gap to the next column — so the alternating stripes frame
    // the hops instead of floating around each node cluster.
    const top = -cam.y / Math.max(cam.k, 0.001) - 40
    const bottom = (height - cam.y) / Math.max(cam.k, 0.001) + 40
    const lanes = o.bands.map((band, i) => {
      const prev = o.bands[i - 1]
      const next = o.bands[i + 1]
      const left = prev ? (prev.x2 + band.x1) / 2 : band.x1 - 60
      const right = next ? (band.x2 + next.x1) / 2 : band.x2 + 60
      return { band, left, right }
    })
    for (const [i, lane] of lanes.entries()) {
      if (lane.right < vp.x0 || lane.left > vp.x1) continue
      ctx.globalAlpha = i % 2 === 0 ? 0.5 : 0.22
      ctx.fillStyle = CANVAS.surface
      ctx.fillRect(lane.left, top, lane.right - lane.left, bottom - top)
      if (i < lanes.length - 1) {
        ctx.globalAlpha = 0.5
        ctx.strokeStyle = 'rgba(120,130,148,0.4)'
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.setLineDash([2 / cam.k, 8 / cam.k])
        ctx.beginPath()
        ctx.moveTo(lane.right, top)
        ctx.lineTo(lane.right, bottom)
        ctx.stroke()
        ctx.setLineDash([])
      }
    }
  }

  // Screen-space occupancy, in world units (the camera scales uniformly): a
  // label may only be drawn where it does not sit on a node or another label.
  // Without this, nine intents pile their titles on top of each other at fit
  // zoom and the picture is unreadable.
  const boxes = new BoxGrid()
  for (const n of visible) {
    if (n.type === 'project' || n.type === 'goal' || (o.layered && n.type === 'step')) {
      boxes.insert({ x1: n.x - n.w - 6, y1: n.y - n.h - 6, x2: n.x + n.w + 6, y2: n.y + n.h + 6 })
    } else if (n.type === 'fact') {
      const r = radiusOf(n)
      boxes.insert({ x1: n.x - r, y1: n.y - r, x2: n.x + r, y2: n.y + r })
    } else if (n.type === 'step') {
      boxes.insert({ x1: n.x - 10, y1: n.y - 10, x2: n.x + 10, y2: n.y + 10 })
    } else {
      boxes.insert({ x1: n.x - 9, y1: n.y - 9, x2: n.x + 9, y2: n.y + 9 })
    }
  }

  // A label budget for the whole frame: unselected background labels stop at
  // lod().maxLabels; sel/hover/hit labels are exempt and always draw.
  const budget = { left: level.maxLabels }
  const takeLabel = (exempt: boolean): boolean => {
    if (exempt) return true
    if (budget.left <= 0) return false
    budget.left--
    return true
  }
  /**
   * A fact label has three tiers:
   *  - `always`: the pointer/search/selection is on it — full title, no budget;
   *  - `focus`:  a fact on the selected node's chain — short, no budget, but it
   *              still refuses to sit on top of another label;
   *  - `budget`: a background label at a zoomed-in view — short and budgeted.
   */
  const drawFactLabel = (n: GraphNode, alpha: number, tier: 'always' | 'focus' | 'budget'): void => {
    const r = radiusOf(n)
    ctx.font = `${11}px "JetBrains Mono","Cascadia Code",monospace`
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    // The pointer asks for the name: give hover/selection more of the title
    // before it is ellipsised. Background labels stay short so they do not
    // crowd the map; a chain fact gets just enough to tell two facts apart.
    const text =
      tier === 'always' ? clip(n.label, 60) : tier === 'focus' ? focusFactLabel(n.label) : clip(n.label, 26)
    const w = ctx.measureText(text).width
    // 两侧都有浮层时以可用带为准：会伸到左侧筛选卡或右侧面板底下的标签改画到
    // 节点另一边，1280 实测右侧链上事实原来只能露出「示例：」三个字。
    const flip = factLabelFlip(n.x * cam.k + cam.x, w, r, width, o.rightInset ?? 0, o.leftInset ?? 0)
    const box = flip
      ? { x1: n.x - r - 2 - w - 8, y1: n.y - 10, x2: n.x - r - 2, y2: n.y + 10 }
      : { x1: n.x + r + 2, y1: n.y - 10, x2: n.x + r + 2 + w + 8, y2: n.y + 10 }
    if (tier !== 'always' && boxes.overlaps(box, 2)) return
    if (tier === 'budget' && !takeLabel(false)) return
    boxes.insert(box)
    ctx.globalAlpha = alpha * 0.92
    ctx.fillStyle = 'rgba(10,12,16,0.75)'
    if (flip) {
      ctx.fillRect(n.x - r - 4 - w - 6, n.y - 8, w + 6, 16)
      ctx.fillStyle = 'rgba(231,234,240,0.92)'
      ctx.textAlign = 'right'
      ctx.fillText(text, n.x - r - 7, n.y + 0.5)
    } else {
      ctx.fillRect(n.x + r + 4, n.y - 8, w + 6, 16)
      ctx.fillStyle = 'rgba(231,234,240,0.92)'
      ctx.fillText(text, n.x + r + 7, n.y + 0.5)
    }
  }

  // ---- edges ----
  const reuse = o.settled
  curveCache.begin(edgeSetIdentity(edges, o.layered))
  if (!reuse) curveCache.clear()

  for (const e of edgesInDrawOrder(edges)) {
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    // Both ends must have been revealed: an edge to a row the axis has not
    // reached yet would draw a line into nothing.
    if (o.timeGate) {
      const gate = o.timeGate
      if (!revealedIn(gate.rank, gate.cursor, s.id) || !revealedIn(gate.rank, gate.cursor, t.id)) continue
    }
    // Cheap reject before any geometry: both ends on the same outside side.
    if (
      (s.x < vp.x0 && t.x < vp.x0) ||
      (s.x > vp.x1 && t.x > vp.x1) ||
      (s.y < vp.y0 && t.y < vp.y0) ||
      (s.y > vp.y1 && t.y > vp.y1)
    ) {
      continue
    }
    const kind = e.kind
    const isSel = e.id === o.selected
    const isHover = e.id === o.hovered
    const curve = cachedEdgeCurve(curveCache, e, s, t, o.layered, reuse)
    if (!curveInViewport(curve, vp)) continue

    const alpha = Math.min(dim(s.id), dim(t.id)) * Math.min(fadeIn(s.id), fadeIn(t.id))
    const intentEdge = kind === 'intent'
    // A step→fact edge is one leaf of a fan: dozens of them must not out-shout
    // the chain. They are thinner and quieter until the pointer or a selection
    // makes that direction the subject.
    const fan = intentEdge && t.type === 'fact'
    // 主干上的边单独提一档：整条链路按 1、2、3 的顺序读下来，跨节点的线必须是
    // 最显眼的那几根，否则「高亮」只剩节点，读不出先后。
    const chainEdge = o.chainEdges?.has(e.id) ?? false
    const emphasized =
      isSel ||
      isHover ||
      chainEdge ||
      Boolean(o.focus && (o.focus.has(s.id) || o.focus.has(t.id)))

    if (!level.shapes) {
      // Dot-level zoom: one straight line, no curve, dashes, arrows or comets.
      ctx.globalAlpha = alpha
      ctx.strokeStyle = edgeStyle(e).color
      ctx.lineWidth = (isSel || isHover ? 2.4 : 1.3) / Math.max(cam.k, 0.5)
      ctx.setLineDash([])
      ctx.lineDashOffset = 0
      ctx.beginPath()
      ctx.moveTo(s.x, s.y)
      ctx.lineTo(t.x, t.y)
      ctx.stroke()
      continue
    }

    const style = edgeStyle(e)
    // 航道的光：只在被聚焦的那几条线上加一层宽而淡的描边，读起来像亮着的航道，
    // 而不是给 798 条边各画一遍光（那会直接吃掉每帧预算）。
    if (emphasized) {
      ctx.save()
      ctx.globalAlpha = alpha * 0.13
      ctx.strokeStyle = style.color
      ctx.lineWidth = (isSel || isHover ? 9 : 5.5) / Math.max(cam.k, 0.4)
      ctx.setLineDash([])
      traceCurve(ctx, curve)
      ctx.stroke()
      ctx.restore()
    }
    ctx.globalAlpha = alpha * (fan && !emphasized ? 0.85 : 1)
    ctx.strokeStyle = style.color
    ctx.lineWidth =
      (intentEdge
        ? (isSel ? 3.4 : fan ? 1.6 : 2.2)
        : kind === 'premise'
          ? (isSel ? 3 : 1.55)
          : kind === 'brief'
                ? 1.5
                : kind === 'reference'
                  ? (isSel ? 2.5 : 1.65)
                  : kind === 'pin'
                    ? (isSel ? 2.7 : 1.7)
                    : kind === 'supports'
                      ? (isSel ? 2.3 : 1.4)
                      : kind === 'goal'
                        ? (isSel ? 2.3 : 1.4)
                        : 1.2) / Math.max(cam.k, 0.5)
    if (chainEdge) {
      // 主干边：比同层最粗的一档再粗一点，但保持整条链一样粗，读的是顺序不是
      // 粗细变化。叠在主干的暗色分离线上，交叉处也分得开。
      ctx.lineWidth = Math.max(ctx.lineWidth, 3.2 / Math.max(cam.k, 0.5))
      ctx.globalAlpha = Math.max(ctx.globalAlpha, alpha * 0.95)
    }
    // Chain edges get a dark separation stroke first: where a dozen faint
    // relations cross, the halo clears a little gutter underneath the structural
    // line so the chain stays readable without making every relation loud.
    if (intentEdge || kind === 'brief') {
      ctx.save()
      ctx.globalAlpha = alpha * 0.85
      ctx.strokeStyle = 'rgba(5,7,10,0.85)'
      ctx.lineWidth = ctx.lineWidth + 2.6 / Math.max(cam.k, 0.5)
      ctx.setLineDash([])
      traceCurve(ctx, curve)
      ctx.stroke()
      ctx.restore()
    }
    const running = intentEdge && e.status === 'running'
    // A reviewing direction is being checked, not executed: its dashes drift
    // slowly rather than marching, and no comet rides the edge.
    const reviewingEdge = intentEdge && e.status === 'reviewing' && o.motion
    const dashed = style.dashed
    ctx.setLineDash(
      kind === 'reference'
          ? [8 / cam.k, 4 / cam.k]
          : kind === 'supports'
            ? [3 / cam.k, 4 / cam.k]
            // A blocked gate is dotted so it never reads as the long marching
            // dashes of a live running edge (both are red by design).
            : kind === 'premise' && e.blocked
              ? [2 / cam.k, 3 / cam.k]
              : dashed
                ? [8 / cam.k, 4 / cam.k]
                : running
                  ? [10 / cam.k, 5 / cam.k]
                  : kind === 'host'
                    ? [2 / cam.k, 3 / cam.k]
                    : [],
    )
    ctx.lineDashOffset = running
      ? -((o.time / 45) % 12) / Math.max(cam.k, 0.5)
      : reviewingEdge
        ? -((o.time / 260) % 18) / Math.max(cam.k, 0.5)
        : 0
    traceCurve(ctx, curve)
    ctx.stroke()

    // A pin is a solid *double* teal line: the second parallel stroke is what
    // keeps it apart from the dashed reference at a glance.
    if (kind === 'pin') {
      const dx = t.x - s.x
      const dy = t.y - s.y
      const len = Math.max(Math.hypot(dx, dy), 0.001)
      const off = 1.5 / Math.max(cam.k, 0.5)
      const ox = (-dy / len) * off
      const oy = (dx / len) * off
      traceCurve(ctx, curve, ox, oy)
      ctx.stroke()
      traceCurve(ctx, curve, -ox, -oy)
      ctx.stroke()
    }

    // Arrowheads point into the node the hop produced. A leaf fan is only
    // arrowed when it is the subject; otherwise 30 triangles are noise. Relation
    // edges (premise/reference/pin/supports/goal) always point at what they feed.
    if (
      (intentEdge && (!fan || emphasized)) ||
      kind === 'brief' ||
      kind === 'premise' ||
      kind === 'reference' ||
      kind === 'pin' ||
      kind === 'supports' ||
      kind === 'goal'
    ) {
      const ang = Math.atan2(curve.ty - curve.c2y, curve.tx - curve.c2x)
      const size = 6 / Math.max(cam.k, 0.5)
      ctx.beginPath()
      ctx.moveTo(curve.tx, curve.ty)
      ctx.lineTo(curve.tx - Math.cos(ang - 0.4) * size, curve.ty - Math.sin(ang - 0.4) * size)
      ctx.lineTo(curve.tx - Math.cos(ang + 0.4) * size, curve.ty - Math.sin(ang + 0.4) * size)
      ctx.closePath()
      ctx.fillStyle = style.color
      ctx.fill()
    }

    // Travelling comet: the live work visibly moves from context to output.
    if (running && o.motion) {
      const phase = (o.time / 1100 + hashString(e.id) * 0.7) % 1
      drawComet(ctx, o, curve, phase, style.color)
    }
  }
  ctx.setLineDash([])
  ctx.lineDashOffset = 0

  // ---- floating labels (intent steps + emphasised names) ----
  type Floating = {
    lx: number
    ly: number
    text: string
    width: number
    alpha: number
    priority: number
    color: string
    /** Selected/hovered/searched labels never count against the budget. */
    exempt: boolean
  }
  const floaters: Floating[] = []
  ctx.font = `600 11px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
  // Hover/selection names the relation it is on: a reference/pin must read as
  // "引用"/"固定" and a hard prerequisite as "必要前提", because the same line
  // used to mean both. Labels stay off the resting picture; the pointer asks.
  for (const e of edges) {
    const kind = e.kind
    if (
      kind !== 'premise' &&
      kind !== 'reference' &&
      kind !== 'supports' &&
      kind !== 'goal' &&
      kind !== 'pin'
    )
      continue
    const isSel = e.id === o.selected
    const isHover = e.id === o.hovered
    if (!isSel && !isHover) continue
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const curve = cachedEdgeCurve(curveCache, e, s, t, o.layered, reuse)
    if (!curveInViewport(curve, vp)) continue
    const mid = pointOnCurve(curve, 0.5)
    if (!pointInViewport(mid.x, mid.y, vp)) continue
    const text = edgeKindLabel(kind)
    floaters.push({
      lx: mid.x,
      ly: mid.y,
      text,
      width: ctx.measureText(text).width + 10,
      alpha: Math.min(dim(s.id), dim(t.id)),
      priority: isSel ? 0 : 1,
      color: edgeStyle(e).color,
      exempt: true,
    })
  }
  for (const n of visible) {
    if (n.type !== 'step') continue
    // In the chain-tree view the step is a pill: its title is inside the node,
    // so there is no floating label to collide with the fan of produced facts.
    if (o.layered) continue
    const isSel = n.id === o.selected
    const isHover = n.id === o.hovered
    const isMatch = o.matches?.has(n.id) === true
    const running = n.status === 'running'
    if (!isSel && !isHover && !isMatch && !running && !level.stepLabels) continue
    // Hover/selection is the operator asking to read the direction: use the
    // wider label budget before ellipsising.
    const text = fitText(ctx, n.label, isSel || isHover ? 260 : 190)
    floaters.push({
      lx: n.x,
      ly: n.y + 20,
      text,
      width: ctx.measureText(text).width + 10,
      alpha: dim(n.id),
      priority: isSel ? 0 : isHover ? 1 : running ? 2 : isMatch ? 2 : 3,
      color: nodeColor(n),
      exempt: isSel || isHover || isMatch,
    })
  }
  // Dot-level zoom keeps the names of the nodes the operator is acting on: the
  // glyph is a dot, so the label is the only identity left on screen.
  if (!level.shapes) {
    for (const n of visible) {
      if (n.type === 'fact') continue
      const isSel = n.id === o.selected
      const isHover = n.id === o.hovered
      const isMatch = o.matches?.has(n.id) === true
      if (!isSel && !isHover && !isMatch && n.id !== o.keyboardFocus) continue
      const text = fitText(ctx, n.label, isSel || isHover ? 260 : 190)
      floaters.push({
        lx: n.x,
        ly: n.y + Math.max(n.h, radiusOf(n)) + 14,
        text,
        width: ctx.measureText(text).width + 10,
        alpha: dim(n.id),
        priority: isSel ? 0 : isHover ? 1 : 2,
        color: nodeColor(n),
        exempt: true,
      })
    }
  }
  floaters.sort((a, b) => a.priority - b.priority)
  for (const label of floaters) {
    if (!pointInViewport(label.lx, label.ly, vp)) continue
    const { lx, width } = label
    let ly = label.ly
    let box = { x1: lx - width / 2, y1: ly - 9, x2: lx + width / 2, y2: ly + 9 }
    if (label.priority > 2) {
      // A background label may step down/up a little to get out of the way of
      // its own node or a neighbouring one; skipping it entirely used to hide
      // every non-running step title behind its own glyph box.
      let placed = false
      for (const dy of [0, 20, -20, 40, -40]) {
        const candidate = { x1: box.x1, y1: box.y1 + dy, x2: box.x2, y2: box.y2 + dy }
        if (!boxes.overlaps(candidate, 3)) {
          ly += dy
          box = candidate
          placed = true
          break
        }
      }
      if (!placed) continue
    }
    if (!takeLabel(label.exempt)) continue
    boxes.insert(box)
    ctx.globalAlpha = label.alpha
    ctx.fillStyle = 'rgba(17,21,28,0.92)'
    ctx.fillRect(lx - width / 2, ly - 9, width, 18)
    ctx.strokeStyle = label.color
    ctx.lineWidth = 0.8 / Math.max(cam.k, 0.5)
    ctx.strokeRect(lx - width / 2, ly - 9, width, 18)
    ctx.fillStyle = label.color
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(label.text, lx, ly + 0.5)
  }

  // ---- nodes ----
  // 不发光：星点就是纯色点。状态（选中/悬停/命中/键盘焦点）只用环来表示，
  // 不再靠亮度或光晕——亮度既看不清层级，也会在 563 个点上互相淹没。
  for (const n of visible) {
    const color = nodeColor(n)
    const alpha = dim(n.id)
    const isSel = n.id === o.selected
    const isHover = n.id === o.hovered
    const isMatch = o.matches?.has(n.id) === true
    const isActiveMatch = o.activeMatch === n.id
    const keyboard = n.id === o.keyboardFocus
    const born = o.appear.get(n.id)
    const age = o.motion && born !== undefined ? o.time - born : 9999
    const entry = o.motion && age < 360 ? 0.55 + 0.45 * (age / 360) : 1

    if (!level.shapes) {
      // Dot-level zoom: one dot per node, no pill, no per-node text. The
      // emphasised node still gets its ring and (below) its label.
      ctx.globalAlpha = alpha * fadeIn(n.id)
      const r = dotRadius(n, cam.k)
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      // A sealed goal keeps its shape channel even as a dot, so a zoomed-out map
      // still shows where the engagement ended.
      if (n.type === 'goal' && n.status === 'met') {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + screenPad(cam.k, 3), 0, TAU)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * 0.55
        ctx.lineWidth = 1.6 / Math.max(cam.k, 0.5)
        ctx.stroke()
        ctx.globalAlpha = alpha * fadeIn(n.id)
      }
      if (isSel || isHover || isMatch || keyboard) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + screenPad(cam.k, 3), 0, TAU)
        ctx.strokeStyle = keyboard
          ? KEYBOARD_FOCUS_RING
          : isSel
            ? 'rgba(231,234,240,0.95)'
            : isActiveMatch
              ? ACTIVE_MATCH_RING
              : isMatch
                ? MATCH_RING
                : 'rgba(231,234,240,0.7)'
        ctx.lineWidth = (isSel ? 1.8 : isActiveMatch ? 2.2 : 1.4) / Math.max(cam.k, 0.5)
        ctx.stroke()
        if (isActiveMatch && !isSel) {
          // A soft outer ring makes the active hit read as one subject rather
          // than a louder version of the other matches.
          ctx.beginPath()
          ctx.arc(n.x, n.y, r + screenPad(cam.k, 7), 0, TAU)
          ctx.globalAlpha = alpha * 0.35
          ctx.lineWidth = 5 / Math.max(cam.k, 0.5)
          ctx.stroke()
          ctx.globalAlpha = alpha * fadeIn(n.id)
        }
      }
      if (n.type === 'fact' && (isSel || isHover || isMatch)) drawFactLabel(n, alpha, 'always')
      // Status stays legible even at dot zoom: a rejected/withdrawn fact keeps
      // its fact colour but is crossed out.
      if (n.type === 'fact' && (n.status === 'rejected' || n.status === 'withdrawn')) {
        drawStatusCross(ctx, n, r, cam.k)
      }
      ctx.globalAlpha = alpha
      continue
    }

    ctx.globalAlpha = alpha * fadeIn(n.id)


    // New node: expanding ripple, drawn under the glyph.
    if (o.motion && age < 1100) {
      const k = age / 1100
      const r = Math.max(radiusOf(n), 9) + k * 26
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.strokeStyle = color
      ctx.lineWidth = 1.4 / cam.k
      ctx.globalAlpha = alpha * (1 - k) * 0.55
      ctx.stroke()
      ctx.globalAlpha = alpha * fadeIn(n.id)
    }

    // A newly recorded fact gets one extra beat on top of the generic ripple: a
    // soft halo behind the dot, so "evidence just landed" reads as a fact event
    // rather than another node appearing. One-shot; it never loops.
    if (o.motion && n.type === 'fact' && age >= 0 && age < 900) {
      const t = age / 900
      ctx.beginPath()
      ctx.arc(n.x, n.y, Math.max(radiusOf(n), 7) + 16 * (1 - t), 0, TAU)
      ctx.fillStyle = color
      ctx.globalAlpha = alpha * 0.2 * (1 - t)
      ctx.fill()
      ctx.globalAlpha = alpha * fadeIn(n.id)
    }

    if (n.type === 'intent' || n.type === 'goal' || n.type === 'project' || (o.layered && n.type === 'step')) {
      const w = n.w * 2
      const h = n.h * 2
      const x = n.x - n.w
      const y = n.y - n.h
      const r = 7
      // A goal only reaches the map once it is met, and it is the operator's
      // endpoint rather than another piece of work: give it a sealed look —
      // a steady halo plus one slow breath, so it cannot be read as a running
      // intent (whose pulse is 0.42s, five times faster). Reduced motion keeps
      // the halo and drops the breath.
      const sealedGoal = n.type === 'goal' && n.status === 'met'
      if (sealedGoal) {
        const phase = o.motion ? 0.5 + 0.5 * Math.sin(o.time / 1600) : 0.5
        ctx.beginPath()
        roundRect(ctx, x - 9, y - 9, w + 18, h + 18, r + 8)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * (0.07 + 0.06 * phase)
        ctx.lineWidth = 7 / cam.k
        ctx.stroke()
        ctx.beginPath()
        roundRect(ctx, x - 4, y - 4, w + 8, h + 8, r + 4)
        ctx.globalAlpha = alpha * (0.20 + 0.22 * phase)
        ctx.lineWidth = 1.6 / cam.k
        ctx.stroke()
        ctx.globalAlpha = alpha
      }
      ctx.beginPath()
      roundRect(ctx, x, y, w, h, r)
      ctx.fillStyle = sealedGoal ? 'rgba(18,34,27,0.94)' : 'rgba(17,21,28,0.92)'
      ctx.fill()
      ctx.lineWidth = (isSel ? 2.4 : 1.4) / cam.k
      ctx.strokeStyle = color
      ctx.stroke()

      if ((n.type === 'intent' || n.type === 'step') && n.status === 'running' && o.motion) {
        const pulse = 0.5 + 0.5 * Math.sin(o.time / 420)
        ctx.beginPath()
        roundRect(ctx, x - 3, y - 3, w + 6, h + 6, r + 3)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * 0.35 * pulse
        ctx.lineWidth = 2 / cam.k
        ctx.stroke()
        ctx.globalAlpha = alpha
      }

      if ((n.type === 'intent' || n.type === 'step') && n.status === 'reviewing') {
        // A calm, slow verification frame, not the fast execution pulse. The
        // dashed outer ring keeps reviewing recognisable by shape when the
        // operator has reduced motion on (no breathing, but still a frame).
        const phase = o.motion ? 0.5 + 0.5 * Math.sin(o.time / 950) : 0.5
        ctx.beginPath()
        roundRect(ctx, x - 3.5, y - 3.5, w + 7, h + 7, r + 3.5)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * (0.16 + 0.14 * phase)
        ctx.lineWidth = 2 / cam.k
        ctx.stroke()
        ctx.beginPath()
        roundRect(ctx, x - 7, y - 7, w + 14, h + 14, r + 7)
        ctx.globalAlpha = alpha * 0.5
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.setLineDash([4 / cam.k, 5 / cam.k])
        ctx.lineDashOffset = o.motion ? -((o.time / 260) % 9) / Math.max(cam.k, 0.5) : 0
        ctx.stroke()
        ctx.setLineDash([])
        ctx.lineDashOffset = 0
        ctx.globalAlpha = alpha
      }

      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      // The label is pre-wrapped by measureNodes; a node measured before the
      // wrap pass existed (tests, a stale model) still renders one ellipsised
      // line instead of nothing.
      const lines =
        n.labelLines && n.labelLines.length > 0
          ? n.labelLines
          : [fitText(ctx, n.label, w - 16)]
      const hasModel = n.type === 'step' && Boolean(n.model)
      const titleLineH = 16
      const modelLineH = 15
      const blockH = lines.length * titleLineH + (hasModel ? modelLineH : 0)
      let ty = n.y - blockH / 2 + titleLineH / 2
      ctx.font = `600 12px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
      ctx.fillStyle = color
      for (const line of lines) {
        ctx.fillText(line, n.x, ty)
        ty += titleLineH
      }
      if (hasModel) {
        ctx.font = `500 11px "JetBrains Mono","Cascadia Code",monospace`
        ctx.fillStyle = 'rgba(169,178,193,0.7)'
        ctx.fillText(fitText(ctx, n.model!, w - 16), n.x, ty)
      }
      // The seal badge sits on the top-right corner, outside the label box, so
      // a long goal statement never collides with it. A check is shape, not
      // colour: it survives greyscale and colour-blind palettes.
      if (sealedGoal) {
        const bx = x + w - 1
        const by = y + 1
        const br = 7
        ctx.save()
        ctx.beginPath()
        ctx.arc(bx, by, br, 0, TAU)
        ctx.fillStyle = color
        ctx.fill()
        ctx.strokeStyle = 'rgba(10,12,16,0.92)'
        ctx.lineWidth = 1.4 / cam.k
        ctx.stroke()
        ctx.beginPath()
        ctx.moveTo(bx - br * 0.46, by + 0.3)
        ctx.lineTo(bx - br * 0.1, by + br * 0.42)
        ctx.lineTo(bx + br * 0.5, by - br * 0.36)
        ctx.strokeStyle = '#0a0c10'
        ctx.lineWidth = 1.8 / cam.k
        ctx.lineCap = 'round'
        ctx.lineJoin = 'round'
        ctx.stroke()
        ctx.restore()
      }
    } else if (n.type === 'step') {
      const r = 7 * entry
      const running = n.status === 'running'
      ctx.save()
      ctx.translate(n.x, n.y)
      ctx.rotate(Math.PI / 4)
      ctx.beginPath()
      ctx.rect(-r * 0.72, -r * 0.72, r * 1.44, r * 1.44)
      ctx.fillStyle = 'rgba(17,21,28,0.95)'
      ctx.fill()
      ctx.strokeStyle = color
      ctx.lineWidth = (isSel ? 2.2 : 1.5) / cam.k
      ctx.stroke()
      ctx.restore()

      if (running && o.motion) {
        const pulse = 0.55 + 0.45 * Math.sin(o.time / 320)
        ctx.beginPath()
        ctx.arc(n.x, n.y, 12, 0, TAU)
        ctx.strokeStyle = color
        ctx.lineWidth = 1.6 / cam.k
        ctx.globalAlpha = alpha * 0.4 * pulse
        ctx.stroke()
        ctx.globalAlpha = alpha
      }

      if (n.status === 'reviewing') {
        // The force-map diamond gets the same calm review frame as the pill:
        // a breathing halo plus a slow dashed orbit.
        const phase = o.motion ? 0.5 + 0.5 * Math.sin(o.time / 950) : 0.5
        ctx.save()
        ctx.translate(n.x, n.y)
        ctx.rotate(Math.PI / 4)
        const inner = r * 0.72 + 4
        ctx.beginPath()
        roundRect(ctx, -inner, -inner, 2 * inner, 2 * inner, 3)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * (0.16 + 0.14 * phase)
        ctx.lineWidth = 2 / cam.k
        ctx.stroke()
        const outer = inner + 3
        ctx.beginPath()
        roundRect(ctx, -outer, -outer, 2 * outer, 2 * outer, 3)
        ctx.globalAlpha = alpha * 0.5
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.setLineDash([4 / cam.k, 5 / cam.k])
        ctx.lineDashOffset = o.motion ? -((o.time / 260) % 9) / Math.max(cam.k, 0.5) : 0
        ctx.stroke()
        ctx.restore()
        // restore() also put the dash pattern and alpha back.
        ctx.globalAlpha = alpha
      }
    } else if (n.type === 'ghost') {
      // The un-produced end of an intent step: a small pulsing ring, never a
      // pill. It disappears when the fact node replaces it.
      const reviewing = n.status === 'reviewing'
      const pulse = reviewing
        ? o.motion
          ? 0.6 + 0.4 * Math.sin(o.time / 950)
          : 0.7
        : n.status === 'running' && o.motion
          ? 0.55 + 0.45 * Math.sin(o.time / 320)
          : 0.7
      ctx.beginPath()
      ctx.arc(n.x, n.y, 7, 0, TAU)
      ctx.strokeStyle = color
      ctx.lineWidth = 1.6 / cam.k
      ctx.globalAlpha = alpha * pulse
      ctx.stroke()
      if (reviewing) {
        // A reviewing direction with nothing produced yet: the same slow dashed
        // frame as a step, so "waiting for the reviewer" is visible at a glance.
        ctx.beginPath()
        ctx.arc(n.x, n.y, 13, 0, TAU)
        ctx.globalAlpha = alpha * 0.5
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.setLineDash([4 / cam.k, 5 / cam.k])
        ctx.lineDashOffset = o.motion ? -((o.time / 260) % 9) / Math.max(cam.k, 0.5) : 0
        ctx.stroke()
        ctx.setLineDash([])
        ctx.lineDashOffset = 0
        ctx.globalAlpha = alpha
      }
      ctx.beginPath()
      ctx.arc(n.x, n.y, 2.4, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      ctx.globalAlpha = alpha
    } else {
      const r = radiusOf(n) * entry
      // Halo for nodes that carry evidence or are verified. The graph
      // projection clears evidence_ids from the fact body, so the count carried
      // by the model (fact_meta) is the only honest source; asking the body made
      // this branch unreachable on the page that actually draws it, while the
      // node panel showed "证据 N" for the same node.
      if (n.status === 'verified' || (n.evidenceCount ?? 0) > 0) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + 4, 0, TAU)
        ctx.fillStyle = color
        ctx.globalAlpha = alpha * 0.1
        ctx.fill()
        ctx.globalAlpha = alpha
      }
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      ctx.lineWidth = (isSel ? 2 : 1) / cam.k
      ctx.strokeStyle = isSel ? CANVAS.ink : 'rgba(10,12,16,0.6)'
      ctx.stroke()
      // needs_review: the claim asserted a version newer than the last review.
      // A dashed outer ring says "read this before trusting it" without touching
      // the verified halo above (which stays a soft fill).
      if (n.type === 'fact' && n.needsReview) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + 4.5, 0, TAU)
        ctx.strokeStyle = REVIEW_RING
        ctx.globalAlpha = alpha * 0.95
        ctx.lineWidth = 1.3 / Math.max(cam.k, 0.5)
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
        ctx.globalAlpha = alpha
      }
      if (n.status === 'rejected' || n.status === 'withdrawn') {
        drawStatusCross(ctx, n, r, cam.k)
      }
    }

    // Step decorations live outside the shape branch: the chain-tree view draws
    // a step as a pill, the force map as a diamond; both carry the same attempt
    // count and "continued from an earlier session" arc.
    if (n.type === 'step') {
      const hw = o.layered ? n.w : 8
      const hh = o.layered ? n.h : 8
      // Attempt badge: "×N" means the direction was retried, so the reader can
      // tell one clean attempt from a path that was walked again.
      if (n.attempts && n.attempts > 1) {
        const bx = n.x + hw - 1
        const by = n.y - hh - 1
        ctx.beginPath()
        ctx.arc(bx, by, 6, 0, TAU)
        ctx.fillStyle = 'rgba(108,182,255,0.92)'
        ctx.fill()
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.strokeStyle = 'rgba(10,12,16,0.9)'
        ctx.stroke()
        ctx.fillStyle = CANVAS.background
        ctx.font = `700 10px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
        ctx.textAlign = 'center'
        ctx.textBaseline = 'middle'
        ctx.fillText(`×${Math.min(n.attempts, 99)}`, bx, by + 0.5)
      }
      // Retry/resume arc: the step was continued from an earlier session.
      if (n.retried) {
        const top = n.y - hh - (o.layered ? 26 : 26)
        ctx.beginPath()
        ctx.moveTo(n.x - hw + 1, n.y - hh - 3)
        ctx.quadraticCurveTo(n.x, top, n.x + hw - 1, n.y - hh - 3)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * 0.7
        ctx.lineWidth = 1.2 / cam.k
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
        if (o.layered || cam.k > 1.05 || isSel || isHover) {
          ctx.fillStyle = color
          ctx.font = `600 10px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
          ctx.textAlign = 'center'
          ctx.textBaseline = 'bottom'
          ctx.fillText('续跑', n.x, top - 1)
        }
        ctx.globalAlpha = alpha
      }
    }

    // 发起者 (second channel, shape only) and 本轮助手 satellites. Both are
    // decorations: no node, edge, depth, layout input or selectable target.
    if (n.type === 'step' || n.type === 'ghost') {
      const decorationAlpha = alpha * fadeIn(n.id)
      drawOriginMark(ctx, n, o.layered, cam.k, decorationAlpha)
      if (o.showHelpers !== false) drawHelperSatellites(ctx, n, o, decorationAlpha, cam.k)
    }

    // Hover/search get a quiet halo. A selected node uses the focus dim instead
    // of a big ring: the related graph staying bright is the highlight. The
    // ring hugs the drawn glyph (a ~2.5px screen-space pad); the old +6 world
    // units read as a loud "big ring", and non-layered steps used to get a
    // Math.hypot-sized circle around their diamond.
    if ((isHover || isMatch) && !isSel) {
      const ringPath = (pad: number) => {
        ctx.beginPath()
        if (n.type === 'step' && !o.layered) {
          // Force-map steps are diamonds: trace the same rounded box rotated onto
          // the diamond, so the ring sits just outside the glyph's real outline.
          const rr = 7 * entry * 0.72 + pad
          ctx.save()
          ctx.translate(n.x, n.y)
          ctx.rotate(Math.PI / 4)
          roundRect(ctx, -rr, -rr, 2 * rr, 2 * rr, 3)
          ctx.restore()
        } else if (
          n.type === 'intent' ||
          n.type === 'goal' ||
          n.type === 'project' ||
          (o.layered && n.type === 'step')
        ) {
          // Pill nodes: follow the box (radius 7) at a 2.5px offset.
          roundRect(
            ctx,
            n.x - n.w - pad,
            n.y - n.h - pad,
            2 * n.w + 2 * pad,
            2 * n.h + 2 * pad,
            7 + pad,
          )
        } else {
          // Facts, ghosts and boundary leaves: a thin ring one pad outside the dot.
          ctx.arc(n.x, n.y, radiusOf(n) + pad, 0, TAU)
        }
      }
      ringPath(screenPad(cam.k, 2.5))
      ctx.strokeStyle = isActiveMatch ? ACTIVE_MATCH_RING : isMatch ? MATCH_RING : HOVER_RING
      ctx.lineWidth = (isActiveMatch ? 2.2 : 1) / Math.max(cam.k, 0.5)
      ctx.stroke()
      if (isActiveMatch) {
        ringPath(screenPad(cam.k, 7))
        ctx.globalAlpha = alpha * 0.35
        ctx.lineWidth = 5 / Math.max(cam.k, 0.5)
        ctx.stroke()
        ctx.globalAlpha = alpha
      }
    }

    // A selected node keeps a standing marker on the canvas. The panel carries
    // the words; the map must still answer "where is the thing I clicked" after
    // the pointer leaves. Full alpha and drawn last, so neither the focus dim
    // nor a hover sweep can hide it.
    if (isSel) {
      nodeRing(ctx, n, o.layered, screenPad(cam.k, 4.5))
      ctx.strokeStyle = SELECTION_RING
      ctx.globalAlpha = 1
      ctx.lineWidth = 2.2 / Math.max(cam.k, 0.5)
      ctx.stroke()
    }

    // Keyboard focus (canvas arrow keys) is a separate ring: the operator can
    // move the focus without selecting, so it must be visible next to selection.
    if (keyboard) {
      nodeRing(ctx, n, o.layered, screenPad(cam.k, 4))
      ctx.strokeStyle = KEYBOARD_FOCUS_RING
      ctx.globalAlpha = alpha
      ctx.lineWidth = 2 / Math.max(cam.k, 0.5)
      ctx.stroke()
    }

    // Labels for facts and boundary leaves: only what the pointer/search is on,
    // or a genuinely zoomed-in view. The graph is a map, not a table; hover is
    // the detail. A boundary leaf's label is what says why it is not a node
    // (`未载入` expandable vs `引用缺失` gone from the ledger vs `已否定`).
    //
    // Selecting a node also names the facts on its chain: before this, reading
    // any fact meant opening the panel (a chain task cost one click per fact).
    // The short label is the answer to "which fact is this", nothing more; a
    // closure too large for a readable map stays unlabelled.
    const onChain =
      n.type === 'fact' && o.focus !== null && o.focus !== undefined && o.focus.has(n.id) && o.focus.size <= FOCUS_LABEL_MAX
    if (
      (n.type === 'fact' || n.type === 'boundary') &&
      (isSel || isHover || isMatch || level.factLabels || onChain)
    ) {
      const tier: 'always' | 'focus' | 'budget' =
        isSel || isHover || isMatch ? 'always' : onChain && !level.factLabels ? 'focus' : 'budget'
      drawFactLabel(n, alpha, tier)
    }
  }

  // Warnings badge: a small amber disc with a count, drawn last so it sits on
  // top of every node family. Hover/selection still reads the full text in the
  // inspector; the badge is only "this direction has something to read".
  for (const n of visible) {
    const count = n.warnings?.length ?? 0
    if (count === 0) continue
    const alpha = dim(n.id)
    let bx = n.x
    let by = n.y
    if (n.type === 'intent') {
      bx = n.x + n.w - 2
      by = n.y - n.h - 2
    } else if (n.type === 'ghost') {
      bx = n.x + 10
      by = n.y - 10
    } else if (n.type === 'step') {
      bx = o.layered ? n.x + n.w - 2 : n.x + 9
      by = o.layered ? n.y - n.h - 2 : n.y - 9
    } else if (n.type === 'fact') {
      const r = radiusOf(n)
      bx = n.x + r + 1
      by = n.y - r - 1
    } else {
      bx = n.x + n.w - 6
      by = n.y - n.h - 4
    }
    ctx.globalAlpha = alpha
    ctx.beginPath()
    ctx.arc(bx, by, 6, 0, TAU)
    ctx.fillStyle = CANVAS.warning
    ctx.fill()
    ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
    ctx.strokeStyle = 'rgba(10,12,16,0.9)'
    ctx.stroke()
    ctx.fillStyle = CANVAS.background
    ctx.font = `700 10px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(count > 9 ? '9+' : String(count), bx, by + 0.5)
  }

  ctx.globalAlpha = 1
  ctx.restore()

  // Depth labels are screen-space: readable at any zoom, never clipped by the
  // fit padding, and fixed while panning. The top corners are occupied by the
  // DOM filter card (left) and the view controls (right); a label that would sit
  // under one of them is dropped instead of peeking out from behind it.
  if (o.layered && o.bands.length > 0) {
    ctx.font = `600 11px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    const left = 252
    const right = width - 268
    for (const band of o.bands) {
      if (band.x2 < vp.x0 || band.x1 > vp.x1) continue
      const sx = cam.x + ((band.x1 + band.x2) / 2) * cam.k
      if (sx < left || sx > right) continue
      ctx.fillStyle = 'rgba(169,178,193,0.6)'
      ctx.fillText(depthLabel(band.depth), sx, 8)
    }
  }
}

/**
 * Where the provenance mark sits: the pill's top-left corner in the chain-tree
 * view (the top-right corner already carries retry/attempt/warning badges), just
 * left of the force-map diamond, or the ghost ring's top-left diagonal. The mark
 * is deliberately outside the label box so a long title never collides with it.
 */
function originMarkAt(n: GraphNode, layered: boolean): { x: number; y: number; r: number } {
  if (n.type === 'ghost') return { x: n.x - 9, y: n.y - 9, r: 3.2 }
  if (layered) return { x: n.x - n.w + 1, y: n.y - n.h - 3, r: 4.2 }
  return { x: n.x - 11, y: n.y - 10, r: 4.2 }
}

function drawOriginMark(
  ctx: CanvasRenderingContext2D,
  n: GraphNode,
  layered: boolean,
  camK: number,
  alpha: number,
): void {
  const shape = originMarker(n.origin)
  if (!shape) return
  const { x, y, r } = originMarkAt(n, layered)
  ctx.save()
  ctx.globalAlpha = alpha
  ctx.strokeStyle = ORIGIN_MARKER_COLOR
  ctx.fillStyle = ORIGIN_MARKER_COLOR
  ctx.lineWidth = 1.3 / Math.max(camK, 0.5)
  if (shape === 'diamond') {
    // 实心菱形: commander
    ctx.beginPath()
    ctx.moveTo(x, y - r)
    ctx.lineTo(x + r, y)
    ctx.lineTo(x, y + r)
    ctx.lineTo(x - r, y)
    ctx.closePath()
    ctx.fill()
  } else if (shape === 'circle') {
    // 空心圆: executor (peer-launch)
    ctx.beginPath()
    ctx.arc(x, y, r, 0, TAU)
    ctx.fillStyle = 'rgba(10,12,16,0.9)'
    ctx.fill()
    ctx.beginPath()
    ctx.arc(x, y, r, 0, TAU)
    ctx.stroke()
  } else {
    // 双环: operator
    ctx.beginPath()
    ctx.arc(x, y, r, 0, TAU)
    ctx.fillStyle = 'rgba(10,12,16,0.9)'
    ctx.fill()
    ctx.beginPath()
    ctx.arc(x, y, r * 0.52, 0, TAU)
    ctx.stroke()
    ctx.beginPath()
    ctx.arc(x, y, r, 0, TAU)
    ctx.stroke()
  }
  ctx.restore()
}

/**
 * Native-subagent satellites: at most three dots plus the real count ("助×N"),
 * at the step pill's top-right / above-right of the force diamond or ghost ring.
 * `active > 0` breathes slowly (a live helper is being waited on); with reduced
 * motion the shape stays and the pulse goes. Drawing-only: nothing here can be
 * selected and nothing joins the layout.
 */
function drawHelperSatellites(
  ctx: CanvasRenderingContext2D,
  n: GraphNode,
  o: PaintOptions,
  alpha: number,
  camK: number,
): void {
  const helpers = n.helpers
  if (!helpers || helpers.calls <= 0) return
  const base =
    n.type === 'ghost'
      ? { x: n.x + 10, y: n.y - 10 }
      : o.layered
        ? { x: n.x + n.w + 6, y: n.y - n.h + 2 }
        : { x: n.x + 11, y: n.y - 10 }
  const pulse = helpers.active > 0 && o.motion ? 0.45 + 0.55 * Math.sin(o.time / 900) : 0.85
  const dots = Math.min(3, helpers.calls)
  ctx.save()
  ctx.fillStyle = HELPER_MARKER_COLOR
  for (let i = 0; i < dots; i++) {
    ctx.globalAlpha = alpha * (helpers.active > 0 ? pulse : 0.8)
    ctx.beginPath()
    ctx.arc(base.x + i * 6, base.y + (i % 2 === 0 ? 0 : 1.5), 2.1, 0, TAU)
    ctx.fill()
  }
  // The count is text, so it obeys the same LOD rule as background step titles.
  if (o.layered || o.showLabels || camK >= LOD_STEP_LABEL_K) {
    ctx.globalAlpha = alpha * pulse
    ctx.fillStyle = HELPER_MARKER_COLOR
    ctx.font = `600 9px "JetBrains Mono","Cascadia Code",monospace`
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    ctx.fillText(`助×${helpers.calls > 99 ? '99+' : helpers.calls}`, base.x + dots * 6 + 2, base.y + 0.5)
  }
  ctx.restore()
}

/** A comet moving along one edge: context → step → produced fact. */
function drawComet(
  ctx: CanvasRenderingContext2D,
  o: PaintOptions,
  curve: EdgeCurve,
  phase: number,
  color: string,
) {
  const r = 3.2 / Math.max(o.cam.k, 0.5)
  const at = (t: number) => pointOnCurve(curve, t)
  // A white-hot core over a coloured halo reads on top of both the dashed
  // running edge and the node colours; the tail makes the direction obvious.
  const head = at(phase)
  ctx.beginPath()
  ctx.arc(head.x, head.y, r * 2.1, 0, TAU)
  ctx.fillStyle = color
  ctx.globalAlpha = 0.28
  ctx.fill()
  for (let i = 4; i >= 0; i--) {
    const t = phase - i * 0.016
    if (t < 0) continue
    const p = at(t)
    ctx.beginPath()
    ctx.arc(p.x, p.y, r * (0.3 + (4 - i) * 0.12), 0, TAU)
    ctx.fillStyle = 'rgba(231,234,240,0.92)'
    ctx.globalAlpha = i === 0 ? 0.95 : 0.18 + (4 - i) * 0.1
    ctx.fill()
  }
  ctx.globalAlpha = 1
}

function depthLabel(depth: number): string {
  return depth === 0 ? '起点' : `第 ${depth} 跳`
}

function roundRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
) {
  const rr = Math.min(r, w / 2, h / 2)
  ctx.moveTo(x + rr, y)
  ctx.arcTo(x + w, y, x + w, y + h, rr)
  ctx.arcTo(x + w, y + h, x, y + h, rr)
  ctx.arcTo(x, y + h, x, y, rr)
  ctx.arcTo(x, y, x + w, y, rr)
  ctx.closePath()
}

function clip(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s
}
