import { useState } from 'react'
import { Activity, CircleAlert, Flag, KeyRound, ShieldCheck } from 'lucide-react'

import { Button } from '@cyber/ui'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { GuardrailCard } from '@/components/GuardrailCard'
import type { OperatorInbox } from '@/lib/types'

/** The three headings the card keeps. acceptance and warnings share the third
 *  one: they are both "the operator has to look at this", and splitting them
 *  would make one of them a second-class row for no reason. */
type InboxSection = 'authorization' | 'handoff' | 'verification'

type PendingRow =
  | { section: 'authorization'; kind: 'authorization'; at: string; order: number; data: OperatorInbox['authorization'][number] }
  | { section: 'handoff'; kind: 'handoff'; at: string; order: number; data: OperatorInbox['handoff'][number] }
  | { section: 'verification'; kind: 'acceptance'; at: string; order: number; data: OperatorInbox['acceptance'][number] }
  | { section: 'verification'; kind: 'warning'; at: string; order: number; data: OperatorInbox['warnings'][number] }

const SECTION_RANK: Record<InboxSection, number> = { authorization: 0, handoff: 1, verification: 2 }
const KIND_RANK: Record<PendingRow['kind'], number> = { authorization: 0, handoff: 1, acceptance: 2, warning: 3 }

/**
 * One operator inbox: authorization requests, per-intent handoffs, stale goal
 * receipts and run warnings. Rows are mixed into one newest-first list instead of
 * being budgeted per category: a stack of authorization requests used to push
 * every handoff out of the visible three, and the operator could not see the row
 * that was actually newest. Category headings stay, printed whenever the list
 * crosses into another category. Rows with no time (older servers, or a receipt
 * with no stamp) sort after every timed row and still group by category.
 */
export function OperatorInboxCard({
  inbox,
  onNavigate,
  onChanged,
  onLocateGuardrail,
  onOpenIntent,
  onOpenRun,
  onOpenAuthorization,
  onOpenGoal,
}: {
  inbox: OperatorInbox
  onNavigate: (view: string) => void
  onChanged: () => void
  /** Point a pending tool approval at the raw call in a run's event stream. */
  onLocateGuardrail?: (target: { callID: string; sessionID: string }) => void
  /** Record-level link: the direction behind a handoff (`?intent=`). */
  onOpenIntent?: (intentID: string) => void
  /** Record-level link: the run behind a warning (`?run=`). */
  onOpenRun?: (runID: string) => void
  /** Record-level link: one authorization request (`?view=access&id=`). */
  onOpenAuthorization?: (requestID: string) => void
  /** Record-level link: one goal's receipt (`?view=state&goal=`). */
  onOpenGoal?: (goalID: string) => void
}) {
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  // The overview's first screen has to keep 目标进度 and 阻塞原因 in view, so the
  // card lists three rows and expands in place: there is no separate 待办 page,
  // and every row keeps its own record link.
  const [showAll, setShowAll] = useState(false)

  async function requestDetails(id: string) {
    setBusy(`details:${id}`)
    setNotice('')
    try {
      await api.requestOperatorDetails(id)
      setNotice('已请求指挥官补齐明细；它会在下一轮写入，不会自动执行动作。')
      onChanged()
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function resolve(id: string) {
    setBusy(`resolve:${id}`)
    setNotice('')
    try {
      await api.resolveOperatorRequest(id, '操作员已处理')
      setNotice('请求已处理；指挥官会收到这条决定的消息并继续推进其他方向，不需要重启项目或等待心跳。')
      onChanged()
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  // Tool approval always shows its state — including "never checked" and "not
  // enabled" — so the operator learns the guardrail status without a failure.
  // Every other section only exists when it has rows.
  const actionable = inbox.total > 0
  const MAX_ROWS = 3
  const totalRows =
    inbox.authorization.length + inbox.handoff.length + inbox.acceptance.length + inbox.warnings.length
  const budget = showAll ? Number.POSITIVE_INFINITY : MAX_ROWS
  const rows: PendingRow[] = [
    ...inbox.authorization.map((data, order): PendingRow => ({
      section: 'authorization', kind: 'authorization', at: data.created_at, order, data,
    })),
    ...inbox.handoff.map((data, order): PendingRow => ({
      section: 'handoff', kind: 'handoff', at: data.at ?? '', order, data,
    })),
    ...inbox.acceptance.map((data, order): PendingRow => ({
      section: 'verification', kind: 'acceptance', at: data.at ?? '', order, data,
    })),
    ...inbox.warnings.map((data, order): PendingRow => ({
      section: 'verification', kind: 'warning', at: data.at ?? '', order, data,
    })),
  ]
  // Time is the primary key: newest first. Ties (and rows without a time) keep
  // category order and then the server's own order, so the list is stable across
  // polls. A missing or unparsable stamp never ranks above a real one.
  const stamp = (at: string) => (at ? Date.parse(at) : Number.NaN)
  rows.sort((a, b) => {
    const at = stamp(a.at)
    const bt = stamp(b.at)
    const timed = Number.isFinite(at)
    const btimed = Number.isFinite(bt)
    if (timed !== btimed) return timed ? -1 : 1
    if (timed && btimed && at !== bt) return bt - at
    if (SECTION_RANK[a.section] !== SECTION_RANK[b.section]) {
      return SECTION_RANK[a.section] - SECTION_RANK[b.section]
    }
    if (KIND_RANK[a.kind] !== KIND_RANK[b.kind]) return KIND_RANK[a.kind] - KIND_RANK[b.kind]
    return a.order - b.order
  })
  const visible = rows.slice(0, budget)
  const hiddenRows = rows.length - visible.length
  if (!actionable && !inbox.guardrail) return null

  const timeChip = (at: string) =>
    at ? (
      <span className="ml-auto shrink-0 font-mono tw-nums text-[10.5px] text-ink-3" title="待处理行按这个时间倒序混排">
        {fmtTime(at)}
      </span>
    ) : null

  function renderRow(row: PendingRow) {
    if (row.kind === 'authorization') {
      const req = row.data
      return (
        <div key={req.id} className="rounded-lg border border-warning/20 bg-card/70 px-3 py-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              className="tw-break text-left text-[12.5px] font-medium text-ink transition-colors hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              onClick={() => onOpenAuthorization?.(req.id)}
              disabled={!onOpenAuthorization}
              title={onOpenAuthorization ? `在授权页打开请求 ${req.id}` : undefined}
            >
              {req.action || '目标未达成，没有在办意图'}
            </button>
            {req.generic && (
              <span className="rounded border border-hairline px-1.5 py-0.5 text-[10px] text-ink-3">通用请求</span>
            )}
            {timeChip(row.at)}
          </div>
          {(req.asset || req.impact) && (
            <div className="tw-break mt-0.5 text-[11.5px] leading-relaxed text-ink-2">
              {req.asset && <span>资产：{req.asset}</span>}
              {req.asset && req.impact && <span className="px-1 text-ink-3">·</span>}
              {req.impact && <span>影响：{req.impact}</span>}
            </div>
          )}
          {req.authorization && (
            <div className="tw-break mt-0.5 text-[11.5px] leading-relaxed text-ink-2">
              需要授权：{req.authorization}
            </div>
          )}
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {/* Only the generic fallback needs the commander to fill details. A
                request the commander already made concrete would only be
                overwritten. */}
            {req.generic && (
              <Button
                size="xs"
                variant="outline"
                disabled={busy !== '' || req.detail_requested}
                onClick={() => void requestDetails(req.id)}
              >
                {req.detail_requested ? '已请求明细' : busy === `details:${req.id}` ? '请求中…' : '让指挥官列明细'}
              </Button>
            )}
            <Button
              size="xs"
              variant="ghost"
              disabled={busy !== ''}
              onClick={() => void resolve(req.id)}
            >
              {busy === `resolve:${req.id}` ? '处理中…' : '标记已处理'}
            </Button>
          </div>
        </div>
      )
    }
    if (row.kind === 'handoff') {
      const item = row.data
      return (
        <button
          key={item.request.id}
          type="button"
          className="block w-full rounded-lg border border-hairline bg-card/60 px-3 py-1.5 text-left transition-colors hover:border-warning/30"
          onClick={() => (onOpenIntent ? onOpenIntent(item.intent_id) : onNavigate('access'))}
          title={item.request.request}
        >
          <span className="flex items-center gap-1.5">
            <span className="line-clamp-1 block min-w-0 text-[12px] font-medium text-ink" title={item.title}>
              {item.title}
            </span>
            {timeChip(row.at)}
          </span>
          <span className="line-clamp-2 block text-[11.5px] leading-relaxed text-ink-2">
            {item.request.request}
          </span>
        </button>
      )
    }
    if (row.kind === 'acceptance') {
      const item = row.data
      return (
        <button
          key={item.goal_id}
          type="button"
          className="block w-full rounded-lg border border-hairline bg-card/60 px-3 py-1.5 text-left transition-colors hover:border-warning/30"
          onClick={() => (onOpenGoal ? onOpenGoal(item.goal_id) : onNavigate('graph'))}
          title={item.statement}
        >
          <span className="flex items-center gap-1.5">
            <span className="line-clamp-1 block min-w-0 text-[12px] font-medium text-ink">目标完成依据已变化</span>
            {timeChip(row.at)}
          </span>
          <span className="line-clamp-2 block text-[11.5px] leading-relaxed text-ink-2">{item.statement}</span>
        </button>
      )
    }
    const item = row.data
    return (
      <button
        key={item.run}
        type="button"
        className="block w-full rounded-lg border border-hairline bg-card/60 px-3 py-1.5 text-left transition-colors hover:border-warning/30"
        onClick={() => (onOpenRun ? onOpenRun(item.run) : onNavigate('runs'))}
        title={item.warnings[0]}
      >
        <span className="flex items-center gap-1.5">
          <span className="line-clamp-1 block min-w-0 text-[12px] font-medium text-ink">
            运行 {item.run} 有交接告警
          </span>
          {timeChip(row.at)}
        </span>
        <span className="line-clamp-2 block text-[11.5px] leading-relaxed text-ink-2">
          {item.warnings[0]}
        </span>
      </button>
    )
  }

  return (
    <section
      className={
        actionable
          ? 'rounded-xl border border-warning/25 bg-warning/[0.06] px-4 py-3'
          : 'rounded-xl border border-hairline bg-surface-2/30 px-4 py-3'
      }
      aria-label="待操作员处理"
      data-testid="operator-inbox"
    >
      <div className="flex items-start gap-3">
        <span
          className={
            actionable
              ? 'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-warning/25 bg-warning/10 text-warning'
              : 'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-hairline bg-surface-2 text-ink-3'
          }
        >
          {actionable ? <CircleAlert className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <h2 className="text-[13px] font-semibold text-ink">{actionable ? '待你处理' : '操作员收件箱'}</h2>
            {/* Same number as the rail badge: an exact total only when every
                category answered; otherwise the rows below are a lower bound. */}
            {actionable && (
              <span
                className="font-mono tw-nums text-[11px] text-ink-3"
                title={inbox.summary && !inbox.summary.countable ? '有类别没读到：这是已知的下限，不是精确总数' : undefined}
              >
                {inbox.summary && !inbox.summary.countable ? `${inbox.summary.total}+` : inbox.total}
              </span>
            )}
            {!actionable && <span className="text-[11px] text-ink-3">当前没有需要你决定的请求</span>}
          </div>

          {inbox.guardrail && (
            <div className="mt-2.5 rounded-lg border border-hairline bg-card/60 px-3 py-2">
              <GuardrailCard summary={inbox.guardrail} onChanged={onChanged} onLocateCall={onLocateGuardrail} />
            </div>
          )}

          {visible.length > 0 && (
            <div className="mt-2.5 space-y-2">
              {visible.map((row, index) => {
                const heading = index === 0 || visible[index - 1].section !== row.section
                return (
                  <div key={`${row.section}:${row.kind}:${row.order}`} className="space-y-1.5">
                    {heading && (
                      <div className="flex items-center gap-1.5 text-[12px] font-medium text-ink">
                        {row.section === 'authorization' ? (
                          <>
                            <KeyRound className="h-3.5 w-3.5 text-warning" /> 需要你授权
                          </>
                        ) : row.section === 'handoff' ? (
                          <>
                            <Flag className="h-3.5 w-3.5 text-warning" /> 等你操作
                          </>
                        ) : (
                          <>
                            <Activity className="h-3.5 w-3.5 text-warning" /> 需要你验收 / 证据告警
                          </>
                        )}
                      </div>
                    )}
                    {renderRow(row)}
                  </div>
                )
              })}
            </div>
          )}

          {hiddenRows > 0 && (
            <div className="mt-2.5">
              <Button size="xs" variant="outline" onClick={() => setShowAll(true)}>
                还有 <span className="font-mono tw-nums">{hiddenRows}</span> 条待处理 · 展开全部
              </Button>
            </div>
          )}
          {showAll && totalRows > MAX_ROWS && (
            <div className="mt-2">
              <Button size="xs" variant="ghost" onClick={() => setShowAll(false)}>收起</Button>
            </div>
          )}

          {notice && (            <div className="mt-2 text-[11px] leading-relaxed text-ink-3" role="status">
              {notice}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}
