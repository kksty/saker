// 缩略图：整张图的俯视定位图 + 当前视口。
//
// 缩略图不承担图例职责。1100 个节点缩到一张小卡里时，逐点按种类上色、逐条画
// 产出边，只会得到一团彩色噪点（从远处看就像一台雷达），结构和位置都读不出来。
// 这里按信息层级重画：
//   - 事实/边界只作中性灰的密度底图；意图/目标保留状态色，已收口意图半透明；
//   - 大图不画边：上千条产出边加上百条前提边在小地图里只会变成放射噪点，关系
//     类型在 184px 上也读不出来；小图仍画全量边和事实颜色，此时它就是一张真正
//     的缩略图；
//   - 视口用暗角和对比色描边圈出，卡片头给出“视口覆盖全图的比例”；
//   - 实时意图在它真实的位置上画呼吸环，不再有绕中心空转的扫描扇面。
//
// 相机由画布发布到 view-store（docs/33 §4.5）；这里用 useView 订阅，所以只有
// 订阅者随相机变化重渲染，图谱根组件不因平移/缩放而重渲染。点击或拖动把屏幕
// 坐标换算回世界坐标交给 onCenter（GraphPanel 接 canvas.centerOn）。
import { useEffect, useMemo, useRef, useState } from 'react'
import type { EdgeKind, GraphModel, GraphNode } from '@/graph/model'
import { DEFAULT_GRAPH_PALETTE, type GraphPalette } from '@/graph/palette'
import { edgeStyle, halfExtentX, nodeColor } from '@/graph/render'
import { radiusOf, type LayoutMode } from '@/graph/sim'
import { useHovered, useView, type GraphView } from '@/graph/view-store'
import { GraphResizeHandle, useGraphResize, type OverlaySize } from './GraphResize'

/**
 * 小地图是「整张图」（LoL 那种）：永远画全图的完整形状，只有代表当前视野的
 * 方框在变。卡片默认方形、尺寸恒定，不跟着包围盒变——力导向每帧都在动，卡片
 * 跟着包围盒变宽变窄会一直抖，表头也会跟着挤；点击目标恒定，眼睛也知道往哪看。
 * 极端比例（细长链路、横条）就在方卡里居中留边，仍是「整图」。
 *
 * 尺寸可从左上角拖动，范围 [MINI_MIN, 画布的三分之一]，写进
 * localStorage（与相机、面板宽度同一类「几何记忆」，重启后仍在）。
 */
export const MINI_LONG = 168
/** 可拖的最小边：88px 还能看清整图形状与视口框，再小就只能看个色块。 */
export const MINI_MIN = 88
/** 最大边占画布的份额：小地图不能反客为主盖住要读的图。 */
export const MINI_MAX_RATIO = 1 / 3
/** 尺寸记忆：与相机（saker.graph.camera:<项目>）、右侧面板宽度同一族。 */
export const MINI_SIZE_KEY = 'saker.graph.minimapSize'
const PADDING = 7
const FRAME_INSET = 3
/**
 * 单击定位要等这么久才执行，好让双击先被认出来：双击直接回正中，不先跳到点上的位置。
 * 拖动不受影响，按下后一动就实时跟手。
 */
const CLICK_DELAY_MS = 250
/** 指针移动超过这么多像素才算拖动；手抖一下仍按单击处理。 */
const DRAG_THRESHOLD = 4
/** 呼吸环的重绘间隔：30fps 足够，避免和主画布抢帧。 */
const PULSE_FRAME_MS = 33
/**
 * 超过这个节点数就切到“密度底图 + 骨架”模式：184px 宽放不下上千个彩色事实点，
 * 全画出来只会糊成一块色斑。小图仍然保留完整的上色和全部边。
 */
const DENSE_NODES = 240
const MAX_VISIBLE_EDGES = 36
/** 视口覆盖到这么多就算“看见全图”，不再画一个跑到画布外的框。 */
const FULL_VIEW = 0.97

const TAU = Math.PI * 2

interface WorldBounds {
  minX: number
  minY: number
  maxX: number
  maxY: number
}

/**
 * 拖出来的尺寸收进合法范围：最小 MINI_MIN，最大画布的三分之一（两个方向各自夹）。
 *
 * 画布很小时（窄屏、右侧面板占掉大半），三分之一的宽度可能比 MINI_MIN 还小；
 * 这时保底给 MINI_MIN——宁可略微超过三分之一，也不能缩到点不准。
 */
export function clampMiniSize(size: OverlaySize, canvas: OverlaySize): OverlaySize {
  const maxW = Math.max(MINI_MIN, Math.floor(canvas.w * MINI_MAX_RATIO))
  const maxH = Math.max(MINI_MIN, Math.floor(canvas.h * MINI_MAX_RATIO))
  const fit = (value: number, max: number) =>
    Math.round(Math.min(max, Math.max(MINI_MIN, Number.isFinite(value) ? value : MINI_LONG)))
  return { w: fit(size.w, maxW), h: fit(size.h, maxH) }
}

/**
 * 卡片尺寸：默认 MINI_LONG 见方，之后由操作员拖出来（clampMiniSize）。
 *
 * 以前这里按包围盒比例算短边，出过两次事：表头把卡片撑到比画布宽（右侧 49px
 * 死区），以及力导向每帧改包围盒时卡片宽度跟着跳。小地图只需要「整图 + 视野框」，
 * 尺寸恒定（或由人决定）比贴合形状有用。
 */
interface MinimapTransform {
  scale: number
  offsetX: number
  offsetY: number
  toWorldX: (px: number) => number
  toWorldY: (py: number) => number
}

interface MiniEdge {
  source: GraphNode
  target: GraphNode
  color: string
  alpha: number
}

/** 一帧要画的东西：分组/边在 useMemo 里重建，rAF 循环里只做坐标换算。 */
interface DrawScene {
  model: GraphModel
  layout: LayoutMode
  bounds: WorldBounds
  /** 绘制边界：视口逐轴夹进世界包围盒（固定 184×116 框内等比铺满）。 */
  frame: WorldBounds
  viewport: { x0: number; y0: number; x1: number; y1: number }
  edges: MiniEdge[]
  dust: GraphNode[]
  marks: GraphNode[]
  active: GraphNode[]
  matched: GraphNode[]
  hovered: string | null
  /** 视口覆盖整张图的比例（0–1）；接近 1 时不再画视口框，免得框跑到画布外。 */
  coverage: number
  dense: boolean
  live: boolean
  motion: boolean
}

/** 节点在图坐标里的半尺寸；用绘制形状而非标签宽度，缩略图只需要大致轮廓。 */
function worldBounds(model: GraphModel, layout: LayoutMode): WorldBounds | null {
  if (model.nodes.length === 0) return null
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const n of model.nodes) {
    const halfW = halfExtentX(n, layout === 'layered')
    const round = n.type === 'fact' || n.type === 'ghost' || (n.type === 'step' && layout === 'force')
    const halfH = round ? radiusOf(n) : Math.max(n.h, 8)
    minX = Math.min(minX, n.x - halfW)
    minY = Math.min(minY, n.y - halfH)
    maxX = Math.max(maxX, n.x + halfW)
    maxY = Math.max(maxY, n.y + halfH)
  }
  // A single node (or a stack at the same point) still needs a non-zero span.
  if (!(maxX > minX)) {
    minX -= 10
    maxX += 10
  }
  if (!(maxY > minY)) {
    minY -= 10
    maxY += 10
  }
  return { minX, minY, maxX, maxY }
}



/**
 * 等比装进卡片（contain），世界坐标 → 缩略图像素；反向换算用于点击定位。
 *
 * 这里画的是整张图：不裁、不跟镜头。卡片形状由 minimapSize 按包围盒比例给出，
 * 所以 contain 之后四周只留 PADDING 那圈边；绝不拉伸——两个意图同一 scale。
 */
function minimapTransform(bounds: WorldBounds, width: number, height: number, pad: number): MinimapTransform {
  const spanX = bounds.maxX - bounds.minX
  const spanY = bounds.maxY - bounds.minY
  const innerW = Math.max(width - pad * 2, 1)
  const innerH = Math.max(height - pad * 2, 1)
  const scale = Math.min(innerW / spanX, innerH / spanY)
  const offsetX = pad + (innerW - spanX * scale) / 2
  const offsetY = pad + (innerH - spanY * scale) / 2
  return {
    scale,
    offsetX,
    offsetY,
    toWorldX: (px) => bounds.minX + (px - offsetX) / scale,
    toWorldY: (py) => bounds.minY + (py - offsetY) / scale,
  }
}

function clampToBounds(value: number, min: number, max: number): number {
  if (value < min) return min
  if (value > max) return max
  return value
}

/** 关系边的权重：骨架边比产出边长，产出边只在非密集图里出现。 */
function edgeAlpha(kind: EdgeKind): number {
  switch (kind) {
    case 'brief':
    case 'supports':
      return 0.5
    case 'premise':
    case 'reference':
    case 'pin':
      return 0.4
    case 'host':
      return 0.3
    default:
      return 0.24
  }
}

/** The camera's visible world rectangle (null until the canvas has a size). */
function viewportOf(view: GraphView): DrawScene['viewport'] | null {
  const { camera, width, height } = view
  if (width <= 0 || height <= 0 || !(camera.k > 0)) return null
  return {
    x0: -camera.x / camera.k,
    y0: -camera.y / camera.k,
    x1: (width - camera.x) / camera.k,
    y1: (height - camera.y) / camera.k,
  }
}

export function GraphMinimap({
  model,
  layout = 'force',
  onCenter,
  onFit,
  live = false,
  matches = null,
  defaultSide = MINI_LONG,
  palette = DEFAULT_GRAPH_PALETTE,
}: {
  model: GraphModel
  layout?: LayoutMode
  onCenter: (worldX: number, worldY: number) => void
  /** 双击：整图居中 + 默认缩放。和工具条的「适配画布」同一个动作，不切换布局。 */
  onFit: () => void
  /** 有意图在跑/复核：只在这时画呼吸环，静止时不保留常驻 rAF。 */
  live?: boolean
  /** 搜索命中的节点 id：在缩略图上圈出来，整图范围也能看到命中位置。 */
  matches?: ReadonlySet<string> | null
  defaultSide?: number
  palette?: GraphPalette
}) {
  const view = useView()
  const hovered = useHovered()
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const dragging = useRef(false)
  const moved = useRef(false)
  const downAt = useRef({ x: 0, y: 0 })
  // 还没落地的单击：双击会把它取消掉，所以双击不会先跳到点上再回正中。
  const clickTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // 系统开启“减少动态效果”后，呼吸环停在第一帧，不再申请动画帧。
  const [motion, setMotion] = useState(true)

  // The simulation moves the model's nodes in place, even when the camera is still.
  const bounds = useMemo(() => worldBounds(model, layout), [model, layout, view.layoutRevision])
  const clamp = (size: OverlaySize) => clampMiniSize(size, { w: view.width, h: view.height })
  const resize = useGraphResize(MINI_SIZE_KEY, 'top-left',
    (saved) => clamp(saved ?? { w: defaultSide, h: defaultSide }), clamp)
  const size = clamp(resize.size ?? { w: defaultSide, h: defaultSide })
  const dense = model.nodes.length > DENSE_NODES
  const viewport = useMemo(() => viewportOf(view), [view])

  // 边只保留模型里两端都还在的那些；模型重建时才重算，平移/缩放不重新分配。
  // 大图不画边：上千条产出边加上百条前提边在小地图里只会变成放射噪点，关系
  // 类型在 184px 上根本读不出来；密度和意图落点已经能说明“图长什么样”。
  const edges = useMemo(() => {
    const visible = model.edges
    const crowded = dense || visible.length > MAX_VISIBLE_EDGES
    const candidates = !crowded ? visible : layout === 'layered'
      ? visible.filter((e) => e.kind === 'brief' || e.kind === 'goal' || e.kind === 'supports')
      : []
    if (candidates.length > MAX_VISIBLE_EDGES) return []
    const list: MiniEdge[] = []
    for (const e of candidates) {
      const s = model.byId.get(e.source)
      const t = model.byId.get(e.target)
      if (!s || !t) continue
      list.push({
        source: s,
        target: t,
        color: edgeStyle(e, palette).color,
        alpha: edgeAlpha(e.kind),
      })
    }
    return list
  }, [model, dense, layout, palette])

  // 事实是底图，意图/目标/项目是骨架：分层画，骨架永远压在密度上面。
  const { dust, marks } = useMemo(() => {
    const dustNodes: GraphNode[] = []
    const markNodes: GraphNode[] = []
    for (const n of model.nodes) {
      if (n.type === 'fact' || n.type === 'ghost' || n.type === 'boundary') dustNodes.push(n)
      else markNodes.push(n)
    }
    return { dust: dustNodes, marks: markNodes }
  }, [model])

  // 实时意图：步骤节点（ghost 是同一意图的未产出端，不重复画环）。
  const active = useMemo(
    () => marks.filter((n) => n.type === 'step' && (n.status === 'running' || n.status === 'reviewing')),
    [marks],
  )
  const matched = useMemo(
    () => (matches ? model.nodes.filter((n) => matches.has(n.id)) : []),
    [model, matches],
  )

  // 视口覆盖比例：不是缩放倍数，而是“当前画面看到了整张图的多少”，操作员更
  // 容易判断自己是在看局部还是全貌。相机移出图外时诚实显示 0%。
  const coverage = useMemo(() => {
    if (!bounds || !viewport) return 0
    const ix = Math.max(
      0,
      Math.min(viewport.x1, bounds.maxX) - Math.max(viewport.x0, bounds.minX),
    )
    const iy = Math.max(
      0,
      Math.min(viewport.y1, bounds.maxY) - Math.max(viewport.y0, bounds.minY),
    )
    const spanX = bounds.maxX - bounds.minX
    const spanY = bounds.maxY - bounds.minY
    if (!(spanX > 0) || !(spanY > 0)) return 0
    return Math.min(1, (ix * iy) / (spanX * spanY))
  }, [bounds, viewport])
  const coveragePct = coverage > 0 ? Math.max(1, Math.round(coverage * 100)) : 0

  const scene: DrawScene | null =
    bounds && viewport
      ? {
          model, layout, bounds, viewport, edges, dust, marks, active, matched, hovered, coverage, dense, live, motion,
          frame: bounds,
        }
      : null
  const sceneRef = useRef<DrawScene | null>(null)
  sceneRef.current = scene

  // 画布：骨架边 → 事实密度 → 意图/目标 → 视口边框 → 命中/悬停/实时环。
  const draw = (now: number) => {
    const canvas = canvasRef.current
    const current = sceneRef.current
    if (!canvas || !current) return
    const dpr = typeof window !== 'undefined' && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1
    const pixelW = Math.round(size.w * dpr)
    const pixelH = Math.round(size.h * dpr)
    if (canvas.width !== pixelW) canvas.width = pixelW
    if (canvas.height !== pixelH) canvas.height = pixelH
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, size.w, size.h)

    const map = minimapTransform(current.frame, size.w, size.h, PADDING)
    const xOf = (x: number) => map.offsetX + (x - current.frame.minX) * map.scale
    const yOf = (y: number) => map.offsetY + (y - current.frame.minY) * map.scale
    ctx.lineCap = 'round'
    ctx.lineJoin = 'round'

    // 1. 关系骨架：只有小图才画边；大图的边见 edges memo，全部跳过。
    ctx.lineWidth = 0.7
    for (const e of current.edges) {
      ctx.globalAlpha = e.alpha * 0.55
      ctx.strokeStyle = e.color
      ctx.beginPath()
      ctx.moveTo(xOf(e.source.x), yOf(e.source.y))
      ctx.lineTo(xOf(e.target.x), yOf(e.target.y))
      ctx.stroke()
    }

    // 2. 事实（以及边界/未产出端）：密集图里是中性灰的密度底图，小图里保留种类色。
    //    密集图的样式是常量，必须提到循环外：1100 次 fillStyle/globalAlpha 赋值
    //    会在首帧堆出几十毫秒的 long task，而 1100 个 1px 方块本身并不慢。
    if (current.dense) {
      ctx.globalAlpha = 0.42
      ctx.fillStyle = palette.colors.ink3
      for (const n of current.dust) {
        ctx.fillRect(xOf(n.x) - 0.7, yOf(n.y) - 0.7, 1.4, 1.4)
      }
    } else {
      for (const n of current.dust) {
        ctx.globalAlpha = n.type === 'ghost' ? 0.6 : 0.9
        ctx.fillStyle = nodeColor(n, palette)
        ctx.fillRect(xOf(n.x) - 1, yOf(n.y) - 1, 2, 2)
      }
    }

    // 3. 意图/目标/项目。大图里已收口（done/parked）的意图退成半透明，只让还在
    //    跑、待处理或失败的意图保持高对比：既看得到意图的分布，又不会被一片完成
    //    状态的色块淹没；小图信息量小，全部保留完整状态色。
    for (const n of current.marks) {
      const x = xOf(n.x)
      const y = yOf(n.y)
      const quiet = n.status === 'done' || n.status === 'parked'
      ctx.globalAlpha = quiet ? 0.55 : 0.96
      ctx.fillStyle = nodeColor(n, palette)
      if (n.type !== 'step' || current.layout === 'layered') {
        const halfWidth = n.type === 'project' ? 3.4 : n.type === 'goal' ? 4.1 : 3.7
        const halfHeight = n.type === 'step' && quiet ? 1.15 : 1.75
        ctx.fillRect(x - halfWidth, y - halfHeight, halfWidth * 2, halfHeight * 2)
        continue
      }
      const r = quiet ? 2 : 2.8
      ctx.beginPath()
      ctx.moveTo(x, y - r)
      ctx.lineTo(x + r, y)
      ctx.lineTo(x, y + r)
      ctx.lineTo(x - r, y)
      ctx.closePath()
      ctx.fill()
    }

    // 4. 视口：外面压暗、里面留亮，再描一圈白框。视口已经罩住几乎整张图时
    //    不画框（否则框会跑到画布外，只剩一条莫名其妙的边）；视口被画布裁掉
    //    时用虚线框，明确“它还在画布之外继续”。
    if (current.viewport && current.coverage < FULL_VIEW) {
      const vx = current.viewport
      const rawX = xOf(vx.x0)
      const rawY = yOf(vx.y0)
      const rawW = (vx.x1 - vx.x0) * map.scale
      const rawH = (vx.y1 - vx.y0) * map.scale
      const rx = Math.max(FRAME_INSET, rawX)
      const ry = Math.max(FRAME_INSET, rawY)
      const right = Math.min(size.w - FRAME_INSET, rawX + rawW)
      const bottom = Math.min(size.h - FRAME_INSET, rawY + rawH)
      const rw = right - rx
      const rh = bottom - ry
      const visible = rw > 0 && rh > 0
      const clipped = rx > rawX || ry > rawY || right < rawX + rawW || bottom < rawY + rawH
      ctx.save()
      ctx.beginPath()
      ctx.rect(0, 0, size.w, size.h)
      if (visible) ctx.rect(rx, ry, rw, rh)
      ctx.fillStyle = palette.minimapDim
      ctx.fill('evenodd')
      ctx.restore()
      if (visible) {
        ctx.globalAlpha = 1
        ctx.fillStyle = palette.band
        ctx.fillRect(rx, ry, rw, rh)
        // 两条对比色描边让视口边界盖住下面的灰色密度。
        if (clipped) ctx.setLineDash([3, 3])
        ctx.lineWidth = 2.4
        ctx.strokeStyle = palette.colors.background
        ctx.strokeRect(rx, ry, rw, rh)
        ctx.lineWidth = 1
        ctx.strokeStyle = palette.rings.selection
        ctx.strokeRect(rx, ry, rw, rh)
        ctx.setLineDash([])
      }
    }

    // 5. 搜索命中、悬停和实时意图各有对应的环。
    if (current.matched.length > 0) {
      ctx.lineWidth = 1
      ctx.strokeStyle = palette.rings.match
      for (const n of current.matched) {
        ctx.beginPath()
        ctx.arc(xOf(n.x), yOf(n.y), 3.2, 0, TAU)
        ctx.stroke()
      }
    }
    if (current.hovered) {
      const n = current.model.byId.get(current.hovered)
      if (n) {
        ctx.lineWidth = 1.2
        ctx.strokeStyle = palette.rings.selection
        ctx.beginPath()
        ctx.arc(xOf(n.x), yOf(n.y), 4, 0, TAU)
        ctx.stroke()
      }
    }
    if (current.live) {
      for (const n of current.active) {
        const pulse = current.motion ? (Math.sin(now / 520) + 1) / 2 : 0.55
        const color = nodeColor(n, palette)
        ctx.globalAlpha = 0.28 + pulse * 0.45
        ctx.lineWidth = 1.2
        ctx.strokeStyle = color
        ctx.beginPath()
        ctx.arc(xOf(n.x), yOf(n.y), 3 + pulse * 2.4, 0, TAU)
        ctx.stroke()
        ctx.globalAlpha = 0.95
        ctx.fillStyle = color
        ctx.beginPath()
        ctx.arc(xOf(n.x), yOf(n.y), 1.6, 0, TAU)
        ctx.fill()
      }
      ctx.globalAlpha = 1
    }
  }
  const drawRef = useRef(draw)
  drawRef.current = draw

  // 数据/相机/悬停/实时状态变化：立即重画一帧。`live` 与 `motion` 也在这里：
  // 关掉实时或系统减少动态效果时必须补画一帧，否则上一帧的呼吸环会留在画布上。
  useEffect(() => {
    drawRef.current(typeof performance !== 'undefined' ? performance.now() : 0)
  }, [bounds, model, view, live, motion, hovered, matches, palette])

  // prefers-reduced-motion 是无障碍设置：环仍画出来，但不再转动。
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => setMotion(!mq.matches)
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])

  // 只在 live 且允许动效时申请动画帧；静止/卸载即停，不留常驻 rAF。
  useEffect(() => {
    if (!live || !motion || typeof requestAnimationFrame !== 'function') return
    let raf = 0
    let last = 0
    const loop = (now: number) => {
      raf = requestAnimationFrame(loop)
      if (now - last < PULSE_FRAME_MS) return
      last = now
      drawRef.current(now)
    }
    raf = requestAnimationFrame(loop)
    return () => cancelAnimationFrame(raf)
  }, [live, motion])

  const cancelPendingClick = () => {
    if (clickTimer.current === null) return
    clearTimeout(clickTimer.current)
    clickTimer.current = null
  }

  // 卸载时把还没落地的单击取消，免得计时器打到已卸载的画布上。
  useEffect(() => cancelPendingClick, [])

  if (!bounds || view.width <= 0 || view.height <= 0) return null

  // 同一套边界与尺寸：画面怎么画，点击就怎么换算，否则点哪儿去不对。
  const centerAt = (clientX: number, clientY: number) => {
    const canvas = canvasRef.current
    if (!canvas || !bounds) return
    const map = minimapTransform(bounds, size.w, size.h, PADDING)
    const rect = canvas.getBoundingClientRect()
    const px = ((clientX - rect.left) * size.w) / Math.max(rect.width, 1)
    const py = ((clientY - rect.top) * size.h) / Math.max(rect.height, 1)
    onCenter(
      clampToBounds(map.toWorldX(px), bounds.minX, bounds.maxX),
      clampToBounds(map.toWorldY(py), bounds.minY, bounds.maxY),
    )
  }

  return (
    <div
      className="relative overflow-hidden rounded-xl border border-hairline bg-card/95 shadow-lifted backdrop-blur"
      // 卡片宽度含两侧 1px 边线：画布按 size.w 画，内容盒必须正好是 size.w，否则右边会被裁掉 1px。
      style={{ inlineSize: size.w + 2 }}
      title={layout === 'layered'
        ? '链图缩略：横条对应链图里的项目、目标和意图。点击定位，拖动移动视图，双击适配画布。'
        : '力导向图缩略：小点是事实等节点，菱形是意图。点击定位，拖动移动视图，双击适配画布。'}
    >
      <div className="flex min-h-7 items-center justify-between gap-1 border-b border-hairline py-1 pl-6 pr-2">
        <span className="whitespace-nowrap text-caption font-medium text-ink-2">{size.w >= 128 ? '全图缩略' : '缩略图'}</span>
        {size.w >= 128 && (
          <span
            className="saker-nums whitespace-nowrap font-mono text-[11px] text-ink-3"
            title="当前视口覆盖图谱外接范围的面积比例"
          >
            视口 {coveragePct}%
          </span>
        )}
      </div>
      <canvas
        ref={canvasRef}
        data-testid="graph-minimap"
        aria-label="图谱缩略图：显示整张图的节点与当前视口；点击定位，拖动移动视图，双击回到整图居中"
        className="block cursor-crosshair"
        style={{ width: size.w, height: size.h }}
        onPointerDown={(event) => {
          event.preventDefault()
          // 双击的第一下会先到这里：把还没落地的单击取消，双击就不会先跳一次。
          cancelPendingClick()
          dragging.current = true
          moved.current = false
          downAt.current = { x: event.clientX, y: event.clientY }
          try {
            event.currentTarget.setPointerCapture(event.pointerId)
          } catch {
            /* 老浏览器不支持捕获也不影响单击定位 */
          }
        }}
        onPointerMove={(event) => {
          if (!dragging.current) return
          if (!moved.current) {
            const dx = event.clientX - downAt.current.x
            const dy = event.clientY - downAt.current.y
            if (Math.hypot(dx, dy) < DRAG_THRESHOLD) return
            moved.current = true
          }
          event.preventDefault()
          centerAt(event.clientX, event.clientY)
        }}
        onPointerUp={(event) => {
          dragging.current = false
          try {
            event.currentTarget.releasePointerCapture(event.pointerId)
          } catch {
            /* 已释放 */
          }
          // 拖动已经在移动中跟手定位了，抬手不再补一次，否则会多跳一格。
          if (moved.current) {
            moved.current = false
            return
          }
          const { clientX, clientY } = event
          clickTimer.current = setTimeout(() => {
            clickTimer.current = null
            centerAt(clientX, clientY)
          }, CLICK_DELAY_MS)
        }}
        onPointerCancel={() => {
          dragging.current = false
          moved.current = false
        }}
        onDoubleClick={(event) => {
          event.preventDefault()
          cancelPendingClick()
          onFit()
        }}
      />
      <GraphResizeHandle resize={resize} label="全图缩略" testId="graph-minimap-resize" />
    </div>
  )
}
