import { useEffect, useMemo, useRef, useState } from 'react'
import { ArrowUpRight, CheckCircle2, Download, FileText, Plus, XCircle } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyState,
  ScrollArea,
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { MiniMarkdown } from '@/components/MiniMarkdown'
import { Page, Panel } from '@/components/Page'
import { ApiError, api } from '@/lib/api'
import { fmtInt, fmtTime, severityLabel, severityVariant, statusKind, statusLabel } from '@/lib/format'
import { useLoad, usePoll, useConsoleLocation } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'
import { factHref, precheckVerdict, unreportedView, withReportParam } from '@/lib/report-trace'
import { tallyFindingSeverities } from '@/lib/report-severity'
import { useSessionState } from '@/lib/session-state'
import type { Fact, ReportFinding, ReportMeta, ReportPrecheck } from '@/lib/types'

/**
 * Reports are a real executor run: a precheck first counts what would land.
 * Unverified conclusions are only allowed as candidates, so a report that writes
 * them confirmed fails validation — the operator gets one chance to review them
 * first, but the precheck is advisory and never blocks the run (D100).
 *
 * The page is one list: every row is a generated report (id, what it found, when,
 * validation checks), and the single primary action is "生成报告". The empty
 * state explains the generate flow instead of leaving a blank panel.
 *
 * The precheck is also the entry to the trace: 报告 → 结论行的事实 id →
 * 断言版本与原始证据. Its `unreported` / `unreadable` / `uncounted` lists are shown
 * as named rows (never folded into 0), and a precheck read that fails stops at
 * 「预检不可用」 with an explicit retry / proceed choice instead of generating.
 *
 * One open preview is a record too (`?report=<id>`), so the trace's way back
 * lands on the report the operator was reading, not on the bare list.
 */
export function Reports({
  onGoVerify,
  onOpenFact,
}: {
  onGoVerify?: () => void
  /** Open one fact's claim/review/evidence panel (the trace's second hop). */
  onOpenFact?: (factID: string) => void
} = {}) {
  const list = usePoll(api.reports, 4000)
  const progress = usePoll(api.reportPrecheck, 4000)
  const runs = usePoll(api.runs, 4000)
  const [pendingReport, setPendingReport] = useSessionState('report.pending.v2', '')
  const [failure, setFailure] = useState('')
  const reportRuns = (runs.data ?? []).filter(r => r.purpose === 'report')
  const trackedRun = pendingReport ? reportRuns.find(r => r.id === pendingReport) : undefined
  const completed = pendingReport ? (list.data ?? []).find(r => r.run_id === pendingReport && r.status !== 'draft') : undefined
  const generating = progress.data?.running || (list.data ?? []).some(r => r.status === 'draft') || reportRuns.some(r => r.status === 'running')
  useEffect(() => {
    if (!pendingReport) return
    if (completed || trackedRun?.status === 'failed' || trackedRun?.status === 'canceled') {
      setPendingReport('')
      list.reload()
      if (trackedRun?.status === 'failed' || trackedRun?.status === 'canceled') {
        setFailure('报告生成失败，请查看运行记录中的失败原因，修复后重新生成。')
      } else if (completed) {
        // A produced report is not the same as a deliverable one: only `ready`
        // has a body to view or download. Say which state it landed in instead
        // of calling every non-ready outcome a generation failure.
        setNote(
          completed.status === 'ready'
            ? '报告已生成，可在下方查看和下载。'
            : `报告已产出（${statusLabel(completed.status)}），正文不对外提供；下方报告行可看状态与校验结果。`,
        )
      }
    }
  }, [pendingReport, completed, trackedRun])
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const location = useConsoleLocation()
  // Non-zero means the precheck found unverified conclusions and the confirm
  // dialog is open.
  const [unverified, setUnverified] = useState(0)
  // The reason a precheck read failed. Kept apart from the generic poll error:
  // this one stops generation until the operator chooses retry or proceed.
  const [precheckIssue, setPrecheckIssue] = useState('')
  const queueLock = useRef(false)

  /** Queue the actual report run (the "生成报告" path, and "仍然生成"). */
  async function queueReport() {
    if (queueLock.current) return
    queueLock.current = true
    setBusy(true)
    setNote('')
    setFailure('')
    try {
      const { run_id } = await api.createReport()
      setNote('已排入生成队列：一次独立的报告撰写运行（不占用指挥官/执行者），写完才出现在列表里。')
      setPendingReport(run_id)
      list.reload()
      progress.reload()
      runs.reload()
    } catch (e) {
      setFailure(`提交失败：${e instanceof Error ? e.message : String(e)}。请检查连接后重试。`)
    } finally {
      queueLock.current = false
      setBusy(false)
    }
  }

  async function generate() {
    if (busy || generating || pendingReport) return
    setBusy(true)
    setNote('')
    setPrecheckIssue('')
    try {
      let precheck: ReportPrecheck | undefined
      let readError = ''
      try {
        precheck = await api.reportPrecheck()
      } catch (e) {
        readError = e instanceof Error ? e.message : String(e)
      }
      // Three states, not two: 预检不可用 must be shown and decided by the
      // operator. Generating on a failed read would hide the very check that
      // says which conclusions cannot land (and which facts no report cites).
      const verdict = precheckVerdict(precheck, readError)
      if (verdict.kind === 'unavailable') {
        setPrecheckIssue(verdict.reason)
        return
      }
      if (verdict.kind === 'loading') {
        setPrecheckIssue('预检没有返回结果')
        return
      }
      if (verdict.kind === 'running') {
        setNote('报告正在生成，将自动更新列表。')
        progress.reload()
        return
      }
      if (verdict.precheck.unverified > 0 && !verdict.precheck.reviews_disabled) {
        setUnverified(verdict.precheck.unverified)
        return
      }
      await queueReport()
    } finally {
      queueLock.current = false
      setBusy(false)
    }
  }

  const rows = list.data ?? []
  // The open preview is a record (`?report=`), not local state: Back from a
  // finding's fact has to land on the same report, and a copied URL has to open
  // it. An id the current read does not know is reported, never swapped for the
  // first row.
  const openedID = new URLSearchParams(location.search).get('report') ?? ''
  const opened = openedID ? rows.find((row) => row.id === openedID) : undefined
  const openedMissing =
    Boolean(openedID) && !opened && !list.loading && !list.error
  // The polled precheck is the page's read of the same answer the generate path
  // asks for: one verdict type, so the panel and the button cannot disagree.
  const verdict = precheckVerdict(progress.data, progress.error)
  const unreported = unreportedView(progress.data)

  /** Open/close one preview without leaving the view (`report` record param). */
  function showReport(id: string | null, replace = false) {
    const next = withReportParam(window.location.search, id)
    navigate(`${window.location.pathname}${next}`, replace)
  }

  return (
    <Page
      title="报告"
      subtitle="汇总项目发现与证据，生成可下载报告；账本变更后会标记为过期"
      actions={
        <Button size="sm" onClick={generate} disabled={busy || !!generating || !!pendingReport}>
          <Plus className="h-3.5 w-3.5" /> {busy ? '提交中…' : generating ? '生成中…' : pendingReport ? '已排队' : '生成报告'}
        </Button>
      }
    >
      {(generating || pendingReport) && (
        <Callout className="mb-3">
          <span role="status">{generating ? '报告生成中，完成后会自动出现在下方。' : '已排队，正在等待报告运行。'}</span>
          <a className="ml-2 underline underline-offset-2" href="?view=runs">查看运行进度</a>
        </Callout>
      )}
      {pendingReport && !generating && progress.data && !progress.error && !runs.error && !list.error && (
        <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
          若等待没有进展，可先查看运行记录。
          <Button size="xs" variant="ghost" onClick={() => { setPendingReport(''); setNote('已结束等待；如需重新生成，请先核对运行记录。') }}>
            已核对，结束等待
          </Button>
        </p>
      )}
      {failure && <Callout tone="destructive" className="mb-3">{failure}</Callout>}
      {(list.error || progress.error || runs.error) && (
        <Callout tone="warning" className="mb-3">
          状态更新失败，当前显示上次读取的数据。
          <Button size="xs" variant="outline" disabled={list.refreshing || progress.refreshing || runs.refreshing} onClick={() => { list.reload(); progress.reload(); runs.reload() }}>
            重试更新
          </Button>
        </Callout>
      )}
      {note && <Callout tone="info" className="mb-3">{note}</Callout>}

      {/* A failed precheck read is a decision point, not a silent fallback: the
          operator either retries the check or says "generate anyway". */}
      {precheckIssue && (
        <Callout tone="warning" className="mb-3" data-testid="precheck-unavailable">
          <div className="font-medium">预检不可用</div>
          <p className="mt-1 text-[12px] leading-relaxed">
            没读到这次的预检（{precheckIssue}）。因此不知道哪些结论未复核、哪些已复核事实还没进过报告。
            生成前可以重试；也可以明知缺这份检查继续。
          </p>
          <div className="mt-2 flex flex-wrap gap-2">
            <Button size="xs" variant="outline" disabled={busy} onClick={() => void generate()}>
              重试预检
            </Button>
            <Button
              size="xs"
              variant="outline"
              disabled={busy}
              title="不读预检直接排队一次报告运行；未复核结论仍只能作为 candidate"
              onClick={() => { setPrecheckIssue(''); void queueReport() }}
            >
              仍要生成
            </Button>
          </div>
        </Callout>
      )}

      <Panel
        title="预检"
        className="mb-3"
        extra={
          <span className="text-[11px] text-ink-3">
            {verdict.kind === 'ok' ? '生成前先看这次会写进什么' : verdict.kind === 'running' ? '报告生成中' : verdict.kind === 'unavailable' ? '不可用' : '读取中'}
          </span>
        }
      >
        {verdict.kind === 'loading' && (
          <div className="text-[12px] text-ink-3" role="status">正在读取预检…</div>
        )}
        {verdict.kind === 'unavailable' && (
          <div className="space-y-2" data-testid="precheck-panel-unavailable">
            <Callout tone="warning">
              <div className="font-medium">预检不可用</div>
              <p className="mt-1 text-[12px] leading-relaxed">
                {verdict.reason}。下面的报告列表仍可查看；生成报告前会再读一次，读不到就停在「预检不可用」。
              </p>
            </Callout>
            <Button size="xs" variant="outline" disabled={progress.refreshing} onClick={progress.reload}>
              重试预检
            </Button>
          </div>
        )}
        {verdict.kind === 'running' && (
          <p className="text-[12px] leading-relaxed text-ink-2" role="status">
            报告正在生成，完成前预检只给上一次的计数；列表会自动更新。
          </p>
        )}
        {verdict.kind === 'ok' && (
          <div className="space-y-2.5">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px]">
              <span className="inline-flex items-baseline gap-1.5">
                <span className="text-ink-3">可入报告</span>
                <b className="saker-nums font-mono font-semibold text-ink">{fmtInt(verdict.precheck.reportable)}</b>
              </span>
              <span className="inline-flex items-baseline gap-1.5">
                <span className="text-ink-3">{progress.data?.reviews_disabled ? '未复核（复核关闭）' : '未复核'}</span>
                <b className="saker-nums font-mono font-semibold text-ink">{fmtInt(verdict.precheck.unverified)}</b>
              </span>
              <span className="inline-flex items-baseline gap-1.5">
                <span className="text-ink-3">已否决</span>
                <b className="saker-nums font-mono font-semibold text-ink">{fmtInt(verdict.precheck.rejected)}</b>
              </span>
              {(verdict.precheck.withdrawn ?? 0) > 0 && (
                <span className="inline-flex items-baseline gap-1.5">
                  <span className="text-ink-3">已撤回</span>
                  <b className="saker-nums font-mono font-semibold text-ink">{fmtInt(verdict.precheck.withdrawn ?? 0)}</b>
                </span>
              )}
            </div>

            {!unreported.present ? (
              <p className="text-[12px] leading-relaxed text-ink-3" data-testid="unreported-absent">
                服务端没有返回「未进过报告的事实」清单（旧版本接口）。这里不写 0——没有数过的事不能说成没有。
              </p>
            ) : unreported.total === 0 ? (
              <p className="text-[12px] leading-relaxed text-ink-3" data-testid="unreported-empty">
                已复核的结论都进过一份通过校验的报告。
              </p>
            ) : (
              <details className="group rounded-lg border border-hairline" data-testid="unreported-list">
                <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-3 py-2 text-[12px] text-ink-2 transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                  <span className="font-medium">未进过报告的事实</span>
                  <span className="saker-nums font-mono text-[11px] text-ink-3">{fmtInt(unreported.total)} 条</span>
                  <span className="ml-auto text-[11px] text-ink-3 group-open:hidden">展开清单</span>
                  <span className="ml-auto hidden text-[11px] text-ink-3 group-open:inline">收起清单</span>
                </summary>
                <div className="space-y-1.5 border-t border-hairline px-3 py-2">
                  <ul className="space-y-1" aria-label="未进过报告的事实">
                    {unreported.items.map((item) => (
                      <li key={item.fact} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-[12px] leading-5">
                        <FactRef id={item.fact} onOpenFact={onOpenFact} />
                        <span className="saker-break min-w-0 flex-1 text-ink-2">{item.title || '（没有标题）'}</span>
                      </li>
                    ))}
                  </ul>
                  {unreported.hidden > 0 && (
                    <p className="text-[11px] leading-relaxed text-ink-3">
                      另有 {fmtInt(unreported.hidden)} 条未列出：接口一次最多返回 {fmtInt(unreported.limit)} 条，上面的数字是全部。
                    </p>
                  )}
                  {unreported.note && <p className="text-[11px] leading-relaxed text-ink-3">{unreported.note}</p>}
                </div>
              </details>
            )}

            {unreported.unreadable.length > 0 && (
              <p className="text-[11px] leading-relaxed text-ink-3" data-testid="precheck-unreadable">
                <span className="text-warning">读不出来的报告 {unreported.unreadable.length} 份</span>
                （{unreported.unreadable.join('、')}）：里面引用过哪些事实无法判断，所以上面的差额没有把它们当成「没引用」。
              </p>
            )}
            {unreported.uncounted.length > 0 && (
              <p className="text-[11px] leading-relaxed text-ink-3" data-testid="precheck-uncounted">
                <span className="text-warning">未通过自身校验的报告 {unreported.uncounted.length} 份</span>
                （{unreported.uncounted.join('、')}）：它们的引用不计入差额——没通过校验的报告不算已交付。
              </p>
            )}
          </div>
        )}
      </Panel>

      <Panel
        title="报告列表"
        extra={
          <span role="status" aria-live="polite" className="saker-nums font-mono text-[11px] text-ink-3">{rows.length} 份</span>
        }
        bodyClassName="p-0"
      >
        {list.loading && (
          <div className="p-4">
            <SkeletonList count={4} />
          </div>
        )}

        {!list.loading && !list.error && !generating && !pendingReport && rows.length === 0 && (
          <EmptyState
            icon={FileText}
            title="还没有报告"
            description="点右上角「生成报告」：先检查未复核结论，确认后排队一次独立的报告撰写运行（不依赖指挥官或执行者），写完才出现在这里；生成前可先完成复核。"
          />
        )}

        {rows.length > 0 && (
          <ul role="list" className="divide-y divide-hairline" aria-label="报告列表">
            {rows.map((r, index) => (
              <li
                key={r.id}
                className={
                  'flex flex-wrap items-start gap-x-3 gap-y-2 px-2.5 py-2.5 transition-colors ' +
                  (index >= 6 ? '[contain-intrinsic-size:auto_6rem] [content-visibility:auto] ' : '') +
                  (openedID === r.id
                    ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                    : 'hover:bg-surface-2')
                }
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <button
                      type="button"
                      onClick={() => showReport(r.id)}
                      title="打开报告预览"
                      aria-expanded={openedID === r.id}
                      className="saker-break rounded font-mono text-[13px] font-medium leading-5 text-ink hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      {r.id}
                    </button>
                    <Badge size="sm" variant="secondary">
                      <span className="saker-nums font-mono">{r.findings}</span> 条结论
                    </Badge>
                    {r.status === 'stale' && (
                      <Badge size="sm" variant="warning">账本变更后过期</Badge>
                    )}
                  </div>
                  {r.checks && r.checks.length > 0 && (
                    <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                      {r.checks.map((c, i) => (
                        <span
                          key={i}
                          title={c.detail}
                          className={`flex items-center gap-1 text-[11px] leading-4 ${c.ok ? 'text-success' : 'text-destructive'}`}
                        >
                          {c.ok ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
                          {c.rule}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 flex-col items-end gap-1.5">
                  <StatusIndicator compact status={statusKind(r.status)} label={statusLabel(r.status)} />
                  <span className="saker-nums whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">
                    {fmtTime(r.created_at)}
                    {r.finished_at ? ` → ${fmtTime(r.finished_at)}` : ''}
                  </span>
                  {r.status === 'ready' ? (
                    <a
                      className="flex items-center gap-1 text-[12px] text-ink-2 transition-colors hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      href={api.reportFileURL(r.id, 'index.md')}
                      target="_blank"
                      rel="noreferrer"
                      title="下载报告正文 index.md"
                    >
                      <Download className="h-3 w-3" /> index.md
                    </a>
                  ) : r.status === 'draft' ? (
                    // checks.json is written when the run finishes, so a draft
                    // has nothing to open yet — a link here would 404.
                    <span className="text-[12px] text-ink-3">正文待生成</span>
                  ) : (
                    <a
                      className="flex items-center gap-1 text-[12px] text-ink-2 transition-colors hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      href={api.reportFileURL(r.id, 'checks.json')}
                      target="_blank"
                      rel="noreferrer"
                      title="正文不对外提供，只能看校验结果"
                    >
                      <FileText className="h-3 w-3" /> checks.json
                    </a>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      {openedMissing && (
        <Callout tone="warning" className="mb-3" data-testid="report-not-found">
          <div className="font-medium">没有找到这份报告</div>
          <p className="mt-1 text-[12px] leading-relaxed">
            链接里的 {openedID} 不在当前报告列表：可能已被删除，或属于另一个项目。不会自动打开别人的报告。
          </p>
          <Button size="xs" variant="outline" className="mt-2" onClick={() => showReport(null, true)}>
            回到报告列表
          </Button>
        </Callout>
      )}

      {/* The open sheet follows the list's current status, not the snapshot it
          was opened with: `stale` is recomputed on every read, so a preview left
          open while the ledger moves has to turn into the refusal. */}
      <ReportSheet
        report={opened ?? null}
        onOpenChange={(o) => !o && showReport(null, true)}
        onOpenFact={onOpenFact}
      />

      {unverified > 0 && (
        <UnverifiedDialog
          count={unverified}
          onClose={() => setUnverified(0)}
          onGoVerify={onGoVerify}
          onStillGenerate={() => { setUnverified(0); void queueReport() }}
          onOpenFact={onOpenFact}
        />
      )}
    </Page>
  )
}

/**
 * One fact id, and the only way the report reaches the ledger: the link carries
 * a real address (`?view=graph&fact=`), and the in-app handler makes it instant.
 * A middle click or a copied URL still works when no handler is wired.
 */
function FactRef({ id, onOpenFact }: { id: string; onOpenFact?: (id: string) => void }) {
  return (
    <a
      href={factHref(id)}
      onClick={(event) => {
        if (!onOpenFact) return
        event.preventDefault()
        onOpenFact(id)
      }}
      title={`打开事实 ${id} 的断言版本、复核与原始证据`}
      className="inline-flex shrink-0 items-center gap-0.5 rounded font-mono text-[11px] text-ink-3 underline-offset-2 transition-colors hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      {id}
      <ArrowUpRight className="h-3 w-3" />
    </a>
  )
}

/**
 * The unverified gate, now with the facts it is talking about. The precheck only
 * counts them (the count is what the gate is based on); the list is read from
 * the same ledger filter the 漏洞 page uses, so a name and a fact link are
 * available without changing the precheck contract.
 */
function UnverifiedDialog({
  count,
  onClose,
  onGoVerify,
  onStillGenerate,
  onOpenFact,
}: {
  count: number
  onClose: () => void
  onGoVerify?: () => void
  onStillGenerate: () => void
  onOpenFact?: (id: string) => void
}) {
  const facts = useLoad<Fact[]>(async () => {
    const [vuln, cred] = await Promise.all([
      api.factsPage({ status: 'unverified', kind: 'vuln', limit: 200 }),
      api.factsPage({ status: 'unverified', kind: 'cred', limit: 200 }),
    ])
    return [...(vuln.facts ?? []), ...(cred.facts ?? [])]
  }, [])
  const listed = facts.data?.length ?? 0
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>还有 {count} 条结论未复核</DialogTitle>
          <DialogDescription>
            未复核的结论在报告中只能作为 candidate，写成 confirmed 会被校验拒绝。
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-64 overflow-y-auto rounded-lg border border-hairline" data-testid="unverified-facts">
          {facts.error ? (
            <p className="px-3 py-2.5 text-[12px] leading-relaxed text-ink-3">
              读不到未复核结论的清单：{facts.error}。下面的选择仍然可用。
            </p>
          ) : !facts.data ? (
            <p className="px-3 py-2.5 text-[12px] text-ink-3" role="status">正在读取未复核结论…</p>
          ) : listed === 0 ? (
            <p className="px-3 py-2.5 text-[12px] leading-relaxed text-ink-3">
              清单里没有未复核的漏洞/凭据结论（可能刚被复核）。预检计数是 {count}。
            </p>
          ) : (
            <ul className="divide-y divide-hairline/70" aria-label="未复核结论">
              {facts.data.map((fact) => (
                <li key={fact.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-2 text-[12px] leading-5">
                  <Badge size="sm" variant="muted">{fact.kind}</Badge>
                  <span className="saker-break min-w-0 flex-1 text-ink">{fact.title || fact.summary || '（没有标题）'}</span>
                  <FactRef id={fact.id} onOpenFact={onOpenFact} />
                </li>
              ))}
            </ul>
          )}
          {listed > 0 && listed !== count && (
            <p className="border-t border-hairline px-3 py-1.5 text-[11px] text-ink-3">
              预检计数 {count} 条，这里列出 {listed} 条（漏洞/凭据两类，各最多 200 条）。
            </p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button
            variant="outline"
            disabled={!onGoVerify}
            title={onGoVerify ? '去漏洞页复核这些结论，再回来生成' : '当前页面没有复核入口'}
            onClick={() => {
              onClose()
              onGoVerify?.()
            }}
          >
            先去复核
          </Button>
          <Button onClick={onStillGenerate}>仍然生成</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * A report body either arrives as markdown or as the server's reason for
 * withholding it. 409 is the delivery gate (still generating, checks failed,
 * ledger moved since generation) — reading it as a transport failure would hide
 * the gate, so the two outcomes stay distinct here.
 */
type ReportBody = { text: string; refused?: undefined } | { text?: undefined; refused: string }

function ReportSheet({
  report,
  onOpenChange,
  onOpenFact,
}: {
  report: ReportMeta | null
  onOpenChange: (v: boolean) => void
  onOpenFact?: (id: string) => void
}) {
  return (
    <Sheet open={!!report} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-2xl">
        <SheetHeader className="shrink-0 space-y-2 border-b border-hairline px-5 py-4 pr-12 text-left">
          <div className="saker-eyebrow">报告预览</div>
          <SheetTitle className="saker-break font-mono text-[15px] leading-6">{report?.id}</SheetTitle>
          {report && (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <StatusIndicator compact status={statusKind(report.status)} label={statusLabel(report.status)} />
                <Badge size="sm" variant="secondary">
                  <span className="saker-nums font-mono">{report.findings}</span> 条结论
                </Badge>
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] leading-4 text-ink-3">
                <span className="saker-nums">生成 {fmtTime(report.created_at)}</span>
                {report.finished_at && <span className="saker-nums">完成 {fmtTime(report.finished_at)}</span>}
                {report.status === 'ready' ? (
                  <a
                    className="inline-flex items-center gap-1 text-ink-2 hover:text-primary"
                    href={api.reportFileURL(report.id, 'index.md')}
                    target="_blank"
                    rel="noreferrer"
                  >
                    <Download className="h-3 w-3" /> 下载 index.md
                  </a>
                ) : report.status === 'draft' ? (
                  <span>正文待生成</span>
                ) : (
                  <a
                    className="inline-flex items-center gap-1 text-ink-2 hover:text-primary"
                    href={api.reportFileURL(report.id, 'checks.json')}
                    target="_blank"
                    rel="noreferrer"
                  >
                    <FileText className="h-3 w-3" /> checks.json
                  </a>
                )}
              </div>
            </>
          )}
        </SheetHeader>
        {/* Discard cached content as soon as the report or its status changes. */}
        {report && (
          <ReportSheetBody
            key={`${report.id}:${report.status}`}
            report={report}
            onOpenFact={onOpenFact}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function ReportSheetBody({ report, onOpenFact }: { report: ReportMeta; onOpenFact?: (id: string) => void }) {
  const doc = useLoad<ReportBody>(async () => {
    try {
      return { text: await api.reportFileText(report.id, 'index.md') }
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) return { refused: e.message }
      throw e
    }
  }, [report.id, report.status])
  const failedChecks = (report.checks ?? []).filter((c) => !c.ok)
  // Only a ready report has findings.json readable.
  const findings = useLoad<ReportFinding[]>(
    () => report.status === 'ready' ? api.reportFindings(report.id) : Promise.resolve([]),
    [report.id, report.status],
  )
  const tally = useMemo(
    () => tallyFindingSeverities(findings.data ?? []).filter((t) => t.count > 0 || t.tier !== 'unlabelled'),
    [findings.data],
  )
  const gradedFindings = useMemo(
    () => (findings.data ?? []).filter((f) => f.severity || f.verdict === 'confirmed'),
    [findings.data],
  )

  return (
    <ScrollArea className="min-h-0 flex-1">
      <div className="px-5 py-4">
        {doc.loading && <SkeletonList count={6} />}
        {doc.error && (
          <Callout tone="destructive">
            {doc.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={doc.reload}>
              重试
            </Button>
          </Callout>
        )}
        {doc.data?.refused && (
          <div className="space-y-3">
            <Callout tone="warning">
              <div className="font-medium">正文不对外提供</div>
              <p className="mt-1 leading-relaxed">{doc.data.refused}</p>
            </Callout>
            {failedChecks.length > 0 && (
              <div>
                <div className="saker-eyebrow mb-1.5">未通过的校验</div>
                <ul className="space-y-1">
                  {failedChecks.map((c, i) => (
                    <li key={i} className="flex items-start gap-1.5 text-[12px] leading-5">
                      <XCircle className="mt-0.5 h-3 w-3 shrink-0 text-destructive" />
                      <span className="font-mono text-ink">{c.rule}</span>
                      {c.detail && <span className="text-ink-3">{c.detail}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {report.status !== 'draft' && (
              <a
                className="inline-flex items-center gap-1 text-[12px] text-ink-2 hover:text-primary"
                href={api.reportFileURL(report.id, 'checks.json')}
                target="_blank"
                rel="noreferrer"
              >
                <FileText className="h-3 w-3" /> 查看完整校验结果 checks.json
              </a>
            )}
          </div>
        )}
        {doc.data?.text && (
          <div className="space-y-4">
            {report.status === 'ready' && (findings.data?.length ?? 0) > 0 && (
              <section className="space-y-2" aria-label="报告分级">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <h3 className="saker-section-title">分级</h3>
                  {tally.map((t) => (
                    <span key={t.tier} className="inline-flex items-baseline gap-1 text-[12px]">
                      <span className="text-ink-3">{t.label}</span>
                      <span className="saker-nums font-mono font-medium text-ink">{t.count}</span>
                    </span>
                  ))}
                </div>
                <ul className="space-y-1">
                  {gradedFindings.map((f, i) => (
                    <li key={f.fact || i} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] leading-5">
                      <Badge size="sm" variant={severityVariant(f.severity)}>
                        {f.severity ? severityLabel(f.severity) : '未标注'}
                      </Badge>
                      <span className="saker-break min-w-0 flex-1 text-ink">{f.title || f.fact}</span>
                      {/* The conclusion's own fact id: without it a finding is a
                          title with no ledger row behind it (the trace's ①). */}
                      {f.fact && <FactRef id={f.fact} onOpenFact={onOpenFact} />}
                      <span className="shrink-0 font-mono text-[11px] text-ink-3">{f.verdict}</span>
                    </li>
                  ))}
                </ul>
                {findings.error && (
                  <p className="text-[11px] leading-relaxed text-ink-3">
                    分级明细读取失败（{findings.error}）；正文仍按上面显示。
                  </p>
                )}
              </section>
            )}
            <MiniMarkdown text={doc.data.text} />
          </div>
        )}
      </div>
    </ScrollArea>
  )
}
