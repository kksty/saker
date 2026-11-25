// 图例：把画布上的颜色/线型翻译成人话。
//
// 文案必须与 render.ts 的绘制一致：颜色、线型、虚线环都是承诺，不是装饰。
// 因此这里不重新声明色值，而是直接 import render.ts 的绘制色表——改画笔时
// 图例自动跟着改，不会再出现「图例说青色、画布画绿色」的漂移。
import { ChevronDown } from 'lucide-react'
import { useSessionState } from '@/lib/session-state'
import {
  BOUNDARY_COLORS,
  CANVAS,
  EDGE_COLORS,
  FACT_COLORS,
  FOCUS_COLD_ALPHA,
  GOAL_COLORS,
  HELPER_MARKER_COLOR,
  INTENT_COLORS,
  KEYBOARD_FOCUS_RING,
  ORIGIN_MARKER_COLOR,
  PROJECT_COLOR,
  REVIEW_RING,
} from '@/graph/render'

const FACT_KINDS = ['vuln', 'cred', 'host', 'service', 'loot', 'artifact', 'note'] as const

/** 中文状态名 → render.ts 的意图状态色键。 */
const INTENT_STATUSES: readonly [string, string][] = [
  ['排队 / 认领 / 等待', 'pending'],
  ['执行中', 'running'],
  ['复核中', 'reviewing'],
  ['失败', 'failed'],
  ['已停止（显式叫停）', 'stopped'],
  ['复核未通过（判定 / 没跑完）', 'review_failed'],
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
      className={`tw-graph-dock flex min-h-10 w-full shrink-0 flex-col overflow-hidden rounded-xl border border-hairline bg-card/95 text-caption shadow-lifted backdrop-blur ${className ?? ''}`}
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
        <div className="tw-scroll max-h-64 min-h-0 flex-1 overflow-y-auto border-t border-hairline px-3 py-2.5">
          <GroupTitle>事实（节点）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {FACT_KINDS.map((name) => (
              <Row key={name}>
                <Dot color={FACT_COLORS[name] ?? CANVAS.ink3} />
                {name}
              </Row>
            ))}
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
          </div>

          <GroupTitle>意图状态（节点 / 产出边）</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            {INTENT_STATUSES.map(([name, key]) => (
              <Row
                key={key}
                title={
                  key === 'reviewing'
                    ? '青色 + 缓慢呼吸框：复核者正在为这条结论跑一次独立复核。产出边是青色虚线，和蓝色实线「必要前提」区分'
                    : key === 'pending'
                      ? '排队、认领（claimed）和等待意图（waiting）都还没有执行，统一用琥珀色；它们的产出边是琥珀虚线'
                      : key === 'stopped'
                        ? '账本里是 failed 或 pending 加 hold：被指挥官/操作员显式叫停，不会自动重派，要 retry 才恢复；不等于执行失败'
                        : undefined
                }
              >
                <span
                  className="h-2 w-3.5 shrink-0 rounded-sm border"
                  style={{ borderColor: INTENT_COLORS[key] ?? CANVAS.ink3 }}
                  aria-hidden="true"
                />
                {name}
              </Row>
            ))}
          </div>
          <Hint>
            排队、认领（claimed）、等待意图（waiting）都还没有执行，统一用琥珀色；等待意图不是停放，它会被唤醒。
          </Hint>
          <Hint>
            复核中（reviewing）：复核者正在为这条结论跑一次独立复核，青色 + 缓慢呼吸框；
            它给出判定后，事实状态才变成已复核或复核未通过。它的产出边是青色虚线；
            必要前提仍是蓝色实线，两者不再同色。
          </Hint>
          <Hint>
            失败与已停止分开看：失败是执行或判定本身没过（紫）；已停止是账本里带 hold 的显式叫停（玫瑰），
            包括派发前就被停掉的意图。已停止不会自动重派，需要显式重试才恢复。
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
            状态色仍是第一通道；发起者只用形状区分（第二通道），不改变任何状态配色。形状画在
            pill 左上角；执行者发起的平级意图会用下面的「发起线」连回发起者。
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
            链路是 DAG，不是一条线：主干只回答「怎么走到这一步」，旁支回答「这个事实还能喂给谁」。
            没有「只看主路径」这种隐藏节点的模式了。
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
            <Row title="parent_intent_id：上游意图委派出的下游意图，上游意图要等它结束；紫色虚线">
              <Line color={EDGE_COLORS.spawn} style="dashed" />
              委托（等待）
            </Row>
            <Row title="图内派生的发起关系：执行者发起的平级意图连回发起者。1px 虚线 + 开口箭头 + 低透明度；不是账本边，不参与布局、深度或力导">
              <Line color={EDGE_COLORS.origin} style="dashed" />
              发起线（平级发布，派生）
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

          <GroupTitle>标记</GroupTitle>
          <div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
            <Row title="事实的 claim_version 高于最新一次复核的版本：琥珀色虚线外环表示现在复核的是旧断言">
              <Ring color={REVIEW_RING} dashed />
              复核的是旧断言
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
            <Row title="画布方向键选中的节点：天蓝色环，可与选中/悬停同时出现">
              <Ring color={KEYBOARD_FOCUS_RING} width={2} />
              键盘焦点
            </Row>
            <Row title="引用边界：引用指向的记录不在当前快照（账本里仍然存在）。这不是孤立事实，也不是缺失；点开加载它的邻域">
              <Ring color={BOUNDARY_COLORS.not_loaded} dashed />
              未载入引用（可展开）
            </Row>
            <Row title="引用指向的 id 在账本里也不存在：点开不会变出内容">
              <Ring color={BOUNDARY_COLORS.missing} dashed />
              引用缺失
            </Row>
            <Row title="引用的事实被明确否定：仍然存在于账本，但不应作为当前依据">
              <Ring color={BOUNDARY_COLORS.rejected} dashed />
              已否定引用
            </Row>
            <Row title="步骤 pill 右上角：最多 3 颗卫星点 + “助×N”。原生 subagent 是父回合内的临时助手（active>0 时慢脉冲），不单独开 run、不计独立 usage、不产生事实；结论仍由本意图声明">
              <HelperGlyph />
              助手（原生 subagent，临时）
            </Row>
          </div>

          <Hint>
            方块第二行显示实际使用的模型；未运行时显示统一连接的模型。执行中和复核中的环表示当前状态。
            助手卫星点是本轮的临时调用记录，不是独立 run，也不代表产生了事实。
          </Hint>
          <Hint>悬停看详情 · 滚轮缩放 · 拖拽节点（位置保留，重新布局复位） · 右侧面板左缘可拖动调宽</Hint>
        </div>
      )}
    </div>
  )
}
