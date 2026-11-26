import { useEffect, useMemo, useState } from 'react'
import {
  Activity,
  ArrowDownToLine,
  ChevronDown,
  CircleAlert,
  CircleDashed,
  FileText,
  Flag,
  Gauge,
  RefreshCw,
  Square,
  Terminal,
  Waypoints,
} from 'lucide-react'
import { Badge, Button, Callout, Chip, EmptyState, ListRow, Meter, SkeletonCard, Spinner, StatTile, StatusIndicator, Textarea } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, pageGutter } from '@/components/Page'
import { WorkspaceSidebar } from '@/components/WorkspaceSidebar'
import { DoneDeclaredBadge } from '@/components/DoneDeclaredBadge'
import { RetryIntentDialog } from '@/components/RetryIntentDialog'
import { StopIntentButton } from '@/components/StopIntentButton'
import { StopIntentDialog, type StopTarget } from '@/components/StopIntentDialog'
import { api } from '@/lib/api'
import { PHASE_LABEL, isPhaseActive, phaseDetail, phaseOf } from '@/lib/decider'
import {
  cancelCauseText,
  fmtBytes,
  fmtDuration,
  fmtInt,
  fmtTime,
  hasBrokenPremise,
  intentWaitText,
  kindVariant,
  oneLine,
  runCancelDetail,
  runCancelReason,
  runStatusLabel,
  shortID,
  statusLabel,
} from '@/lib/format'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import { premiseBlockers, summarizeIntentFacts } from '@/lib/intent-detail'
import { reviewFailureHint, reviewFailureLabel, type ReviewFailure } from '@/lib/review-failure'
import { doneDeclaredByShort, doneDeclaredDetail } from '@/lib/settle'
import { useReviewFailure } from '@/lib/use-review-failure'
import { useLatestScroll } from '@/lib/latest-scroll'
import { navigate } from '@/lib/navigation'
import { findGoal } from '@/lib/record-links'
import type {
  BoardResponse,
  Event as RunEvent,
  FrontierItem,
  Goal,
  Intent,
  Run,
  RunPulse,
} from '@/lib/types'

/**
 * 状态 = 战场状态：指挥官此刻在做什么，每个执行者跑到哪一步、为什么停。
 *
 * The page is one reading of two ledgers, not a merged one:
 *
 *  - 执行者名单按 **意图** 成组，一次意图一行。An intent can be attempted
 *    several times (failed → retry / steer / handoff); every new attempt used to
 *    appear as another chip because the old page listed raw runs. The roster
 *    therefore keys on intent id, and the attempts live inside the row's detail
 *    as a chain — a retried direction keeps exactly one tab.
 *  - Only `purpose == ""` runs are executor attempts. Commander / verifier /
 *    report runs are support runs: they have no intent (a commander run carries
 *    an empty intent_id), so they must never be listed as executors and their
 *    count must not be repeated as "executor runs". The situation strip counts
 *    both scopes with explicit labels.
 *  - An intent run whose intent is missing from the ledger is surfaced as an
 *    orphan row instead of being dropped: a page must not hide an inconsistent
 *    ledger, it must show it.
 *
 * All numbers come from the server (board / intents / runs); nothing is
 * recomputed client-side except grouping and the live elapsed clock.
 *
 * The direction is the unit of the page. Its detail answers four questions in
 * one scroll, in this order: 现状 (status badges + the selected attempt's state),
 * 等待原因 (wait / park / broken premises, with the premise ids as links), 历次运行
 * (the attempt chain — visible, not behind a disclosure) and 关联证据 (the facts
 * the direction declared, from `/api/intents/{id}`, with their stored status and
 * evidence count). Logs and native sessions stay on the 运行 page: this page links
 * out instead of rebuilding a log viewer.
 *
 * Visual hierarchy (视觉规范 v2): the page is a read-only command board. The
 * 战况 strip is the widest, quietest summary; the roster rows are compact
 * 12.5px content rows whose selection is a 2px left marker + soft primary wash
 * (never a full red row); the detail pane gets the larger title and real
 * section rhythm, so "the list" and "the selected direction" never weigh the
 * same. Failure/warning colours only ever appear as small tints and dots.
 */
export function ProjectState({
  onNavigate,
  onOpenIntent,
  onOpenRun,
  onOpenFact,
}: {
  /** Switch the console view (?view=…). */
  onNavigate?: (view: string) => void
  /** Open one direction in the graph. */
  onOpenIntent?: (id: string) => void
  /** Open one run in the run ledger. */
  onOpenRun?: (id: string) => void
  /** Open one fact on the graph (a goal receipt's cited/changed facts). */
  onOpenFact?: (id: string) => void
}) {
  const board = usePoll(api.board, 4000)
  const intents = usePoll(api.intents, 4000)
  const runs = usePoll(api.runs, 4000)
  // Deep link from the run ledger: ?view=state&intent=<id>. The URL is the
  // route, so the selection starts from it instead of always landing on the
  // first row of the roster.
  const location = useConsoleLocation()
  const picked = new URLSearchParams(location.search).get('intent') ?? ''
  // Deep link from the inbox: ?view=state&goal=<id> opens one goal's receipt —
  // the record whose cited facts changed after the seal. Read-only: the receipt
  // is historical and is never rewritten here.
  const focusedGoalID = new URLSearchParams(location.search).get('goal') ?? ''
  const goalList = usePoll(
    () => (focusedGoalID ? api.goals() : Promise.resolve([])),
    focusedGoalID ? 8000 : 0,
    [focusedGoalID],
  )
  const [pickedRun, setPickedRun] = useState('')
  const [sidebarOpen, setSidebarOpen] = useState(false)
  useEffect(() => { setPickedRun('') }, [picked])
  // Sticky stop and its retry entry. Both dialogs target one direction; after a
  // stop the same row shows 已停止（人工）and the retry button beside it.
  const [stopTarget, setStopTarget] = useState<StopTarget | null>(null)
  const [retryTarget, setRetryTarget] = useState<StopTarget | null>(null)

  const allRuns = runs.data ?? []
  const unitList = useMemo(
    () => buildUnits(intents.data ?? [], allRuns, board.data),
    [intents.data, allRuns, board.data],
  )
  const grouped = useMemo(() => groupUnits(unitList), [unitList])
  const support = useMemo(() => supportCounts(allRuns), [allRuns])
  const orphans = useMemo(() => orphanRuns(intents.data ?? [], allRuns), [intents.data, allRuns])
  // Battlefield order: what is moving or blocked first, history last. The first
  // unit is the default selection, so opening the page lands where the operator
  // would look for trouble.
  const ordered = useMemo(
    () => GROUP_ORDER.flatMap((key) => sortGroup(key, grouped[key])),
    [grouped],
  )
  const selected = ordered.find((u) => u.intent.id === picked) ?? ordered[0]
  // An attempt is picked explicitly; otherwise the latest attempt of the
  // selected intent follows the ledger (a retry appears without a click).
  const attempt =
    selected?.attempts.find((r) => r.id === pickedRun) ?? selected?.attempts[selected.attempts.length - 1]

  /** Selection is deep-linkable: the run page links here with ?intent=<id>. */
  function pickIntent(id: string) {
    setPickedRun('')
    const params = new URLSearchParams(window.location.search)
    params.set('view', 'state')
    params.set('intent', id)
    navigate(`${window.location.pathname}?${params.toString()}`)
  }

  /** The 尝试 chips switch the detail pane without touching the URL: a plain
   *  state change, painted on the next frame (no transition, no fade). */
  function pickRun(id: string) {
    setPickedRun(id)
  }

  const loading = board.loading || intents.loading || runs.loading
  const refreshing = board.refreshing || intents.refreshing || runs.refreshing
  const retry = () => { board.reload(); intents.reload(); runs.reload() }
  const error = board.error ?? intents.error ?? runs.error

  return (
    <Page
      title="状态"
      subtitle="一个意图一页：现状、等待原因、历次运行与关联证据。"
      meta={<LiveBadge board={board.data} unavailable={!!error || loading} />}
      scroll={false}
      className="@container"
    >
      <div className={cn('flex h-full min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain py-4 sm:py-5 @3xl:overflow-hidden', pageGutter)}>
        {error && (
          <Callout tone="destructive" className="shrink-0" icon={<CircleAlert className="h-4 w-4" />}>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[12px] leading-relaxed">
                更新失败，当前数据可能已过期。
                {board.updatedAt && `上次状态更新：${fmtTime(new Date(board.updatedAt).toISOString())}`}
              </span>
              <Button size="xs" variant="outline" disabled={loading || refreshing} onClick={retry}>
                {refreshing ? (
                  <>
                    <Spinner size="sm" className="mr-1.5" />
                    重试中…
                  </>
                ) : (
                  '重试更新'
                )}
              </Button>
            </div>
            <details className="mt-1.5 text-[11px] text-ink-3">
              <summary className="cursor-pointer">错误详情</summary>
              <div className="saker-break mt-1">{error}</div>
            </details>
          </Callout>
        )}
        {focusedGoalID && (
          <GoalReceiptPanel
            goalID={focusedGoalID}
            goals={goalList.data}
            loading={goalList.loading}
            error={goalList.error}
            onOpenFact={onOpenFact}
            onClose={() => {
              const params = new URLSearchParams(window.location.search)
              params.delete('goal')
              navigate(`${window.location.pathname}${params.toString() ? `?${params}` : ''}`)
            }}
          />
        )}
        {loading ? (
          <div className="min-h-0 flex-1 space-y-3" aria-busy="true">
            <SkeletonCard />
            <div className="grid min-h-0 grid-cols-1 gap-3 @3xl:grid-cols-[minmax(264px,29%)_minmax(0,1fr)]">
              <SkeletonCard />
              <SkeletonCard />
            </div>
          </div>
        ) : (
          <>
            {unitList.length === 0 && orphans.length === 0 ? (
              // One empty message, not two: with no directions there is no roster
              // and no case, and the split panes would just repeat themselves.
              <div className="saker-panel min-h-0 flex-1 rounded-xl">
                <EmptyState
                  icon={Gauge}
                  title={error ? '暂时无法读取执行者状态' : '项目还没有执行者'}
                  description={
                    error
                      ? '请重试读取，恢复连接后会自动更新。'
                      : `这个项目还没有意图。支持运行不占执行者位置：全部运行 ${fmtInt(board.data?.runs.total ?? 0)} 条（指挥 ${fmtInt(support.commander)} · 复核 ${fmtInt(support.verifier)} · 报告 ${fmtInt(support.report)}）。派发第一条意图后，这里按意图显示尝试链与 case。`
                  }
                  action={
                    error ? (
                      <Button size="sm" variant="outline" onClick={retry} disabled={refreshing}>
                        重试读取
                      </Button>
                    ) : (
                      <Button size="sm" onClick={() => navigate(`${window.location.pathname}?view=overview#new`)}>
                        新建意图
                      </Button>
                    )
                  }
                />
              </div>
            ) : (
              <div className="grid gap-3 @3xl:min-h-0 @3xl:flex-1 @3xl:grid-cols-[minmax(264px,29%)_minmax(0,1fr)]">
                <WorkspaceSidebar label="意图列表" summary={`${fmtInt(unitList.length)} 个意图`} expanded={sidebarOpen} onToggle={() => setSidebarOpen(open => !open)}>
                  <Roster
                    grouped={grouped}
                    counts={{ total: unitList.length, running: board.data?.intents.running ?? 0 }}
                    support={support}
                    board={board.data}
                    selectedID={selected?.intent.id ?? ''}
                    orphans={orphans}
                    onSelect={pickIntent}
                    onNavigate={onNavigate}
                    onStop={setStopTarget}
                    onRetry={setRetryTarget}
                    className="max-h-[min(52dvh,24rem)] flex-1 @3xl:max-h-none"
                  />
                </WorkspaceSidebar>
                <UnitDetail
                  unit={selected}
                  run={attempt}
                  siblings={unitList}
                  onPickRun={pickRun}
                  onOpenIntent={onOpenIntent}
                  onOpenRun={onOpenRun}
                  onOpenFact={onOpenFact}
                  onNavigate={onNavigate}
                  onRunsChanged={retry}
                  onStop={setStopTarget}
                  onRetry={setRetryTarget}
                  className="min-w-0"
                />
              </div>
            )}
          </>
        )}
      </div>

      <StopIntentDialog
        target={stopTarget}
        onOpenChange={(open) => { if (!open) setStopTarget(null) }}
        onStopped={(_result, target) => {
          // Keep the operator on the same direction: it now reads 已停止（人工）
          // and offers 重试 in the same row.
          pickIntent(target.id)
          retry()
        }}
      />
      <RetryIntentDialog
        open={!!retryTarget}
        intentId={retryTarget?.id ?? ''}
        onOpenChange={(open) => { if (!open) setRetryTarget(null) }}
        onRetried={() => {
          setRetryTarget(null)
          retry()
        }}
      />
    </Page>
  )
}

/** A quiet live marker: 11px ink-3, one dot, no chrome competing with the page. */
function LiveBadge({ board, unavailable }: { board?: BoardResponse; unavailable: boolean }) {
  if (unavailable) {
    return (
      <span role="status" className="inline-flex items-center gap-1.5 text-[11px] text-warning">
        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />
        状态待更新
      </span>
    )
  }
  const phase = phaseOf(board?.decider)
  const live = isPhaseActive(phase)
  const label = live ? PHASE_LABEL[phase] ?? phase : phase === 'paused' ? '已暂停' : phase === 'blocked' ? '未就绪' : '空闲'
  return (
    <span
      role="status"
      className="inline-flex items-center gap-1.5 rounded-full border border-hairline bg-surface-2/60 px-2 py-0.5 text-[11px] text-ink-3"
    >
      <span className="relative flex h-1.5 w-1.5">
        {live && <span className="absolute inline-flex h-full w-full motion-safe:animate-ping rounded-full bg-info opacity-60" />}
        <span
          className={cn(
            'relative inline-flex h-1.5 w-1.5 rounded-full',
            live ? 'bg-info' : phase === 'paused' ? 'bg-warning' : phase === 'blocked' ? 'bg-caution' : 'bg-ink-3/50',
          )}
        />
      </span>
      {live ? '实时 · ' : ''}
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// 执行者名单: one row per intent, grouped by what the battlefield needs first.
// ---------------------------------------------------------------------------

function Roster({
  grouped,
  counts,
  support,
  board,
  selectedID,
  orphans,
  onSelect,
  onNavigate,
  onStop,
  onRetry,
  className,
}: {
  grouped: Record<GroupKey, Unit[]>
  counts: { total: number; running: number }
  support: SupportCounts
  board?: BoardResponse
  selectedID: string
  orphans: Run[]
  onSelect: (id: string) => void
  onNavigate?: (view: string) => void
  onStop?: (target: StopTarget) => void
  onRetry?: (target: StopTarget) => void
  className?: string
}) {
  return (
    <section
      data-testid="exec-roster"
      className={cn('saker-panel flex min-h-0 flex-col rounded-xl', className)}
    >
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-hairline px-4">
        <Terminal className="h-4 w-4 shrink-0 text-ink-3" />
        <span className="saker-section-title">执行者 · 意图</span>
        <span className="ml-auto font-mono text-[11px] saker-nums text-ink-3">
          {counts.total} 个意图 · {counts.running} 在跑
        </span>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
        {counts.total === 0 && orphans.length === 0 && (
          <EmptyState
            compact
            icon={Gauge}
            title="还没有执行者"
            description="项目里还没有意图。指挥官/复核/报告的运行是支持运行，不会出现在这里；等指挥官派发第一条意图后，这里按意图显示尝试链。"
          />
        )}
        {GROUP_ORDER.map((key) => {
          const list = sortGroup(key, grouped[key])
          if (list.length === 0) return null
          return (
            <div key={key}>
              <div className="sticky top-0 z-10 flex items-center gap-2 border-b border-hairline/70 bg-card/95 px-3 py-1.5 backdrop-blur">
                <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', GROUP_DOT[key])} />
                <span className="saker-eyebrow">{GROUP_LABEL[key]}</span>
                <span className="ml-auto font-mono text-[11px] saker-nums text-ink-3">{list.length}</span>
              </div>
              <div className="divide-y divide-hairline/60">
                {list.map((u) => (
                  <UnitRow
                    key={u.intent.id}
                    unit={u}
                    active={selectedID === u.intent.id}
                    onClick={() => onSelect(u.intent.id)}
                    onStop={onStop}
                    onRetry={onRetry}
                  />
                ))}
              </div>
            </div>
          )
        })}

        {orphans.length > 0 && (
          <div>
            <div className="flex items-center gap-2 border-b border-hairline/70 bg-destructive/[0.06] px-3 py-1.5">
              <CircleAlert className="h-3.5 w-3.5 shrink-0 text-destructive" />
              <span className="saker-eyebrow text-destructive">无主运行（意图不在账本里）</span>
              <span className="ml-auto font-mono text-[11px] saker-nums text-ink-3">{orphans.length}</span>
            </div>
            {orphans.map((r) => (
              <div key={r.id} className="saker-hover-row border-b border-hairline/60 px-3 py-2">
                <div className="flex items-center gap-2">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive" />
                  <span className="font-mono text-[11px] saker-nums text-ink-3" title={r.id}>
                    {shortID(r.id)}
                  </span>
                  <span className="ml-auto rounded bg-destructive/10 px-1.5 py-0.5 text-[11px] leading-4 text-destructive">
                    {runStatusLabel(r)}
                  </span>
                </div>
                <div className="saker-break mt-1 truncate font-mono text-[11px] text-ink-3">
                  intent_id={r.intent_id || '（空）'} · {fmtTime(r.started_at)}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <footer className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-hairline px-4 py-2 text-[11px] text-ink-3">
        {/* The 战况 strip is gone; its only unique numbers survive as this one
            line, so support runs are still countable without a dashboard. */}
        <span>
          支持运行 {fmtInt(support.total)}（指挥 {fmtInt(support.commander)} · 复核 {fmtInt(support.verifier)} · 报告{' '}
          {fmtInt(support.report)}）· 目标 {fmtInt(board?.goals.met ?? 0)}/{fmtInt(board?.goals.total ?? 0)}
          {(board?.redispatch?.total ?? 0) > 0 ? ` · 重派 ${fmtInt(board?.redispatch?.total ?? 0)}` : ''}
        </span>
        <button
          type="button"
          className="ml-auto rounded px-1 font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => onNavigate?.('runs')}
        >
          运行页 →
        </button>
      </footer>
    </section>
  )
}

function UnitRow({
  unit,
  active,
  onClick,
  onStop,
  onRetry,
}: {
  unit: Unit
  active: boolean
  onClick: () => void
  onStop?: (target: StopTarget) => void
  onRetry?: (target: StopTarget) => void
}) {
  const { intent, attempts, latest, live, pulse, frontier } = unit
  // Attempts that actually ran: intent.attempts is the claim count and can be
  // decremented when a run is canceled, so it must not drive this label.
  const attemptCount = attempts.length
  const stopped = latest && (latest.status === 'failed' || latest.status === 'canceled')
  // Cancellable means "the server will accept a stop now": done/parked/failed
  // rows already have nothing to stop (and retry is the way back).
  const stoppable = !intent.hold && ['pending', 'claimed', 'running', 'waiting', 'reviewing'].includes(intent.status)
  // failed+Hold is the sticky operator stop, not a harness failure; pending+Hold
  // is a direction stopped before it ever dispatched.
  const heldStop = Boolean(intent.hold) && (intent.status === 'failed' || intent.status === 'pending')
  const quiet = pulse ? pulse.idle_seconds : 0
  const lastPulse = pulse ? pulseLine(pulse) : ''
  // "复核未通过" has two causes and two next steps; the ledger separates them
  // (a verdict writes a review row, an interrupted verifier run writes none).
  const reviewFailure = useReviewFailure(
    intent.status === 'review_failed' ? intent.review_failed_facts : undefined,
  )

  return (
    <div
      className={cn(
        'flex items-start gap-1 rounded-lg transition-colors',
        active ? 'bg-primary/[0.05]' : 'hover:bg-surface-2 focus-within:bg-surface-2',
      )}
    >
      <div className="min-w-0 flex-1">
    <ListRow
      data-testid={`intent-tab-${intent.id}`}
      data-intent-id={intent.id}
      data-intent-status={intent.status}
      active={active}
      onClick={onClick}
      title={`${intent.title} · ${intent.id}`}
      leading={<span className={cn('mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full', stateDot(intent))} />}
      className="items-start rounded-none px-3 py-2.5 [&>span]:block"
    >
      <div className="flex items-center gap-2">
        <span className={cn('min-w-0 flex-1 truncate text-[12.5px] leading-5', active ? 'font-medium text-ink' : 'text-ink')}>
          {intent.title}
        </span>
        {attemptCount > 1 && (
          <Badge size="sm" variant="muted" className="shrink-0" title={`已派发 ${attemptCount} 次（首派 + 重派）`}>
            ×{attemptCount}
          </Badge>
        )}
        <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-[11px] leading-4', stateTint(intent))}>
          {intentStateLabel(intent, reviewFailure.data)}
        </span>
      </div>
      {/* The id stays put; the reason/time may wrap onto its own line. With a
          250px list pane, one `truncate` span sharing the row with the id and the
          token count was clipped to 78px — the time and the stop reason were
          unreadable even at 1280. */}
      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-ink-3">
        <span className="shrink-0 font-mono saker-nums">{shortID(intent.id)}</span>
        {intent.status === 'done' && intent.done_declared_by && (
          <span className="shrink-0" title={doneDeclaredDetail(intent)}>
            {doneDeclaredByShort(intent.done_declared_by)}
          </span>
        )}
        {live && pulse ? (
          <span className={cn('min-w-0 flex-1 basis-[12rem] truncate font-mono saker-nums', quiet >= 180 && 'text-warning')}>
            静默 {fmtIdle(quiet)} · {fmtInt(pulse.events)} ev{lastPulse ? ` · ${lastPulse}` : ''}
          </span>
        ) : stopped && latest ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate font-mono saker-nums" title={lastStopText(latest)}>
            {lastStopText(latest)} · {fmtTime(latest.started_at)}
          </span>
        ) : hasBrokenPremise(frontier) ? (
          <span
            className="min-w-0 flex-1 basis-[12rem] truncate text-caution"
            title={`前提事实不成立：${(frontier?.blocked_on ?? []).join('，')}（派发会被拒绝）`}
          >
            前提不成立（{frontier?.blocked_on?.length ?? 0}）
          </span>
        ) : intent.status === 'reviewing' ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate text-review">复核中；通过后才完成</span>
        ) : intent.status === 'review_failed' ? (
          <span
            className={cn(
              'min-w-0 flex-1 basis-[12rem] truncate',
              reviewFailure.data?.kind === 'review_unfinished' ? 'text-caution' : 'text-destructive',
            )}
            title={reviewFailureHint(reviewFailure.data)}
          >
            {reviewFailureLabel(reviewFailure.data)}
          </span>
        ) : intent.hold && intent.status === 'failed' ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate text-caution">
            已停止（人工）· 不会自动重派，需重试
          </span>
        ) : intent.status === 'failed' ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate">已失败（自动）</span>
        ) : intent.hold ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate text-caution">已停止（人工）</span>
        ) : intent.status === 'waiting' && intent.wait ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate">
            {intent.wait.actions?.length
              ? `等待 ${intent.wait.actions.length} 个外部动作`
              : intent.wait.operator
                ? `等待人工：${intent.wait.operator.request}`
                : '等待回执'}
          </span>
        ) : latest ? (
          <span className="min-w-0 flex-1 basis-[12rem] truncate font-mono saker-nums">
            最近 {fmtTime(latest.started_at)} · {fmtDuration(latest.started_at, latest.ended_at)}
          </span>
        ) : (
          <span className="min-w-0 flex-1 basis-[12rem] truncate">尚未派发</span>
        )}
        {latest && latest.tokens ? (
          <span className="shrink-0 font-mono saker-nums">{fmtInt(latest.tokens)} tok</span>
        ) : null}
      </div>
    </ListRow>
      </div>
      {stoppable && onStop && (
        <StopIntentButton
          title={intent.title}
          onStop={() => onStop({ id: intent.id, title: intent.title })}
          className="mt-2"
        />
      )}
      {heldStop && onRetry && (
        <Button
          size="xs"
          variant="outline"
          className="mt-1.5 shrink-0"
          title="重试：恢复这条已停止的意图（可续原会话或开新会话）"
          onClick={() => onRetry({ id: intent.id, title: intent.title })}
        >
          <RefreshCw className="h-3.5 w-3.5" /> 重试
        </Button>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 选中意图: the order, its attempts, and the case of the selected attempt.
// ---------------------------------------------------------------------------

/** A parked direction can outlive the blocker its prose describes. The server
 *  records which premises were unready at park time; once readiness flips to
 *  true this banner offers the explicit reopen instead of a stale reason. */
function ParkedPremiseNotice({ intent, onChanged }: { intent: Intent; onChanged?: () => void }) {
  const active = intent.status === 'parked' && (intent.parked_premise_fact_ids?.length ?? 0) > 0
  const readiness = usePoll(
    () => (active ? api.intentReadiness(intent.id) : Promise.resolve(null)),
    15000,
    [intent.id, active],
  )
  const [opening, setOpening] = useState(false)
  if (!active || !readiness.data?.ready) return null
  async function reopen() {
    setOpening(true)
    try {
      await api.reopenIntent(intent.id)
      onChanged?.()
    } finally {
      setOpening(false)
    }
  }
  return (
    <Callout tone="info">
      <div className="flex flex-wrap items-center gap-2">
        <span>停放时未就绪的前提现在已经通过复核；可重开继续，或确认已并入其他意图。</span>
        <Button size="xs" variant="soft" onClick={reopen} disabled={opening}>
          {opening ? '重开中…' : '重开'}
        </Button>
      </div>
    </Callout>
  )
}

/** 停并换向 on the direction itself: stop the live turn, continue the same
 *  session with one sentence. It is an operation on a run, not a message and not
 *  a boundary change, so it belongs next to the direction, not in the 下发 box. */
function SteerCallout({ intent, onChanged }: { intent: Intent; onChanged?: () => void }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const live = intent.status === 'running' || intent.status === 'claimed'
  if (!live) return null
  async function steer() {
    const note = text.trim()
    if (!note || busy) return
    setBusy(true)
    setError('')
    setNote('')
    try {
      const result = await api.steerIntent(intent.id, note)
      setNote(`已停并换向：新 run ${result.run_id}。`)
      setText('')
      setOpen(false)
      onChanged?.()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div data-testid="steer-callout" className="rounded-lg border border-hairline px-3 py-2">
      <div className="flex flex-wrap items-center gap-2 text-[12px] leading-relaxed">
        <span className="text-ink-2">这一轮正在跑：可以先停掉它，用同一会话接着改造。</span>
        <Button size="xs" variant="outline" className="ml-auto" onClick={() => setOpen((value) => !value)} disabled={busy}>
          <Square className="h-3.5 w-3.5" /> {open ? '取消' : '停并换向'}
        </Button>
      </div>
      {open && (
        <div className="mt-2 space-y-1.5">
          <Textarea
            rows={2}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="换向后的下一步：停掉现在的打法，改做哪一件。"
            aria-label="换向说明"
            className="min-h-[52px] text-[12px] leading-relaxed"
          />
          <div className="flex items-center gap-2">
            <Button size="xs" onClick={() => void steer()} disabled={busy || !text.trim()}>
              {busy ? '处理中…' : '确认停并换向'}
            </Button>
            <span className="text-[11px] text-ink-3">会取消当前 run，已写出的证据保留。</span>
          </div>
        </div>
      )}
      {note && <p className="mt-1 text-[11px] text-ink-2">{note}</p>}
      {error && (
        <p role="alert" className="mt-1 text-[11px] text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}

function UnitDetail({
  unit,
  run,
  siblings,
  onPickRun,
  onOpenIntent,
  onOpenRun,
  onOpenFact,
  onNavigate,
  onRunsChanged,
  onStop,
  onRetry,
  className,
}: {
  unit?: Unit
  run?: Run
  /** Every direction in the list: a wait names child ids, their state lives here. */
  siblings?: Unit[]
  onPickRun: (id: string) => void
  onOpenIntent?: (id: string) => void
  onOpenRun?: (id: string) => void
  /** Open one fact on the graph (premises and the direction's own facts). */
  onOpenFact?: (id: string) => void
  /** Leave for the run ledger: logs and native sessions live there. */
  onNavigate?: (view: string) => void
  onRunsChanged?: () => void
  onStop?: (target: StopTarget) => void
  onRetry?: (target: StopTarget) => void
  className?: string
}) {
  if (!unit) {
    return (
      <section className={cn('saker-panel flex min-h-0 flex-col rounded-xl', className)}>
        <EmptyState
          icon={Gauge}
          title="没有执行者"
          description="项目里还没有意图。派发一条意图后，这里显示它的意图说明、尝试链和 case；指挥官与支持运行在顶部战况条里。"
        />
      </section>
    )
  }

  const { intent, attempts, pulse, frontier } = unit
  // A wait names child ids only; the sentence reads better with the child's own
  // state, so look it up in the same roster the page already has.
  const childOf = (id: string) => siblings?.find((row) => row.intent.id === id)?.intent
  const reviewFailure = useReviewFailure(
    intent.status === 'review_failed' ? intent.review_failed_facts : undefined,
  )

  return (
    <section
      data-testid="exec-detail"
      data-intent-id={intent.id}
      className={cn('saker-panel flex min-h-0 flex-col rounded-xl', className)}
    >
      <div className="min-h-0 flex-1 overflow-y-auto">
        <header className="border-b border-hairline px-4 py-4 sm:px-5">
          <div className="flex flex-wrap items-start gap-3">
            <div className="min-w-0 flex-1 basis-full @3xl:basis-0">
              <div className="saker-eyebrow flex flex-wrap items-center gap-2">
                <span>选中的意图</span>
                <span className="font-mono saker-nums text-ink-3">{shortID(intent.id)}</span>
              </div>
              <h2
                data-testid="detail-title"
                className="mt-1.5 min-w-0 break-words text-[17px] font-semibold leading-6 tracking-tight text-ink"
              >
                {intent.title}
              </h2>
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <Badge
                  size="sm"
                  variant={intentStatusVariant(intent)}
                  title={`状态：${intent.status}${intent.hold ? '（人工停止，需重试恢复）' : ''}`}
                >
                  {intentStateLabel(intent, reviewFailure.data)}
                </Badge>
                <DoneDeclaredBadge intent={intent} />
                {intent.hold && (
                  <Badge size="sm" variant="warning">
                    已停止（人工）
                  </Badge>
                )}
                {intent.aborted && (
                  <Badge size="sm" variant="danger">
                    子树已取消
                  </Badge>
                )}
                {/* "前提不成立" is a claim about from_facts, not a synonym for
                    "cannot dispatch": board.ready is also false for failed/held
                    directions, which already have their own badges above. */}
                {hasBrokenPremise(frontier) && (
                  <Badge
                    size="sm"
                    variant="warning"
                    title={`前提事实不成立：${(frontier?.blocked_on ?? []).join('，')}（派发会被拒绝）`}
                  >
                    前提不成立
                  </Badge>
                )}
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              {intent.hold && (intent.status === 'failed' || intent.status === 'pending') && onRetry && (
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => onRetry({ id: intent.id, title: intent.title })}
                  title="重试：恢复这条已停止的意图（可续原会话或开新会话）"
                >
                  <RefreshCw className="h-3.5 w-3.5" /> 重试
                </Button>
              )}
              {!intent.hold && ['pending', 'claimed', 'running', 'waiting', 'reviewing'].includes(intent.status) && onStop && (
                <StopIntentButton
                  title={intent.title}
                  onStop={() => onStop({ id: intent.id, title: intent.title })}
                  alwaysShowLabel
                  className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                />
              )}
              {onOpenIntent && (
                <Button size="xs" variant="ghost" onClick={() => onOpenIntent(intent.id)} title="在项目图谱里打开这条意图">
                  <Waypoints className="h-3.5 w-3.5" /> 图谱
                </Button>
              )}
              {run && onOpenRun && (
                <Button size="xs" variant="ghost" onClick={() => onOpenRun(run.id)} title="在运行账本里打开这一条 run">
                  <Activity className="h-3.5 w-3.5" /> 运行
                </Button>
              )}
            </div>
          </div>
        </header>

        <div className="space-y-4 p-4 sm:p-5">
          {/* The composer no longer offers "stop and steer": that is an action on
              a live turn, so it lives here, one screen from the direction it stops. */}
          <SteerCallout intent={intent} onChanged={onRunsChanged} />
          {intent.wait && (
            <Callout tone="info" icon={<CircleDashed className="h-4 w-4" />}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]">
                {/* 等待条件与下游意图当前状态同一句：不必逐条点开才知道等的是谁、还差谁。 */}
                <span data-testid="intent-wait">{intentWaitText(intent.wait)}</span>
                {intent.wait.deadline && (
                  <span className="ml-auto font-mono text-[11px] saker-nums">截止 {fmtTime(intent.wait.deadline)}</span>
                )}
                {intent.wait.wake_pending && <span className="text-info">已满足，待唤醒</span>}
              </div>
            </Callout>
          )}

          {intent.park_reason && (
            <div className="rounded-lg border border-caution/25 bg-caution/[0.07] px-3 py-2 text-[12px] leading-relaxed text-ink-2">
              停放原因：{intent.park_reason}
            </div>
          )}
          <ParkedPremiseNotice intent={intent} onChanged={onRunsChanged} />
          <PremiseBlockers frontier={frontier} onOpenFact={onOpenFact} />
          {intent.status === 'review_failed' && (
            <div
              data-testid="review-failure"
              data-review-kind={reviewFailure.data?.kind ?? 'reading'}
              className={cn(
                'rounded-lg border px-3 py-2 text-[12px] leading-relaxed',
                reviewFailure.data?.kind === 'review_unfinished'
                  ? 'border-caution/25 bg-caution/[0.07]'
                  : 'border-destructive/25 bg-destructive/[0.06]',
              )}
            >
              <div className="font-medium text-ink">{reviewFailureLabel(reviewFailure.data)}</div>
              <div className="mt-0.5 text-[11px] text-ink-2">{reviewFailureHint(reviewFailure.data)}</div>
              {(intent.review_failed_facts?.length ?? 0) > 0 && (
                <div className="mt-1.5 flex flex-wrap items-center gap-1">
                  <span className="text-[11px] text-ink-3">待复核事实</span>
                  {intent.review_failed_facts!.map((id) => (
                    <button
                      key={id}
                      type="button"
                      disabled={!onOpenFact}
                      onClick={() => onOpenFact?.(id)}
                      title={`在图上打开事实 ${id}`}
                      className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-2 transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-60"
                    >
                      {shortID(id)}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}

          {run ? (
            <>
              {/* 历次运行 is visible, not a disclosure: one attempt hides that the
                  direction was retried. The selected attempt's own state comes
                  right below it, so the chain and the run read as one block. */}
              <AttemptChain
                intent={intent}
                attempts={attempts}
                selectedRunID={run.id}
                onPickRun={onPickRun}
                onOpenRuns={onNavigate ? () => onNavigate('runs') : undefined}
              />
              <RunStatusLine
                run={run}
                pulse={pulse}
                attemptIndex={attempts.findIndex((r) => r.id === run.id) + 1}
                onOpenRun={onOpenRun}
              />
              {/* Evidence sits above the log tail: the four questions the page
                  answers end at 关联证据, and the event stream is the deepest
                  detail (its full log and session live on the 运行 page). */}
              <IntentFacts intent={intent} live={run?.status === 'running'} onOpenFact={onOpenFact} />
              <RunEvents run={run} />

              <details className="rounded-lg border border-hairline" data-testid="detail-more">
                <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden [&[open]>svg]:rotate-180">
                  <ChevronDown className="h-3.5 w-3.5 shrink-0 text-ink-3 transition-transform" />
                  任务书、意图说明与运行参数
                  <span className="ml-auto font-mono text-[11px] saker-nums text-ink-3">
                    {attempts.length} 次 · {intent.doc ? 'doc' : '无 doc'}
                  </span>
                </summary>
                <div className="space-y-4 border-t border-hairline px-3 py-3">
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] saker-nums text-ink-3">
                    <span>创建 {fmtTime(intent.created_at)}</span>
                    <span>更新 {fmtTime(intent.updated_at)}</span>
                    {intent.node && <span>节点 {intent.node}</span>}
                    {intent.profile && <span>画像 {intent.profile}</span>}
                    {intent.claimed_by && <span>认领 {shortID(intent.claimed_by)}</span>}
                    {attempts.length > 0 && <span>尝试 {attempts.length} 次</span>}
                    {intent.steer_count ? <span>续话 {intent.steer_count}</span> : null}
                    {intent.say_count ? <span>插话 {intent.say_count}</span> : null}
                  </div>

                  <IntentBrief intent={intent} />
                  <IntentDoc doc={intent.doc} />
                  <RunMeta run={run} pulse={pulse} onRunsChanged={onRunsChanged} />
                  <RunCase run={run} />
                </div>
              </details>
            </>
          ) : (
            <>
              <IntentBrief intent={intent} />
              <AttemptChain
                intent={intent}
                attempts={attempts}
                selectedRunID=""
                onPickRun={onPickRun}
                onOpenRuns={onNavigate ? () => onNavigate('runs') : undefined}
              />
              <IntentDoc doc={intent.doc} />
              <IntentFacts intent={intent} live={false} onOpenFact={onOpenFact} />
              <EmptyState
                compact
                icon={Activity}
                title="这条意图还没有派发过"
                description="它还在排队，等指挥官或操作员发起第一次运行；派发后这里显示这一轮的运行状态与事件。"
                action={
                  onOpenIntent ? (
                    <Button size="xs" variant="outline" onClick={() => onOpenIntent(intent.id)}>
                      去图谱
                    </Button>
                  ) : undefined
                }
              />
            </>
          )}
        </div>
      </div>
    </section>
  )
}

function IntentBrief({ intent }: { intent: Intent }) {
  return (
    <>
      {intent.description && (
        <DetailSection label="意图说明">
          <div className="saker-inset saker-break max-h-28 overflow-y-auto whitespace-pre-wrap rounded-lg px-3 py-2.5 text-[12px] leading-relaxed text-ink-2">
            {intent.description}
          </div>
        </DetailSection>
      )}

      {intent.notes && intent.notes.length > 0 && (
        <DetailSection label="备注">
          <div className="space-y-1.5">
            {intent.notes.map((n, i) => (
              <div key={i} className="flex items-start gap-2 rounded-lg border border-hairline px-2.5 py-1.5 text-[12px]">
                <Badge size="sm" variant={n.kind === 'retry' ? 'warning' : 'info'}>
                  {n.kind === 'retry' ? '重派备注' : n.kind === 'steer' ? '续话备注' : n.kind}
                </Badge>
                <span className="saker-break min-w-0 flex-1 leading-relaxed text-ink-2">{n.text}</span>
                <span className="shrink-0 font-mono text-[11px] text-ink-3">{fmtTime(n.at)}</span>
              </div>
            ))}
          </div>
        </DetailSection>
      )}
    </>
  )
}

/**
 * 等待原因, named: the header badge can only say 「前提不成立」, and the board's
 * frontier row keeps bare fact ids. A tooltip is not an answer, so the ids are
 * links to the fact on the graph.
 */
function PremiseBlockers({
  frontier,
  onOpenFact,
}: {
  frontier?: FrontierItem
  onOpenFact?: (id: string) => void
}) {
  const { blocked, changed } = premiseBlockers(frontier)
  if (blocked.length === 0 && changed.length === 0) return null
  return (
    <div
      data-testid="premise-blockers"
      className="rounded-lg border border-caution/25 bg-caution/[0.07] px-3 py-2 text-[12px] leading-relaxed text-ink-2"
    >
      {blocked.length > 0 && (
        <>
          <div>
            前提不成立：这 {blocked.length} 条前提现在不能用于派发（原因由后端记录），派发会被拒绝。
          </div>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {blocked.map((row) => (
              <FactLink key={row.id} id={row.id} reason={row.reason} onOpenFact={onOpenFact} />
            ))}
          </div>
        </>
      )}
      {changed.length > 0 && (
        <>
          <div className={cn(blocked.length > 0 && 'mt-2')}>
            前提的断言版本已变：旧结论不能代替新内容，派发前请重新核对这 {changed.length} 条。
          </div>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {changed.map((id) => (
              <FactLink key={id} id={id} onOpenFact={onOpenFact} />
            ))}
          </div>
        </>
      )}
    </div>
  )
}

/** A fact id as a link when the graph can open it, plain text otherwise. */
function FactLink({
  id,
  reason,
  onOpenFact,
}: {
  id: string
  /** Backend-recorded reason (不存在 / 已撤回 / 已否定); a missing fact has no page. */
  reason?: string
  onOpenFact?: (id: string) => void
}) {
  const text = `font-mono text-[11px] saker-nums`
  const label = reason ? `${id}（${reason}）` : id
  if (!onOpenFact || reason === '不存在') {
    return <span className={cn(text, 'text-ink-3')}>{label}</span>
  }
  return (
    <button
      type="button"
      onClick={() => onOpenFact(id)}
      title={`在项目图谱里打开 ${id}`}
      className={cn(
        text,
        'rounded px-1 text-ink-2 underline-offset-2 transition-colors hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
      )}
    >
      {label}
    </button>
  )
}

/**
 * 关联证据: the facts this direction declared, read from the same ledger query the
 * facts page uses (`GET /api/intents/{id}`). Status and evidence count are shown
 * as stored; a premise the page did not receive is listed as missing, never as
 * proven. Evidence itself is opened from the fact, not copied here.
 */
function IntentFacts({
  intent,
  live,
  onOpenFact,
}: {
  intent: Intent
  /** A live attempt declares facts; a settled direction almost never changes. */
  live?: boolean
  onOpenFact?: (id: string) => void
}) {
  const detail = usePoll(
    () => api.intent(intent.id),
    live ? 8000 : 30000,
    [intent.id],
  )
  const summary = useMemo(() => {
    const data = detail.data
    if (!data) return undefined
    return summarizeIntentFacts(data.facts ?? [], {
      premiseIDs: intent.from_fact_ids,
      total: data.facts_total,
    })
  }, [detail.data, intent.from_fact_ids])

  return (
    <section data-testid="intent-facts" className="min-w-0 border-t border-hairline pt-3">
      <div className="mb-2 flex flex-wrap items-baseline gap-2">
        <h3 className="text-[12px] font-semibold text-ink">关联证据 · 这条意图声明的事实</h3>
        {summary && (
          <span className="ml-auto flex flex-wrap items-center gap-x-2 font-mono text-[11px] saker-nums text-ink-3">
            <span>共 {fmtInt(summary.total)} 条</span>
            <span>已复核 {fmtInt(summary.verified)}</span>
            <span>未复核 {fmtInt(summary.unverified)}</span>
            {summary.closed > 0 && <span>已否决/撤回 {fmtInt(summary.closed)}</span>}
            <span>证据 {fmtInt(summary.evidence)} 份</span>
          </span>
        )}
      </div>

      {detail.error ? (
        <div className="rounded-lg border border-hairline px-3 py-2.5 text-[12px] text-ink-3">
          读不到这条意图的事实清单：{detail.error}。这不代表它没有声明过事实，重试或稍后再看。
        </div>
      ) : !summary ? (
        <CaseSkeleton rows={2} />
      ) : summary.rows.length === 0 ? (
        <div className="rounded-lg border border-dashed border-hairline px-3 py-2.5 text-[12px] text-ink-3">
          账本里还没有这条意图声明的事实。派发后写出的结论会出现在这里，并带各自的复核状态与证据数。
        </div>
      ) : (
        <div className="max-h-64 overflow-y-auto rounded-lg border border-hairline">
          {summary.rows.map((row) => (
            <button
              key={row.id}
              type="button"
              data-testid={`intent-fact-${row.id}`}
              onClick={() => onOpenFact?.(row.id)}
              disabled={!onOpenFact}
              title={`${row.id} · ${row.rawStatus}${row.version ? ` · v${row.version}` : ''}${row.role === 'premise' ? ' · 前提' : ''}`}
              className="flex w-full items-start gap-2 border-b border-hairline/70 px-2.5 py-2 text-left transition-colors last:border-0 enabled:hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50 disabled:cursor-default"
            >
              <Badge size="sm" variant={kindVariant(row.kind)}>
                {row.kind}
              </Badge>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[12px] text-ink">{row.title}</span>
                <span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11px] saker-nums text-ink-3">
                  <span
                    className={cn(
                      row.rawStatus === 'verified' && 'text-success',
                      (row.rawStatus === 'rejected' || row.rawStatus === 'withdrawn') && 'text-warning',
                    )}
                  >
                    {row.status}
                  </span>
                  {row.version > 0 && <span>v{row.version}</span>}
                  <span>证据 {row.evidence}</span>
                  {row.role === 'premise' && <span className="text-ink-2">前提</span>}
                  <span>{fmtTime(row.createdAt)}</span>
                </span>
              </span>
            </button>
          ))}
        </div>
      )}

      {summary && summary.missingPremises.length > 0 && (
        <div className="mt-1.5 text-[11px] leading-relaxed text-ink-3">
          前提里有 {summary.missingPremises.length} 条不在这次返回的事实里（
          {summary.missingPremises.join('、')}）：页面不能据此认定它们成立，点开图谱或等下一轮刷新再核对。
        </div>
      )}
      {summary && summary.truncated && (
        <div className="mt-1.5 text-[11px] text-ink-3">
          服务端共有 {fmtInt(summary.total)} 条，这里只列出 {fmtInt(summary.listed)} 条；上面的计数只覆盖列出的部分。
        </div>
      )}
      {summary && summary.rows.length > 0 && (
        <div className="mt-1.5 text-[11px] text-ink-3">
          内容来自账本，不是 case 目录的快照；证据本体在事实详情里打开。
        </div>
      )}
    </section>
  )
}

function AttemptChain({
  intent,
  attempts,
  selectedRunID,
  onPickRun,
  onOpenRuns,
}: {
  intent: Intent
  attempts: Run[]
  selectedRunID: string
  onPickRun: (id: string) => void
  /** Cross-direction logs and native sessions stay on the run page. */
  onOpenRuns?: () => void
}) {
  return (
    <DetailSection
      label="历次运行"
      extra={
        attempts.length > 0 ? (
          <span className="font-mono saker-nums">
            {attempts.length} 次 · 最近 {fmtTime(attempts[attempts.length - 1].started_at)}
          </span>
        ) : undefined
      }
    >
      {attempts.length === 0 ? (
        <div className="rounded-lg border border-dashed border-hairline px-3 py-2.5 text-[12px] text-ink-3">
          {intent.status === 'pending' ? '尚未派发（排队中）' : '账本里还没有这条意图的 run'}
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-1.5">
          {attempts.map((r, i) => {
            const active = r.id === selectedRunID
            return (
              <Chip
                key={r.id}
                data-testid={`attempt-tab-${r.id}`}
                active={active}
                onClick={() => onPickRun(r.id)}
                title={`${r.id} · ${runStatusLabel(r)}${r.stop_reason ? ` · ${r.stop_reason}` : ''}${r.cancel_cause ? ` · ${cancelCauseText(r.cancel_cause)}` : ''}${r.exit_reason ? ` · ${r.exit_reason}` : ''}`}
                className="gap-1.5 rounded-md px-2 py-1 text-[11px] font-normal leading-4"
              >
                <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', runDot(r))} />
                <span className="font-mono saker-nums">#{i + 1}</span>
                <span>{runStatusLabel(r)}</span>
                <span className="font-mono saker-nums opacity-70">{fmtDuration(r.started_at, r.ended_at)}</span>
              </Chip>
            )
          })}
        </div>
      )}
      {onOpenRuns && (
        <div className="mt-1.5">
          <Button
            size="xs"
            variant="ghost"
            onClick={onOpenRuns}
            title="运行页按意图与角色列出全部运行、事件流和原生会话；这里只留这条意图的几次尝试。"
          >
            在运行页查全部意图与原生会话 →
          </Button>
        </div>
      )}
    </DetailSection>
  )
}

function IntentDoc({ doc }: { doc?: string }) {
  const [open, setOpen] = useState(false)
  if (!doc || !doc.trim()) return null
  return (
    <div className="rounded-lg border border-hairline">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-[12px] text-ink-2 transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <FileText className="h-3.5 w-3.5 shrink-0 text-ink-3" />
        约束与交接 · doc
        <span className="ml-auto font-mono text-[11px] text-ink-3">{open ? '收起' : '展开'}</span>
      </button>
      {open && (
        <pre className="saker-break max-h-64 overflow-auto whitespace-pre-wrap border-t border-hairline px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
          {doc}
        </pre>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// One attempt: its ledger row and the case directory it was handed. Case files
// are keyed by intent (the API reads the intent's case dir), events by run.
// ---------------------------------------------------------------------------

/**
 * The visible status of one attempt: which attempt, its state, why it stopped,
 * and — while it is live — how long it has been quiet. This is the one block
 * the operator must see without opening anything.
 */
function RunStatusLine({
  run,
  pulse,
  attemptIndex,
  onOpenRun,
}: {
  run: Run
  pulse?: RunPulse
  attemptIndex: number
  onOpenRun?: (id: string) => void
}) {
  const live = run.status === 'running'
  const quiet = pulse?.idle_seconds ?? 0
  const ctxTokens = pulse?.context_tokens ?? 0
  const ctxWindow = pulse?.context_window ?? 0
  const pressure = ctxWindow > 0 ? ctxTokens / ctxWindow : 0

  return (
    <div data-testid="attempt-meta" className="saker-inset rounded-lg">
      <div className="flex flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
        <span className="text-[11px] font-medium text-ink-3">第 {attemptIndex || 1} 次</span>
        <Badge size="sm" variant={runStatusVariant(run)} title={`run 状态：${run.status}`}>
          {runStatusLabel(run)}
        </Badge>
        {run.stop_reason && <span className="font-mono text-[11px] text-ink-3">stop: {run.stop_reason}</span>}
        <span className="saker-break min-w-0 truncate font-mono text-[11px] text-ink-3">{run.id}</span>
        <button
          type="button"
          className="ml-auto rounded px-1 text-[11px] font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => onOpenRun?.(run.id)}
        >
          运行详情 →
        </button>
      </div>

      <div className="space-y-2.5 px-3 py-2.5">
        {run.status === 'canceled' && (
          // 一行一个宽度：长原因（操作员原话、多句）在窄卡里被双列切成竖条。
          // 类别 already sits in the badge above, so the body is who + why only.
          <div className="rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-2">
            <div className="text-[11px] font-medium text-warning">取消原因</div>
            <div className="saker-break mt-1 text-[12px] leading-relaxed text-ink-2" title={cancelCauseText(run.cancel_cause)}>
              {runCancelDetail(run)}
            </div>
          </div>
        )}

        {live && pulse && (
          <div className="rounded-lg bg-surface-2/60 px-2.5 py-2">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
              <span
                className={cn(
                  'flex items-center gap-1.5 font-mono saker-nums',
                  quiet >= 180 ? 'text-warning' : 'text-ink-3',
                )}
              >
                <span className={cn('h-1.5 w-1.5 rounded-full', quiet >= 180 ? 'bg-warning' : 'motion-safe:animate-pulse bg-info')} />
                静默 {fmtIdle(quiet)}
              </span>
              <span className="font-mono saker-nums text-ink-3">{fmtInt(pulse.events)} 事件</span>
              {ctxWindow > 0 && (
                <span className="font-mono saker-nums text-ink-3">
                  上下文 {fmtInt(ctxTokens)}/{fmtInt(ctxWindow)}
                </span>
              )}
              {pulseLine(pulse) && (
                <span className="min-w-0 flex-1 truncate text-ink-2" title={pulseLine(pulse)}>
                  {pulseLine(pulse)}
                </span>
              )}
            </div>
            {ctxWindow > 0 && (
              <Meter
                value={ctxTokens}
                max={ctxWindow}
                tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
                className="mt-1.5 h-1"
                aria-label="执行者上下文压力"
              />
            )}
          </div>
        )}

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] saker-nums text-ink-3">
          <span>开始 {fmtTime(run.started_at)}</span>
          <span>耗时 {fmtDuration(run.started_at, run.ended_at)}</span>
          <span>tokens {fmtInt(run.tokens ?? 0)}</span>
          <span>事件 {fmtInt(run.events)}</span>
        </div>
      </div>
    </div>
  )
}

/** Events of the selected attempt, five rows by default, rest on demand. */
function RunEvents({ run }: { run: Run }) {
  const live = run.status === 'running'
  const events = usePoll(() => api.events(run.id, 160), live ? 2000 : 15000)
  return (
    <EventsPanel
      events={events.data ?? []}
      loading={events.loading}
      error={events.error}
      live={live}
      runKey={run.id}
      maxVisible={5}
    />
  )
}

function RunMeta({
  run,
  pulse,
  onRunsChanged,
}: {
  run: Run
  pulse?: RunPulse
  onRunsChanged?: () => void
}) {
  const [dismissing, setDismissing] = useState(false)
  const [dismissError, setDismissError] = useState('')

  async function dismissWarnings() {
    setDismissing(true)
    setDismissError('')
    try {
      await api.dismissRunWarnings(run.id)
      onRunsChanged?.()
    } catch (e) {
      setDismissError(e instanceof Error ? e.message : String(e))
    } finally {
      setDismissing(false)
    }
  }

  return (
    <div className="space-y-2.5">
      <div className="grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
        <Meta label="开始">{fmtTime(run.started_at)}</Meta>
        <Meta label="耗时">{fmtDuration(run.started_at, run.ended_at)}</Meta>
        <Meta label="tokens">{fmtInt(run.tokens ?? 0)}</Meta>
        <Meta label="事件">{fmtInt(run.events)}</Meta>
        <Meta label="事实">{fmtInt(run.declared_facts)}</Meta>
        {run.node && <Meta label="节点">{run.node}</Meta>}
        {run.model && <Meta label="模型" title={run.model}>{run.model}</Meta>}
        {run.profile_source && run.profile_source !== 'default' && (
          <Meta label="画像">{run.resolved_profile || run.profile || run.profile_source}</Meta>
        )}
        {run.generation && run.generation > 1 ? <Meta label="代">{`第 ${run.generation} 代`}</Meta> : null}
        {run.continued_from && <Meta label="续自" title={run.continued_from}>{shortID(run.continued_from)}</Meta>}
        {run.handoff_from && (
          <Meta label="交接自" title={`${run.handoff_from} v${run.handoff_version ?? '?'}`}>
            {shortID(run.handoff_from)}
          </Meta>
        )}
        {run.brief_path && <Meta label="brief" title={run.brief_path}>{run.brief_path}</Meta>}
      </div>

      {(run.warnings?.length ?? 0) > 0 &&
        (run.warnings_dismissed ? (
          <div className="text-[11px] text-ink-3">
            交接告警已忽略（{run.warnings!.length} 条，原始记录仍保留在账本）
          </div>
        ) : (
          <details>
            <summary className="cursor-pointer rounded text-[11px] text-warning focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
              交接告警 {run.warnings!.length} 条（非致命）
            </summary>
            <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[11px] leading-relaxed text-ink-2">
              {run.warnings!.map((w, i) => (
                <li key={i} className="saker-break">{w}</li>
              ))}
            </ul>
            {run.status === 'done' && (
              <div className="mt-1.5 flex flex-wrap items-center gap-2">
                <Button size="xs" variant="ghost" onClick={dismissWarnings} disabled={dismissing}>
                  {dismissing ? '忽略中…' : '忽略这些告警'}
                </Button>
                {dismissError && <span className="text-[11px] text-destructive">{dismissError}</span>}
              </div>
            )}
          </details>
        ))}
      {(run.infos?.length ?? 0) > 0 && (
        <details>
          <summary className="cursor-pointer rounded text-[11px] text-ink-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
            交接提示 {run.infos!.length} 条（非问题）
          </summary>
          <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[11px] leading-relaxed text-ink-3">
            {run.infos!.map((info, i) => (
              <li key={i} className="saker-break">{info}</li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

interface GraphSnapshot {
  scope?: string
  facts?: unknown[]
  goals?: unknown[]
  intents?: { open?: unknown[]; running?: unknown[]; parked?: unknown[]; done?: unknown[] }
}

function RunCase({ run }: { run: Run }) {
  const live = run.status === 'running'
  const ctx = usePoll(() => api.runContext(run.id), live ? 4000 : 0)
  const data = ctx.data
  const graph = data?.graph as GraphSnapshot | undefined

  const counts = useMemo(() => {
    const g = graph?.intents
    return {
      facts: graph?.facts?.length ?? 0,
      open: g?.open?.length ?? 0,
      running: g?.running?.length ?? 0,
      goals: graph?.goals?.length ?? 0,
    }
  }, [graph])

  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h3 className="text-[12px] font-semibold text-ink">本次 case 与派发输入</h3>
        <span className="text-[11px] text-ink-3">文件按意图目录读取</span>
      </div>

      <div className="mt-3 space-y-4">
        {ctx.error && (
          <Callout tone="destructive">
            {ctx.error.includes('意图不存在')
              ? `这条 run 的意图不在账本里（intent_id=${run.intent_id || '空'}）：这是账本不一致，不是普通执行者。`
              : ctx.error}
          </Callout>
        )}

        <div className="grid gap-4 md:grid-cols-2">
          <CaseBlock
            title="派发简报"
            extra={
              data?.prompt ? (
                <>
                  <span className="font-mono saker-nums">{fmtInt(data.prompt.length)}</span> 字
                </>
              ) : run.brief_path ? (
                '归档未提供正文'
              ) : (
                '未记录'
              )
            }
          >
            {!data ? (
              <CaseSkeleton />
            ) : data.prompt ? (
              <pre className="saker-break max-h-64 overflow-auto whitespace-pre-wrap rounded-lg bg-surface-2/50 px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
                {data.prompt}
              </pre>
            ) : (
              <div className="space-y-1.5 text-[11px] leading-relaxed text-ink-3">
                <div>case 目录里没有 prompt.txt。</div>
                {run.brief_path ? (
                  <div className="saker-break font-mono">简报已归档：{run.brief_path}（当前接口不返回这份归档正文）</div>
                ) : (
                  <div>这条 run 没有归档简报（续跑/唤醒沿用原会话，不重写简报）。</div>
                )}
              </div>
            )}
          </CaseBlock>

          <CaseBlock
            title="只读图 · graph.json"
            extra={
              graph ? (
                <span className="block max-w-[220px] truncate font-mono" title={graph.scope || ''}>
                  scope: {graph.scope || '—'}
                </span>
              ) : (
                '未记录'
              )
            }
          >
            {!data ? (
              <CaseSkeleton rows={2} />
            ) : graph ? (
              <div className="grid grid-cols-2 gap-x-4 gap-y-3">
                <StatTile label="事实" value={fmtInt(counts.facts)} />
                <StatTile label="开放意图" value={fmtInt(counts.open)} />
                <StatTile label="在跑" value={fmtInt(counts.running)} />
                <StatTile label="目标" value={fmtInt(counts.goals)} />
              </div>
            ) : (
              <div className="text-[11px] text-ink-3">没有 graph.json。</div>
            )}
          </CaseBlock>

          {/* The declared facts are NOT repeated here: the ledger section above
              (关联证据) is the same list with the stored status, evidence count and
              a link to each fact. Two lists of the same facts would disagree
              the moment one of them is filtered. */}

          <CaseBlock
            title="case 文件"
            extra={
              <>
                <span className="font-mono saker-nums">{data?.files?.length ?? 0}</span> 个
              </>
            }
          >
            {(data?.files?.length ?? 0) === 0 ? (
              <div className="text-[11px] text-ink-3">目录为空。</div>
            ) : (
              <div className="max-h-56 overflow-y-auto rounded-lg border border-hairline">
                {data!.files!.map((f) => (
                  <div
                    key={f.name}
                    className="flex items-center gap-2 border-b border-hairline/70 px-3 py-1.5 last:border-0"
                  >
                    <FileText className="h-3.5 w-3.5 shrink-0 text-ink-3" />
                    <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-2">{f.name}</span>
                    <span className="shrink-0 font-mono text-[11px] saker-nums text-ink-3">{fmtBytes(f.size)}</span>
                  </div>
                ))}
              </div>
            )}
          </CaseBlock>
        </div>
      </div>
    </div>
  )
}

function EventsPanel({
  events,
  loading,
  error,
  live,
  runKey,
  maxVisible,
}: {
  events: RunEvent[]
  loading: boolean
  error?: string
  live: boolean
  runKey: string
  /** Show only the newest N rows until the operator asks for the rest. */
  maxVisible?: number
}) {
  const [expanded, setExpanded] = useState(false)
  const limit = expanded || !maxVisible ? 80 : maxVisible
  const shown = events.slice(-limit)
  const hidden = Math.max(0, events.length - shown.length)
  // Open on the newest event even for a finished run; while the operator is at
  // the bottom, appended events keep the view there. Scrolling up pauses that
  // until the explicit jump below.
  //
  // Deliberately NO `content-visibility: auto` on these rows: the panel pins
  // itself to the bottom by reading `scrollHeight`, and deferred rows would
  // only report their `contain-intrinsic-size` placeholder — the list would
  // stop short of the newest event and then jump once those rows rendered.
  const latest = useLatestScroll<HTMLDivElement>(events.length, runKey)

  return (
    <section>
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-[12px] font-semibold text-ink">事件流 · 这一条 run</h3>
        <span className="ml-auto flex items-center gap-1.5 text-[11px] text-ink-3">
          {live && <span className="h-1.5 w-1.5 motion-safe:animate-pulse rounded-full bg-success" />}
          <span className="font-mono saker-nums">{fmtInt(events.length)}</span> 事件
          {!latest.pinned && (
            <Button size="xs" variant="ghost" onClick={latest.jump} title="跳到这条 run 的最新事件">
              <ArrowDownToLine className="h-3.5 w-3.5" />
              {latest.unread > 0 ? `${latest.unread} 条新事件` : '回到最新'}
            </Button>
          )}
        </span>
      </div>
      <div className="mt-2">
        {/* The viewport stays mounted in every state: the latest-scroll hook
            attaches its listener to this node once, and a conditional wrapper
            that only appears after the first poll would leave that listener
            behind (the jump button would never show). */}
        <div
          ref={latest.ref}
          className="max-h-64 overflow-y-auto overscroll-contain rounded-lg border border-hairline [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin]"
          aria-busy={loading}
        >
          {error ? (
            <div className="p-2"><Callout tone="destructive">{error}</Callout></div>
          ) : loading && events.length === 0 ? (
            <div className="p-2"><CaseSkeleton rows={3} /></div>
          ) : events.length === 0 ? (
            <EmptyState
              compact
              icon={Activity}
              title="还没有事件"
              description="这一条 run 产出工具事件后会出现在这里；结束的 run 只保留历史事件。"
            />
          ) : (
            shown.map((ev, i) => (
              <div
                key={`${ev.at}-${i}`}
                className="flex items-start gap-2 border-b border-hairline/60 px-3 py-1.5 last:border-0"
              >
                <span className="w-[92px] shrink-0 whitespace-nowrap font-mono text-[11px] saker-nums text-ink-3">
                  {fmtTime(ev.at)}
                </span>
                <Badge size="sm" variant={ev.kind === 'turn_ended' ? 'info' : ev.kind === 'error' ? 'danger' : 'muted'}>
                  {ev.kind}
                </Badge>
                <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2" title={ev.text ?? ''}>
                  {oneLine(ev.text ?? '', 160)}
                  {ev.kind === 'turn_ended' && ev.stop_reason ? ` · ${ev.stop_reason}` : ''}
                </span>
              </div>
            ))
          )}
          {hidden > 0 && !error && (
            <div className="border-t border-hairline px-3 py-1.5">
              <Button size="xs" variant="ghost" onClick={() => setExpanded(true)}>
                展开更早的 {hidden} 条
              </Button>
            </div>
          )}
        </div>
      </div>
    </section>
  )
}

/** A flat sub-block of the case: a 12px title and a hairline, not another card. */
function CaseBlock({
  title,
  extra,
  children,
}: {
  title: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="min-w-0 border-t border-hairline pt-3">
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-[12px] font-semibold text-ink">{title}</h3>
        {extra && <span className="ml-auto text-[11px] text-ink-3">{extra}</span>}
      </div>
      <div className="mt-2">{children}</div>
    </section>
  )
}

/** A quiet section inside the detail pane: 12px heading + hairline. */
function DetailSection({
  label,
  extra,
  children,
}: {
  label: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="min-w-0 border-t border-hairline pt-3 first:border-t-0 first:pt-0">
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-[12px] font-semibold text-ink">{label}</h3>
        {extra && <span className="ml-auto text-[11px] text-ink-3">{extra}</span>}
      </div>
      {children}
    </section>
  )
}

/** A labelled mono value: 11px label, 12px tabular number/ID. */
function Meta({ label, title, children }: { label: string; title?: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] leading-4 text-ink-3">{label}</div>
      <div className="truncate font-mono text-[12px] leading-5 saker-nums text-ink-2" title={title}>
        {children}
      </div>
    </div>
  )
}

/** A quiet loading row shaped like the text it replaces. */
function CaseSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="h-3.5 rounded bg-surface-3/70" style={{ width: `${88 - i * 14}%` }} />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Aggregation. One intent = one unit; attempts are its `purpose == ""` runs.
// ---------------------------------------------------------------------------

interface Unit {
  intent: Intent
  /** Attempts of this intent, oldest first (the attempt chain order). */
  attempts: Run[]
  live?: Run
  latest?: Run
  /** Live heartbeat from /api/board (running intents only). */
  pulse?: RunPulse
  /** The open direction's premise state, when the board included it. */
  frontier?: FrontierItem
}

interface SupportCounts {
  commander: number
  verifier: number
  report: number
  other: number
  total: number
}

/** The two axes of the roster: what a direction is doing, then its detail. */
type GroupKey = 'live' | 'failed' | 'waiting' | 'queued' | 'held' | 'parked' | 'done'

const GROUP_ORDER: GroupKey[] = ['live', 'failed', 'waiting', 'queued', 'held', 'parked', 'done']

const GROUP_LABEL: Record<GroupKey, string> = {
  live: '在跑',
  failed: '失败 / 已停止',
  waiting: '等待结果',
  queued: '排队',
  held: '已停止',
  parked: '已停放',
  done: '完成',
}

/** Group dots are semantic tints, never saturated blocks. */
const GROUP_DOT: Record<GroupKey, string> = {
  live: 'motion-safe:animate-pulse bg-success',
  failed: 'bg-destructive',
  waiting: 'bg-warning',
  queued: 'bg-info',
  held: 'bg-caution',
  parked: 'bg-ink-3/60',
  done: 'bg-ink-3/40',
}

function buildUnits(intents: Intent[], runs: Run[], board?: BoardResponse): Unit[] {
  const pulseByIntent = new Map<string, RunPulse>()
  for (const item of board?.running_intents ?? []) if (item.pulse) pulseByIntent.set(item.id, item.pulse)
  const frontierByIntent = new Map<string, FrontierItem>()
  for (const item of board?.frontier ?? []) frontierByIntent.set(item.id, item)

  const known = new Set(intents.map((i) => i.id))
  const byIntent = new Map<string, Run[]>()
  for (const r of runs) {
    // purpose != "" is a support run (commander / verifier / report): it belongs
    // to no executor. Only purpose == "" is an attempt on an intent.
    if (r.purpose) continue
    if (!known.has(r.intent_id)) continue // shown as an orphan row instead
    const list = byIntent.get(r.intent_id) ?? []
    list.push(r)
    byIntent.set(r.intent_id, list)
  }

  return intents.map((intent) => {
    // Chronological, not string order: two rows can carry different UTC
    // offsets (old writes, imported ledgers), and "T12:59Z" sorts before
    // "T17:03+08:00" even though it is later.
    const attempts = (byIntent.get(intent.id) ?? [])
      .toSorted((a, b) => timeValue(a.started_at) - timeValue(b.started_at))
    const latest = attempts[attempts.length - 1]
    const live = attempts.find((r) => r.status === 'running')
    return { intent, attempts, latest, live, pulse: pulseByIntent.get(intent.id), frontier: frontierByIntent.get(intent.id) }
  })
}

function groupUnits(units: Unit[]): Record<GroupKey, Unit[]> {
  const out: Record<GroupKey, Unit[]> = {
    live: [],
    failed: [],
    waiting: [],
    queued: [],
    held: [],
    parked: [],
    done: [],
  }
  for (const u of units) out[groupOf(u.intent)].push(u)
  return out
}

function groupOf(intent: Intent): GroupKey {
  switch (intent.status) {
    case 'running':
    case 'claimed':
    case 'reviewing':
      return 'live'
    case 'waiting':
      return 'waiting'
    case 'failed':
    case 'review_failed':
      return 'failed'
    case 'parked':
      return 'parked'
    case 'done':
      return 'done'
    case 'pending':
      return intent.hold ? 'held' : 'queued'
    default:
      return 'queued'
  }
}

/** Live groups lead with what moved last; the queue leads with what waited longest. */
function sortGroup(key: GroupKey, list: Unit[]): Unit[] {
  if (key === 'queued' || key === 'held') {
    return list.toSorted((a, b) => timeValue(a.intent.created_at) - timeValue(b.intent.created_at))
  }
  return list.toSorted((a, b) => timeValue(b.intent.updated_at) - timeValue(a.intent.updated_at))
}

/** ISO timestamp → comparable milliseconds; an unparsable value sorts oldest. */
function timeValue(iso?: string): number {
  if (!iso) return 0
  const n = Date.parse(iso)
  return Number.isNaN(n) ? 0 : n
}

function supportCounts(runs: Run[]): SupportCounts {
  const out: SupportCounts = { commander: 0, verifier: 0, report: 0, other: 0, total: 0 }
  for (const r of runs) {
    if (!r.purpose) continue
    out.total++
    switch (r.purpose) {
      case 'commander':
        out.commander++
        break
      case 'verifier':
        out.verifier++
        break
      case 'report':
        out.report++
        break
      default:
        out.other++
    }
  }
  return out
}

/** An intent run whose intent is gone. The page must show it, not drop it. */
function orphanRuns(intents: Intent[], runs: Run[]): Run[] {
  const known = new Set(intents.map((i) => i.id))
  return runs.filter((r) => !r.purpose && !known.has(r.intent_id))
}

// ---------------------------------------------------------------------------
// Labels. A run's stop cause is displayed through runStatusLabel (lib/format),
// which owns the one cancel-cause table shared with the run ledger; the 状态 page
// adds the why ("failed 4m23s" vs "第 2 次完成") around it.
// ---------------------------------------------------------------------------

function lastStopText(run: Run): string {
  if (run.status === 'canceled') return runCancelReason(run)
  if (run.stop_reason) return run.stop_reason
  return statusLabel(run.status)
}

function intentStateLabel(intent: Intent, failure?: ReviewFailure | null): string {
  // Operator stop is not a harness failure: the word has to say "a human
  // stopped this and only a retry brings it back".
  if (intent.status === 'parked') return '已停放'
  if (intent.hold) return '已停止（人工）'
  if (intent.status === 'claimed') return '派发中'
  if (intent.status === 'waiting') return '等下游意图'
  if (intent.status === 'running') return '运行中'
  if (intent.status === 'reviewing') return '复核中'
  // Derived, not a new status: a verdict that said no is a different thing from
  // a review phase that never produced one.
  if (intent.status === 'review_failed') return reviewFailureLabel(failure)
  return statusLabel(intent.status)
}

/** The one-line state chip on a roster row: small area, semantic tint only. */
function stateTint(intent: Intent): string {
  if (intent.hold) return 'bg-caution/10 text-caution'
  if (intent.status === 'failed' || intent.status === 'review_failed') return 'bg-destructive/10 text-destructive'
  if (intent.status === 'reviewing') return 'bg-review/10 text-review'
  if (intent.status === 'waiting') return 'bg-warning/10 text-warning'
  if (intent.hold) return 'bg-caution/10 text-caution'
  if (intent.status === 'claimed' || intent.status === 'pending') return 'bg-warning/10 text-warning'
  if (intent.status === 'running') return 'bg-info/10 text-info'
  if (intent.status === 'done') return 'text-ink-3'
  if (intent.status === 'parked') return 'bg-surface-2 text-ink-3'
  return 'bg-surface-2 text-ink-3'
}

function stateDot(intent: Intent): string {
  if (intent.status === 'claimed') return 'bg-warning'
  if (intent.status === 'running') return 'motion-safe:animate-pulse bg-success'
  if (intent.status === 'reviewing') return 'motion-safe:animate-pulse bg-review'
  if (intent.status === 'pending') return intent.hold ? 'bg-caution' : 'bg-warning'
  switch (intent.status) {
    case 'done':
      return 'bg-ink-3/40'
    case 'failed':
    case 'review_failed':
      return intent.hold ? 'bg-caution' : 'bg-destructive'
    case 'parked':
      return 'bg-ink-3/60'
    case 'waiting':
      return 'bg-warning'
    default:
      return intent.hold ? 'bg-caution' : 'bg-info'
  }
}

function runDot(run: Run): string {
  switch (run.status) {
    case 'running':
      return 'motion-safe:animate-pulse bg-success'
    case 'failed':
      return 'bg-destructive'
    case 'canceled':
      return 'bg-warning'
    default:
      return 'bg-ink-3/40'
  }
}

function intentStatusVariant(intent: Intent): 'success' | 'danger' | 'warning' | 'info' | 'muted' {
  if (intent.hold && intent.status !== 'parked') return 'warning'
  switch (intent.status) {
    case 'done':
      return 'success'
    case 'failed':
      return 'danger'
    case 'parked':
      return 'warning'
    case 'running':
    case 'claimed':
      return 'info'
    default:
      return intent.hold ? 'warning' : 'muted'
  }
}

function runStatusVariant(run: Run): 'success' | 'danger' | 'warning' | 'info' | 'muted' {
  switch (run.status) {
    case 'done':
      return 'success'
    case 'failed':
      return 'danger'
    case 'canceled':
      return 'warning'
    case 'running':
      return 'info'
    default:
      return 'muted'
  }
}

function pulseLine(pulse: RunPulse): string {
  const text = oneLine(pulse.last_text ?? '', 40)
  if (text) return text
  if (pulse.last_tool) return pulse.last_tool
  return pulse.last_kind ?? ''
}

function fmtIdle(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/** One-second clock while something is actually moving; frozen otherwise. */
function useTick(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

/**
 * One goal's receipt, opened from the inbox (`?view=state&goal=`).
 *
 * The seal is historical (docs: a met goal is never un-set), so this panel
 * reports what the ledger says and, when the receipt is stale, which cited
 * facts changed after the stamp. It never re-stamps, and it never claims the
 * goal is unmet: the operator decides what to do, on the record itself.
 */
function GoalReceiptPanel({
  goalID,
  goals,
  loading,
  error,
  onOpenFact,
  onClose,
}: {
  goalID: string
  goals?: Goal[]
  loading: boolean
  error?: string
  onOpenFact?: (factID: string) => void
  onClose: () => void
}) {
  const goal = findGoal(goals, goalID)
  return (
    <section className="saker-panel shrink-0 rounded-xl" aria-label="目标验收回执" data-testid="state-goal-receipt">
      <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
        <Flag className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" />
        <h2 className="saker-section-title">目标验收回执</h2>
        <span className="font-mono text-[11px] text-ink-3">{goalID}</span>
        {goal?.receipt_stale && (
          <Badge size="sm" variant="warning">依据已变化</Badge>
        )}
        <Button size="xs" variant="ghost" className="ml-auto" onClick={onClose}>
          关闭
        </Button>
      </header>
      <div className="space-y-2 px-4 py-3">
        {loading && !goals ? (
          <SkeletonCard />
        ) : error ? (
          <Callout tone="warning">
            读取目标失败：<span className="saker-break">{error}</span>。这不代表回执有效或无效。
          </Callout>
        ) : !goal ? (
          <p className="text-[12px] leading-relaxed text-ink-2" data-testid="state-goal-missing">
            没有找到目标 <span className="font-mono">{goalID}</span>：可能已被删除，或这个项目里没有它。
          </p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <StatusIndicator
                compact
                status={goal.closed ? 'idle' : goal.met ? 'success' : 'warning'}
                label={goal.closed ? '已关闭' : goal.met ? '已达成' : '未达成'}
              />
              {goal.receipt_stale && <span className="text-[11px] text-warning">历史完成证明保留，需重新判断依据是否还成立</span>}
            </div>
            <p className="saker-break text-[13px] leading-relaxed text-ink">{goal.statement}</p>
            {goal.start && (
              <p className="saker-break text-[11.5px] leading-relaxed text-ink-3">起点：{goal.start}</p>
            )}
            {goal.met && (
              <div className="space-y-1 text-[12px] leading-relaxed text-ink-2">
                <div>{goal.note ? `为什么达成：${goal.note}` : '盖章时未记录理由（旧数据）'}</div>
                {goal.completed_at && <div className="text-[11px] text-ink-3">达成于 <span className="font-mono saker-nums">{fmtTime(goal.completed_at)}</span></div>}
              </div>
            )}
            {(goal.receipt_changed ?? []).length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="text-[11px] text-ink-3">变更事实</span>
                {goal.receipt_changed!.map((fid) => (
                  <button
                    key={fid}
                    type="button"
                    className="rounded border border-warning/30 bg-card/60 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-warning/60 hover:text-warning focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    onClick={() => onOpenFact?.(fid)}
                    disabled={!onOpenFact}
                    title={onOpenFact ? `在图谱中打开事实 ${fid}` : fid}
                  >
                    {fid}
                  </button>
                ))}
              </div>
            )}
            {(goal.from ?? []).length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="text-[11px] text-ink-3">达成依据</span>
                {goal.from!.map((fid) => (
                  <button
                    key={fid}
                    type="button"
                    className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-primary/40 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    onClick={() => onOpenFact?.(fid)}
                    disabled={!onOpenFact}
                    title={onOpenFact ? `在图谱中打开事实 ${fid}` : fid}
                  >
                    {fid}
                  </button>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </section>
  )
}
