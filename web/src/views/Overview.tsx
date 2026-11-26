import { GoalEditorDialog } from '@/components/GoalEditorDialog'
import { useSessionState } from '@/lib/session-state'
import { SealGoalDialog } from '@/components/SealGoalDialog'
import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  Bot,
  ChevronDown,
  ChevronUp,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Flag,
  Inbox,
  Network,
  Play,
  Plus,
  ScrollText,
  Send,
  Server,
  ShieldCheck,
  Target,
  Terminal,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  ListRow,
  Meter,
  SkeletonCard,
  Spinner,
  StatTile,
  StatusIndicator,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel, pageGutter } from '@/components/Page'
import { ChatComposer } from '@/components/ChatComposer'
import { CreateGoalDialog } from '@/components/CreateGoalDialog'
import { RetryIntentDialog } from '@/components/RetryIntentDialog'
import { StopIntentButton } from '@/components/StopIntentButton'
import { StopIntentDialog, type StopTarget } from '@/components/StopIntentDialog'
import { api } from '@/lib/api'
import { OperatorInboxCard } from '@/components/OperatorInboxCard'
import { usageBreakdownView } from '@/lib/usage-breakdown'
import { PHASE_LABEL, isPhaseActive, phaseDetail, phaseOf } from '@/lib/decider'
import {
  fmtInt,
  fmtTime,
  fmtUptime,
  frontierMarkers,
  frontierPremiseChangedText,
  kindVariant,
  originVariant,
  runStatusLabel,
  statusKind,
  statusLabel,
} from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { AsyncState } from '@/lib/hooks'
import { projectStatusSummary } from '@/lib/project-status'
import type { BoardItem, BoardResponse, Goal, OperatorFrontierItem, OperatorInbox, ProjectInfo, Run } from '@/lib/types'

// A run quiet this long is worth surfacing on the dashboard. It is not the
// guard's reap threshold (that is --idle-timeout, shown in the panel): this is
// the "looks stuck" line, far earlier than the destructive one.
const QUIET_SECONDS = 300

/**
 * 概览 = 这个项目的作战台。
 *
 * Reading order, strongest → weakest (docs/UI · 视觉规范 v2):
 *   1. the page title and the single primary CTA (新建目标);
 *   2. the metrics that need action now (failed runs / directions to dispatch /
 *      blocked premises / running workers), then the quieter ledger totals;
 *   3. the foreground panels (goals, commander, dispatch, frontier, facts);
 *   4. reference material (knowledge, delivery/host diagnostics) collapsed.
 * The header carries only a quiet 11px meta line (project id, snapshot, live
 * phase, decision tokens) so the counters never compete with the actions.
 */
export function Overview({
  current,
  projects,
  board,
  inbox,
  onRefresh,
  onNavigate,
  onOpenNode,
  onLocateGuardrail,
  onOpenIntent,
  onOpenRun,
  onOpenAuthorization,
  onOpenGoal,
  refreshGoalsSeq,
}: {
  current: string
  projects: ProjectInfo[]
  board?: BoardResponse
  /** The shell's inbox poll: the badge, this card and the access page share one
   *  read. The page used to poll the same endpoint itself, so every period sent
   *  two requests and the two numbers could disagree for a moment. */
  inbox: AsyncState<OperatorInbox>
  onRefresh: () => void
  onNavigate?: (view: string) => void
  /** Cross-page link (docs/34 §4): select a graph node (`f:`/`s:`) on the graph
   *  page. Optional — without it the same rows stay plain text. */
  onOpenNode?: (graphNodeID: string) => void
  /** Cross-page link: open the run that owns this approval and point its event
   *  stream at the exact call. Optional — without it the row stays read-only. */
  onLocateGuardrail?: (target: { callID: string; sessionID: string }) => void
  /** Cross-page link: open one direction's own detail (`?view=state&intent=`). */
  onOpenIntent?: (intentID: string) => void
  /** Cross-page link: open one run's event stream (`?view=runs&run=`). */
  onOpenRun?: (runID: string) => void
  /** Cross-page link: open one authorization request (`?view=access&id=`). */
  onOpenAuthorization?: (requestID: string) => void
  /** Cross-page link: open one goal's receipt (`?view=state&goal=`). */
  onOpenGoal?: (goalID: string) => void
  /** Bumped by the shell after "追加目标并继续" succeeds: the goals poll reloads
   *  immediately instead of waiting for its next period. */
  refreshGoalsSeq?: number
}) {
  const goals = usePoll(api.goals, 15000)
  const runs = usePoll(api.runs, 10000)
  const nodes = usePoll(api.nodes, 30000)
  // One quiet all-in total in the header. The per-bucket split is not needed to
  // decide the next action; the run detail carries the usage of its own run.
  const usage = usePoll(api.usageBreakdown, 15000)

  // The append-goal dialog lives in the project shell; when it succeeds the
  // overview's own goals poll must pick the new goal up right away.
  const goalsSeqRef = useRef(refreshGoalsSeq)
  useEffect(() => {
    if (refreshGoalsSeq === undefined || goalsSeqRef.current === refreshGoalsSeq) return
    goalsSeqRef.current = refreshGoalsSeq
    goals.reload()
    // goals.reload is stable for this poll; only a new shell signal retriggers.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshGoalsSeq])

  const [newGoal, setNewGoal] = useState(false)
  const [editingGoal,setEditingGoal]=useState<Goal|null>(null)
  // Reference material, not the foreground: closed unless the operator opens it.
  const [diagnosticsOpen,setDiagnosticsOpen]=useSessionState('overview.diagnostics',false)
  const [metGoal, setMetGoal] = useState<Goal | null>(null)
  const [starting, setStarting] = useState(false)
  const [startNote, setStartNote] = useState('')
  // Sticky stop from a frontier row, and the retry entry it leaves behind.
  const [stopTarget, setStopTarget] = useState<StopTarget | null>(null)
  const [stopped, setStopped] = useState<StopTarget | null>(null)
  const [retryTarget, setRetryTarget] = useState<StopTarget | null>(null)

  const project = projects.find((p) => p.id === current)
  const decider = board?.decider
  const paused = Boolean(board?.project_paused || board?.decider.paused)
  const bound = (nodes.data?.nodes ?? []).filter((n) => n.bound)
  const defaultNode = nodes.data?.project_default ?? ''
  const runList = runs.data ?? []
  const openGoals = useMemo(
    () => (goals.data ?? []).toSorted((a, b) => Number(a.met) - Number(b.met)),
    [goals.data],
  )
  const fresh = Boolean(board) && board!.intents.total === 0 && board!.facts.total === 0 && openGoals.length === 0
  // Starting no longer needs a goal: the commander divides the brief into goals
  // itself (POST /api/goals). A bound local execution host is the only gate.
  const canStart = bound.length > 0

  const autorun = useRef(new URLSearchParams(window.location.search).get('autorun') === '1')

  async function startRound() {
    setStarting(true)
    setStartNote('')
    try {
      await api.decide('operator')
      setStartNote('已排队：指挥官会读 board 派发，去「图谱」或「运行」看进展。')
    } catch (e) {
      setStartNote(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
    }
  }

  // "创建并开始" lands here with ?autorun=1: one round, once, then the flag is
  // removed so a refresh does not dispatch again.
  useEffect(() => {
    if (!autorun.current) return
    autorun.current = false
    const params = new URLSearchParams(window.location.search)
    params.delete('autorun')
    const qs = params.toString()
    window.history.replaceState(null, '', `${window.location.pathname}${qs ? `?${qs}` : ''}`)
    void startRound()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function refreshAll() {
    goals.reload()
    runs.reload()
    nodes.reload()
    usage.reload()
    // The inbox is the shell's read now: onRefresh reloads it together with the
    // board, so reloading it here as well would send two requests.
    onRefresh()
  }

  const dataError = goals.error || runs.error || nodes.error
  const scope = project?.scope?.trim() || '未指定——按操作员当前有效指令与项目授权口径执行，执行者不自行扩大目标'

  return (
    <Page
      title="概览"
      // Tier 1: the one thing that changes every few seconds. It owns the slot
      // after the title, so the eye finds it before any counter.
      status={<LiveMeta board={board} />}
      // Tier 2: reference. One muted line beside the actions; the separators
      // are dots, not three hairlines, so it reads as one quiet group.
      meta={
        <>
          <span className="text-[11px] text-ink-3">
            快照 <span className="font-mono saker-nums">{board ? fmtTime(board.generated_at) : '—'}</span>
          </span>
          <MetaDot />
          {/* The two map layouts, where the operator decides to open the map:
              the tree is the working view, the force layout is kept for the one
              thing a tree cannot show. No verdict depends on either. */}
          <span
            className="text-[11px] text-ink-3"
            title="链路树是主视图：读一条依赖顺序用它。力导向保留只为看一眼整体形状、孤岛和离群点，节点一多就难读；砍掉它不丢任何结论，只丢这一眼。"
          >
            图谱 链路树为主
          </span>
          <MetaDot />
          <span className="text-[11px] text-ink-3" title="项目全部模型调用合计（含裁判），模型上报">
            Token 合计 <span className="font-mono saker-nums">{usageBreakdownView(usage.data).all.text}</span>
          </span>
        </>
      }
      actions={
        // The one daily action on this page. Refreshing is the board's own job
        // (5s poll), and creating a project belongs to the instance page.
        <Button size="sm" className="gap-1.5" onClick={() => setNewGoal(true)}>
          <Flag className="h-3.5 w-3.5" /> 新建目标
        </Button>
      }
      scroll={false}
    >
      <div className={cn('mx-auto flex h-full min-h-0 w-full max-w-[1680px] flex-col gap-5 overflow-y-auto overscroll-contain py-4 sm:py-5', pageGutter)}>
        {(startNote || stopped || board?.reviews || dataError) && (
          <div className="flex shrink-0 flex-col gap-3">
            {startNote && (
              <Callout tone="info">
                <span className="text-[12.5px] leading-relaxed">{startNote}</span>
              </Callout>
            )}
            {stopped && (
              <Callout tone="warning">
                <div className="flex flex-wrap items-center gap-2 text-[12px] leading-relaxed">
                  <span className="min-w-0">
                    已停止（人工）：{stopped.title}。下游意图已一并停止；恢复要用重试，不会自动重派。
                  </span>
                  <Button size="xs" variant="outline" onClick={() => setRetryTarget(stopped)}>
                    重试
                  </Button>
                  <Button size="xs" variant="ghost" onClick={() => setStopped(null)}>
                    关闭
                  </Button>
                </div>
              </Callout>
            )}
            {board?.reviews && (board.reviews.pending > 0 || board.reviews.processing > 0 || board.reviews.paused) && (
              <Callout tone={board.reviews.paused || board.reviews.disabled ? 'warning' : 'info'}>
                <span className="text-[12px] leading-relaxed">
                  复核中：<span className="font-mono saker-nums">{board.reviews.processing}</span> 项，
                  <span className="font-mono saker-nums">{board.reviews.pending}</span> 项待复核
                  {board.reviews.cooling > 0 ? `（${board.reviews.cooling} 项等待重试）` : ''}。
                  {board.reviews.disabled
                    ? '复核已关闭：不会再有复核 run，可在结论页开启。'
                    : board.reviews.paused
                      ? '复核已暂停，可在结论页恢复。'
                      : board.reviews.automatic
                        ? '队列会自动继续推进。'
                        : '自动复核未开启，可在结论页发起复核。'}
                </span>
              </Callout>
            )}
            {dataError && (
              <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                部分数据读取失败，当前保留上次成功结果：<span className="saker-break">{dataError}</span>
              </Callout>
            )}
          </div>
        )}

        {!board ? (
          // Shape the loading state like the page that replaces it: the metrics
          // strip, then the foreground split, then the collapsed diagnostics.
          <div className="space-y-5" aria-busy="true">
            <SkeletonCard />
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-12">
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-7" />
              <SkeletonCard className="xl:col-span-5" />
            </div>
          </div>
        ) : fresh ? (
          <div className="min-h-0 flex-1">
            <Onboarding
              project={project}
              scope={project?.scope ?? ''}
              bound={bound}
              defaultNode={defaultNode}
              deciderPaused={Boolean(decider?.paused)}
              canStart={canStart}
              starting={starting}
              onStart={startRound}
              onNewGoal={() => setNewGoal(true)}
              onNavigate={onNavigate}
            />
          </div>
        ) : (
          <>
            {/* The first screen answers, in this order: what the engagement is
                doing (项目状态), what waits on the operator (待你处理), how far
                the goals are (目标进度), and why work is not moving (阻塞原因).
                Ledger totals that no action depends on live behind the panels
                and the 诊断 expander instead of competing here. */}
            <ProjectStatusSection board={board} project={project} onNavigate={onNavigate} />

            <PendingSection
              inbox={inbox}
              onNavigate={onNavigate}
              onLocateGuardrail={onLocateGuardrail}
              onOpenIntent={onOpenIntent}
              onOpenRun={onOpenRun}
              onOpenAuthorization={onOpenAuthorization}
              onOpenGoal={onOpenGoal}
            />

            <div className="grid shrink-0 items-start gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
              {/* 目标进度 and 阻塞原因 pair up from 1024px so all four first-screen
                  blocks fit one row; below that they stack in the same order. */}
              <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                <GoalsPanel
                  goals={openGoals}
                  paused={paused}
                  onEdit={setEditingGoal}
                  onNew={() => setNewGoal(true)}
                  onPick={setMetGoal}
                  onOpenNode={onOpenNode}
                  className="max-h-[28rem]"
                />
                <FrontierPanel
                  items={board.operator_frontier ?? board.frontier ?? []}
                  waiting={board.waiting_intents ?? []}
                  onOpen={() => onNavigate?.('graph')}
                  onOpenNode={onOpenNode}
                  onStop={setStopTarget}
                  className="max-h-[28rem]"
                />
              </div>
              <div className="grid min-w-0 gap-4">
                <Panel
                  title={
                    <span className="flex items-center gap-1.5">
                      <Send className="h-3.5 w-3.5 text-ink-3" /> 下发
                    </span>
                  }
                  bodyClassName="p-0"
                >
                  <ChatComposer paused={paused} runningExecutors={board.runs.running} onSent={refreshAll} />
                </Panel>
                <FactsDisclosure
                  items={board.recent_facts ?? []}
                  onOpen={() => onNavigate?.('graph')}
                  onOpenNode={onOpenNode}
                />
              </div>
            </div>

            {/* Reference material, not the console's foreground: what the next
                round will be told, delivery/handoff warnings, per-run/model/host
                details and the guard's sweep. Collapsed unless the operator asks. */}
            <details open={diagnosticsOpen} onToggle={e=>setDiagnosticsOpen(e.currentTarget.open)} className="saker-panel shrink-0 rounded-xl [&[open]>summary>svg]:rotate-180">
              <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                <ScrollText className="h-4 w-4 shrink-0 text-ink-3" />
                <span className="saker-section-title">运行与投递诊断</span>
                <span className="hidden text-[11px] text-ink-3 sm:inline">交接告警 · 模型 / 宿主 / 投递积压</span>
                <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-ink-3 transition-transform" />
              </summary>
              <div className="space-y-4 border-t border-hairline p-4">
                <details className="saker-inset rounded-lg" data-testid="overview-brief">
                  <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-[12px] font-medium text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden [&[open]>svg]:rotate-180">
                    <ScrollText className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" /> 项目简报
                    <span className="ml-auto text-[11px] font-normal text-ink-3">当前口径，可动态补充</span>
                  </summary>
                  <p className="saker-break max-h-48 overflow-y-auto whitespace-pre-wrap px-3 pb-3 text-[12.5px] leading-relaxed text-ink-2">
                    {scope}
                  </p>
                </details>
                <BriefingBar board={board} />
                <div className="flex flex-wrap gap-3 text-xs text-ink-3">
                  <span>指挥官上下文 {decider?.context_percent ?? '—'}%</span>
                  <span>交接未回执 {decider?.handoff_missing ?? 0} 轮{decider?.handoff_released ? '（已释放压力）' : ''}</span>
                  <span>非交接轮无动作 {decider?.no_action_rounds ?? 0} 轮</span>
                  <span>待复核 {board.facts.reviewable_unverified ?? 0} · 笔记 {board.facts.notes ?? 0}</span>
                </div>
                {(board.handoff_warnings?.length ?? 0) > 0 && (
                  <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                    <div className="space-y-1">
                      <div className="text-[12.5px] font-medium text-ink">
                        最近 <span className="font-mono saker-nums">{board.handoff_warnings!.length}</span> 个历史 run 有非致命告警（已带入下一轮提示词）
                      </div>
                      {board.handoff_warnings!.slice(0, 3).map((w) => (
                        <div key={w.run} className="saker-break text-[11px] leading-relaxed text-ink-2">
                          <span className="font-mono">{w.run}</span>
                          {w.intent_id ? ` · ${w.intent_id}` : ''}：{w.warnings[0] ?? ''}
                          {w.warnings.length > 1 ? `（共 ${w.warnings.length} 条）` : ''}
                        </div>
                      ))}
                      {board.handoff_warnings!.length > 3 && (
                        <div className="text-[11px] text-ink-3">…更多见「运行」页每条 run 的告警列表</div>
                      )}
                    </div>
                  </Callout>
                )}
                {(board.ingest_pending?.length ?? 0) > 0 && (
                  <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                    <div className="space-y-1">
                      <div className="text-[12.5px] font-medium text-ink">
                        最近 <span className="font-mono saker-nums">{board.ingest_pending!.length}</span> 个 run 已结束但交接入账未完成（修复不重跑执行者）
                      </div>
                      {board.ingest_pending!.slice(0, 3).map((w) => (
                        <div key={w.run} className="saker-break text-[11px] leading-relaxed text-ink-2">
                          <span className="font-mono">{w.run}</span>
                          {w.intent_id ? ` · ${w.intent_id}` : ''}：已入账 {w.persisted}/{w.declared} 条
                          {w.pending?.[0]
                            ? `；待补 ${w.pending[0].declaration_id || w.pending[0].title || '未命名声明'}${w.pending[0].location ? ` @ ${w.pending[0].location}` : ''}`
                            : w.reasons?.[0]
                              ? `；${w.reasons[0]}`
                              : ''}
                        </div>
                      ))}
                      {board.ingest_pending!.length > 3 && (
                        <div className="text-[11px] text-ink-3">…更多见「运行」页每条 run 的交接入账状态</div>
                      )}
                    </div>
                  </Callout>
                )}
                <ExecutorPanel
                  board={board}
                  runs={runList}
                  bound={bound}
                  defaultNode={defaultNode}
                  onNavigate={onNavigate}
                  className="h-auto"
                />
                <GuardPanel board={board} runs={runList} onNavigate={onNavigate} className="h-auto" active={diagnosticsOpen} />
              </div>
            </details>
          </>
        )}
      </div>

      <GoalEditorDialog goal={editingGoal} onClose={()=>setEditingGoal(null)} onSaved={refreshAll} />
      <CreateGoalDialog open={newGoal} onOpenChange={setNewGoal} onCreated={refreshAll} />

      <SealGoalDialog goal={metGoal} open={!!metGoal} paused={paused}
        onOpenChange={(next) => { if (!next) setMetGoal(null) }}
        onSealed={refreshAll} />

      <StopIntentDialog
        target={stopTarget}
        onOpenChange={(open) => { if (!open) setStopTarget(null) }}
        onStopped={(_result, target) => {
          setStopped(target)
          refreshAll()
        }}
      />
      <RetryIntentDialog
        open={!!retryTarget}
        intentId={retryTarget?.id ?? ''}
        onOpenChange={(open) => { if (!open) setRetryTarget(null) }}
        onRetried={() => {
          setRetryTarget(null)
          setStopped(null)
          refreshAll()
        }}
      />
    </Page>
  )
}

// ---- header + metric primitives -------------------------------------------

function MetaDot() {
  return <span aria-hidden="true" className="text-[11px] text-ink-3/40">·</span>
}

/**
 * The live phase, in the header's one fixed status slot.
 *
 * It is the only header item that changes every few seconds, so it gets a chip
 * of its own: a semantic dot plus a 12px label in ink-2. The reference line
 * (snapshot / layout / tokens) stays at 11px ink-3, one step quieter, and
 * nothing here moves with the page's content.
 */
function LiveMeta({ board }: { board?: BoardResponse }) {
  const phase = phaseOf(board?.decider)
  const live = isPhaseActive(phase)
  const label = live ? PHASE_LABEL[phase] ?? phase : phase === 'paused' ? '已暂停' : phase === 'blocked' ? '未就绪' : '空闲'
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-hairline bg-surface-1/70 px-2 py-0.5 text-[12px] leading-5 text-ink-2">
      <span
        className={cn(
          'h-1.5 w-1.5 shrink-0 rounded-full',
          live
            ? 'motion-safe:animate-pulse bg-info'
            : phase === 'paused'
              ? 'bg-warning'
              : phase === 'blocked'
                ? 'bg-caution'
                : 'bg-ink-3/50',
        )}
      />
      {live ? '实时 · ' : ''}
      {label}
    </span>
  )
}

/**
 * Metrics in two weights: the first group is what the operator may need to act
 * on (failed runs, directions waiting to be dispatched, blocked premises,
 * running workers) at the 26px metric size; the second is the quiet ledger.
 */
/** A KeyMetrics count. Plain text: a metric that rolls to its new value is motion
 *  for decoration, and the polled number must be readable the moment it arrives. */
function MetricCount({ value }: { value: number }) {
  return <>{fmtInt(value)}</>
}

function KeyMetrics({
  board,
  project,
  onNavigate,
}: {
  board: BoardResponse
  project?: ProjectInfo
  onNavigate?: (view: string) => void
}) {
  const failed = board.runs.failed
  const pending = Math.max(0, board.intents.pending - (board.intents.held ?? 0))
  const blocked = (board.operator_frontier ?? board.frontier ?? []).filter((f) => !f.ready).length
  const limit = project?.max_runs_effective
  return (
    <section className="saker-panel shrink-0 overflow-hidden rounded-xl" aria-label="关键指标">
      <div className="saker-metric-grid grid-cols-2 sm:grid-cols-3">

        <StatTile
          size="lg"
          label="待派意图"
          icon={<CircleDashed aria-hidden="true" className="size-3.5" />}
          value={<MetricCount value={pending} />}
          tone={pending > 0 ? 'warning' : 'muted'}
          hint={pending > 0 ? '指挥官将派发' : '没有排队意图'}
        />
        <StatTile
          size="lg"
          label="被挡意图"
          icon={<Target aria-hidden="true" className="size-3.5" />}
          value={<MetricCount value={blocked} />}
          tone={blocked > 0 ? 'caution' : 'muted'}
          hint={blocked > 0 ? '详见阻塞原因' : '当前列表无阻塞'}
        />
        <StatTile
          size="lg"
          label="在跑执行"
          icon={<Activity aria-hidden="true" className={cn('size-3.5', board.runs.running > 0 && 'text-info')} />}
          value={
            <>
              <MetricCount value={board.runs.running} />/{limit ? <MetricCount value={limit} /> : '∞'}
            </>
          }
          tone={board.runs.running > 0 ? 'default' : 'muted'}
          hint="当前 / 并发上限"
        />
      </div>
      {/* The commander's full panel moved to 决策; this line is enough to
          answer "is it alive, and did the last round fail?" */}
      <CommanderStrip board={board} onNavigate={onNavigate} />
    </section>
  )
}

// ---- first screen: state → pending → goals → blockers --------------------

/**
 * The engagement's state in one block: what it is doing, the backend's reason
 * for a pause, how far the goals are, and one sentence on what happens next.
 *
 * It reads the board and never rules on 「已收口」: that verdict depends on
 * pending directions, unread messages and a running closeout, so it belongs to
 * the backend's action contract (available_actions), not to paused+goal counts
 * recomputed here. The counters below the line are the ones that name a next
 * action; ledger totals live on the pages that own them.
 */
function ProjectStatusSection({
  board,
  project,
  onNavigate,
}: {
  board: BoardResponse
  project?: ProjectInfo
  onNavigate?: (view: string) => void
}) {
  const status = projectStatusSummary(board)!
  const allMet = status.goalsTotal > 0 && status.goalsMet >= status.goalsTotal
  return (
    <section aria-label="项目状态" data-testid="overview-status" className="flex shrink-0 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 className="saker-section-title flex items-center gap-1.5">
          <Activity className="h-4 w-4 shrink-0 text-ink-3" aria-hidden="true" /> 项目状态
        </h2>
        <Badge size="sm" variant={status.paused ? 'warning' : 'success'}>
          {status.paused ? '暂停中' : '运行中'}
        </Badge>
        {status.reason && (
          <span className="min-w-0 max-w-full truncate text-[11px] text-ink-3" title={status.reason}>
            {status.reason}
          </span>
        )}
        {board.runs.failed > 0 && (
          <button
            type="button"
            onClick={() => onNavigate?.('runs')}
            className="inline-flex shrink-0 items-center gap-1 rounded-full border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive transition-colors hover:bg-destructive/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            title="去运行页查看失败的 run"
            data-testid="overview-failed-runs"
          >
            <CircleAlert className="h-3 w-3" aria-hidden="true" />
            失败运行 <span className="font-mono saker-nums">{fmtInt(board.runs.failed)}</span>
          </button>
        )}
        <span className="ml-auto flex items-center gap-2 text-[11px] text-ink-3">
          目标进度
          <span className="font-mono saker-nums text-[12px] font-semibold text-ink-2">
            {status.goalsMet}/{status.goalsTotal}
          </span>
          <Meter
            value={status.goalsMet}
            max={Math.max(1, status.goalsTotal)}
            tone={allMet ? 'success' : 'primary'}
            className="h-1 w-16"
            aria-label={`目标进度 ${status.goalsMet}/${status.goalsTotal}`}
          />
        </span>
      </div>
      {status.notes.length > 0 ? (
        <ul className="space-y-1" data-testid="overview-status-notes">
          {status.notes.map((note) => (
            <li key={note.code} className="text-[12px] leading-relaxed text-ink-2">
              {note.text}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12px] leading-relaxed text-ink-2" data-testid="overview-status-next">
          {status.nextStep}
        </p>
      )}
      <KeyMetrics board={board} project={project} onNavigate={onNavigate} />
    </section>
  )
}

/** Tool approval is one inbox category, and its unknown states must not read as
 *  "zero pending": 尚未查询 is not 没有待审批, and 不可用 is a configuration
 *  state rather than an error. The label comes from the summary category, the
 *  single read the badge and the detail list also use. */
function guardrailNote(inbox: OperatorInbox): string {
  const category = inbox.summary?.categories.find((entry) => entry.kind === 'guardrail')
  if (!category) return '工具审批：本次读取未返回状态'
  switch (category.state) {
    case 'never_checked':
      return '工具审批：尚未查询'
    case 'unavailable':
      return `工具审批：不可用${category.reason ? `（${category.reason}）` : ''}`
    case 'ok':
      return `工具审批：${category.count} 项待审批`
    default:
      return '工具审批：没有待审批'
  }
}

/**
 * 「待你处理」 as its own block, in one of four honest states: rows to handle,
 * nothing to handle, not readable, or not read yet. An inbox that fails must not
 * disappear — a vanished block reads as "no work", which is the one wrong
 * conclusion an operator could draw from it.
 */
function PendingSection({
  inbox,
  onNavigate,
  onLocateGuardrail,
  onOpenIntent,
  onOpenRun,
  onOpenAuthorization,
  onOpenGoal,
}: {
  inbox: AsyncState<OperatorInbox>
  onNavigate?: (view: string) => void
  onLocateGuardrail?: (target: { callID: string; sessionID: string }) => void
  onOpenIntent?: (intentID: string) => void
  onOpenRun?: (runID: string) => void
  onOpenAuthorization?: (requestID: string) => void
  onOpenGoal?: (goalID: string) => void
}) {
  const data = inbox.data
  // The card's own row count and the badge's number come from the same read:
  // `summary.total` when the backend sent one, otherwise the legacy `total`.
  const actionable = (data?.summary?.total ?? data?.total ?? 0) > 0 || Boolean(data?.guardrail)
  return (
    <section aria-label="待你处理" data-testid="overview-pending" className="flex shrink-0 flex-col gap-2">
      {inbox.error && (
        <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
          <span className="text-[12px] leading-relaxed" data-testid="overview-pending-error">
            待办读取失败：<span className="saker-break">{inbox.error}</span>
            {data ? '；下面是上次读到的结果，可能已经过期。' : '；还没有成功读到过待办，这不等于没有待办。'}
          </span>
        </Callout>
      )}
      {data && actionable && (
        <OperatorInboxCard
          inbox={data}
          onNavigate={(view) => onNavigate?.(view)}
          onChanged={inbox.reload}
          onLocateGuardrail={onLocateGuardrail}
          onOpenIntent={onOpenIntent}
          onOpenRun={onOpenRun}
          onOpenAuthorization={onOpenAuthorization}
          onOpenGoal={onOpenGoal}
        />
      )}
      {data && !actionable && !inbox.error && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-hairline bg-surface-2/30 px-4 py-2.5">
          <Inbox className="h-4 w-4 shrink-0 text-ink-3" aria-hidden="true" />
          <span className="text-[12px] font-medium text-ink-2">待你处理</span>
          <span className="text-[12px] text-ink-3">
            没有待办：授权请求、交接、失效的验收回执和运行告警都是空的。
          </span>
          <span className="ml-auto text-[11px] text-ink-3">{guardrailNote(data)}</span>
        </div>
      )}
      {!data && inbox.loading && <SkeletonCard />}
    </section>
  )
}

// ---- role panels ---------------------------------------------------------

/** A panel-header title: 13px/600 (via .saker-section-title), quiet glyph. */
function PanelTitle({ icon: Icon, children, glyphClass }: { icon: typeof Bot; children: React.ReactNode; glyphClass?: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <Icon className={cn('h-3.5 w-3.5 text-ink-3', glyphClass)} />
      {children}
    </span>
  )
}

/**
 * 指挥官 in one line: phase, what it is doing now, rounds, last error. The full
 * detail (attention queue, last actions, notes, recent decisions, state.md) is
 * one click away on 决策/记忆 and is not what the overview is for.
 */
function CommanderStrip({ board, onNavigate }: { board: BoardResponse; onNavigate?: (view: string) => void }) {
  const dec = board.decider
  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const now = useNow(active)
  const since = dec?.activity?.since ? new Date(dec.activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const detail = phaseDetail(dec)
  const paused = phase === 'paused'
  const blocked = phase === 'blocked'
  const dot = active ? 'motion-safe:animate-pulse bg-info' : paused ? 'bg-warning' : blocked ? 'bg-caution' : 'bg-ink-3/50'

  return (
    <div
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-hairline px-4 py-2.5"
      role="group"
      aria-label="指挥官状态"
    >
      <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dot)} aria-hidden="true" />
      <span className="shrink-0 text-[12px] font-medium text-ink">指挥官</span>
      <span className="shrink-0 text-[11px] text-ink-3">{PHASE_LABEL[phase] ?? phase}</span>
      {!dec?.enabled ? (
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2">
          未启用：没有配置模型端点 / Key，决策循环不会跑
        </span>
      ) : (
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2" title={detail}>{detail}</span>
      )}
      {active && since > 0 && <span className="shrink-0 font-mono text-[11px] saker-nums text-ink-3">{elapsed}s</span>}
      {dec?.enabled && (
        <span className="shrink-0 font-mono text-[11px] saker-nums text-ink-3">{fmtInt(dec.rounds)} 轮</span>
      )}
      {dec?.last_error && (
        <span className="w-full min-w-0 truncate text-[11px] text-destructive" title={dec.last_error}>
          上一轮出错：{dec.last_error}
        </span>
      )}
      <Button size="xs" variant="ghost" className="ml-auto shrink-0" onClick={() => onNavigate?.('decisions')}>
        决策
      </Button>
    </div>
  )
}

function ExecutorPanel({
  board,
  runs,
  bound,
  defaultNode,
  onNavigate,
  className,
}: {
  board: BoardResponse
  runs: Run[]
  bound: { id: string; transport?: string; endpoint?: string; bin?: string }[]
  defaultNode: string
  onNavigate?: (view: string) => void
  className?: string
}) {
  const running = board.running_intents ?? []
  const recent = useMemo(
    () => runs.toSorted((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at)).slice(0, 5),
    [runs],
  )

  return (
    <Panel
      title={<PanelTitle icon={Terminal}>执行者</PanelTitle>}
      extra={
        <div className="flex items-center gap-1.5">
          <Badge size="sm" variant={running.length > 0 ? 'primary' : 'muted'}>
            {running.length} 在跑
          </Badge>
          {board.intents.pending > 0 && (
            <Badge size="sm" variant="warning">
              {board.intents.pending} 排队
            </Badge>
          )}
        </div>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      {running.length === 0 ? (
        // When there is no run at all, the single empty state lives under
        // 最近运行 (below); this slot only explains an idle executor while the
        // recent-runs table still has history to show.
        recent.length === 0 ? null : (
          <EmptyState
            compact
            icon={Activity}
            title="没有在跑的 run"
            description="指挥官派发意图后，这里显示每个 run 的脉搏与上下文压力。"
            action={
              <Button size="xs" variant="outline" onClick={() => onNavigate?.('runs')}>
                查看运行
              </Button>
            }
          />
        )
      ) : (
        <div className="space-y-2">
          {running.map((it) => (
            <RunningRow key={it.id} item={it} />
          ))}
        </div>
      )}

      <SubSection
        label="最近运行"
        extra={
          <button
            type="button"
            className="rounded px-1 text-[11px] font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            onClick={() => onNavigate?.('runs')}
          >
            全部
          </button>
        }
      >
        {recent.length === 0 ? (
          <EmptyState
            compact
            icon={Activity}
            title="还没有运行记录"
            description="派发第一条意图后，这里显示最近 5 条 run 的状态、节点与 token。"
            action={
              <Button size="xs" variant="outline" onClick={() => onNavigate?.('runs')}>
                查看运行
              </Button>
            }
          />
        ) : (
          <div className="@container">
            <table className="w-full text-left text-[11px]">
              <caption className="sr-only">最近运行：run、状态、节点、token</caption>
              <thead className="text-ink-3">
                <tr>
                  <th scope="col" className="py-1 pr-2 font-medium">run</th>
                  <th scope="col" className="py-1 pr-2 font-medium">状态</th>
                  <th scope="col" className="hidden py-1 pr-2 font-medium @sm:table-cell">节点</th>
                  <th scope="col" className="py-1 text-right font-medium">tok</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-hairline/70">
                {recent.map((r) => (
                  <tr key={r.id} className="saker-hover-row">
                    <th scope="row" className="py-1.5 pr-2 text-left font-mono saker-nums font-normal text-ink-3" title={r.id}>
                      {r.id.slice(-6)}
                      <span className="ml-1 text-ink-3/70">
                        {r.purpose === 'verifier' ? '复' : r.purpose === 'report' ? '报' : ''}
                      </span>
                    </th>
                    <td className="py-1.5 pr-2">
                      <StatusIndicator compact status={statusKind(r.status)} label={runStatusLabel(r)} />
                    </td>
                    <td className="hidden py-1.5 pr-2 font-mono text-ink-3 @sm:table-cell">{r.node || '—'}</td>
                    <td className="py-1.5 text-right font-mono saker-nums text-ink-2">{r.tokens ? fmtInt(r.tokens) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </SubSection>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-3">
        <Badge size="sm" variant="success" className="font-mono saker-nums">
          完成 {fmtInt(board.runs.done)}
        </Badge>
        <Badge size="sm" variant="danger" className="font-mono saker-nums">
          失败 {fmtInt(board.runs.failed)}
        </Badge>
        <Badge size="sm" variant="muted" className="font-mono saker-nums">
          取消 {fmtInt(board.runs.canceled)}
        </Badge>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-3">
        <span className="text-[11px] text-ink-3">节点</span>
        {bound.length === 0 ? (
          <Badge size="sm" variant="warning">
            未绑定
          </Badge>
        ) : (
          bound.map((n) => (
            <Badge
              key={n.id}
              size="sm"
              variant={n.id === defaultNode ? 'primary' : 'secondary'}
              className="font-mono"
              title={n.transport === 'aop' ? n.endpoint : n.bin}
            >
              {n.id}
              {n.id === defaultNode ? ' · 首选' : ''}
            </Badge>
          ))
        )}
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => onNavigate?.('nodes')}>
          <Server className="h-3.5 w-3.5" /> 管理
        </Button>
      </div>
    </Panel>
  )
}

function RunningRow({ item }: { item: BoardItem }) {
  const idle = item.pulse?.idle_seconds ?? 0
  const events = item.pulse?.events ?? 0
  const ctx = item.pulse?.context_tokens ?? 0
  const win = item.pulse?.context_window ?? 0
  const hot = idle >= QUIET_SECONDS
  const pressure = win > 0 ? ctx / win : 0
  return (
    <div className="saker-inset rounded-lg px-2.5 py-2">
      <div className="flex items-center gap-2">
        <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', hot ? 'bg-warning' : 'bg-success')} />
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink" title={item.title}>
          {item.title}
        </span>
        <span className={cn('shrink-0 font-mono text-[11px] saker-nums', hot ? 'text-warning' : 'text-ink-3')}>
          {fmtIdle(idle)} · {fmtInt(events)} ev
        </span>
        {(item.pulse?.tool_failures ?? 0) > 0 && (
          <Badge size="sm" variant="warning" className="font-mono saker-nums" title="本轮结构化失败工具结果的累计数；不代表运行失败">
            工具失败 {fmtInt(item.pulse?.tool_failures)}
          </Badge>
        )}
      </div>
      {win > 0 && (
        <div className="mt-1.5 flex items-center gap-2">
          <Meter
            value={ctx}
            max={win}
            tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
            className="h-1"
            aria-label="这一轮的上下文压力"
          />
          <span className="shrink-0 font-mono text-[11px] saker-nums text-ink-3">{Math.round(pressure * 100)}%</span>
        </div>
      )}
    </div>
  )
}

function GuardPanel({
  board,
  runs,
  onNavigate,
  className,
  active = true,
}: {
  board: BoardResponse
  runs: Run[]
  onNavigate?: (view: string) => void
  className?: string
  /** The diagnostics expander is collapsed: a 1 Hz clock under it only burns
   *  renders. The panel keeps its last numbers instead of ticking them. */
  active?: boolean
}) {
  const now = useNow(active)
  const dec = board.decider
  const guard = board.guard
  const interval = guard?.interval_seconds ?? 30
  const lastTick = guard?.last_tick_at ? Math.max(0, Math.round((now - new Date(guard.last_tick_at).getTime()) / 1000)) : null
  const freshTick = lastTick !== null && lastTick <= interval * 3
  const loopStatus = !dec?.enabled ? 'error' : dec?.paused ? 'warning' : 'success'
  const loopLabel = !dec?.enabled ? '未启用' : dec?.paused ? '已暂停' : '运行中'
  const lastRoundAt = dec?.last_round_at
    ? Math.max(0, Math.round((now - new Date(dec.last_round_at).getTime()) / 1000))
    : null
  // Three full passes over the run list per render; the clock made that happen
  // once a second. The list only changes when polling delivers a new one.
  const { quiet, recovered, recoveries, failed, stalled } = useMemo(() => {
    const watchdogRuns = runs.filter((r) => r.cancel_cause === 'watchdog')
    const sorted = [...watchdogRuns].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
    return {
      quiet: (board.running_intents ?? []).filter((it) => (it.pulse?.idle_seconds ?? 0) >= QUIET_SECONDS),
      recovered: sorted.slice(0, 3),
      recoveries: watchdogRuns.length,
      failed: runs.filter((r) => r.status === 'failed').length,
      stalled: dec?.stalled ?? 0,
    }
  }, [board.running_intents, runs, dec?.stalled])

  return (
    <Panel
      title={<PanelTitle icon={ShieldCheck}>看门狗</PanelTitle>}
      extra={
        <div className="flex items-center gap-1.5">
          <StatusIndicator compact status={loopStatus} label={loopLabel} />
          <StatusIndicator compact status={freshTick ? 'success' : 'idle'} label={freshTick ? '巡检中' : '待巡检'} />
        </div>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      <div className="saker-eyebrow">指挥循环</div>
      <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
        <Row label="循环" value={loopLabel} />
        <Row label="心跳" value={dec?.heartbeat_seconds ? fmtSeconds(dec.heartbeat_seconds) : '—'} />
        <Row label="上次决策" value={lastRoundAt === null ? '—' : `${fmtIdle(lastRoundAt)} 前`} />
        <Row label="上次唤醒" value={dec?.last_wake || '—'} />
        <Row label="已运行" value={fmtUptime(dec?.uptime_seconds)} />
        <Row label="轮次 / 无新事实" value={`${fmtInt(dec?.rounds ?? 0)} / ${fmtInt(stalled)}`} />
      </div>

      <div className="mt-3 border-t border-hairline pt-3">
        <div className="saker-eyebrow">巡检 · 重派</div>
        <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
          <Row label="巡检周期" value={`${interval}s`} />
          <Row label="静默阈值" value={guard?.idle_timeout_seconds ? fmtSeconds(guard.idle_timeout_seconds) : '关闭'} />
          <Row label="上次巡检" value={lastTick === null ? '—' : `${lastTick}s 前`} />
          <Row label="自动重派" value={fmtInt(recoveries)} />
          <Row label="失败运行" value={fmtInt(failed)} />
          <Row label="静默 >5m" value={fmtInt(quiet.length)} />
        </div>
      </div>

      {quiet.length > 0 && (
        <Callout tone="warning" className="mt-3" icon={<CircleAlert className="h-4 w-4" />}>
          <div className="text-[12px] font-semibold text-ink">有 run 长时间没有事件</div>
          <div className="mt-1 space-y-0.5">
            {quiet.slice(0, 2).map((it) => (
              <div key={it.id} className="truncate text-[11px]">
                {it.title} · 静默 {fmtIdle(it.pulse?.idle_seconds ?? 0)}
              </div>
            ))}
          </div>
        </Callout>
      )}

      {recovered.length > 0 && (
        <SubSection label="最近自动重派">
          <div className="divide-y divide-hairline/70">
            {recovered.map((r) => (
              <div key={r.id} className="flex items-center gap-2 py-1.5 text-[11px]">
                <span className="shrink-0 font-mono saker-nums text-ink-3">{r.id.slice(-6)}</span>
                <span className="min-w-0 flex-1 truncate text-ink-2" title={r.exit_reason}>
                  {r.exit_reason || '运行守卫超时'}
                </span>
              </div>
            ))}
          </div>
        </SubSection>
      )}

      <p className="mt-3 text-[11px] leading-relaxed text-ink-3">
        心跳/唤醒让指挥官持续跑（只有唤醒才调模型）；运行守卫超时后自动重派该意图，最多 3 次；巡检本身不调模型。
      </p>
      <div className="mt-2">
        <Button size="xs" variant="ghost" onClick={() => onNavigate?.('runs')}>
          查看运行
        </Button>
      </div>
    </Panel>
  )
}

// ---- ledger panels -------------------------------------------------------

function GoalsPanel({
  goals,
  paused,
  onEdit,
  onNew,
  onPick,
  onOpenNode,
  className,
  style,
}: {
  goals: Goal[]
  /** 收口 runs one commander round and the server refuses it while running. */
  paused: boolean
  onEdit: (g: Goal) => void
  onNew: () => void
  onPick: (g: Goal) => void
  /** Cross-page link: a sealed goal's cited facts become graph nodes. */
  onOpenNode?: (graphNodeID: string) => void
  className?: string
  style?: React.CSSProperties
}) {
  return (
    <Panel
      title={<PanelTitle icon={Flag}>目标进度</PanelTitle>}
      extra={
        <Button size="xs" variant="ghost" onClick={onNew}>
          <Plus className="h-3.5 w-3.5" /> 新建
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      style={style}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
    >
      {goals.length === 0 ? (
        <EmptyState
          compact
          icon={Flag}
          title="还没有目标"
          description="目标由你定义：先写起点和终点；也可以先开跑，指挥官按简报推进，等你补充终点。达成必须挂真实证据。"
          action={
            <Button size="xs" variant="outline" onClick={onNew}>
              新建目标
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-hairline/70">
          {goals.map((g) => (
            <li key={g.id} className="saker-hover-row px-3 py-3">
              <Button size="xs" variant="ghost" className="float-right" onClick={()=>onEdit(g)}>管理</Button>
              {g.closed && <p className="text-xs text-ink-3">关闭原因：{g.closed_reason}</p>}
              <div className="flex items-start gap-2.5">
                <span className="mt-0.5 shrink-0">
                  {g.met ? (
                    <CircleCheck className="h-4 w-4 text-success" />
                  ) : (
                    <CircleDashed className="h-4 w-4 text-ink-3" />
                  )}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="line-clamp-2 text-[12.5px] leading-relaxed text-ink">{g.statement}</div>
                  {g.start && (
                    <div className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-ink-3" title={g.start}>起点：{g.start}</div>
                  )}
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                    <StatusIndicator compact status={!g.closed && g.met ? 'success' : 'idle'} label={g.closed ? '已关闭' : g.met ? '已达成' : '未达成'} />
                    <span className="saker-break font-mono text-[11px] text-ink-3">{g.id}</span>
                  </div>
                  {/* A sealed goal carries its own argument: why, on which facts,
                      and when. Old seals predate those fields, say so instead of
                      drawing a blank. */}
                  {g.met && (
                    <div className="mt-1.5 space-y-1">
                      {g.note ? (
                        <div className="line-clamp-2 text-[12px] leading-relaxed text-ink-2" title={g.note}>
                          为什么达成：{g.note}
                        </div>
                      ) : (
                        <div className="text-[11px] italic text-ink-3">盖章时未记录理由（旧数据）</div>
                      )}
                      <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-ink-3">
                        <span>依据</span>
                        {g.from && g.from.length > 0 ? (
                          // 依据是事实 id：给了跨页回调就变成图谱入口（f:<id>），
                          // 没给时保持原来的纯文本（可选 prop 不改变既有页面）。
                          g.from.map((fid) =>
                            onOpenNode ? (
                              <button
                                key={fid}
                                type="button"
                                data-testid="overview-goal-fact"
                                onClick={() => onOpenNode(`f:${fid}`)}
                                className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-primary/40 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                                title={`在图谱中打开事实 ${fid}`}
                              >
                                {fid}
                              </button>
                            ) : (
                              <span
                                key={fid}
                                className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2"
                              >
                                {fid}
                              </span>
                            ),
                          )
                        ) : g.evidence_id ? (
                          <span className="font-mono text-[11px]" title="旧数据：只记录了证据 id">
                            证据 {g.evidence_id}
                          </span>
                        ) : (
                          <span>未记录</span>
                        )}
                      </div>
                      {g.completed_at && (
                        <div className="text-[11px] text-ink-3">
                          达成于 <span className="font-mono saker-nums">{fmtTime(g.completed_at)}</span>
                        </div>
                      )}
                      {/* F9 follow-through: the seal is historical and is not
                          rewritten when a cited fact changes; the server computes
                          the staleness, the UI reports it and never un-sets met. */}
                      {g.receipt_stale && (
                        <div
                          data-testid="overview-goal-receipt-stale"
                          className="mt-1.5 rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-2 text-[12px] leading-relaxed text-ink-2"
                          title="目标仍记为已达成；历史完成证明保留，需要重新判断依据是否还成立"
                        >
                          <div className="font-medium text-warning">收据过时：所依据事实已更新，需重新判断（历史完成证明保留）</div>
                          {(g.receipt_changed ?? []).length > 0 && (
                            <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                              <span className="text-ink-3">变更事实</span>
                              {g.receipt_changed!.map((fid) =>
                                onOpenNode ? (
                                  <button
                                    key={fid}
                                    type="button"
                                    onClick={() => onOpenNode(`f:${fid}`)}
                                    className="rounded border border-warning/30 bg-card/60 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-warning/60 hover:text-warning focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                                    title={`在图谱中打开事实 ${fid}`}
                                  >
                                    {fid}
                                  </button>
                                ) : (
                                  <span key={fid} className="font-mono text-[11px]">
                                    {fid}
                                  </span>
                                ),
                              )}
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
                {!g.closed && !g.met && (
                  <Button
                    size="xs"
                    variant="soft"
                    className="shrink-0"
                    disabled={!paused}
                    title={paused ? '让指挥官核对已有事实与证据并整理收口；项目保持暂停' : '先暂停项目，再让指挥官收口'}
                    onClick={() => onPick(g)}
                  >
                    收口
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

const frontierStatusLabels: Record<string, string> = {
  parked: '已停放',
  failed: '上轮失败',
  review_failed: '复核未通过',
}

function FrontierPanel({
  items,
  waiting = [],
  onOpen,
  onOpenNode,
  onStop,
  className,
  style,
}: {
  items: OperatorFrontierItem[]
  /** Directions whose turn yielded (children/operator/external action): the
   *  board reports them separately from pending work, and the console must show
   *  them as "等待中" instead of letting them look like missing work. */
  waiting?: BoardItem[]
  onOpen: () => void
  /** Cross-page link: a frontier row selects its step node (`s:<intent id>`). */
  onOpenNode?: (graphNodeID: string) => void
  /** Sticky operator stop, with a mandatory reason and subtree warning. */
  onStop?: (target: StopTarget) => void
  className?: string
  style?: React.CSSProperties
}) {
  const blocked = items.filter((f) => !f.ready).length
  // Blocked first: the question this panel answers is "why is work not moving",
  // so the rows that carry a reason come before the ones that are ready to run.
  const ordered = items.toSorted((a, b) => Number(a.ready) - Number(b.ready))
  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          <Target className="h-3.5 w-3.5 text-ink-3" /> 阻塞原因
          {blocked > 0 && (
            <Badge size="sm" variant="warning" title="当前列表中前提未就绪、挂起、停放或上轮失败的意图">
              {blocked} 被挡
            </Badge>
          )}
          {waiting.length > 0 && (
            <Badge size="sm" variant="secondary" title="等待意图 / 操作员 / 外部动作的意图">
              {waiting.length} 等待中
            </Badge>
          )}
        </span>
      }
      extra={
        <Button size="xs" variant="ghost" onClick={onOpen}>
          图谱
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      style={style}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
    >
      {waiting.length > 0 && (
        <div className="border-b border-hairline px-3 py-2.5" data-testid="overview-waiting-intents">
          <div className="saker-eyebrow">等待中（不是卡住：已让出槽位）</div>
          <ul className="mt-1.5 space-y-1">
            {waiting.map((item) => {
              const label = (
                // The status goes under the title, not beside it: sharing one row
                // with `shrink-0` left the title 83px in the 330px pane, so
                // 「整理本轮原始证据」 printed as 「整理本轮原…」 even at 1280.
                <div className="min-w-0">
                  <div className="truncate text-[12px] text-ink" title={item.title}>
                    {item.title}
                  </div>
                  {item.status && (
                    <div className="mt-0.5 text-[11px] text-ink-3">{statusLabel(item.status)}</div>
                  )}
                </div>
              )
              return (
                <li
                  key={item.id}
                  className="flex items-start gap-1 rounded-lg transition-colors hover:bg-surface-2 focus-within:bg-surface-2"
                >
                  <div className="min-w-0 flex-1">
                    {onOpenNode ? (
                      <ListRow
                        onClick={() => onOpenNode(`s:${item.id}`)}
                        title="在图谱中选中这条意图"
                        leading={<StatusIndicator compact status="warning" label="等待" className="shrink-0" />}
                        className="items-center rounded-lg px-2 py-1.5 hover:bg-transparent"
                      >
                        {label}
                      </ListRow>
                    ) : (
                      <div className="flex items-center gap-2 px-2 py-1.5">
                        <StatusIndicator compact status="warning" label="等待" className="shrink-0" />
                        {label}
                      </div>
                    )}
                  </div>
                  {onStop && (
                    <StopIntentButton
                      title={item.title}
                      onStop={() => onStop({ id: item.id, title: item.title })}
                      className="mt-1"
                    />
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      )}
      {items.length === 0 ? (
        waiting.length === 0 ? (
          <EmptyState
            compact
            icon={Target}
            title="没有待办意图"
            description="指挥官派发或你新建意图后，这里显示每个意图的前提是否成立。"
            action={
              <Button size="xs" variant="outline" onClick={onOpen}>
                去图谱
              </Button>
            }
          />
        ) : null
      ) : (
        <ul className="divide-y divide-hairline/70">
          {items.length > 0 && blocked === 0 && (
            <li className="px-3 py-2 text-[11px] text-ink-3" data-testid="overview-no-blockers">
              当前列表未发现阻塞。派发前还会校验原件。
            </li>
          )}
          {ordered.map((f) => {
            // Markers are display-only: a row from an older board simply has no
            // new fields, so nothing is invented for it.
            const markers = frontierMarkers(f)
            const changedText = frontierPremiseChangedText(f)
            const body = (
              <div className="min-w-0">
                <div className="truncate text-[12.5px] text-ink">{f.title}</div>
                {markers.length > 0 && (
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    {markers.map((marker) => (
                      <Badge key={marker.key} size="sm" variant={marker.tone} title={marker.detail}>
                        {marker.label}
                      </Badge>
                    ))}
                  </div>
                )}
                <div className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-[11px] text-ink-3">
                  {f.last_stop && <span>上轮 {f.last_stop}</span>}
                  {f.last_declared !== undefined && <span>声明 {f.last_declared}</span>}
                  {changedText && <span className="text-warning">{changedText}</span>}
                  {!f.premise_issues?.length && f.blocked_on?.length
                    ? <span className="text-caution">前提 {f.blocked_on.join('、')}</span> : null}
                </div>
                {!!f.no_evidence_facts?.length && (
                  <div className="mt-1 text-[11px] leading-relaxed text-ink-3">
                    无原始证据：{f.no_evidence_facts.join('、')}（仅标注，不拦派发）
                  </div>
                )}
                {!!f.premise_issues?.length && (
                  <div className="mt-1 space-y-0.5 text-[11px] leading-relaxed text-caution">
                    {f.premise_issues.map(({ fact_id, reason }) => (
                      <div key={fact_id} className="break-words">
                        <span className="font-mono">{fact_id}</span> · {reason}
                      </div>
                    ))}
                    {!!f.omitted_premise_issues && <div>另有 {f.omitted_premise_issues} 条前提未就绪</div>}
                  </div>
                )}
              </div>
            )
            const hasPremiseIssue = Boolean(f.premise_issues?.length || f.blocked_on?.length)
            const gateLabel = f.ready
              ? '前提初检通过'
              : frontierStatusLabels[f.status ?? ''] ?? (f.held ? '已挂起' : hasPremiseIssue ? '前提未就绪' : '被挡')
            const gate = (
              <StatusIndicator
                compact
                status={f.ready ? 'success' : 'error'}
                label={gateLabel}
                className="mt-0.5 shrink-0"
              />
            )
            return (
              <li
                key={f.id}
                className="flex items-start gap-1 transition-colors hover:bg-surface-2 focus-within:bg-surface-2"
              >
                <div className="min-w-0 flex-1">
                  {onOpenNode ? (
                    <ListRow
                      data-testid="overview-frontier-link"
                      onClick={() => onOpenNode(`s:${f.id}`)}
                      title="在图谱中选中这条意图"
                      leading={gate}
                      className="items-start rounded-none px-3 py-2.5 hover:bg-transparent [&>span]:block"
                    >
                      {body}
                    </ListRow>
                  ) : (
                    <div className="flex items-start gap-3 px-3 py-2.5">
                      {gate}
                      {body}
                    </div>
                  )}
                </div>
                {onStop && f.status !== 'parked' && (
                  <StopIntentButton
                    title={f.title}
                    onStop={() => onStop({ id: f.id, title: f.title })}
                    className="mt-2.5 mr-1"
                  />
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

/**
 * Recent facts behind one disclosure: the rows are unchanged (kind, id,
 * evidence count, status, graph link), only their default visibility is. A fact
 * list is reference material; it must not take a column of the first screen.
 */
function FactsDisclosure({
  items,
  onOpen,
  onOpenNode,
}: {
  items: BoardItem[]
  onOpen: () => void
  /** Cross-page link: a recent fact opens as its graph node (`f:<id>`). */
  onOpenNode?: (graphNodeID: string) => void
}) {
  return (
    <details
      className="saker-panel col-span-full shrink-0 rounded-xl [&[open]>summary>svg]:rotate-180"
      data-testid="overview-facts"
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
        <Network className="h-4 w-4 shrink-0 text-ink-3" aria-hidden="true" />
        <span className="saker-section-title">最近事实</span>
        <span className="saker-nums font-mono text-[11px] text-ink-3">{items.length}</span>
        <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-ink-3 transition-transform" />
      </summary>
      <div className="max-h-[320px] overflow-y-auto border-t border-hairline">
        {items.length === 0 ? (
          <EmptyState
            compact
            icon={Network}
            title="还没有事实"
            description="执行者按交接契约写下结论后，项目图谱会把它们列在这里。"
            action={
              <Button size="xs" variant="outline" onClick={onOpen}>
                去图谱
              </Button>
            }
          />
        ) : (
          <ul className="divide-y divide-hairline/70">
            {items.map((f) => {
              const body = (
                <div className="min-w-0">
                  <div className="truncate text-[12.5px] text-ink">{f.title}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-ink-3">
                    <span className="saker-break font-mono">{f.id}</span>
                    <span>证据 {f.evidence ?? 0}</span>
                    {f.origin && (
                      <Badge variant={originVariant(f.origin)} size="sm">
                        {f.origin}
                      </Badge>
                    )}
                    {f.source_ref?.startsWith('ioa:') && (
                      <Badge
                        variant="outline"
                        size="sm"
                        title="来自 harness IOA 记录：子会话在账本外，没有 usage、工具事件与独立 run"
                      >
                        IOA · 账本外
                      </Badge>
                    )}
                    <StatusIndicator compact status={statusKind(f.status)} label={statusLabel(f.status)} />
                  </div>
                </div>
              )
              return (
                <li key={f.id}>
                  {onOpenNode ? (
                    <ListRow
                      data-testid="overview-fact-link"
                      onClick={() => onOpenNode(`f:${f.id}`)}
                      title="在图谱中打开这条事实"
                      leading={
                        <Badge variant={kindVariant(f.kind)} size="sm" className="mt-0.5 shrink-0">
                          {f.kind}
                        </Badge>
                      }
                      className="items-start rounded-none px-3 py-2.5 [&>span]:block"
                    >
                      {body}
                    </ListRow>
                  ) : (
                    <div className="flex items-start gap-3 px-3 py-2.5">
                      <Badge variant={kindVariant(f.kind)} size="sm" className="mt-0.5 shrink-0">
                        {f.kind}
                      </Badge>
                      {body}
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </details>
  )
}

// ---- briefing (inside the diagnostics expander) ---------------------------

/** The board as one sentence, plus the only number the decider actually reads:
 *  the digest. The panels below show the ledger; this strip shows the briefing —
 *  what the next round will be told, and whether there will be a next round at
 *  all. `digest` is the same value the heartbeat compares with `last_digest`, so
 *  "same" means the next wake is skipped, not that nothing is happening.
 *
 *  It reports, it does not judge: no same-family call, no coverage arithmetic
 *  beyond "a host has no direction that names it". Those stay with the commander
 *  (D54/D59). */
function BriefingBar({ board }: { board: BoardResponse }) {
  const [open, setOpen] = useState(false)
  const dec = board.decider
  const digest = board.digest ?? ''
  const last = dec?.last_digest ?? ''
  const same = Boolean(digest && last && digest === last)

  const held = board.intents.held ?? 0
  const pending = Math.max(0, board.intents.pending - held)
  const running = board.running_intents ?? []
  const quiet = running.filter((it) => (it.pulse?.idle_seconds ?? 0) >= QUIET_SECONDS)
  const blocked = (board.frontier ?? []).filter((f) => !f.ready)
  const ports = board.ports ?? []
  const uncovered = (board.coverage ?? []).filter((c) => !c.open && !c.running)

  const parts = [
    pending > 0 ? `${fmtInt(pending)} 个意图待派` : '没有待派意图',
    board.intents.running > 0
      ? `${fmtInt(board.intents.running)} 个在跑${quiet.length ? `（${quiet.length} 个静默 >5m）` : ''}`
      : '没有在跑的 run',
    blocked.length ? `${blocked.length} 个前提被挡` : null,
    (board.intents.waiting ?? 0) > 0 ? `${board.intents.waiting} 个意图等待中` : null,
    held ? `${held} 个已停放` : null,
    ports.length ? `${ports.length} 组端口` : null,
    uncovered.length ? `${uncovered.length} 台主机没有意图覆盖` : null,
  ].filter(Boolean)

  const verdict = !dec?.enabled
    ? '指挥官未启用：摘要只作观测'
    : !last
      ? '还没有决策轮'
      : same
        ? '与上次决策一致：唤醒会被跳过'
        : '有变化：下一次唤醒会跑一轮'

  return (
    <div className="saker-inset shrink-0 rounded-lg">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <span className="flex items-center gap-1.5 text-[12px] font-medium text-ink">
          <ScrollText className="h-3.5 w-3.5 text-ink-3" /> 作战简报
        </span>
        <Badge
          size="sm"
          variant={!dec?.enabled ? 'muted' : same ? 'muted' : 'warning'}
          className="font-mono"
          title={`本轮 ${digest || '—'}${last ? ` · 上次决策 ${last}` : ''}`}
        >
          摘要 {digest ? digest.slice(0, 8) : '—'}
        </Badge>
        <span className="text-[11px] text-ink-3">{verdict}</span>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen((v) => !v)}>
          {open ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          {open ? '收起' : '展开'}
        </Button>
      </div>

      <div className="saker-break px-3 pb-2.5 text-[12px] leading-relaxed text-ink-2">{parts.join(' · ')}</div>

      {open && (
        <div className="grid gap-x-5 gap-y-3 border-t border-hairline px-3 py-3 text-[11px] xl:grid-cols-2">
          <BriefGroup label="前提被挡" empty="没有意图卡在前提上">
            {blocked.map((f) => (
              <div key={f.id} className="truncate" title={f.title}>
                {f.title}
                <span className="ml-1 text-caution">（{(f.blocked_on ?? []).join('、') || '—'}）</span>
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="端口观测" empty="还没有 service 事实">
            {ports.map((p) => (
              <div key={p.port} className="truncate font-mono saker-nums">
                {p.port} × {p.hosts.length}
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="未覆盖主机" empty="每一台已知主机都有意图">
            {uncovered.slice(0, 8).map((c) => (
              <div key={c.host} className="truncate font-mono">
                {c.host}
                {c.services ? <span className="ml-1 text-ink-3">{c.services} 服务</span> : null}
              </div>
            ))}
            {uncovered.length > 8 && <div className="text-ink-3">还有 {uncovered.length - 8} 台…</div>}
          </BriefGroup>
        </div>
      )}
    </div>
  )
}

function BriefGroup({
  label,
  empty,
  children,
}: {
  label: string
  empty: string
  children: React.ReactNode
}) {
  // Children arrive as one node, an array from a map, or an array of those when
  // a group has a second expression (the "还有 N 台…" line). Flatten one level
  // so a group with no rows still gets its empty state instead of rendering [].
  const items = (Array.isArray(children) ? children.flat(1) : [children]).filter(Boolean)
  return (
    <div className="min-w-0">
      <div className="saker-eyebrow mb-1">{label}</div>
      {items.length === 0 ? <div className="text-ink-3">{empty}</div> : items}
    </div>
  )
}

// ---- onboarding -----------------------------------------------------------

function Onboarding({
  project,
  scope,
  bound,
  defaultNode,
  deciderPaused,
  canStart,
  starting,
  onStart,
  onNewGoal,
  onNavigate,
}: {
  project?: ProjectInfo
  scope: string
  bound: { id: string; transport?: string }[]
  defaultNode: string
  deciderPaused: boolean
  canStart: boolean
  starting: boolean
  onStart: () => void
  onNewGoal: () => void
  onNavigate?: (view: string) => void
}) {
  return (
    <Panel title="从这里开始" extra={<span className="text-[11px] text-ink-3">三步进入作战状态</span>}>
      <div className="grid gap-6 md:grid-cols-3">
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] saker-nums text-primary">1</span>
            写项目简报
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            目标与起点、已知资产、时间窗口与禁止动作。简报和内置约束会原样进入每次派发和决策：测试中新增或移除的资产，直接用「通信」页补充，不必回来改这里。
          </p>
          <div className="saker-inset saker-break rounded-lg px-2.5 py-2 font-mono text-[11px] leading-relaxed text-ink-3">
            {scope || '未指定'}
          </div>
          <Button size="xs" variant="ghost" onClick={() => window.location.assign('/g/?view=projects')}>
            去改项目简报
          </Button>
        </div>
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] saker-nums text-primary">2</span>
            目标（可先不定）
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            可以先不写目标：开跑后指挥官会按项目简报推进；目标由你在新建项目或「新建目标」里定义（起点 + 终点），达成必须挂真实证据。
          </p>
          <Button size="xs" variant="outline" onClick={onNewGoal}>
            <Flag className="h-3.5 w-3.5" /> 新建目标
          </Button>
        </div>
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] saker-nums text-primary">3</span>
            执行节点
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            默认使用本机 scan；可在「执行」页启用同一个 Hub 的其他 scan，设置项目默认节点或派发时指定节点。
          </p>
          <div className="flex flex-wrap gap-1">
            {bound.length ? (
              bound.map((n) => (
                <Badge key={n.id} variant={n.id === defaultNode ? 'primary' : 'secondary'} className="font-mono">
                  {n.id}
                  {n.id === defaultNode ? ' · 首选' : ''}
                </Badge>
              ))
            ) : (
              <Badge variant="warning">还没有启用执行节点</Badge>
            )}
          </div>
          <Button size="xs" variant="ghost" onClick={() => onNavigate?.('nodes')}>
            查看执行宿主
          </Button>
        </div>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-hairline pt-4 text-[12px] text-ink-2">
        <Play className="h-3.5 w-3.5 text-ink-3" />
        写清项目简报（起点）、启用执行节点就能开跑；终点由你在新建项目或「新建目标」里定义。
        <Button
          size="sm"
          className="ml-auto"
          onClick={onStart}
          disabled={!canStart || starting || deciderPaused}
          title={
            !canStart
              ? '还没有启用执行节点：先到「执行」页检查节点状态'
              : deciderPaused
                ? '项目已暂停：先点顶部「继续项目」'
                : undefined
          }
        >
          {starting ? (
            <>
              <Spinner size="sm" className="mr-1.5" />
              已排队…
            </>
          ) : (
            '开始运行'
          )}
        </Button>
      </div>
      <div className="mt-2 text-[11px] text-ink-3">
        项目：{project?.name ?? '—'} · ID <span className="saker-break font-mono">{project?.id ?? '—'}</span>
      </div>
    </Panel>
  )
}

// ---- helpers --------------------------------------------------------------

/** One-second clock; the page re-renders only while mounted. */
function useNow(active = true): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

/** A labelled mono value: 11px label, 12px tabular number/ID. */
function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] leading-4 text-ink-3">{label}</div>
      <div
        className="truncate font-mono text-[12px] leading-5 saker-nums text-ink-2"
        title={typeof value === 'string' ? value : undefined}
      >
        {value}
      </div>
    </div>
  )
}

/** A quiet section inside a panel: 11px label + hairline, so detail hierarchies
 *  read as sections rather than as nested cards. */
function SubSection({
  label,
  extra,
  children,
}: {
  label: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="mt-3 border-t border-hairline pt-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[11px] font-medium text-ink-3">{label}</span>
        {extra && <span className="ml-auto">{extra}</span>}
      </div>
      {children}
    </div>
  )
}

function fmtIdle(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m${String(seconds % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

function fmtSeconds(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`
  if (seconds >= 60) return `${Math.round(seconds / 60)}m`
  return `${seconds}s`
}
