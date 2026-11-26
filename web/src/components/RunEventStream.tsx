import { runStreamViewport } from '@/lib/run-stream'
import { useLatestScroll } from '@/lib/latest-scroll'
import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { ArrowDownToLine } from 'lucide-react'
import { Badge, Button, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@cyber/ui'
import { eventArguments, eventFailed, groupRunEvents, runEventMetrics, type EventGroup } from '@/lib/run-events'
import { evaluatorBadgeVariant, evaluatorRoundText, evaluatorVerdictLabel, parseEvaluatorEvent } from '@/lib/run-evaluator'
import { guardrailActionLabel, guardrailActionVariant, parseGuardrailEvent } from '@/lib/guardrail'
import { GuardrailEventRow } from '@/components/GuardrailEventRow'
import { MediaPreview } from '@/components/MediaPreview'
import { useScrollMemoryOn, readViewMemory, writeViewMemory } from '@/lib/view-memory'
import { fmtTime } from '@/lib/format'
import type { Event } from '@/lib/types'

/** How long after its last event a stream still counts as live (two 3 s polls). */
const LIVE_TAIL_MS = 6_000

/**
 * Whether the stream is still moving: the run itself is running, or its newest
 * event landed inside the last two poll intervals.
 *
 * The local timer is not redundant with the run status: an unchanged poll keeps
 * the previous payload/render (deep-equal in the data layer), so without a
 * deadline a just-finished stream would keep pulsing until something unrelated
 * changed the page.
 */
function useStreamLive(events: Event[], running: boolean): boolean {
  const lastAt = events.length > 0 ? Date.parse(events[events.length - 1].at) : Number.NaN
  const [live, setLive] = useState(running)
  useEffect(() => {
    if (running) {
      setLive(true)
      return
    }
    const remaining = Number.isFinite(lastAt) ? LIVE_TAIL_MS - (Date.now() - lastAt) : 0
    if (remaining <= 0) {
      setLive(false)
      return
    }
    setLive(true)
    const timer = window.setTimeout(() => setLive(false), remaining)
    return () => window.clearTimeout(timer)
  }, [running, lastAt])
  return live
}

/**
 * One collapsible event group.
 *
 * Extracted and memoised because the stream is the highest-churn list on the
 * page: polling appends events while the operator toggles filters and copies
 * JSON, and every one of those state flips used to rebuild all ~400 groups.
 * With this boundary only the groups whose `open`/`focus`/`copied` actually
 * changed re-render.
 *
 * No `content-visibility: auto` here on purpose: the scroll position of this
 * panel is remembered and restored by `useScrollMemoryOn`, and the runs page
 * calls `scrollIntoView` on a `[data-event-index]` target. Both depend on the
 * real box of every group, which deferred rows would not have.
 */
interface GroupCardProps {
  group: EventGroup
  events: Event[]
  /** The run this stream belongs to: media URLs are run-scoped. */
  runID: string
  /** The run's session: a pending guardrail review can approve against it. */
  sessionID?: string
  open: boolean
  focused: boolean
  copied: string
  /** The whole stream still belongs to a running (or just-active) run. */
  live: boolean
  onToggle: (key: string) => void
  onCopy: (key: string, value: unknown) => void
}

const GroupCard = memo(function GroupCard({ group, events, runID, sessionID, open, focused, copied, live, onToggle, onCopy }: GroupCardProps) {
  const first = events[group.indices[0]]
  const failed = group.indices.some((i) => eventFailed(events[i]))
  const source = first.tool || first.role
  const summary = first.text || source || first.kind
  // The evaluator's verdict is already folded into the status text; the row
  // badge lets the operator spot pass/fail/error without opening the group.
  const evaluator = parseEvaluatorEvent(first)
  // Guardrail decisions are a policy verdict of their own: record/review/block
  // get the same action badge in the collapsed row.
  const guardrail = parseGuardrailEvent(first)
  return (
    <article
      role="listitem"
      className={`rounded-lg border px-3 py-2 ${failed ? 'border-warning/40 bg-warning/5' : 'border-hairline'} ${focused ? 'bg-primary/10' : ''}`}
    >
      <button
        className="flex w-full items-start gap-2 rounded text-left focus-visible:outline-2 focus-visible:outline-ring"
        aria-expanded={open}
        onClick={() => onToggle(group.key)}
      >
        <span className="text-xs leading-5 text-ink-3" aria-hidden="true">{open ? '▾' : '▸'}</span>
        {/* The dot is the only live marker handed to the card: the card itself
            never animates (a whole-card effect on the highest-churn list is a
            per-frame paint cost with no extra information). */}
        <span
          aria-hidden="true"
          title={live ? '这次运行仍在运行，事件流可能继续追加' : undefined}
          className={`mt-[7px] inline-block h-1.5 w-1.5 shrink-0 rounded-full ${live ? 'bg-success' : failed ? 'bg-warning' : 'bg-ink-3/35'}`}
        />
        <span className="min-w-0 flex-1 space-y-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {failed && <Badge size="sm" variant="warning">异常</Badge>}
            <span className="saker-break line-clamp-2 text-[13px] font-medium leading-5 text-ink">
              {summary.slice(0, 160)}
            </span>
            {evaluator && (
              <Badge size="sm" variant={evaluatorBadgeVariant(evaluator)} title={first.text}>
                {evaluatorVerdictLabel(evaluator)}
                {evaluatorRoundText(evaluator) ? ` · ${evaluatorRoundText(evaluator)}` : ''}
              </Badge>
            )}
            {guardrail && (
              <Badge size="sm" variant={guardrailActionVariant(guardrail.action)} title={first.text}>
                {guardrailActionLabel(guardrail.action)}
              </Badge>
            )}
          </span>
          <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] leading-4 text-ink-3">
            {first.kind !== summary && <span className="font-mono">{first.kind}</span>}
            {source && source !== summary && <span className="saker-break font-mono">{source}</span>}
            {guardrail?.tool && guardrail.tool !== source && <span className="saker-break font-mono">{guardrail.tool}</span>}
            <span>回合 {group.turn || 1}</span>
            {group.result?.duration_ms !== undefined && <span>{(group.result.duration_ms / 1000).toFixed(2)}s</span>}
            <span className="ml-auto whitespace-nowrap">{fmtTime(first.at)}</span>
          </span>
        </span>
      </button>
      {group.indices.map((index) => {
        const event = events[index]
        // A preserved medium is the one payload whose content is a file: show
        // its thumbnail even while the group is collapsed, so the timeline
        // itself is scannable for screenshots/audio. Other events stay hidden.
        const mediaPath = event.kind === 'media' && event.text ? event.text : ''
        const guardrailEvent = parseGuardrailEvent(event)
        return (
          <div
            key={index}
            data-event-index={index}
            className={
              open
                ? 'mt-2 border-t border-hairline pt-2'
                : mediaPath
                  ? 'mt-1.5 border-t border-hairline pt-1.5'
                  : 'h-0 overflow-hidden'
            }
          >
            {!open && mediaPath && <MediaPreview runID={runID} path={mediaPath} />}
            {open && (
              <>
                <div className="mb-1 flex items-center gap-2 text-xs text-ink-3">
                  <span>{event.kind} {event.call_id}</span>
                  <Button size="xs" variant="ghost" onClick={() => onCopy(String(index), event)}>
                    {copied === String(index) ? '已复制' : '复制 JSON'}
                  </Button>
                </div>
                {guardrailEvent ? (
                  <GuardrailEventRow event={event} sessionID={sessionID} />
                ) : (
                  <pre className="saker-break whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{event.text || ''}</pre>
                )}
                {mediaPath && (
                  <div className="mt-2">
                    <div className="mb-1 text-xs text-ink-3">媒体预览（点击放大）</div>
                    <MediaPreview runID={runID} path={mediaPath} />
                  </div>
                )}
                {event.raw !== undefined && (
                  <>
                    <div className="mt-2 text-xs text-ink-3">
                      {event.kind === 'tool_call' ? '工具参数（解码）' : '原始事件 / 返回'}
                    </div>
                    <pre className="saker-break whitespace-pre-wrap text-[11px] text-ink-3">
                      {JSON.stringify(event.kind === 'tool_call' ? eventArguments(event) : event.raw, null, 2)}
                    </pre>
                  </>
                )}
              </>
            )}
          </div>
        )
      })}
    </article>
  )
})

export function RunEventStream({
  events,
  runID,
  sessionID,
  focusIndex,
  running = false,
}: {
  events: Event[]
  runID: string
  /** The run's session id: guardrail reviews can approve against it. */
  sessionID?: string
  focusIndex: number | null
  /** The selected run's status: "running" keeps the stream head pulsing even
   *  while the model is silent between events. */
  running?: boolean
}) {
  const scope = `run-stream:${window.location.pathname}:${runID}`
  const [expanded, setExpanded] = useState<string[]>(() => readViewMemory<string[]>(scope, 'expanded') ?? [])
  const [filter, setFilter] = useState(() => readViewMemory<string>(scope, 'filter') ?? 'all')
  const [copied, setCopied] = useState('')
  const [error, setError] = useState('')
  // First visit lands on the newest event; a remembered position still wins.
  const root = useScrollMemoryOn<HTMLDivElement>(scope, runStreamViewport, 'end')
  const groups = useMemo(() => groupRunEvents(events), [events])
  const metrics = useMemo(() => runEventMetrics(events), [events])
  const live = useStreamLive(events, running)
  const expand = useCallback((next: string[]) => {
    setExpanded(next)
    writeViewMemory(scope, 'expanded', next)
  }, [scope])
  useEffect(() => {
    if (focusIndex === null) return
    const group = groups.find((g) => g.indices.includes(focusIndex))
    if (group && !expanded.includes(group.key)) expand([...expanded, group.key])
  }, [focusIndex, groups])
  const copy = useCallback(async (key: string, value: unknown) => {
    try {
      await navigator.clipboard.writeText(typeof value === 'string' ? value : JSON.stringify(value, null, 2))
      setCopied(key)
      setError('')
    } catch {
      setError('复制失败：剪贴板不可用')
    }
  }, [])
  const toggle = useCallback((key: string) => {
    setExpanded((current) => {
      const next = current.includes(key) ? current.filter((k) => k !== key) : [...current, key]
      writeViewMemory(scope, 'expanded', next)
      return next
    })
  }, [scope])
  const visibleGroups = useMemo(
    () =>
      groups.filter(
        (group) =>
          filter === 'all' ||
          group.indices.includes(focusIndex ?? -1) ||
          group.indices.some((index) => {
            const e = events[index]
            return filter === 'errors'
              ? eventFailed(e)
              : filter === 'input'
                ? ['input', 'prompt', 'injected'].includes(e.kind)
                : e.kind === 'injected' || e.state?.includes('compact') || e.kind === 'state'
          }),
      ),
    [groups, events, filter, focusIndex],
  )
  // Appending events keeps a reader who is already at the bottom at the bottom;
  // scrolling up pauses that until the explicit jump.
  const latest = useLatestScroll(events.length, scope, runStreamViewport, root)
  return (
    <div ref={root} className="space-y-2">
      <div className="sticky top-0 z-10 flex flex-wrap items-center gap-2 border-b border-hairline bg-card p-2 text-xs text-ink-3">
        <span className="order-last w-full text-[11px]" title="保留最近 400 条事件及本次输入">已载入 {metrics.events} 条 · {metrics.perMinute ?? '—'}/分钟 · 工具失败 {metrics.failures}/{metrics.completed}{metrics.unknown > 0 ? ` · 未知 ${metrics.unknown}` : ''}</span>
        <Button size="xs" variant="ghost" onClick={() => expand(groups.map((g) => g.key))}>全部展开</Button>
        <Button size="xs" variant="ghost" onClick={() => expand([])}>全部收起</Button>
        <Button size="xs" variant="ghost" onClick={() => void copy('all', events)}>{copied === 'all' ? '已复制' : '复制原始 JSON'}</Button>
        <label className="sr-only" htmlFor={`${scope}-filter`}>事件筛选</label>
        <Select value={filter} onValueChange={(value) => { setFilter(value); writeViewMemory(scope, 'filter', value) }}>
          <SelectTrigger id={`${scope}-filter`} className="h-7 w-28"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部事件</SelectItem>
            <SelectItem value="errors">仅异常</SelectItem>
            <SelectItem value="handoff">交接与压缩</SelectItem>
            <SelectItem value="input">输入提示词</SelectItem>
          </SelectContent>
        </Select>
        <span role="status" aria-live="polite" className="sr-only">
          显示 {visibleGroups.length} / {groups.length} 个事件组
        </span>
      </div>
      {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
      {groups.length === 0 && (
        <p className="px-1 py-6 text-center text-xs text-ink-3">这一条 run 还没有事件。</p>
      )}
      {groups.length > 0 && visibleGroups.length === 0 && (
        <p className="px-1 py-6 text-center text-xs text-ink-3">当前筛选下没有事件。</p>
      )}
      <div role="list" aria-label="运行事件组" className="space-y-2">
        {visibleGroups.map((group, index) => (
          <GroupCard
            key={group.key}
            group={group}
            events={events}
            runID={runID}
            sessionID={sessionID}
            open={expanded.includes(group.key)}
            focused={group.indices.includes(focusIndex ?? -1)}
            copied={copied}
            // Only the head of a live stream is marked live: it is the group
            // more events will join, and marking every group of a running
            // direction would say nothing extra.
            live={live && index === visibleGroups.length - 1}
            onToggle={toggle}
            onCopy={copy}
          />
        ))}
      </div>
      {!latest.pinned && (
        <div className="sticky bottom-1 z-10 flex justify-end pr-1">
          <Button size="xs" variant="outline" className="bg-popover shadow-lifted" onClick={latest.jump}>
            <ArrowDownToLine className="h-3.5 w-3.5" />
            {latest.unread > 0 ? `${latest.unread} 条新事件` : '回到最新'}
          </Button>
        </div>
      )}
    </div>
  )
}
