// The graph canvas toolbar (T6, docs/33 §4.6): the draw controls plus the view popover.
// Buttons only report intent; the page owns the state and the canvas ref.
//
// 这排只留在画布上真正高频的六个动作：下发 →（放大 / 缩小 / 适配 / 重新布局）→ 视图。
// 时间轴与悬停预览是低频开关，收进「视图」的「显示」段——条上不再既有时针又有箭头，
// 也不再有第三条分隔线（291px 宽里三条线全是噪点）。
//
import {
  Clock,
  GitBranch,
  List,
  Maximize2,
  MessageSquare,
  MousePointer,
  Network,
  RotateCcw,
  SlidersHorizontal,
  Tags,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import {
  Button,
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@cyber/ui'
import type { LayoutMode } from '@/graph/sim'
import { useZoomPercent } from '@/graph/view-store'
import { TIMELINE_BODY_ID } from './GraphTimeline'

export interface GraphToolbarProps {
  chatDock: boolean
  onToggleChatDock: () => void
  layout: LayoutMode
  onLayout: (layout: LayoutMode) => void
  showLabels: boolean
  onToggleLabels: () => void
  /** The accessible list equivalent of the canvas. */
  listView: boolean
  onToggleListView: () => void
  onZoom: (factor: number) => void
  onFit: () => void
  /** Discard manual node drops and restore the computed layout. */
  onRelayout: () => void
  /** The time axis strip at the map footer; off means it takes no height. */
  timelineOpen: boolean
  onToggleTimeline: () => void
  /** Revealed/total for the switch's tooltip, so the state is readable closed. */
  timelineCounts?: string
  /**
   * 悬停预览：默认开（这个开关用来关掉它）。开则指针碰到节点/线就高亮、显示标签
   * 并弹出右侧详情；关则只有点击才高亮、才出右侧面板。力导向与链路树共用这一份状态。
   */
  hoverPreview: boolean
  onToggleHoverPreview: () => void
}

const BOX = 'saker-graph-tool shrink-0'
const ICON = 'saker-graph-tool-icon shrink-0'
/**
 * 选中态：淡色底 + 主色图标。不用 `Button` 的 `active`（它是 `bg-primary/10`，在深色
 * 底上仍是一块红板），这里按同一套语义自己给一层更淡的底，`aria-pressed` 照旧。
 */
const ON = 'bg-primary/[0.08] text-primary hover:bg-primary/[0.12] hover:text-primary'

/** 「视图」面板的内容。单独导出，测试不必经过 Radix 的 Portal 就能断言开关状态。 */
export type GraphViewProps = Pick<
  GraphToolbarProps,
  | 'layout'
  | 'onLayout'
  | 'showLabels'
  | 'onToggleLabels'
  | 'listView'
  | 'onToggleListView'
  | 'timelineOpen'
  | 'onToggleTimeline'
  | 'timelineCounts'
  | 'hoverPreview'
  | 'onToggleHoverPreview'
>

export function GraphViewPanel({
  layout,
  onLayout,
  showLabels,
  onToggleLabels,
  listView,
  onToggleListView,
  timelineOpen,
  onToggleTimeline,
  timelineCounts,
  hoverPreview,
  onToggleHoverPreview,
}: GraphViewProps) {
  return (
    <>
      <div className="px-1.5 pb-1 pt-0.5 text-[10.5px] uppercase tracking-wide text-ink-3">布局</div>
      <div className="flex gap-1">
        <Button
          size="sm"
          variant="ghost"
          className={`flex-1 justify-start gap-1.5 ${layout === 'layered' ? ON : ''}`}
          aria-pressed={layout === 'layered'}
          title="链路树：起点 → 每一步意图 → 它产出的事实；目标达成后接到证据事实上（未达成的终点在左下终点卡）"
          onClick={() => onLayout('layered')}
        >
          <GitBranch className="h-3.5 w-3.5" /> 链路树
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className={`flex-1 justify-start gap-1.5 ${layout === 'force' ? ON : ''}`}
          aria-pressed={layout === 'force'}
          title="力导向：按连接关系铺开，看整体形状与离群节点；要读单条依赖顺序用链路树"
          onClick={() => onLayout('force')}
        >
          <Network className="h-3.5 w-3.5" /> 力导向
        </Button>
      </div>
      <p className="px-1.5 pb-1.5 pt-0.5 text-[10.5px] leading-4 text-ink-3">
        链路树按依赖顺序读一条链；力导向按连接关系铺开，看整体形状与离群节点。
      </p>

      <div className="border-t border-hairline" />
      <div className="px-1.5 pb-0.5 pt-1.5 text-[10.5px] uppercase tracking-wide text-ink-3">显示</div>
      <Button
        size="sm"
        variant="ghost"
        className={`w-full justify-start gap-1.5 ${showLabels ? ON : ''}`}
        aria-pressed={showLabels}
        title={showLabels ? '意图标签已显示：点一下隐藏' : '意图标签已隐藏：点一下显示'}
        onClick={onToggleLabels}
      >
        <Tags className="h-3.5 w-3.5" /> 意图标签
        <span className="ml-auto text-[11px] text-ink-3">{showLabels ? '开' : '关'}</span>
      </Button>
      {/* 时间轴：原先在条上单独占一个时钟键，收进这里；展开时 footer 才存在。 */}
      <Button
        size="sm"
        variant="ghost"
        className={`w-full justify-start gap-1.5 ${timelineOpen ? ON : ''}`}
        aria-label="时间轴"
        aria-expanded={timelineOpen}
        aria-controls={timelineOpen ? TIMELINE_BODY_ID : undefined}
        title={
          timelineOpen
            ? '时间轴已展开：点一下收起，图谱恢复完整显示'
            : `时间轴：按入账顺序揭示节点${timelineCounts ? `（${timelineCounts}）` : ''}`
        }
        onClick={onToggleTimeline}
      >
        <Clock className="h-3.5 w-3.5" /> 时间轴
        <span className="ml-auto text-[11px] text-ink-3">{timelineOpen ? '开' : '关'}</span>
      </Button>
      {/* 悬停预览：默认开，这个开关用来关掉——指针扫过就高亮是常态，不想被「碰」
          打扰时才关。开着时悬停与点击用同一套集合（整条主干 + 一跳旁支）。 */}
      <Button
        size="sm"
        variant="ghost"
        className={`w-full justify-start gap-1.5 ${hoverPreview ? ON : ''}`}
        aria-pressed={hoverPreview}
        title={
          hoverPreview
            ? '悬停预览已开启（默认）：指针碰到节点或线就高亮并显示右侧详情。点一下关掉，恢复「只有点击才高亮」'
            : '悬停预览已关闭：只有点击才高亮、才出右侧栏。点一下重新开启'
        }
        onClick={onToggleHoverPreview}
      >
        <MousePointer className="h-3.5 w-3.5" /> 悬停预览
        <span className="ml-auto text-[11px] text-ink-3">{hoverPreview ? '开' : '关'}</span>
      </Button>
      <Button
        size="sm"
        variant="ghost"
        className={`w-full justify-start gap-1.5 ${listView ? ON : ''}`}
        aria-pressed={listView}
        title="列表视图（画布的可访问等价入口；键盘可达，Esc 关闭）"
        onClick={onToggleListView}
      >
        <List className="h-3.5 w-3.5" /> 列表视图
      </Button>
    </>
  )
}

function ZoomPercent() {
  const zoom = useZoomPercent()
  return <span className="saker-graph-zoom saker-nums text-center font-mono text-caption text-ink-2" aria-label={`缩放比例 ${zoom}%`}>{zoom}%</span>
}

export function GraphToolbar({
  chatDock,
  onToggleChatDock,
  layout,
  onLayout,
  showLabels,
  onToggleLabels,
  listView,
  onToggleListView,
  onZoom,
  onFit,
  onRelayout,
  timelineOpen,
  onToggleTimeline,
  timelineCounts,
  hoverPreview,
  onToggleHoverPreview,
}: GraphToolbarProps) {
  return (
    <div
      className="saker-graph-corner-tr saker-graph-toolbar absolute z-20 flex items-center gap-1 rounded-xl border border-hairline bg-card/95 p-1 shadow-lifted backdrop-blur"
      role="toolbar"
      aria-label="图谱视图控件"
    >
      <Button
        size="icon-sm"
        variant="ghost"
        className={`saker-graph-tool-text ${BOX} ${chatDock ? ON : ''}`}
        aria-pressed={chatDock}
        title="下发：打开通信页的下发入口（右侧面板）"
        aria-label="下发"
        onClick={onToggleChatDock}
      >
        <MessageSquare className={ICON} />
        <span>下发</span>
      </Button>
      <div role="group" aria-label="缩放与适配" className="flex shrink-0 items-center rounded-lg bg-surface-2/70">
        <Button
          size="icon-sm"
          variant="ghost"
          className={BOX}
          title="放大"
          aria-label="放大"
          onClick={() => onZoom(1.25)}
        >
          <ZoomIn className={ICON} />
        </Button>
        <ZoomPercent />
        <Button
          size="icon-sm"
          variant="ghost"
          className={BOX}
          title="缩小"
          aria-label="缩小"
          onClick={() => onZoom(0.8)}
        >
          <ZoomOut className={ICON} />
        </Button>
        <Button
          size="icon-sm"
          variant="ghost"
          className={BOX}
          title="适配画布（键盘：方向键移动选择 · Enter 打开 · Esc 取消）"
          aria-label="适配画布"
          onClick={onFit}
        >
          <Maximize2 className={ICON} />
        </Button>
      </div>
      <Button
        size="icon-sm"
        variant="ghost"
        className={BOX}
        title="重新布局：按依赖重新排列，并清除手动拖动的节点位置"
        aria-label="重新布局"
        onClick={onRelayout}
      >
        <RotateCcw className={ICON} />
      </Button>
      <Popover>
        <PopoverTrigger asChild>
          <Button
            size="icon-sm"
            variant="ghost"
            className={`saker-graph-tool-text ${BOX}`}
            title="视图：布局、标签、列表、时间轴与悬停预览"
            aria-label="视图"
          >
            <SlidersHorizontal className={ICON} />
            <span>视图</span>
          </Button>
        </PopoverTrigger>
        <PopoverContent align="end" className="w-64 p-1.5">
          <GraphViewPanel
            layout={layout}
            onLayout={onLayout}
            showLabels={showLabels}
            onToggleLabels={onToggleLabels}
            listView={listView}
            onToggleListView={onToggleListView}
            timelineOpen={timelineOpen}
            onToggleTimeline={onToggleTimeline}
            timelineCounts={timelineCounts}
            hoverPreview={hoverPreview}
            onToggleHoverPreview={onToggleHoverPreview}
          />
        </PopoverContent>
      </Popover>
    </div>
  )
}
