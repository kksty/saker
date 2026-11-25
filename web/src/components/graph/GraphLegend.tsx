// 图例：把画布上的颜色/线型翻译成人话。
//
// 文案必须与 render.ts 的绘制一致：颜色、线型、虚线环都是承诺，不是装饰。
// 因此这里不重新声明色值，而是直接 import render.ts 的绘制色表——改画笔时
// 图例自动跟着改，不会再出现「图例说青色、画布画绿色」的漂移。
import { ChevronDown } from 'lucide-react'
import { useSessionState } from '@/lib/session-state'
import {
  ACTIVE_MATCH_RING,
  BOUNDARY_COLORS,
  CANVAS,
  EDGE_COLORS,
  FACT_COLORS,
  GOAL_COLORS,
  HELPER_MARKER_COLOR,
  HOVER_RING,
  INTENT_COLORS,
  KEYBOARD_FOCUS_RING,
  MATCH_RING,
  ORIGIN_MARKER_COLOR,
  PROJECT_COLOR,
  REVIEW_RING,
  SELECTION_RING,
} from '@/graph/render'

const FACT_KINDS = ['vuln', 'cred', 'host', 'service', 'loot', 'artifact', 'note'] as const

/** 中文状态名 → render.ts 的意图状态色键。标签尽量短：两列宽度有限，
 *  详情放在 title 里，图例先保证「一眼扫得完」。 */
const INTENT_STATUSES: readonly [string, string, string?][] = [
  ['排队/认领/等待', 'pending', '排队、认领（claimed）和等待意图（waiting）都还没有执行，统一用琥珀色；等待意图不是停放，它会被唤醒。'],
  ['执行中', 'running'],
  ['复核中', 'reviewing', '复核中（reviewing）：复核者正在为这条结论跑一次独立复核，青色 + 缓慢呼吸框；产出边是青色虚线。必要前提仍是蓝色实线，两者不同色。'],
  ['失败', 'failed'],
  ['已停止', 'stopped', '账本里是 failed 或 pending 加 hold：被指挥官/操作员显式叫停，不会自动重派，要 retry 才恢复；不等于执行失败。'],
  ['复核未通过', 'review_failed', '复核未通过（判定 / 没跑完）：工作跑完了，但至少一条结论没能被证明；可原地重试。'],
  ['完成', 'done'],
  ['停放', 'parked'],
]

function Dot({ color }: { color: string }) {
  return <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: color }} aria-hidden="true" />
}

function Line({ color, style }: { color: string; style: 'solid' | 'dashed' | 'dotted' | 'double' }) {
  if (style === 'double') {
    return (
      <span className="flex h-3 w-4 shrink-0 flex-col justify-center gap-[2px]" aria-hidden="true">
        <span className="h-0.5 w-4 rounded-full" style={{ background: color }} />
        <span className="h-0.5 w-4 rounded-full" style={{ background: color }} />
      </span>
    )
  }
  return (
    <span
      className="h-0 w-4 shrink-0"
      style={{
        borderTopWidth: 2,
        borderTopStyle: style === 'dotted' ? 'dotted' : style === 'dashed' ? 'dashed' : 'solid',
        borderColor: color,
      }}
      aria-hidden="true"
    />
  )
}

/** A produced edge is not one colour: it follows the direction's status. A
 *  gradient swatch says that in the legend without pretending the line is
 *  always green or always grey. */
function StatusLine() {
  return (
    <span
      className="h-0.5 w-4 shrink-0 rounded-full"
      style={{
        background: `linear-gradient(90deg, ${INTENT_COLORS.pending}, ${INTENT_COLORS.running}, ${INTENT_COLORS.reviewing}, ${INTENT_COLORS.done})`,
      }}
      aria-hidden="true"
    />
  )
}

function Ring({ color, dashed = false, width = 1 }: { color: string; dashed?: boolean; width?: number }) {
  return (
    <span
      className="h-3 w-3 shrink-0 rounded-full"
      style={{ border: `${width}px ${dashed ? 'dashed' : 'solid'} ${color}` }}
      aria-hidden="true"
    />
  )
}

/** 当前搜索项：画布上是一圈实红环 + 一圈淡红外环。 */
function DoubleRing({ color }: { color: string }) {
  return (
    <span
      className="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full border"
      style={{ borderColor: color, opacity: 0.55 }}
      aria-hidden="true"
    >
      <span className="h-2 w-2 rounded-full border-2" style={{ borderColor: color }} />
    </span>
  )
}

/** 已否定/已撤回的事实：保留事实本身的颜色，但画一到叉。 */
function CrossedDot({ color }: { color: string }) {
  return (
    <span
      className="relative flex h-3 w-3 shrink-0 items-center justify-center rounded-full"
      style={{ background: color }}
      aria-hidden="true"
    >
      <span className="text-[9px] font-bold leading-none" style={{ color: CANVAS.background }}>
        ×
      </span>
    </span>
  )
}

/** 发起者第二通道：形状，不是颜色。与 render.ts 的 originMarker 一一对应。 */
function OriginGlyph({ shape }: { shape: 'diamond' | 'circle' | 'double-ring' }) {
  if (shape === 'diamond') {
    return (
      <span className="flex h-3.5 w-3.5 shrink-0 items-center justify-center" aria-hidden="true">
        <span className="h-2 w-2 rotate-45" style={{ background: ORIGIN_MARKER_COLOR }} />
      </span>
    )
  }
  if (shape === 'circle') {
    return (
      <span
        className="h-3 w-3 shrink-0 rounded-full border"
        style={{ borderColor: ORIGIN_MARKER_COLOR }}
        aria-hidden="true"
      />
    )
  }
  return (
    <span className="flex h-3.5 w-3.5 shrink-0 items-center justify-center" aria-hidden="true">
      <span
        className="flex h-3.5 w-3.5 items-center justify-center rounded-full border"
        style={{ borderColor: ORIGIN_MARKER_COLOR }}
      >
        <span
          className="h-1.5 w-1.5 rounded-full border"
          style={{ borderColor: ORIGIN_MARKER_COLOR }}
        />
      </span>
    </span>
  )
}

/** 助手卫星点：最多 3 颗 + “助×N”。 */
function HelperGlyph() {
  return (
    <span className="relative flex h-4 w-6 shrink-0 items-center" aria-hidden="true">
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          className="h-1 w-1 shrink-0 rounded-full"
          style={{ background: HELPER_MARKER_COLOR, marginLeft: i === 0 ? 0 : 2 }}
        />
      ))}
      <span className="ml-0.5 text-[9px] leading-none" style={{ color: HELPER_MARKER_COLOR }}>
        助×3
      </span>
    </span>
  )
}

/** A legend row reads as a quiet reference line, but still responds to hover. */
function Row({ children, title }: { children: React.ReactNode; title?: string }) {
  return (
    <span
      className="-mx-1 flex items-center gap-1.5 rounded px-1 py-0.5 text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink"
      title={title}
    >
      {children}
    </span>
  )
}

function GroupTitle({ children }: { children: React.ReactNode }) {
  return <div className="tw-eyebrow mb-1 mt-2 first:mt-0">{children}</div>
}

function Hint({ children }: { children: React.ReactNode }) {
  return <div className="mt-1.5 text-[11px] leading-relaxed text-ink-3">{children}</div>
}

export function GraphLegend({ className }: { className?: string }) {
  const [open, setOpen] = useSessionState('graph.legendOpen', false)

  return (
    // The card is a column: the title row (with the collapse control) is pinned,
    // the body is the only part that scrolls. The bottom-left stack bounds the
    // card's height (app.css `.tw-graph-stack`), so an expanded legend scrolls
    // inside the map instead of growing up under the page banner. `min-h-10` is
    // the title row: the control stays reachable even in the smallest box.
    // `shrink-0` keeps that promise: without it the legend is the only flexible
    // child in the stack, so an expanded 指挥官 card squeezed it into a thin strip
    // that scrolled a few pixels instead of the stack scrolling. The stack is the
    // scroll container; each card keeps its full height inside it.
    <div
      className={`tw-graph-dock flex min-h-10 w-full flex-col overflow-hidden rounded-xl border border-hairline bg-card/95 text-caption shadow-lifted backdrop-blur ${
        open ? 'min-h-40 flex-1' : 'shrink-0'
      } ${className ?? ''}`}
      data-testid="graph-legend"
    >
      <button
        type="button"
        onClick={() => setOpen(value => !value)}
        aria-expanded={open}
        className="flex min-h-10 w-full shrink-0 items-center gap-2 px-3 py-1.5 text-left transition-colors hover:bg-surface-2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
      >
        <ChevronDown
          className={`h-4 w-4 shrink-0 text-ink-3 transition-transform duration-150 ${open ? '' : '-rotate-90'}`}
          aria-hidden="true"
        />
        <span className="tw-section-title">图例</span>
      </button>

      {open && (
        <div className="tw-scroll tw-graph-legend-body min-h-0 flex-1 overflow-y-auto border-t border-hairline px-3 py-2.5">          <GroupTitle>事实（节点）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {FACT_KINDS.map((name) => (
              <Row key={name}>
                <Dot color={FACT_COLORS[name] ?? CANVAS.ink3} />
                {name}
              </Row>
            ))}
            <Row title="账本里出现图例之外的 fact.kind 时按中性灰绘制，不丢弃；筛选条只列上面七种常用类型">
              <Dot color={CANVAS.ink3} />
              其他类型
            </Row>
            <Row title="项目简报是图的起点">
              <span
                className="h-2 w-3.5 shrink-0 rounded-sm border"
                style={{ borderColor: PROJECT_COLOR }}
                aria-hidden="true"
              />
              项目/范围
            </Row>
            <Row title="目标不单独上图：达成后作为终点接到承载证据的事实上，带 ✓ 角标与缓慢呼吸光环；未达成时只在左下「终点」卡显示">
              <span
                className="h-2 w-3.5 shrink-0 rounded-sm border"
                style={{ borderColor: GOAL_COLORS.met }}
                aria-hidden="true"
              />
              目标（达成后）
            </Row>
            <Row title="事实本身仍在账本里，但状态是 rejected（已否定）或 withdrawn（已撤回）：画布保留事实色，再画一个 ×。与「引用边界」的虚环不是同一件事">
              <CrossedDot color={CANVAS.ink2} />
              否定/撤回
            </Row>
          </div>

          <GroupTitle>意图状态（节点 / 产出边）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {INTENT_STATUSES.map(([name, key, hint]) => (
              <Row key={key} title={hint}>
                <span
                  className="h-2 w-3.5 shrink-0 rounded-sm border"
                  style={{ borderColor: INTENT_COLORS[key] ?? CANVAS.ink3 }}
                  aria-hidden="true"
                />
                {name}
              </Row>
            ))}
            <Row title="还没有产出任何事实的意图：画布只画一个虚环（颜色仍跟随意图状态），产出事实后虚环消失">
              <Ring color={CANVAS.ink3} dashed />
              未产出（虚环）
            </Row>
          </div>
          <Hint>
            排队/认领/等待同色（尚未执行）；复核中为青色呼吸框，产出边青色虚线；失败与已停止不同：
            hold 是显式叫停，需重试恢复。
          </Hint>

          <GroupTitle>发起者（第二通道 · 形状）</GroupTitle>
          <div className="space-y-0.5">
            <Row title="created_by_participant_id = commander：指挥官发起，执行者执行">
              <OriginGlyph shape="diamond" />
              指挥官（实心菱形）
            </Row>
            <Row title="created_by_participant_id = pt_<意图>：由另一个执行者发起，双方是平级，不是上游意图">
              <OriginGlyph shape="circle" />
              执行者（空心圆）
            </Row>
            <Row title="created_by_participant_id = operator：操作员直接发起">
              <OriginGlyph shape="double-ring" />
              操作员（双环）
            </Row>
            <Row title="旧数据或无法分类的参与者：不画标记，避免把猜测写成发起者">
              <span className="flex h-3.5 w-3.5 shrink-0 items-center justify-center text-ink-3/70" aria-hidden="true">
                —
              </span>
              未记录（不画）
            </Row>
          </div>
          <Hint>
            状态色是第一通道，发起者只用形状区分，形状画在 pill 左上角；不画连线，免得把「谁发起」误读成因果。
          </Hint>

          <GroupTitle>选中的链路</GroupTitle>
          <div className="space-y-0.5">
            <Row title="选中节点后，画布用 1、2、3…… 标出主干顺序">
              <span
                className="tw-nums flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full border text-[9px] font-semibold"
                style={{ borderColor: CANVAS.info, color: CANVAS.ink }}
                aria-hidden="true"
              >
                1
              </span>
              画布上的主干顺序
            </Row>
            <Row title="主干之间的边加粗：整条链按顺序读下来，跨节点的线就是顺序本身">
              <span className="h-0.5 w-4 shrink-0 rounded" style={{ background: CANVAS.ink2 }} aria-hidden="true" />
              主干边（加粗）
            </Row>
            <Row title="一跳旁支：同一个事实还被哪些意图消费、这些意图又产出了什么；留在图上，但比主干暗一档">
              <Dot color={CANVAS.ink3} />
              旁支（次亮）
            </Row>
            <Row title="链路之外的节点只是压暗，不隐藏：全量闭包始终在图上">
              <Dot color={CANVAS.hairline} />
              其余（压暗但可见）
            </Row>
          </div>
          <Hint>
            链路是 DAG：主干回答「怎么走到这一步」，旁支回答「这个事实还能喂给谁」。
          </Hint>

          <GroupTitle>关系（边）</GroupTitle>
          <div className="space-y-0.5">
            <Row title="意图边（步骤 → 产出的每个事实）：颜色跟随意图状态；它表示这次工作产出了什么，不是前提">
              <StatusLine />
              产出（颜色跟随意图状态）
            </Row>
            <Row title="from_fact_ids：意图派发的硬门槛；蓝色实线是唯一「硬门槛」色。前提不可派发时会改成点线红（阻挡）">
              <Line color={EDGE_COLORS.premise} style="solid" />
              必要前提
            </Row>
            <Row title="不可复核/未核验/缺失的前提都会挡住派发（与准入同源规则）；点线红，复核通过后恢复蓝色实线">
              <Line color={EDGE_COLORS.premiseBlocked} style="dotted" />
              前提不满足（阻挡）
            </Row>
            <Row title="context_fact_ids：阅读选择，不是派发门槛；绿色虚线">
              <Line color={EDGE_COLORS.reference} style="dashed" />
              引用（绿色虚线）
            </Row>
            <Row title="pinned_fact_ids：明确固定要读的上下文，绿色实线双线；仍是阅读选择，不是门槛">
              <Line color={EDGE_COLORS.pin} style="double" />
              固定（绿色实线双线）
            </Row>
            <Row title="事实支持已达成目标所依据的证据（账本 supports 边）；金色细点线">
              <Line color={EDGE_COLORS.supports} style="dotted" />
              支持目标
            </Row>
            <Row title="旧卡片的盖章只记录了证据 id 时，由承载该证据的事实连到目标；朴素金色实线，与上面的依据边区分">
              <Line color={EDGE_COLORS.goal} style="solid" />
              目标证据（旧盖章）
            </Row>

            <Row title="项目简报到初始意图：图的起点，不是事实关系；灰色细线">
              <Line color={EDGE_COLORS.brief} style="solid" />
              项目范围
            </Row>
            <Row title="service 事实落到同名 host 事实上的分组线（宿主机归属）；灰色点线，不算因果">
              <Line color={EDGE_COLORS.host} style="dotted" />
              宿主机分组
            </Row>
          </div>
          <Hint>
            引用/固定是阅读选择，不是必须满足的前提；缺前提挡派发，缺引用不挡。引用、同实体与
            宿主机分组只表示「读到/属于」，不会被画成因果链。
          </Hint>

          <GroupTitle>画布状态（环，不改节点色）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            <Row title="点击选中的节点：白色实环；右侧详情面板解释内容">
              <Ring color={SELECTION_RING} width={2} />
              选中（白环）
            </Row>
            <Row title="指针停在节点或边上：灰色细环，仅作预览；点击才固定">
              <Ring color={HOVER_RING} />
              悬停（灰环）
            </Row>
            <Row title="画布方向键选中的节点：天蓝色环，可与选中/悬停同时出现">
              <Ring color={KEYBOARD_FOCUS_RING} width={2} />
              键盘焦点（蓝环）
            </Row>
            <Row title="搜索命中的节点：红色细环。命中不会被选中态的压暗藏起来">
              <Ring color={MATCH_RING} />
              搜索命中（红环）
            </Row>
            <Row title="搜索结果列表当前的这一项：实红环 + 淡红外环；↑↓ 只移动它，Enter 才定位">
              <DoubleRing color={ACTIVE_MATCH_RING} />
              当前搜索（双环）
            </Row>
            <Row title="事实的 claim_version 高于最新一次复核的版本：琥珀色虚线外环表示现在复核的是旧断言">
              <Ring color={REVIEW_RING} dashed />
              旧断言复核
            </Row>
            <Row title="该意图的 run 记录了非致命告警，悬停看详情">
              <span
                className="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full text-[11px] font-bold"
                style={{ background: CANVAS.warning, color: CANVAS.background }}
                aria-hidden="true"
              >
                !
              </span>
              警告数
            </Row>
            <Row title="步骤 pill 右上角：最多 3 颗卫星点 + “助×N”。原生 subagent 是父回合内的临时助手（active>0 时慢脉冲），不单独开 run、不计独立 usage、不产生事实；结论仍由本意图声明">
              <HelperGlyph />
              助手（临时）
            </Row>
          </div>

          <Hint>
            选中、悬停、搜索和键盘焦点只加环，不改节点颜色；方块第二行显示实际使用的模型。
            助手是本轮的临时调用记录，不是独立 run，也不代表产生了事实。
          </Hint>

          <GroupTitle>引用边界（虚环）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            <Row title="引用指向的记录不在当前快照（账本里仍然存在）。这不是孤立事实，也不是缺失；点开加载它的邻域">
              <Ring color={BOUNDARY_COLORS.not_loaded} dashed />
              未载入（可展开）
            </Row>
            <Row title="引用指向的 id 在账本里也不存在：点开不会变出内容">
              <Ring color={BOUNDARY_COLORS.missing} dashed />
              引用缺失
            </Row>
            <Row title="引用的事实被明确否定：仍然存在于账本，但不应作为当前依据">
              <Ring color={BOUNDARY_COLORS.rejected} dashed />
              已否定引用
            </Row>
            <Row title="引用的事实已被作者或操作员撤回：记录与证据仍在账本里，但不作为结论或前提">
              <Ring color={BOUNDARY_COLORS.withdrawn} dashed />
              已撤回引用
            </Row>
          </div>
          <Hint>
            引用边界是「指向的记录不在图上」，四种状态由后端 boundary.state 给出；
            只有「未载入」能点开加载邻域，缺失、已否定、已撤回都不会点出内容。
          </Hint>
          <Hint>悬停看详情 · 滚轮缩放 · 拖拽节点（位置保留，重新布局复位） · 右侧面板左缘可拖动调宽</Hint>
        </div>
      )}
    </div>
  )
}
