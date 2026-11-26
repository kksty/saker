import { RunEventStream } from '@/components/RunEventStream'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Activity, Ban, Bot, Copy, FileText, RefreshCw, Search, ShieldCheck, TriangleAlert } from 'lucide-react'
import { cn } from '@cyber/theme'
import {
  Badge, Button, Callout, EmptyState, Input, ScrollArea, Skeleton, SkeletonList,
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue, StatusIndicator,
} from '@cyber/ui'
import { Page, Panel, pageGutter } from '@/components/Page'
import { WorkspaceSidebar } from '@/components/WorkspaceSidebar'
import { RunEvidenceView } from '@/components/RunEvidenceView'
import { RunReview } from '@/components/RunReview'
import { GuardrailCard } from '@/components/GuardrailCard'
import { RunEvaluator } from '@/components/RunEvaluator'
import { RunCommandsPanel, RunHistoryPanel } from '@/components/RunNativeSession'
import { TokenBreakdownCard } from '@/components/TokenBreakdownCard'
import { api, ApiError } from '@/lib/api'
import { deepEqual } from '@/lib/equal'
import {
  cancelCauseText,
  fmtDuration,
  fmtInt,
  fmtTime,
  intentWaitText,
  purposeLabel,
  runCancelReason,
  runMatchesFilter,
  runStatusLabel,
  statusKind,
} from '@/lib/format'
import { useConsoleLocation, useLoad, usePoll } from '@/lib/hooks'
import { useSessionState } from '@/lib/session-state'
import { navigate } from '@/lib/navigation'
import { guardrailCallEventIndex } from '@/lib/guardrail'
import { createRequestGate } from '@/lib/requests'
import { groupRunHistory, participantsByIntent, runExecutor, selectRunHistory } from '@/lib/run-history'
import type { Event, Intent, Run } from '@/lib/types'

const PROFILE_SOURCE_LABEL: Record<string, string> = {
  explicit: '显式指定',
  default: '实例默认',
  fallback: '凭据故障后切换',
  default_fallback: '默认模型暂停后自动切换',
  project_worker: '项目 worker 档',
  project_worker_fallback: '项目默认暂停后自动切换',
  project_strong: '项目 strong 档',
  commander_strong: '项目指挥官默认',
  commander_strong_fallback: '指挥官默认暂停后自动切换',
}

/**
 * Support runs are not executor attempts and must look different at a glance:
 * the commander is a session of its own (own model, own turns), verifier/report
 * are one-shot support jobs. A badge with an icon is the marker; the raw
 * purpose string stays in the tooltip.
 */
function PurposeBadge({ purpose }: { purpose: string }) {
  const common = 'shrink-0'
  switch (purpose) {
    case 'commander':
      return (
        <Badge size="sm" variant="primary" className={common} title="指挥官会话：决策与派发，不执行具体意图">
          <Bot className="h-3.5 w-3.5" /> 指挥官
        </Badge>
      )
    case 'verifier':
      return (
        <Badge size="sm" variant="info" className={common} title="复核 run：独立重跑并给事实下 verdict">
          <ShieldCheck className="h-3.5 w-3.5" /> 复核
        </Badge>
      )
    case 'report':
      return (
        <Badge size="sm" variant="muted" className={common} title="报告 run：从账本生成报告">
          <FileText className="h-3.5 w-3.5" /> 报告
        </Badge>
      )
    default:
      return (
        <Badge size="sm" variant="muted" className={common} title={`purpose=${purpose}`}>
          {purposeLabel(purpose)}
        </Badge>
      )
  }
}

export function Runs({
  onOpenIntent,
  onOpenGraphIntent,
}: {
  onOpenIntent?: (id: string) => void
  /** Cross-page link (docs/34 §4): open the run's direction on the graph as
   *  `s:<intent id>`. Optional — without it no graph entry is rendered. */
  onOpenGraphIntent?: (intentID: string) => void
}) {
  const runs = usePoll(api.runs, 4000)
  // A run is keyed by run id; the direction it executed is the unit the
  // operator reasons about. Joining intents into the list (and the detail)
  // is what makes "which direction is this run?" answerable, and lets the
  // page deep-link back to the state page for that intent.
  const intents = usePoll(api.intents, 6000)
  // The operator asks "which executor did the commander send this to", so the
  // left list names the worker (participant) and its session, not an intent id.
  const participants = usePoll(api.participants, 8000)
  // The frozen input versions name fact ids; resolve their titles and links. It
  // is a read-only index, and an unloaded/old id still renders as its id.
  const facts = usePoll(() => api.facts({ limit: 500 }), 10000)
  // Deep link from the fleet page: ?view=runs&run=<id>. Resolving it here keeps
  // the URL the single source of "which run am I looking at".
  const location = useConsoleLocation()
  const requestedRun = location.searchParams.get('run')
  const requestedGroup = location.searchParams.get('run_group')
  // A pending approval names its session, not its run: resolve the owner before
  // the history selector runs so the detail panel opens on the right attempt.
  const requestedSession = location.searchParams.get('session')
  const requestedCall = location.searchParams.get('call')
  const sessionRunID = useMemo(
    () => (requestedSession ? runs.data?.find(r => r.session_id === requestedSession)?.id ?? null : null),
    [requestedSession, runs.data],
  )
  const [filter, setFilter] = useSessionState('runs.search', '')
  const [events, setEvents] = useState<Event[]>([])
  const [eventRun, setEventRun] = useState<string | null>(null)
  const [eventError, setEventError] = useState('')
  // A run without a journal is a fact about the run, not a console failure:
  // verifier/report runs started before D138 never wrote one. Render it as an
  // explanation instead of the red callout used for real read errors.
  const [eventMissing, setEventMissing] = useState(false)
  const [eventsLoading, setEventsLoading] = useState(false)
  // Which event stream row the execution review last pointed at. Highlights the
  // raw row and scrolls it into view; the stream keeps rendering every event.
  const [reviewFocus, setReviewFocus] = useState<number | null>(null)
  // How pointing at one exact call went. A miss stays on screen: the operator
  // asked for one call and must learn it is not in the stream, not be shown the
  // nearest row as if it were the one they asked for.
  const [locateNote, setLocateNote] = useState<{ tone: 'info' | 'warning'; text: string } | null>(null)
  const streamRef = useRef<HTMLDivElement | null>(null)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState('')
  const [canceling, setCanceling] = useState<string | null>(null)
  const [dismissingWarnings, setDismissingWarnings] = useState('')
  const cancelLock = useRef(false)
  const [notice, setNotice] = useState('')
  const [logRefresh, setLogRefresh] = useState(0)
  const refreshing = runs.loading || runs.refreshing || intents.refreshing || participants.refreshing || facts.refreshing
  function refreshAll() { runs.reload(); intents.reload(); participants.reload(); facts.reload(); setLogRefresh(n => n + 1) }

  /** Operator "ignore" for a finished run's non-fatal handoff warnings. The
   *  rows stay in the ledger; the list/detail stop presenting them as an alarm. */
  async function dismissWarnings(run: Run) {
    setDismissingWarnings(run.id)
    setError('')
    try {
      await api.dismissRunWarnings(run.id)
      refreshAll()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setDismissingWarnings('')
    }
  }


  async function copyBrief(path: string) {
    try {
      await navigator.clipboard.writeText(path)
      setCopied(path)
      window.setTimeout(() => setCopied(''), 1500)
    } catch {
      setError('复制失败：浏览器没有剪贴板权限')
    }
  }

  function selectRun(id: string) {
    const params = new URLSearchParams(window.location.search)
    params.set('run', id)
    params.delete('run_group')
    navigate(`${window.location.pathname}?${params.toString()}`)
  }

  function selectGroup(key: string, replace = false) {
    const params = new URLSearchParams(window.location.search)
    params.delete('run')
    params.set('run_group', key)
    navigate(`${window.location.pathname}?${params.toString()}`, replace)
  }

  /** A frozen input fact opens on the graph as the selected fact node: the
   *  evidence view is a pointer, so clicking it must be able to leave the list. */
  function openFactInGraph(factID: string) {
    navigate(`${window.location.pathname}?view=graph#${encodeURIComponent(`f:${factID}`)}`)
  }

  const groups = useMemo(() => groupRunHistory(runs.data ?? []), [runs.data])
  const intentByID = useMemo(() => {
    const m = new Map<string, Intent>()
    for (const i of intents.data ?? []) m.set(i.id, i)
    return m
  }, [intents.data])
  const intentOf = (r: Run): Intent | undefined => (!r.purpose && r.intent_id ? intentByID.get(r.intent_id) : undefined)
  const executorByIntent = useMemo(() => participantsByIntent(participants.data ?? []), [participants.data])
  const executorOf = (r: Run) => runExecutor(r, intentOf(r), executorByIntent)
  // Filter runs by what an operator actually has in hand: a run id from an
  // alert, an intent id from the state page, an executor/session, or a title
  // and model they remember.
  const needle = filter.trim().toLowerCase()
  const matchesFilter = (r: Run): boolean => {
    if (runMatchesFilter(r, intentOf(r), filter)) return true
    const executor = executorOf(r)
    return [executor.participantID, executor.sessionID, executor.label, executor.detail]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(needle)
  }
  // Search every attempt but keep the full group so historical matches remain
  // reachable in the selector, even when the latest run has a different model.
  const visible = groups.filter(g => g.runs.some(matchesFilter))
  const { group: currentGroup, run: current } = selectRunHistory(groups, requestedRun ?? sessionRunID, requestedGroup)
  const selected = current?.id ?? null
  const currentIntent = current ? intentOf(current) : undefined
  // A yield is not a stop: the run gave its slot back and will resume when the
  // condition holds. Printing it as a cancel reason repeats the wait line below
  // and reads as a decision someone made.
  const yieldOnly =
    current?.cancel_cause === 'operator_wait' ||
    current?.cancel_cause === 'action_wait'
  const currentExecutor = current ? executorOf(current) : null
  const firstLoad = runs.loading && !runs.data

  // Keep the initial group stable when another executor starts during polling.
  // The group still follows new attempts within its own history.
  const currentGroupKey = currentGroup?.key
  useEffect(() => {
    if (!requestedRun && !requestedGroup && currentGroupKey) selectGroup(currentGroupKey, true)
  }, [requestedRun, requestedGroup, currentGroupKey])

  useEffect(() => {
    if (eventRun !== selected) { setEvents([]); setNotice(''); setError('') }
    setEventRun(selected)
    setEventError('')
    setEventMissing(false)
    setReviewFocus(null)
    setLocateNote(null)
    setEventsLoading(Boolean(selected))
    if (!selected) return
    const requests = createRequestGate<Event[]>()
    const tick = () => {
      if (document.visibilityState === 'hidden') return
      void requests.run(() => api.events(selected, 400, true), {
        start: () => {},
        value: evs => {
          // The journal poll parses a fresh array every 3 s. Handing it to
          // setState unconditionally re-rendered every event group (the memo
          // boundary compares the array by reference) even when no new event had
          // arrived; keep the previous array when the content is identical.
          setEvents(prev => (prev.length === evs.length && deepEqual(prev, evs) ? prev : evs))
          setEventError('')
          setEventMissing(false)
        },
        error: e => {
          setEventMissing(e instanceof ApiError && e.status === 404)
          setEventError(e instanceof Error ? e.message : String(e))
        },
        finish: () => setEventsLoading(false),
      })
    }
    tick()
    const timer = window.setInterval(tick, 3000)
    return () => {
      requests.invalidate()
      window.clearInterval(timer)
    }
  }, [selected, logRefresh])

  // The execution review points at the raw stream instead of replacing it:
  // selecting a review row highlights that event and scrolls it into view.
  useEffect(() => {
    if (reviewFocus === null) return
    const row = streamRef.current?.querySelector(`[data-event-index="${reviewFocus}"]`)
    row?.scrollIntoView({ block: 'nearest' })
  }, [reviewFocus])

  // `?call=` names one exact provider call; the run's journal is the only
  // authority for whether it is there. While the stream loads, say nothing.
  // Once it has loaded, highlight it or report the miss (deleted, or older than
  // the loaded window) — never scroll to a neighbour as if it were the call.
  // The parameter is consumed either way so a 3 s poll cannot re-point forever.
  useEffect(() => {
    if (!requestedCall || !selected || eventRun !== selected) return
    const consume = () => {
      const params = new URLSearchParams(window.location.search)
      params.delete('call')
      params.delete('session')
      navigate(`${window.location.pathname}?${params.toString()}`, true)
    }
    if (eventMissing) {
      setLocateNote({ tone: 'warning', text: `这次运行没有日志记录，无法定位调用 ${requestedCall}。` })
      consume()
      return
    }
    if (eventsLoading || eventError) return
    const index = guardrailCallEventIndex(events, requestedCall)
    setReviewFocus(index >= 0 ? index : null)
    setLocateNote(
      index >= 0
        ? { tone: 'info', text: `已定位调用 ${requestedCall}（事件 #${index + 1}）` }
        : { tone: 'warning', text: `事件流（最近 ${events.length} 条）里没有调用 ${requestedCall}：记录可能已删除，或早于列表窗口。` },
    )
    consume()
  }, [requestedCall, selected, eventRun, eventsLoading, eventError, eventMissing, events])

  // A session-only link that matches no run is a fact about the ledger, not a
  // console failure: say so once instead of quietly showing another attempt.
  useEffect(() => {
    if (!requestedSession || requestedRun || sessionRunID || !runs.data) return
    setLocateNote({
      tone: 'warning',
      text: `没有找到会话 ${requestedSession.slice(0, 12)}… 对应的运行：审批所属的运行可能已不在账本里。`,
    })
  }, [requestedSession, requestedRun, sessionRunID, runs.data])

  // Same-page entry from this run's approval card: the stream is already loaded,
  // so the index is answerable now.
  function locateCallInStream(callID: string) {
    const index = guardrailCallEventIndex(events, callID)
    if (index < 0) {
      setLocateNote({ tone: 'warning', text: `当前事件流（${events.length} 条）里没有调用 ${callID}：可能早于列表窗口。` })
      return
    }
    setReviewFocus(index)
    setLocateNote({ tone: 'info', text: `已定位调用 ${callID}（事件 #${index + 1}）` })
  }

  async function cancel(run: Run) {
    if (cancelLock.current) return
    cancelLock.current = true
    setCanceling(run.id)
    setError('')
    setNotice('')
    try {
      await api.cancelRun(run.id)
      setNotice('取消请求已接受，正在同步运行状态。')
      runs.reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { cancelLock.current = false; setCanceling(null) }
  }

  const streamLoading = eventsLoading || eventRun !== selected
  const [sidebarOpen, setSidebarOpen] = useState(false)

  return (
    // The header carries no refresh: the page polls every 4s and the list has
    // its own compact refresh for "I do not want to wait".
    <Page title="运行" subtitle="按执行者或角色查看运行历史与事件。" className="@container" scroll={false}>
      <div className={cn('flex h-full min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain py-4 sm:py-5 @3xl:grid @3xl:grid-cols-[minmax(240px,29%)_minmax(0,1fr)] @3xl:gap-4 @3xl:overflow-hidden', pageGutter)}>
        {/* Left: who ran. Scan-first, weaker than the detail. */}
        <WorkspaceSidebar
          label="运行列表"
          summary={runs.error || intents.error || participants.error || facts.error ? '读取失败' : `${fmtInt(groups.length)} 组运行`}
          expanded={sidebarOpen}
          onToggle={() => setSidebarOpen(open => !open)}
        >
          <Panel
            title={`执行者与角色（${visible.length}${needle ? ` / ${groups.length}` : ''}）`}
            className="flex max-h-[min(52dvh,24rem)] min-h-0 flex-1 flex-col @3xl:max-h-none"
            bodyClassName="flex min-h-0 flex-1 flex-col p-0"
            extra={
              <span className="saker-nums font-mono text-[11px] text-ink-3">
                共 {fmtInt(runs.data?.length ?? 0)} 次
              </span>
            }
          >
            <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
              <div className="relative min-w-0 flex-1 basis-[160px]">
                <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
                <Input
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  placeholder="搜索 run / 执行者 / 会话 / 意图标题 / 模型"
                  className="h-8 pl-7 text-[12px]"
                  aria-label="过滤运行"
                  type="search"
                  inputMode="search"
                  enterKeyHint="search"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              {filter.trim() && (
                <Button size="xs" variant="ghost" className="shrink-0" onClick={() => setFilter('')}>
                  清除
                </Button>
              )}
              <Button size="icon-sm" variant="outline" className="shrink-0" aria-label="刷新执行者与运行记录" title="刷新执行者与运行记录" disabled={refreshing} onClick={refreshAll}>
                <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? 'motion-safe:animate-spin' : ''}`} />
              </Button>
            </div>
            {/* Every poll on this page feeds the same list: a failed intent or
                roster read degrades titles, executor labels and deep links, so it
                must not be silent just because the run rows themselves loaded. */}
            {(runs.error || intents.error || participants.error || facts.error) && (
              <div className="shrink-0 border-b border-hairline p-2.5">
                <Callout tone="destructive">
                  <div className="flex flex-wrap items-center gap-2">
                    <span>
                      {runs.error || intents.error || participants.error || facts.error}
                      {runs.error && runs.updatedAt ? ` · 上次更新 ${fmtTime(new Date(runs.updatedAt).toISOString())}` : ''}
                    </span>
                    <Button size="xs" variant="outline" onClick={refreshAll}>重试</Button>
                  </div>
                </Callout>
              </div>
            )}
            <div
              className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2 [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin]"
              aria-busy={firstLoad}
            >
              {firstLoad && <SkeletonList count={5} />}
              {!firstLoad && !runs.error && groups.length === 0 && (
                <EmptyState
                  compact
                  icon={Activity}
                  title="还没有运行"
                  description="创建意图后，执行记录会出现在这里。点「新建意图」开始。"
                  action={
                    <Button size="sm" onClick={() => navigate(`${window.location.pathname}?view=overview#new`)}>
                      新建意图
                    </Button>
                  }
                />
              )}
              {groups.length > 0 && visible.length === 0 && (
                <EmptyState
                  compact
                  icon={Search}
                  title="没有匹配的运行"
                  description={`没有匹配「${filter.trim()}」的运行。`}
                  action={<Button size="sm" variant="outline" onClick={() => setFilter('')}>清除筛选</Button>}
                />
              )}
              <p role="status" aria-live="polite" className="sr-only">
                显示 {visible.length} / {groups.length} 组运行
              </p>
              <ul role="list" className="space-y-0.5" aria-label="执行者与运行记录">
                {visible.map((group, index) => {
                  const r = group.runs[0]
                  const executor = executorOf(r)
                  const active = currentGroup?.key === group.key
                  return (
                    <li
                      key={group.key}
                      className={`rounded-lg transition-colors ${
                        index >= 6 ? '[contain-intrinsic-size:auto_9rem] [content-visibility:auto]' : ''
                      } ${
                        active ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]' : 'hover:bg-surface-2'
                      }`}
                    >
                      <button
                        type="button"
                        onClick={() => selectGroup(group.key)}
                        aria-pressed={active}
                        className="w-full rounded-lg px-2.5 py-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      >
                        <div className="flex items-center gap-2">
                          <StatusIndicator compact status={statusKind(r.status)} label={runStatusLabel(r)} />
                          {r.purpose ? <PurposeBadge purpose={r.purpose} /> : <Badge size="sm" variant="muted">执行者</Badge>}
                          {(r.warnings?.length ?? 0) > 0 && !r.warnings_dismissed && (
                            <span className="text-[11px] font-medium text-warning" title={r.warnings!.join('\n')}>
                              ⚠ {r.warnings!.length}
                            </span>
                          )}
                          <span className="saker-nums ml-auto font-mono text-[11px] text-ink-3">
                            {fmtDuration(r.started_at, r.ended_at)}
                          </span>
                        </div>
                        <div className="mt-1 truncate text-[13px] font-medium text-ink" title={executor.label}>
                          {executor.kind === 'executor' && <span className="text-ink-3">指挥官 → </span>}
                          {executor.label}
                        </div>
                        {needle && !matchesFilter(r) && (
                          <div className="text-[11px] font-medium text-primary">匹配历史运行，可在详情下拉选择</div>
                        )}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </div>
          </Panel>
        </WorkspaceSidebar>

        {/* Right: the selected run — heavier: status strip, config, stream. */}
        <Panel
          className="flex min-h-0 min-w-0 shrink-0 flex-col"
          bodyClassName="flex min-h-0 flex-1 flex-col p-0 @3xl:overflow-y-auto @3xl:overscroll-contain"
          title={currentIntent?.title || (current ? purposeLabel(current.purpose || '') || '运行详情' : '运行详情')}
          extra={
            current && (
              <>
                <span className="saker-nums hidden font-mono text-[11px] text-ink-3 sm:inline">
                  {fmtTime(current.started_at)}
                </span>
                {current.status === 'running' && (
                  <Button
                    size="xs"
                    variant="ghost"
                    className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                    disabled={canceling !== null}
                    onClick={() => cancel(current)}
                  >
                    <Ban className="h-3.5 w-3.5" /> {canceling === current.id ? '取消中…' : '取消'}
                  </Button>
                )}
              </>
            )
          }
        >
          {firstLoad && (
            <div className="p-3">
              <SkeletonList count={5} />
            </div>
          )}
          {!current && !firstLoad && !runs.error && (
            <EmptyState
              icon={Activity}
              title={requestedRun || requestedGroup ? '未找到指定运行' : '还没有运行'}
              description="按执行者或角色选择一组运行，再从历史下拉框查看具体记录。"
              action={
                requestedRun || requestedGroup ? (
                  <Button size="sm" variant="outline" onClick={refreshAll}>刷新运行记录</Button>
                ) : undefined
              }
            />
          )}
          {current && (
            <div className="flex min-h-0 flex-col @3xl:h-full">
              {/* Status strip: the one-glance verdict + run totals. */}
              <div className="shrink-0 space-y-1.5 border-b border-hairline px-3 py-2.5">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px] text-ink-2">
                  <StatusIndicator compact status={statusKind(current.status)} label={runStatusLabel(current)} />
                  <span>耗时 <span className="saker-nums font-mono text-ink">{fmtDuration(current.started_at, current.ended_at)}</span></span>
                  {current.warnings?.length && !current.warnings_dismissed ? (
                    <span className="text-warning">⚠ {current.warnings.length} 条交接告警</span>
                  ) : null}
                </div>
                {(current.status === 'canceled' || current.exit_reason) && !yieldOnly && (
                  <p className="text-[12px] leading-relaxed text-warning">
                    {current.status === 'canceled' ? runCancelReason(current) : current.exit_reason}
                  </p>
                )}
                {/* 传输自愈与关闭原因都是账本字段，界面照抄；不按错误文本推断。 */}
                {(current.auto_resume_attempt ?? 0) > 0 && (
                  <p data-testid="run-auto-resume" className="text-[12px] leading-relaxed text-warning">
                    网络中断，自动重跑第 {current.auto_resume_attempt} 次（上限 3）
                  </p>
                )}
                {current.status === 'failed' && current.failure_class === 'intentional' && (
                  <p data-testid="run-intentional-close" className="text-[12px] leading-relaxed text-ink-2">
                    宿主主动关闭，未续跑
                  </p>
                )}
                {/* 这条 run 所属意图在等什么：下游意图带着当前状态，不必再逐条点开。 */}
                {currentIntent?.wait && (
                  <p data-testid="run-intent-wait" className="text-[12px] leading-relaxed text-ink-2">
                    {intentWaitText(currentIntent.wait)}
                  </p>
                )}
                {notice && <p role="status" className="text-[12px] text-ink-2">{notice}</p>}
                {error && <p role="alert" className="text-[12px] text-destructive">操作失败：{error}。请检查连接后重试。</p>}
                {/* Evaluator verdict: the pass/fail/error sentence already lives
                    in a status event; surface it before the raw stream. */}
                <RunEvaluator events={events} />
              </div>

              {/* One 12px toolbar: history, copy, evidence. */}
              <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
                {currentGroup ? (
                  <>
                    <span className="shrink-0 text-[11px] text-ink-3">历史（{currentGroup.runs.length}）</span>
                    <Select
                      value={requestedRun ? `run:${current.id}` : 'latest'}
                      onValueChange={value => value === 'latest' ? selectGroup(currentGroup.key) : selectRun(value.slice(4))}
                    >
                      <SelectTrigger className="h-8 min-w-0 flex-1 basis-[220px] text-[12px]" aria-label="选择历史运行">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent className="max-w-[calc(100vw-2rem)]">
                        <SelectItem value="latest">最新一次（自动跟随）</SelectItem>
                        {currentGroup.runs.map((run, index) => (
                          <SelectItem key={run.id} value={`run:${run.id}`} className="text-[12px] [&>span:last-child]:break-all">
                            {index === 0 ? '最新 · ' : ''}{fmtTime(run.started_at)} · {runStatusLabel(run)} · {run.generation ? `第 ${run.generation} 代 · ` : ''}{run.continued_from ? '续话 · ' : ''}{run.model ? `${run.model} · ` : ''}{run.id}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {requestedRun && current.id !== currentGroup.runs[0].id && <Badge size="sm" variant="secondary">查看历史</Badge>}
                  </>
                ) : null}
              </div>

              <RememberedRunDetails key={current.id} memoryKey={`runs.metadata.${current.id}`} className="shrink-0 border-b border-hairline bg-surface-2/20">
                <summary className="cursor-pointer select-none px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2/60">
                  运行配置与证据
                  {(current.warnings?.length ?? 0) > 0
                    ? current.warnings_dismissed
                      ? ' · 告警已忽略'
                      : ` · ${current.warnings!.length} 条告警`
                    : ''}
                  {(current.infos?.length ?? 0) > 0 ? ` · ${current.infos!.length} 条提示` : ''}
                </summary>
                <div className="max-h-[38vh] space-y-2 overflow-y-auto px-3 py-2 text-[12px]">
                  <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px]">
                    {(current.resolved_profile || current.profile) && (
                      <Badge size="sm" variant="secondary" title="历史运行记录：本次 run 所在的模型配置">
                        profile {current.resolved_profile || current.profile}
                      </Badge>
                    )}
                    {current.profile && current.profile !== current.resolved_profile && (
                      <Badge size="sm" variant="outline" title="派发时请求的 profile">
                        请求 {current.profile}
                      </Badge>
                    )}
                    {current.profile_source && (
                      <span className="text-ink-3">
                        {PROFILE_SOURCE_LABEL[current.profile_source] ?? current.profile_source}
                      </span>
                    )}
                    {current.model && <span className="text-ink-3">模型 {current.model}</span>}
                    {current.provider && <span className="text-ink-3">协议 {current.provider}</span>}
                    {current.context_window ? (
                      <span className="text-ink-3">上下文窗口 {fmtInt(current.context_window)}</span>
                    ) : null}
                    {current.purpose && <PurposeBadge purpose={current.purpose} />}
                    {current.generation && <span className="text-ink-3">第 {current.generation} 代</span>}
                  </div>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[11px] text-ink-3">
                    {current.queued_at && (
                      <>
                        <span>
                          复核排队{' '}
                          {current.queue_ms == null
                            ? fmtDuration(current.queued_at, current.execution_started_at ?? current.ended_at)
                            : `${(current.queue_ms / 1000).toFixed(1)}s`}
                        </span>
                        <span>
                          执行{' '}
                          {current.execution_ms == null
                            ? current.execution_started_at
                              ? fmtDuration(current.execution_started_at, current.ended_at)
                              : '尚未开始'
                            : `${(current.execution_ms / 1000).toFixed(1)}s`}
                        </span>
                      </>
                    )}
                    <span>Token {current.tokens == null ? '未记录' : fmtInt(current.tokens)}</span>
                    <span>工具失败 {current.tool_failures ?? '未记录'}</span>
                    {!!current.native_subagent_calls && (
                      <span>原生 subagent {current.native_subagent_calls} 次</span>
                    )}
                    {current.note_read && (
                      <span>
                        笔记输入 v{current.note_read.version} · 写入{' '}
                        {current.note_writes?.map((ref) => `v${ref.version}`).join('、') || '无回执'}
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    <span className="shrink-0 text-ink-3">意图</span>
                    {currentIntent ? (
                      onOpenIntent ? (
                        <button
                          type="button"
                          data-testid="run-intent-link"
                          onClick={() => onOpenIntent(currentIntent.id)}
                          className="min-w-0 truncate rounded text-left text-[12px] font-medium text-info transition-colors hover:bg-surface-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                          title="在状态页打开这条意图"
                        >
                          {currentIntent.title}
                          <span className="ml-1 font-mono text-[11px] text-ink-3">{currentIntent.id}</span>
                        </button>
                      ) : (
                        <span className="min-w-0 truncate text-[12px] text-ink-2">
                          {currentIntent.title}
                          <span className="ml-1 font-mono text-[11px] text-ink-3">{currentIntent.id}</span>
                        </span>
                      )
                    ) : !current.purpose && current.intent_id ? (
                      <span className="min-w-0 truncate font-mono text-[11px] text-warning" title="意图记录不在账本里">
                        {current.intent_id}（意图不在账本）
                      </span>
                    ) : (
                      <span className="text-ink-3">支持运行，不挂意图</span>
                    )}
                    {/* 跨页联动（docs/34 §4）：与「状态」入口并列的图谱入口。
                        只对有 intent_id 的 run 显示；support run 不挂意图，就没有。 */}
                    {onOpenGraphIntent && current.intent_id && (
                      <Button
                        size="xs"
                        variant="ghost"
                        data-testid="run-open-graph"
                        title="在图谱中选中这条意图"
                        onClick={() => onOpenGraphIntent(current.intent_id!)}
                      >
                        图谱
                      </Button>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    <span className="shrink-0 text-ink-3">执行者</span>
                    {currentExecutor?.participantID ? (
                      <span
                        className="min-w-0 truncate text-[12px] text-ink-2"
                        title={[currentExecutor.participantID, currentExecutor.sessionID].filter(Boolean).join(' · ')}
                      >
                        <span className="font-mono">{currentExecutor.participantID}</span>
                        {currentExecutor.sessionID && (
                          <span className="ml-1 font-mono text-[11px] text-ink-3">
                            {' · 会话 '}{currentExecutor.sessionID.slice(0, 10)}…
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className="text-[12px] text-ink-3">
                        {currentExecutor?.kind === 'executor' ? '执行者不在参与者列表' : '支持运行，没有执行者参与者'}
                      </span>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[11px] text-ink-3">
                    <span>
                      usage{' '}
                      {current.usage?.known
                        ? `in ${fmtInt(current.usage.input_tokens ?? 0)} / out ${fmtInt(current.usage.output_tokens ?? 0)} · 其中缓存输入 ${fmtInt(current.usage.cached_input_tokens ?? 0)} · ${fmtInt(current.usage.requests ?? 0)} req`
                        : current.usage
                          ? '未知（供应商未报告）'
                          : '未记录'}
                    </span>
                    {current.tool_failures !== undefined && <span>工具失败 {fmtInt(current.tool_failures)}</span>}
                    {current.exit_reason && current.status !== 'canceled' && (
                      <span className="truncate" title={current.exit_reason}>
                        exit {current.exit_reason}
                      </span>
                    )}
                  </div>
                  {current.status === 'canceled' && (
                    // Same shape as the 状态 page: label own line, reason full width.
                    <div className="min-w-0">
                      <div className="w-fit rounded border border-warning/25 bg-warning/12 px-1 font-medium text-warning">取消原因</div>
                      <div
                        className="saker-break mt-1 text-[11px] leading-relaxed text-ink-2"
                        title={cancelCauseText(current.cancel_cause)}
                      >
                        {runCancelReason(current)}
                      </div>
                    </div>
                  )}
                  {current.brief_path && (
                    <div className="flex items-center gap-1.5">
                      <span className="shrink-0 text-ink-3">任务书归档</span>
                      <span className="saker-break min-w-0 flex-1 truncate font-mono text-[11px] text-ink-2" title={current.brief_path}>
                        {current.brief_path}
                      </span>
                      <Button
                        size="icon-xs"
                        variant="ghost"
                        title="复制任务书路径（只读，不发起文件下载）"
                        onClick={() => copyBrief(current.brief_path!)}
                      >
                        <Copy className="h-3.5 w-3.5" />
                      </Button>
                      <span className="shrink-0 text-[11px] text-ink-3">
                        {copied === current.brief_path ? '已复制' : '只读路径'}
                      </span>
                      {current.brief_archive?.sha256 && (
                        <span className="shrink-0 font-mono text-[10px] text-ink-3" title={current.brief_archive.sha256}>
                          sha {current.brief_archive.sha256.slice(0, 12)}
                        </span>
                      )}
                    </div>
                  )}
                  {current.brief_archive?.status === 'pending' && (
                    <div className="saker-break rounded-md border border-warning/25 bg-warning/12 px-2.5 py-1.5 text-[11px] leading-relaxed text-warning" data-testid="brief-archive-pending">
                      任务书文件可能已经写成，但账本路径没有写回：本次下发无法按引用追溯。不要用后来的任务书措辞补写这份归档。
                    </div>
                  )}
                  {current.brief_archive?.status === 'write_failed' && (
                    <div className="saker-break rounded-md border border-destructive/25 bg-destructive/12 px-2.5 py-1.5 text-[11px] leading-relaxed text-destructive" data-testid="brief-archive-failed">
                      任务书归档写入失败：{current.brief_archive.error || '未知原因'}；本次下发无法追溯。
                    </div>
                  )}
                  {/* What this run was dispatched against, frozen: premise claim
                      versions and the context record versions it actually saw. */}
                  <RunEvidenceView run={current} facts={facts.data ?? []} onSelectFact={openFactInGraph} />
                  {/* The run's own outcome and its handoff delivery are two
                      different states: a done run can still owe facts or
                      evidence. Both are shown, with the exact declaration
                      spans an offline idempotent repair needs. */}
                  {current.ingest && (
                    <div
                      className={cn(
                        'space-y-1 rounded-md border px-2.5 py-2',
                        current.ingest.committed
                          ? 'border-hairline bg-surface-2/30'
                          : 'border-warning/25 bg-warning/[0.07]',
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] font-medium">
                        {current.ingest.committed ? (
                          <ShieldCheck className="h-3.5 w-3.5 text-ink-3" />
                        ) : (
                          <TriangleAlert className="h-3.5 w-3.5 text-warning" />
                        )}
                        <span className={current.ingest.committed ? 'text-ink-2' : 'text-warning'}>
                          交接入账：{current.ingest.committed ? '已全部落账' : '运行已结束，交付未入账'}
                        </span>
                        <span className="text-[11px] font-normal text-ink-3">
                          声明 {fmtInt(current.ingest.declared)} 条 · 事实{' '}
                          {fmtInt(current.ingest.persisted_facts)}/{fmtInt(current.ingest.facts)} ·
                          证据 {fmtInt(current.ingest.persisted_evidence)} · 打法{' '}
                          {fmtInt(current.ingest.persisted_playbooks)}/{fmtInt(current.ingest.playbooks)} ·
                          游标 {fmtInt(current.ingest.cursor_before)}
                          {current.ingest.committed
                            ? ` → ${fmtInt(current.ingest.cursor_after)}`
                            : `（停在 ${fmtInt(current.ingest.cursor_before)}）`}
                        </span>
                      </div>
                      {!current.ingest.committed && (
                        <>
                          {(current.ingest.pending ?? []).slice(0, 8).map((p, i) => (
                            <div key={`${p.declaration_id ?? p.location ?? i}`} className="saker-break text-[11px] leading-relaxed text-ink-2">
                              <span className="font-mono text-ink-1">{p.declaration_id || p.title || '未命名声明'}</span>
                              {p.location ? <span className="ml-1 font-mono text-ink-3">{p.location}</span> : null}
                              {(p.reasons ?? []).length > 0 ? <span>：{(p.reasons ?? []).join('；')}</span> : null}
                            </div>
                          ))}
                          {(current.ingest.pending ?? []).length > 8 && (
                            <div className="text-[11px] text-ink-3">
                              …还有 {(current.ingest.pending ?? []).length - 8} 条，见 run.ingest.pending
                            </div>
                          )}
                          {(current.ingest.reasons ?? []).length > 0 && (
                            <div className="saker-break text-[11px] leading-relaxed text-ink-3">
                              {(current.ingest.reasons ?? []).join('；')}
                            </div>
                          )}
                          <div className="text-[11px] text-ink-3">
                            修复只按同一批声明幂等补写账本，不重跑执行者、不重做外部动作：先停止 saker 并暂停项目，再执行
                            <span className="ml-1 font-mono">saker-repair --project &lt;项目&gt; --intent {current.intent_id} --ingest --commit</span>
                          </div>
                        </>
                      )}
                    </div>
                  )}
                  {current.wait_delivery && (
                    <div className="rounded-md border border-hairline bg-surface-2/30 px-2.5 py-1.5 text-[11px] text-ink-3">
                      等待结果交付：
                      {current.wait_delivery.status === 'received'
                        ? '执行者已收到'
                        : current.wait_delivery.status === 'archived'
                          ? '输入已归档，等待执行者确认'
                          : '结果已就绪，等待派发归档'}
                      {current.wait_delivery.brief_path ? (
                        <span className="ml-1 font-mono">{current.wait_delivery.brief_path}</span>
                      ) : null}
                    </div>
                  )}
                  {(current.warnings?.length ?? 0) > 0 &&
                    (current.warnings_dismissed ? (
                      <div className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-2 text-[11px] text-ink-3">
                        交接告警已忽略（{current.warnings!.length} 条，原始记录仍保留在账本）
                      </div>
                    ) : (
                      <div className="space-y-1 rounded-md border border-warning/25 bg-warning/[0.07] px-2.5 py-2">
                        <div className="flex flex-wrap items-center gap-1.5 text-[12px] font-medium text-warning">
                          <TriangleAlert className="h-3.5 w-3.5" /> 告警（{current.warnings!.length}）
                          {current.status === 'done' && current.intent_id && (
                            <Button
                              size="xs"
                              variant="ghost"
                              className="ml-auto"
                              disabled={dismissingWarnings === current.id}
                              onClick={() => dismissWarnings(current)}
                            >
                              {dismissingWarnings === current.id ? '忽略中…' : '忽略这些告警'}
                            </Button>
                          )}
                        </div>
                        {current.warnings!.map((w, i) => (
                          <div key={i} className="saker-break text-[11px] leading-relaxed text-ink-2">
                            {/subagent/i.test(w) && (
                              <Badge size="sm" variant="warning" className="mr-1">
                                原生 subagent
                              </Badge>
                            )}
                            {w}
                          </div>
                        ))}
                      </div>
                    ))}
                  {(current.infos?.length ?? 0) > 0 && (
                    <RememberedRunDetails memoryKey={`runs.infos.${current.id}`} className="rounded-md border border-hairline px-2.5 py-1.5">
                      <summary className="cursor-pointer text-[11px] text-ink-3">
                        交接提示（{current.infos!.length} 条，非问题）
                      </summary>
                      <div className="mt-1 space-y-1">
                        {current.infos!.map((info, i) => (
                          <div key={i} className="saker-break text-[11px] leading-relaxed text-ink-3">
                            {info}
                          </div>
                        ))}
                      </div>
                    </RememberedRunDetails>
                  )}
                </div>
              </RememberedRunDetails>

              {/* Tool approval for this run's session. Loaded on demand: merely
                  selecting a settled run must not dial its host (a pending read
                  is a management query that journals its own attempt). */}
              {current.session_id && (
                <RememberedRunDetails
                  key={`guardrail-${current.id}`}
                  memoryKey={`runs.guardrail.${current.id}`}
                  className="shrink-0 border-b border-hairline bg-surface-2/20"
                >
                  <summary className="cursor-pointer select-none px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2/60">
                    工具调用审批 · 会话 {current.session_id.slice(0, 10)}…
                  </summary>
                  <div className="px-3 py-2">
                    <GuardrailCard
                      session={current.session_id}
                      autoLoad={false}
                      onChanged={refreshAll}
                      onLocateCall={({ callID }) => locateCallInStream(callID)}
                    />
                  </div>
                </RememberedRunDetails>
              )}

              {/* Canonical host session: the native command menu and ListEvents
                  history. Both load on demand (one host dial each). */}
              {current.session_id && (
                <RememberedRunDetails
                  key={`native-${current.id}`}
                  memoryKey={`runs.native.${current.id}`}
                  className="shrink-0 border-b border-hairline bg-surface-2/20"
                >
                  <summary className="cursor-pointer select-none px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2/60">
                    原生会话（命令目录 / 宿主历史）
                  </summary>
                  <div className="max-h-[38vh] space-y-4 overflow-y-auto overscroll-contain px-3 py-2">
                    <RunCommandsPanel runID={current.id} sessionID={current.session_id} />
                    <RunHistoryPanel runID={current.id} sessionID={current.session_id} />
                  </div>
                </RememberedRunDetails>
              )}

              {/* Cost of the whole engagement, folded in here because this is
                  where the operator decides whether to call a run off. It is
                  project-scoped on purpose: one run carries one role, so the
                  "who is burning tokens" split only exists across runs — the
                  summary says so, or the numbers would read as this run's. */}
              <RunTokenSection key={`tokens-${current.id}`} runID={current.id} />

              {/* Event stream: mono, timestamps in ink-3, the raw source of truth. */}
              <div className="saker-panel-header sticky top-0 z-10 flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-hairline px-3 py-2">
                <h2 className="text-caption font-semibold text-ink">事件流</h2>
                <span className="saker-nums font-mono text-[11px] text-ink-3">{events.length} 条</span>
                {locateNote ? (
                  <span className={`text-[11px] ${locateNote.tone === 'warning' ? 'text-warning' : 'text-primary'}`}>{locateNote.text}</span>
                ) : reviewFocus !== null ? (
                  <span className="text-[11px] text-primary">已在下方定位审查条目 #{reviewFocus + 1}</span>
                ) : null}
                <span className="ml-auto text-[11px] text-ink-3">每 3 秒刷新</span>
              </div>
              <ScrollArea className="saker-run-stream min-h-0 @3xl:min-h-40 @3xl:flex-1">
                <div ref={streamRef} className="space-y-0.5 p-2">
                  {error && <Callout tone="destructive">{error}</Callout>}
                  {eventRun === selected && eventError && eventMissing && (
                    <Callout tone="info">
                      <div className="flex flex-wrap items-center gap-2">
                        <span>这次运行没有日志记录：{eventError}</span>
                        <Button size="xs" variant="outline" onClick={() => setLogRefresh(n => n + 1)}>重试日志</Button>
                      </div>
                    </Callout>
                  )}
                  {eventRun === selected && eventError && !eventMissing && (
                    <Callout tone="destructive">
                      <div className="flex flex-wrap items-center gap-2">
                        <span>日志更新失败，已保留上次记录。</span>
                        <Button size="xs" variant="outline" onClick={() => setLogRefresh(n => n + 1)}>重试日志</Button>
                      </div>
                      <details className="mt-1">
                        <summary className="cursor-pointer text-[11px] text-ink-3 hover:text-ink">错误详情</summary>
                        <div className="saker-break mt-1 font-mono text-[11px] text-ink-3">{eventError}</div>
                      </details>
                    </Callout>
                  )}
                  {streamLoading && (
                    <div aria-busy="true" className="space-y-1.5 py-1">
                      {Array.from({ length: 4 }).map((_, i) => (
                        <div key={i} className="flex items-center gap-2 px-1">
                          <Skeleton className="h-5 w-16 rounded" />
                          <Skeleton className="h-3.5 flex-1" />
                          <Skeleton className="h-3 w-12" />
                        </div>
                      ))}
                    </div>
                  )}
                  {eventRun === selected && !eventsLoading && !eventError && events.length === 0 && (
                    <EmptyState
                      compact
                      icon={Activity}
                      title="日志为空"
                      description="这次运行还没有写入事件；新一轮开始后，工具调用与模型用量会出现在这里。"
                    />
                  )}
                  {eventRun === selected && !eventsLoading && !eventError && events.length > 0 && (
                    <RunReview
                      events={events}
                      totalTokens={current.tokens}
                      focusIndex={reviewFocus}
                      onSelect={setReviewFocus}
                    />
                  )}
                  {/* `running` keeps the stream head's live dot pulsing while the
                      model is silent between events; the stream falls back to
                      "newest event within a few seconds" for a status poll that
                      has not caught up yet. */}
                  {eventRun === selected && <RunEventStream key={current.id} runID={current.id} sessionID={current.session_id} events={events} focusIndex={reviewFocus} running={current.status === 'running'} />}

                </div>
              </ScrollArea>
            </div>
          )}
        </Panel>
      </div>
    </Page>
  )
}

function RememberedRunDetails({memoryKey,className,children}:{memoryKey:string;className:string;children:ReactNode}) {
  const [open,setOpen]=useSessionState(memoryKey,false)
  return <details open={open} onToggle={e=>setOpen(e.currentTarget.open)} className={className}>{children}</details>
}

/**
 * The token split, folded into the run detail. Numbers are the project's, not
 * this run's: a run is one role (commander / executor / judge), so a per-role
 * split only exists across runs. The read happens on open — selecting a settled
 * run must not add a request that aggregates every run in the project.
 */
function RunTokenSection({ runID }: { runID: string }) {
  const [open, setOpen] = useSessionState(`runs.tokens.${runID}`, false)
  const usage = useLoad(() => (open ? api.usageBreakdown() : Promise.resolve(undefined)), [open])
  return (
    <details
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="shrink-0 border-b border-hairline bg-surface-2/20"
    >
      <summary className="cursor-pointer select-none px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2/60">
        Token 分账 · 本项目累计（指挥官 / 执行者 / 裁判）
      </summary>
      <div className="px-3 py-2">
        <TokenBreakdownCard breakdown={usage.data} error={usage.error} loading={usage.loading} />
      </div>
    </details>
  )
}
