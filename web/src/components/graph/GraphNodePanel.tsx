// The pinned node inspector (moved verbatim out of GraphView.tsx by T6, docs/33
// §4.6). It is loaded with React.lazy so the ~800 lines of fact/intent detail
// actions do not sit in the GraphView chunk. The logic below is unchanged: this
// file only owns the panel, the page shell owns selection.

import { useCallback, useEffect, useMemo, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import {
  Activity,
  Download,
  Eye,
  GitBranch,
  Maximize2,
  Play,
  RefreshCw,
  RotateCcw,
  Send,
  ShieldCheck,
  ShieldX,
  X,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Input,
  ScrollArea,
  StatusIndicator,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { EvidencePreview } from '@/components/EvidencePreview'
import { DoneDeclaredBadge } from '@/components/DoneDeclaredBadge'
import { FactClaimDetail } from '@/components/FactClaimDetail'
import { IntentContextEditor } from '@/components/IntentContextEditor'
import { IntentPlanEditor } from '@/components/IntentPlanEditor'
import { RetryIntentDialog } from '@/components/RetryIntentDialog'
import { RunEvidenceView } from '@/components/RunEvidenceView'
import { CancelReasonCallout } from './CancelReasonCallout'
import { RunSummaryLine } from './RunSummaryLine'
import { api, ApiError } from '@/lib/api'
import { evidencePreviewFile, type PreviewFile } from '@/lib/evidence-preview'
import { factDocLong, factDocText } from '@/lib/fact-doc'
import { fmtBytes, fmtInt, fmtTime, kindVariant, runStatusLabel, severityLabel, severityVariant, shortID, statusKind, statusLabel } from '@/lib/format'
import { readinessApplies, readinessNotice } from '@/lib/intent-detail'
import { reviewFailureHint, reviewFailureLabel } from '@/lib/review-failure'
import { closeAndFocusCanvas, isEscapeToClose } from './focus'
import { useReviewFailure } from '@/lib/use-review-failure'
import { useLoad, usePoll } from '@/lib/hooks'
import { latestExecutorRun } from '@/lib/run-evidence'
import { intentStatusDot, intentStatusTint, intentStopped, INTENT_STOPPED_LABEL } from '@/graph/status'
import { intentOrigin, nodeTypeLabel, originLabel, type GraphNode } from '@/graph/model'
import type { Fact, Run } from '@/lib/types'
import { DockResizeHandle, type DockSize } from './DockResize'

/** A titled block: one hairline between sections, 13px ink-1 title row. */
function Section({ title, extra, children }: { title: ReactNode; extra?: ReactNode; children: ReactNode }) {
  return (
    <section className="border-t border-hairline pt-3 first:border-t-0 first:pt-0">
      <div className="mb-1.5 flex items-center gap-1.5">
        <h3 className="saker-eyebrow min-w-0 normal-case tracking-normal text-ink-2">{title}</h3>
        {extra && <div className="ml-auto flex items-center gap-1.5">{extra}</div>}
      </div>
      <div className="min-w-0 space-y-1.5">{children}</div>
    </section>
  )
}

/** A direction's status chip. Intent statuses take their colour from the graph
 *  status vocabulary (themed tokens, so light mode stays readable); fact and
 *  other statuses keep the shared chip palette. An explicit stop (`hold`) is
 *  its own chip: "已停止" must never read as "失败". */
function IntentStatusChip({ status, hold }: { status?: string; hold?: boolean }) {
  if (!status) return null
  const stopped = intentStopped(hold)
  const tint = intentStatusTint(status, hold)
  if (!tint) {
    return <StatusIndicator compact status={statusKind(status)} label={statusLabel(status)} className="shrink-0" />
  }
  const pulsing = !stopped && (status === 'running' || status === 'reviewing')
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1.5 rounded border px-2 py-0 text-[11px] font-medium leading-4 ${tint}`}
      title={stopped ? '显式叫停：不会自动重派，需重试' : undefined}
    >
      <span
        className={`h-1.5 w-1.5 shrink-0 rounded-full ${intentStatusDot(status, hold) ?? ''} ${pulsing ? 'motion-safe:animate-pulse' : ''}`}
        aria-hidden="true"
      />
      {stopped ? INTENT_STOPPED_LABEL : statusLabel(status)}
    </span>
  )
}

/** A property row: 11px label in a fixed column, 13px value, one row height. */
function Prop({
  label,
  value,
  mono = true,
  title,
}: {
  label: string
  value: ReactNode
  mono?: boolean
  title?: string
}) {
  return (
    <div className="flex items-baseline gap-2 py-0.5" title={title}>
      <span className="w-14 shrink-0 text-[11px] leading-4 text-ink-3">{label}</span>
      <span
        className={cn(
          'min-w-0 flex-1 text-[13px] leading-5 text-ink-2',
          mono ? 'saker-nums font-mono' : '',
        )}
      >
        {value}
      </span>
    </div>
  )
}

/** Empty region copy: says what would appear here, not just "none". */
function EmptyLine({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-hairline px-2.5 py-2 text-[12px] leading-relaxed text-ink-3">
      {children}
    </div>
  )
}

const REVIEW_STATE_LABELS: Record<string, string> = {
  queued: '复核排队中',
  running: '复核进行中',
  cooling: '复核冷却中',
  paused: '复核已暂停',
  failed: '复核未通过，需补证据/修正',
  disabled: '复核未启用',
  ineligible: '不可独立复核',
}

function reviewStateLabel(state: string): string {
  return REVIEW_STATE_LABELS[state] ?? state
}

/** Native-subagent aggregate duration; a missing/zero value is honestly unknown. */
function helperDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '未记录'
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

export function GraphNodePanel(props: Parameters<typeof NodeWorkspace>[0]) {
  // Detail reads, drafts and confirmation dialogs belong to one selected
  // node. Retire them together before rendering a different record.
  return <NodeWorkspace key={props.node.id} {...props} />
}

function NodeWorkspace({
  node,
  facts,
  runs,
  dock,
  onSelectFact,
  onClose,
  onChanged,
  /** Cross-page link: open one run in the run ledger. */
  onOpenRun,
  treeActive = false,
  onOnlyTree,
  onShowAll,
}: {
  node: GraphNode
  /** The graph's fact poll: resolves a goal's cited fact ids to titles. */
  facts: Fact[]
  /** The run poll: a direction's frozen input versions live on its latest run. */
  runs: Run[]
  /** Shared width + drag glue for the graph's right-hand overlays. */
  dock: DockSize
  /** Select a fact node (`f:<id>`) when a cited fact is clicked. */
  onSelectFact: (factId: string) => void
  onClose: () => void
  onChanged: () => void
  /** Cross-page link: open one run in the run ledger (`?view=runs&run=…`). */
  onOpenRun?: (runID: string) => void
  /** 「只看这棵树」是否开着（画布上只剩这棵树）。 */
  treeActive?: boolean
  onOnlyTree?: () => void
  onShowAll?: () => void
}) {
  const isFact = node.type === 'fact'
  const intent = node.meta?.intent
  const factID = node.meta?.fact?.id ?? ''
  const detail = useLoad(() => (isFact && factID ? api.fact(factID) : Promise.resolve(null)), [factID])
  const usage = useLoad(
    () => (intent ? api.intentUsage(intent.id) : Promise.resolve(null)),
    [intent?.id],
  )
  const premisePolicy = useLoad(api.premisePolicy, [])
  // A review failure is "nothing decided it" or "a verdict said no"; the ledger
  // separates them by whether a review row exists, so read that instead of the note.
  const reviewFailure = useReviewFailure(
    intent?.status === 'review_failed' ? intent.review_failed_facts : undefined,
  )
  // verifyBytes: this surface tells the operator why a direction cannot be
  // dispatched, so it runs the byte check the dispatch gate runs. A held or
  // finished direction will not be dispatched at all: no poll, no file reads —
  // the panel shows the state sentence instead (readinessNotice).
  const showReadiness = readinessApplies(intent?.status, intent?.hold)
  const readiness = usePoll(
    () => (showReadiness && intent ? api.intentReadiness(intent.id, { verifyBytes: true }) : Promise.resolve(null)),
    showReadiness ? 10000 : 0,
    [intent?.id, showReadiness],
  )
  const [handoffOpen, setHandoffOpen] = useState(false)
  const [handoffNote, setHandoffNote] = useState('')
  // The version the next handoff submit will send. A conflict refresh sets it to
  // the newly read version, so the operator can review and resubmit the same
  // note without retyping it (or silently retrying against the old version).
  const [handoffVersion, setHandoffVersion] = useState<number | null>(null)
  useEffect(() => {
    setHandoffVersion(null)
    setHandoffNote('')
    setHandoffOpen(false)
  }, [intent?.id])
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [parking, setParking] = useState(false)
  const [retrying, setRetrying] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [withdrawing, setWithdrawing] = useState(false)
  const [reason, setReason] = useState('')
  const [intervening, setIntervening] = useState(false)
  const [interveneMode, setInterveneMode] = useState<'say' | 'steer'>('say')
  const [interveneText, setInterveneText] = useState('')
  const [refFact, setRefFact] = useState('')
  const [interrupt, setInterrupt] = useState(false)
  // Evidence is opened in the shared preview sheet, like the evidence page: the
  // detail panel is where you decide "is this the proof?", not where you save it.
  const [preview, setPreview] = useState<PreviewFile | null>(null)
  // The body renders expanded; a long one can be shrunk back to a preview.
  const [docCollapsed, setDocCollapsed] = useState(false)

  const reviewNotes = intent?.notes?.filter((n) => n.kind === 'review') ?? []
  const latestReviewNote = reviewNotes.length > 0 ? reviewNotes[reviewNotes.length - 1] : undefined
  const goal = node.meta?.goal
  const fact = node.meta?.fact
  // What this direction's latest executor run actually read, frozen at dispatch.
  const latestRun = intent ? latestExecutorRun(runs, intent.id) : undefined
  // 发起者: resolved by buildGraph from created_by (+ delegated_by_run fallback).
  // The panel never re-derives it from free text; an unresolvable row says so.
  const origin = node.origin ?? (intent ? intentOrigin(intent) : 'unknown')
  const originWho = originLabel(origin)
  const helpers = node.helpers

  // The seal's provenance: the facts it cited when the server recorded them;
  // else (legacy seal) the conclusion facts that carry the sealed evidence —
  // a bank case's e_… is not a fact, f_… is where it actually landed.
  const goalRefs = useMemo((): { legacy: boolean; items: { id: string; fact?: Fact }[] } => {
    if (!goal) return { legacy: false, items: [] }
    const byId = new Map(facts.map((f) => [f.id, f]))
    if (goal.from && goal.from.length > 0) {
      return { legacy: false, items: goal.from.map((id) => ({ id, fact: byId.get(id) })) }
    }
    const eid = goal.evidence_id
    if (goal.met && eid) {
      return {
        legacy: true,
        items: facts
          .filter((f) => (f.evidence_ids ?? []).includes(eid))
          .map((f) => ({ id: f.id, fact: f })),
      }
    }
    return { legacy: false, items: [] }
  }, [goal, facts])

  // Rows that arrive from several endpoints can come back partial: an older
  // server, a 404 wrapped into 200 + {}, or a shape change. A missing list must
  // read as "没读全", never as an empty list — and never be trusted as an array,
  // because `.length` on undefined used to white-screen the whole console
  // instead of showing the panel's error state.
  const readinessPremises = Array.isArray(readiness.data?.premises) ? readiness.data!.premises : undefined
  const usageDirect = usage.data?.direct
  const detailFact = detail.data?.fact
  // The graph's fact record is a lightweight summary and usually has no `doc`;
  // the full text (断言 + 复现过程) comes back on the detail read. Prefer that,
  // fall back to the record's own doc, and render nothing when both are blank.
  const docText = factDocText(detailFact?.doc, fact?.doc)
  const docLong = factDocLong(docText)
  const detailEvidence = Array.isArray(detail.data?.evidence) ? detail.data!.evidence : undefined
  const detailReviews = Array.isArray(detail.data?.reviews) ? detail.data!.reviews : undefined
  const detailRefs = Array.isArray(detailFact?.refs) ? detailFact.refs : []
  // Data arrived but is unusable: name every half that is missing instead of
  // acting on the rest. A missing review list must not read as "没有复核记录".
  const detailMissing: string[] = []
  if (detail.data) {
    if (!detailFact) detailMissing.push('断言本体')
    if (!detailReviews) detailMissing.push('复核记录')
    if (!detailEvidence) detailMissing.push('原件清单')
  }
  const detailShort = detailMissing.length > 0

  async function run(fn: () => Promise<unknown>, done: string) {
    setBusy(true)
    setNote('')
    try {
      await fn()
      setNote(done)
      onChanged()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  /**
   * A human verdict is bound to the assertion the operator read. The version
   * travels with the request; a 409 means the claim changed after the panel
   * loaded, so the answer is "refresh and re-judge", never an automatic resend.
   */
  async function submitReview(
    verdict: 'reject' | 'needs_evidence',
    notes: string,
    done: string,
  ): Promise<boolean> {
    if (!fact) return false
    const seen = detail.data?.version ?? fact.claim_version
    setBusy(true)
    setNote('')
    try {
      await api.reviewFact(fact.id, verdict, notes, seen)
      setNote(done)
      onChanged()
      return true
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        detail.reload()
        setNote('这条事实的断言在你判定期间改过，已刷新到最新版本；请核对后重新判定。')
      } else {
        setNote(e instanceof Error ? e.message : String(e))
      }
      return false
    } finally {
      setBusy(false)
    }
  }

  async function sendIntervene() {
    if (!intent || !interveneText.trim()) return
    setBusy(true)
    setNote('')
    try {
      if (interveneMode === 'say') {
        const r = await api.sayIntent(intent.id, interveneText.trim(), interrupt, refFact.trim())
        setNote(`已投递（IOA 接受，${r.message_id}）`)
      } else {
        const r = await api.steerIntent(intent.id, interveneText.trim(), refFact.trim())
        setNote(`已中断并续话（新 run ${r.run_id}）`)
      }
      setInterveneText('')
      setRefFact('')
      setIntervening(false)
      onChanged()
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function openIntervene() {
    setIntervening((v) => !v)
  }

  async function doHandoff() {
    if (!intent) return
    setBusy(true)
    setNote('')
    const expected = handoffVersion ?? intent.handoff_version ?? 0
    try {
      const r = await api.handoffIntent(
        intent.id,
        handoffNote.trim(),
        expected,
      )
      setNote(`已交给新执行者（新 run ${r.run_id}）。旧执行者的日志保留。`)
      setHandoffOpen(false)
      setHandoffNote('')
      setHandoffVersion(null)
      onChanged()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && (e.code === 'handoff_conflict' || !e.code)) {
        // Version conflict: the note is the operator's input and must survive.
        // Read the current intent while the button stays busy, then keep the form
        // open against the version we just observed. The operator decides whether
        // to submit again after checking the latest handoff record.
        setNote('交接版本已变化：正在读取最新的意图与交接记录…')
        try {
          const fresh = await api.intent(intent.id)
          const latest = fresh.intent.handoff_version ?? 0
          setHandoffVersion(latest)
          setHandoffOpen(true)
          onChanged()
          setNote(`交接版本已变化（当前 v${latest}）：说明已保留，请核对最新交接记录后再次提交。`)
        } catch (refreshError) {
          setNote(`交接未成功（${e.message}）；读取最新记录也失败：${refreshError instanceof Error ? refreshError.message : String(refreshError)}。说明已保留，请稍后重试。`)
        }
        return
      }
      setNote(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  // 关掉详情后焦点回主画布，别掉到 BODY：画布的 Esc 只在画布自己拿着焦点时生效，
  // 焦点进了面板之后再按 Esc 原本什么都不会发生，而按钮的 title 写着「关闭（Esc）」。
  const closePanel = useCallback(() => {
    closeAndFocusCanvas(onClose)
  }, [onClose])

  function onPanelKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    // 证据预览/确认框是 Portal，但按 React 树仍冒到这里：它们自己处理过 Esc 时
    // （defaultPrevented）就不再关面板，否则一次 Esc 会关掉两层。
    if (!isEscapeToClose(event)) return
    event.preventDefault()
    closePanel()
  }

  return (
    // Pinned inspector: a definite top+bottom (not max-height) is what lets the
    // inner scroll area shrink and scroll. A max-height on an auto-height flex
    // column only clamps the box — the content kept its height and was simply
    // clipped, so a long fact had unreachable rows and no wheel scrolling.
    <div
      className="saker-graph-dock saker-graph-dock-right saker-graph-panel saker-graph-under-toolbar absolute flex flex-col overflow-hidden rounded-xl border border-hairline bg-card/95 shadow-elevated backdrop-blur"
      style={{ width: dock.width }}
      onKeyDown={onPanelKeyDown}
    >
      <DockResizeHandle dock={dock} />
      <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-hairline bg-surface-1/60 px-4 py-2.5">
        <Badge size="sm" variant={isFact ? kindVariant(fact?.kind) : 'primary'}>
          {nodeTypeLabel(node.type, fact?.kind)}
        </Badge>
        {node.attempts && node.attempts > 1 && (
          <Badge size="sm" variant="muted" title="这条意图的尝试次数（重试/续跑会累加）">
            ×{node.attempts}
          </Badge>
        )}
        {node.status && isnStatus(node.status) && (
          <IntentStatusChip status={node.status} hold={node.meta?.intent?.hold} />
        )}
        <DoneDeclaredBadge intent={intent} />
        <div className="ml-auto flex items-center gap-1">
          {treeActive && onShowAll && (
            <Button size="xs" variant="ghost" title="恢复显示全部节点" onClick={onShowAll}>
              <Maximize2 className="size-3.5" aria-hidden="true" /> 看全图
            </Button>
          )}
          {!treeActive && onOnlyTree && (
            <Button
              size="xs"
              variant="ghost"
              title="显示当前节点、全部祖先及向下两跳"
              onClick={onOnlyTree}
            >
              <GitBranch className="size-3.5" aria-hidden="true" /> 只看这棵树
            </Button>
          )}
          <Button size="icon-sm" variant="ghost" onClick={closePanel} aria-label="关闭" title="关闭（Esc）">
            <X className="size-4" />
          </Button>
        </div>
      </div>

      {/* Radix ScrollArea sizes its viewport as a CSS table so horizontal scroll
          can work; with a controller-only panel every long row then widened the
          table to its max-content width (a single review note made the content
          15k px wide) and the panel showed a clipped slice of it. The panel only
          scrolls vertically, so lay the ScrollArea content out as a block: it
          stays 100% of the 360px panel and text wraps. */}
      <ScrollArea className="min-h-0 flex-1 [&_[data-radix-scroll-area-viewport]>div]:!block">
        <div className="min-w-0 space-y-4 p-4 text-[12px] leading-5">
          <div className="min-w-0">
            <div className="saker-break text-[14px] font-semibold leading-5 text-ink">{node.label}</div>
            {node.sub && <div className="saker-break mt-1 text-[12px] leading-relaxed text-ink-2">{node.sub}</div>}
          </div>
          {/* The plan text is fixed once dispatched; before that it can be
              corrected here without opening a separate dialog. */}
          {intent && <IntentPlanEditor intent={intent} onSaved={onChanged} />}
          {/* Why the direction is stopped, in the same words the run page uses.
              Only a cancelled executor run has this text, and only a held
              direction shows it: a continuation cancel (steer/retry/pause) is
              not a stop. */}
          {intent && node.stopped && (
            <CancelReasonCallout
              run={latestRun?.status === 'canceled' ? latestRun : undefined}
              heldBy={intent.held_by}
              holdReason={intent.hold_reason}
            />
          )}
          {intent && latestRun && <RunSummaryLine run={latestRun} onOpenRun={onOpenRun} />}
          {!showReadiness && intent && readinessNotice(intent.status, intent.hold) && (
            <p className="text-[11px] text-ink-3">{readinessNotice(intent.status, intent.hold)}</p>
          )}
          {showReadiness && readiness.data && (
            <Callout tone={readiness.data.ready ? 'info' : 'warning'} className="leading-relaxed">
              <div className="text-[12px] font-semibold">{readiness.error ? '暂时无法获取最新前提' : readiness.data.ready ? readiness.data.policy === 'verified_only' ? '前提初检通过，执行前还会校验原件' : '当前政策允许派发，线索与证明仍分别记录' : '暂不可执行：前提需要处理'}</div>
              <div className="mt-1 text-[11px] text-ink-3">按当前账本核对；图的历史快照与当前断言分别保留。</div>
              <div className="mt-1 space-y-1">
                {readinessPremises ? (
                  <>
                    {readinessPremises.length === 0 && <div className="text-[12px]">授权首轮探测，没有声明事实前提。</div>}
                    {readinessPremises.map((p) => (
                      <div key={p.id} className="text-[12px]">
                        <button className="text-left underline underline-offset-2" onClick={() => onSelectFact(p.id)}>{p.title || p.id}</button>
                        <span> · 断言 v{p.claim_version ?? '?'} · {p.usable ? '准入通过' : p.reason}</span>
                        {p.review?.state && <span className="text-ink-3"> · {reviewStateLabel(p.review.state)}</span>}
                        {p.review_id && <div className="text-ink-3">独立核验收据已绑定本版断言；原件完整性另行检查。</div>}
                      </div>
                    ))}
                  </>
                ) : (
                  <div className="text-[12px] text-warning">前提清单没读全：服务端没有返回条目，这里不能说「没有前提」。</div>
                )}
              </div>
              {readiness.data.review_hint && <div className="mt-2 text-[11px] text-ink-2">{readiness.data.review_hint}</div>}
              {readiness.data.self_reference_hint && (
                <div className="mt-2 text-[11px] text-warning">{readiness.data.self_reference_hint}</div>
              )}
            </Callout>
          )}
          {showReadiness && readiness.error && <Callout tone="warning" className="leading-relaxed">暂时无法核对前提，请刷新后再决定是否执行。</Callout>}
          {intent?.status === 'review_failed' && (
            <Callout tone="warning" className="leading-relaxed">
              <div className="text-[12px] font-semibold">{reviewFailureLabel(reviewFailure.data)}</div>
              <div className="mt-0.5 text-[11px] leading-relaxed">{reviewFailureHint(reviewFailure.data)}</div>
              {latestReviewNote && (
                <div className="saker-break mt-1 text-[11px] leading-relaxed">{latestReviewNote.text}</div>
              )}
              {(intent.review_failed_facts?.length ?? 0) > 0 && (
                <div className="mt-1.5 flex flex-wrap items-center gap-1">
                  <span className="text-[11px] text-ink-3">待复核事实</span>
                  {intent.review_failed_facts!.map((id) => (
                    <button
                      key={id}
                      type="button"
                      onClick={() => onSelectFact(id)}
                      title={`在图上打开事实 ${id}`}
                      className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-2 transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      {shortID(id)}
                    </button>
                  ))}
                </div>
              )}
              <div className="mt-1 text-[11px] text-ink-3">以事实当前断言版本的最新复核 verdict 为准；旧备注只作历史。可在这条意图上重试/续跑。</div>
            </Callout>
          )}

          {fact && (
            <Section title="事实">
              <Prop label="置信" value={fact.confidence || '—'} />
              {/* The grade lives on the fact (like the title): show it here so
                  clicking a vulnerability node answers "how severe" without
                  leaving the graph. Absent means ungraded, not low. */}
              <Prop
                label="等级"
                mono={false}
                value={
                  fact.severity ? (
                    <Badge size="sm" variant={severityVariant(fact.severity)}>{severityLabel(fact.severity)}</Badge>
                  ) : (
                    <span className="text-ink-3">未标注</span>
                  )
                }
              />
              <Prop label="证明" value={fact.proof ?? 'dynamic'} />
              <Prop label="创建" value={fmtTime(fact.created_at)} />
            </Section>
          )}

          {/* Claim identity and review provenance: which assertion version each
              review proved, and whether the assertion changed after the proof.
              It replaces the old note-only review list (a note cannot say which
              version it judged). */}
          {isFact && detailFact && detailReviews && (
            <FactClaimDetail fact={detailFact} reviews={detailReviews} />
          )}

          {/* A read that failed or came back without the fact/evidence half is
              reported here: the panel keeps the node's own facts and says what it
              could not check, instead of rendering a made-up "证据（0）". */}
          {isFact && !detail.data && detail.error && (
            <Section title="事实详情">
              <EmptyLine>这条事实的详情没读全：{detail.error}</EmptyLine>
              <Button size="xs" variant="outline" className="mt-1" onClick={detail.reload}>
                重试读取
              </Button>
            </Section>
          )}
          {isFact && detailShort && (
            <Section title="事实详情">
              <EmptyLine>
                这条事实的详情没读全：缺少{detailMissing.join('、')}。上面显示的是图谱里的记录，
                不能据此判断复核与证据。
              </EmptyLine>
              <Button size="xs" variant="outline" className="mt-1" onClick={detail.reload}>
                重试读取
              </Button>
            </Section>
          )}

          {/* 正文 / 复现: the summary above is not the record. The detail read
              carries the full doc; the graph record is the fallback. It renders
              expanded by default — the operator opened the fact for the body —
              and only a genuinely long one offers 收起 (a preview-size cap);
              while the detail is still loading nothing is shown, and a failed
              read keeps the 事实详情 hint above instead of inventing text. */}
          {docText && !detail.loading && (
            <Section
              title="详情 / 复现"
              extra={
                docLong ? (
                  <Button
                    size="xs"
                    variant="ghost"
                    aria-expanded={!docCollapsed}
                    onClick={() => setDocCollapsed((v) => !v)}
                  >
                    {docCollapsed ? '展开全文' : '收起'}
                  </Button>
                ) : undefined
              }
            >
              <pre
                className={cn(
                  'saker-mono saker-break overflow-auto whitespace-pre-wrap rounded-lg border border-hairline bg-surface-2 p-2 text-[11px] leading-relaxed',
                  // Expanded is the default and stays readable: scroll inside the
                  // body rather than clipping it to a sliver. 收起 is the short view.
                  docCollapsed ? 'max-h-40' : 'max-h-[70vh]',
                )}
              >
                {docText}
              </pre>
            </Section>
          )}

          {isFact && detailEvidence && (
            <Section title={`证据（${detailEvidence.length}）`}>
              {detailEvidence.length === 0 && (
                <EmptyLine>没有证据引用：需要证明这条断言时，证据会以文件/请求记录出现在这里。</EmptyLine>
              )}
              {detailEvidence.map((ev) => (
                <div
                  key={ev.id}
                  className="flex items-center gap-1.5 rounded-lg border border-hairline px-1.5 transition-colors hover:border-ink-3/40 hover:bg-surface-2/60"
                >
                  <button
                    type="button"
                    onClick={() => setPreview(evidencePreviewFile(ev))}
                    title="点击预览"
                    className="flex min-w-0 flex-1 items-center gap-1.5 py-1.5 text-left"
                  >
                    <Eye className="h-3.5 w-3.5 shrink-0 text-ink-3" />
                    <span className="saker-break min-w-0 flex-1 font-mono text-[11px] text-ink-2">{ev.uri}</span>
                  </button>
                  <span className="saker-nums shrink-0 font-mono text-[11px] text-ink-3">
                    {fmtBytes(ev.size)}
                  </span>
                  <a
                    href={api.evidenceDownloadURL(ev.id)}
                    download={ev.name || ev.uri.split('/').pop() || undefined}
                    title="下载原文件"
                    aria-label={`下载 ${ev.uri}`}
                    className="shrink-0 rounded px-1.5 py-1 text-ink-3 transition-colors hover:bg-surface-2 hover:text-primary"
                  >
                    <Download className="h-3.5 w-3.5" />
                  </a>
                </div>
              ))}
            </Section>
          )}

          {/* 出处：项目图谱记下了从哪来，但打不开（url / artifact / 已删的文件）。
              它不是证据，所以既不做成链接，也不和上面的证据合并计数（D44）。 */}
          {isFact && detailRefs.length > 0 && (
            <Section title={`出处（打不开，${detailRefs.length}）`}>
              {detailRefs.map((ref, i) => (
                <div
                  key={`${ref.scheme}:${ref.locator}:${i}`}
                  className="flex items-center gap-1.5 rounded-lg border border-dashed border-hairline px-2 py-1.5"
                >
                  <span className="shrink-0 rounded bg-surface-2 px-1.5 font-mono text-[11px] text-ink-3">
                    {ref.scheme}
                  </span>
                  <span className="saker-break min-w-0 flex-1 font-mono text-[11px] text-ink-2">{ref.locator}</span>
                </div>
              ))}
              <div className="text-[11px] leading-relaxed text-ink-3">
                出处只说明"从哪来"，不能当已证实——报告里它只会出现在范围与限制。
              </div>
            </Section>
          )}

          {intent && (
            <Section title="意图">
              <Prop label="尝试" value={fmtInt(intent.attempts)} />
              {intent.node && <Prop label="节点" value={intent.node} title="执行节点" />}
              {node.model && (
                <Prop
                  label="模型"
                  value={node.model}
                  title="实际使用的模型"
                />
              )}
              <Prop
                label="发起"
                value={originWho}
                mono={false}
                title={intent.created_by_participant_id ? `created_by_participant_id = ${intent.created_by_participant_id}` : '旧数据没有记录发起者'}
              />
              {intent.updated_at && <Prop label="更新" value={fmtTime(intent.updated_at)} />}
              <Prop
                label="计划正文"
                value={`v${intent.edit_version ?? 0}${intent.edited_by ? ` · ${intent.edited_by}` : ''}`}
                title="标题/说明/验收标准的版本；派发前可编辑，派发后正文随任务书归档"
              />
            </Section>
          )}
          {helpers && helpers.calls > 0 && (
            <Section title="本轮助手">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[12px] text-ink-2">
                <span className="saker-nums">{`调用 ${fmtInt(helpers.calls)} 次`}</span>
                <span className="saker-nums">{`活跃 ${fmtInt(helpers.active)}`}</span>
                <span className="saker-nums">{`累计 ${helperDuration(helpers.durationMs)}`}</span>
              </div>
              <div className="text-[11px] leading-relaxed text-ink-3">
                助手归父回合管理，不登记 run/usage、不产生事实；结论仍由本意图声明。
              </div>
            </Section>
          )}
          {(node.warnings?.length ?? 0) > 0 && (
            <details className="group rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-2">
              <summary className="flex cursor-pointer select-none items-center gap-1.5 text-[12px] font-medium text-warning">
                ⚠ 告警（{node.warnings!.length}）
                <span className="ml-auto text-[11px] font-normal text-ink-3 group-open:hidden">展开</span>
                <span className="ml-auto hidden text-[11px] font-normal text-ink-3 group-open:inline">收起</span>
              </summary>
              <div className="mt-1.5 space-y-1">
                {node.warnings!.map((w, i) => (
                  <div key={i} className="saker-break text-[12px] leading-relaxed text-ink-2">
                    {/subagent/i.test(w) && (
                      <Badge size="sm" variant="warning" className="mr-1">
                        原生 subagent
                      </Badge>
                    )}
                    {w}
                  </div>
                ))}
              </div>
            </details>
          )}
          {intent?.park_reason && (
            <Callout tone="warning" className="leading-relaxed">
              停放理由：{intent.park_reason}
            </Callout>
          )}
          {intent?.status === 'parked' &&
            (intent.parked_premise_fact_ids?.length ?? 0) > 0 &&
            readiness.data?.ready && (
              <Callout tone="info" className="leading-relaxed">
                <div className="flex flex-wrap items-center gap-2">
                  <span>停放时未就绪的前提现在已经通过复核；可重开继续，或确认已并入其他意图。</span>
                  <Button
                    size="xs"
                    variant="soft"
                    disabled={busy}
                    onClick={() => run(() => api.reopenIntent(intent.id), '已重开')}
                  >
                    重开
                  </Button>
                </div>
              </Callout>
            )}
          {intent?.doc && (
            <pre className="saker-mono saker-break max-h-32 overflow-auto whitespace-pre-wrap rounded-lg border border-hairline bg-surface-2 p-2.5 text-[11px] leading-relaxed text-ink-2">
              {intent.doc}
            </pre>
          )}

          {/* Context editing: references/pins/interest entities, version CAS.
              Hard premises stay read-only inside the editor and do not move. */}
          {intent && (
            <IntentContextEditor intent={intent} onSaved={onChanged} onSelectFact={onSelectFact} />
          )}

          {/* Evidence of what this direction's latest executor run actually read,
              frozen at dispatch (Run.premise_claim_versions/context_fact_versions). */}
          {intent && latestRun && (
            <details className="group rounded-lg border border-hairline bg-surface-2/40 px-2.5 py-2">
              <summary className="cursor-pointer text-[12px] font-medium text-ink-2 transition-colors hover:text-ink">
                本次运行读到的版本（{latestRun.id}）
              </summary>
              <div className="mt-1.5">
                <RunEvidenceView run={latestRun} facts={facts} onSelectFact={onSelectFact} />
              </div>
            </details>
          )}

          {usage.data && (
            <Section title="token 用量">
              {usageDirect ? (
                <div className="flex flex-wrap gap-x-3 gap-y-1 font-mono text-[12px] text-ink-2">
                  <span className="saker-nums">直接 {fmtInt(usageDirect.total_tokens)} tok</span>
                  <span className="saker-nums">{usageDirect.runs} run</span>
                  {usageDirect.unknown_usage_runs > 0 && (
                    <span className="text-ink-3">{usageDirect.unknown_usage_runs} 个用量未知</span>
                  )}
                </div>
              ) : (
                <EmptyLine>用量没读全：服务端没有返回意图用量，这里不显示 0。</EmptyLine>
              )}
            </Section>
          )}

          {intent && (
            <div className="rounded-lg border border-hairline bg-surface-2/40 px-2.5 py-2">
              <div className="flex items-center gap-1.5">
                <span className="saker-eyebrow normal-case tracking-normal text-ink-2">新执行者接班</span>
                <Badge size="sm" variant="muted">交接 v{handoffVersion ?? intent.handoff_version ?? 0}</Badge>
                {<Button
                  size="xs"
                  variant="outline"
                  className="ml-auto"
                  onClick={() => setHandoffOpen((v) => !v)}
                >
                  换新执行者
                </Button>}
              </div>
              {handoffOpen && (
                <div className="mt-1.5 space-y-1.5">
                  <Input
                    value={handoffNote}
                    onChange={(e) => setHandoffNote(e.target.value)}
                    placeholder="交接说明（已确认事实、做过但无效的尝试、下一步）"
                    className="h-8 text-[12px]"
                  />
                  <div className="flex gap-1.5">
                    <Button size="xs" onClick={() => void doHandoff()} disabled={busy}>
                      交接并接班
                    </Button>
                    <Button size="xs" variant="ghost" onClick={() => setHandoffOpen(false)}>
                      取消
                    </Button>
                  </div>
                  <div className="text-ink-3">
                    会停掉当前 run、撤销旧身份，由新执行者用同一统一模型连接继续，并携带事实/证据与等待项快照；旧对话不会复制过去。
                  </div>
                </div>
              )}
            </div>
          )}


          {/* 重派/续话/复核备注：给操作员和下一轮决策看的历史，不是执行者约束。
              它不在 intent.doc 里，派发提示词也不读（D72）。 */}
          {intent?.notes && intent.notes.length > 0 && (
            <Section title={`备注（决策记录，${intent.notes.length}）`}>
              {intent.notes.map((n, i) => (
                <div key={`${n.at}:${i}`} className="rounded-lg border border-hairline px-2.5 py-2">
                  <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
                    <Badge size="sm" variant={n.kind === 'retry' || n.kind === 'restart' || n.kind === 'review' || n.kind === 'bootstrap' ? 'warning' : 'info'}>
                      {n.kind === 'retry' ? '重派' : n.kind === 'restart' ? '重启' : n.kind === 'steer' ? '续话' : n.kind === 'review' ? '复核' : n.kind === 'bootstrap' ? '首触确认' : n.kind}
                    </Badge>
                    <span className="saker-nums font-mono">{fmtTime(n.at)}</span>
                    {n.ref_fact && <span className="ml-auto truncate font-mono">依据 {n.ref_fact}</span>}
                  </div>
                  <div className="saker-break mt-1 line-clamp-3 text-[12px] leading-relaxed text-ink-2" title={n.text}>
                    {n.text}
                  </div>
                </div>
              ))}
              <div className="text-[11px] leading-relaxed text-ink-3">
                这是操作员/指挥官给本轮的解释，不会进入执行者的约束段。
              </div>
            </Section>
          )}

          {intent?.interjections && intent.interjections.length > 0 && (
            <Section title={`插话记录（${intent.interjections.length}）`}>
              {intent.interjections.map((it, i) => (
                <div key={`${it.at}:${i}`} className="rounded-lg border border-hairline px-2.5 py-2">
                  <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
                    <span className="saker-nums font-mono">{fmtTime(it.at)}</span>
                    {it.interrupt && (
                      <Badge size="sm" variant="warning">
                        打断
                      </Badge>
                    )}
                    {it.message_id && <span className="ml-auto truncate font-mono">{it.message_id}</span>}
                  </div>
                  <div className="saker-break mt-1 line-clamp-3 text-[12px] leading-relaxed text-ink-2" title={it.text}>
                    {it.text}
                  </div>
                </div>
              ))}
            </Section>
          )}

          {goal && (
            <Section
              title="目标"
              extra={
                <Badge size="sm" variant={goal.met ? 'success' : 'warning'}>
                  {goal.closed ? '已关闭' : goal.met ? '已达成' : '未达成'}
                </Badge>
              }
            >
              <div className="saker-break text-[12.5px] font-medium leading-relaxed text-ink">{goal.statement}</div>
              {goal.start ? (
                <div className="saker-break mt-1 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">
                  起点：{goal.start}
                </div>
              ) : goal.quantified_target ? (
                <div className="saker-break mt-1 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">
                  口径（旧记录）：{goal.quantified_target}
                </div>
              ) : null}
              <Prop
                label="时间"
                value={goal.completed_at ? `达成于 ${fmtTime(goal.completed_at)}` : `创建于 ${fmtTime(goal.created_at)}`}
                mono={false}
              />
              {goal.met && (
                <div className="mt-1.5 space-y-1.5 border-t border-hairline pt-2">
                  <div className="saker-eyebrow normal-case tracking-normal text-ink-2">为什么达成</div>
                  {goal.note ? (
                    <div className="saker-break whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">
                      {goal.note}
                    </div>
                  ) : (
                    <div className="text-[12px] italic text-ink-3">达成理由未记录（旧数据）</div>
                  )}
                  <div className="saker-eyebrow normal-case tracking-normal text-ink-2">
                    依据{goalRefs.legacy ? '（证据所在结论）' : ''}
                  </div>
                  {goalRefs.items.length > 0 ? (
                    <div className="space-y-0.5">
                      {goalRefs.items.map(({ id, fact: refFact }) => (
                        <button
                          key={id}
                          type="button"
                          onClick={() => onSelectFact(refFact?.id ?? id)}
                          className="flex w-full items-start gap-1.5 rounded px-1.5 py-1 text-left transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                          title={refFact ? `${id} · ${refFact.title}` : `${id}（事实不在当前列表）`}
                        >
                          <span className="shrink-0 pt-px font-mono text-[11px] leading-relaxed text-info">{id}</span>
                          <span className="saker-break min-w-0 flex-1 text-[12px] text-ink-2">
                            {refFact?.title ?? '事实不在当前列表'}
                          </span>
                        </button>
                      ))}
                    </div>
                  ) : (
                    <div className="font-mono text-[11px] text-ink-3">
                      {goal.evidence_id ? `证据 ${goal.evidence_id}` : '未记录依据'}
                    </div>
                  )}
                  {/* The receipt is a historical record: a later correction of a
                      cited fact does not rewrite it. The server computes this
                      flag; saying it is stale is the honest report, un-setting
                      `met` would erase the proof the operator already accepted. */}
                  {goal.receipt_stale && (
                    <Callout tone="warning" className="mt-1.5 leading-relaxed">
                      <div className="text-[12px] font-semibold">
                        收据过时：所依据事实已更新，需重新判断（历史完成证明保留）
                      </div>
                      {(goal.receipt_changed ?? []).length > 0 && (
                        <div className="mt-1 flex flex-wrap items-center gap-1 text-[11px]">
                          <span className="text-ink-3">变更事实</span>
                          {goal.receipt_changed!.map((fid) => (
                            <button
                              key={fid}
                              type="button"
                              onClick={() => onSelectFact(fid)}
                              className="rounded border border-warning/40 bg-warning/10 px-1.5 font-mono text-[11px] leading-4 text-warning transition-colors hover:border-warning/70 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                              title={`在图谱中打开事实 ${fid}`}
                            >
                              {fid}
                            </button>
                          ))}
                        </div>
                      )}
                      <div className="mt-1 text-[11px] text-ink-3">
                        目标仍记为已达成；历史盖章不会被自动撤销。
                      </div>
                    </Callout>
                  )}
                </div>
              )}
            </Section>
          )}

          {/* actions: one soft main action per state, the rest outline/ghost.
              Disabled buttons keep their label, so the state is readable. */}
          <div className="space-y-2 border-t border-hairline pt-3">
            <div className="saker-eyebrow">操作</div>
            <div className="flex flex-wrap gap-2">
            {/* A done direction cannot be claimed for a fresh run; "续跑" below
                continues its session instead. Showing 派发 on done only produced
                "无法认领（状态为 done）". */}
            {intent && intent.status === 'pending' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={() => run(() => api.runIntent(intent.id), '已派发')}>
                <Play className="h-3.5 w-3.5" /> 派发
              </Button>
            )}
            {intent && intent.status === 'running' && intent.run_id && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => run(() => api.cancelRun(intent.run_id!), '已取消')}>
                <Activity className="h-3.5 w-3.5" /> 取消
              </Button>
            )}
            {intent && intent.status === 'running' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={openIntervene}>
                <Send className="h-3.5 w-3.5" /> 干预
              </Button>
            )}
            {intent && intent.status === 'parked' && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => run(() => api.reopenIntent(intent.id), '已重开')}>
                <RotateCcw className="h-3.5 w-3.5" /> 重开
              </Button>
            )}
            {intent && (intent.status === 'failed' || intent.status === 'review_failed' || intent.status === 'pending') && (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => setRetrying(true)}
                title="重试：可续跑原会话（保留记忆）或重派新会话（丢弃记忆），都会重投未确认消息"
              >
                <RefreshCw className="h-3.5 w-3.5" /> 重试
              </Button>
            )}
            {intent && (intent.status === 'done' || intent.status === 'review_failed' || intent.status === 'parked') && (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() =>
                  run(
                    () => api.continueIntent(intent.id),
                    '已续跑：同一会话继续（未重发任务书）',
                  )
                }
                title="同一会话继续，不重发任务书；没有原会话会报 continue_unavailable，用重试新会话"
              >
                <RefreshCw className="h-3.5 w-3.5" /> 续跑
              </Button>
            )}
            {intent && intent.status !== 'parked' && (
              <Button
                size="xs"
                variant="ghost"
                disabled={intent.status === 'running' || intent.status === 'claimed'}
                title={
                  intent.status === 'running' || intent.status === 'claimed'
                    ? '先停止，等回到 pending 再停放'
                    : '停放：写清理由，记住为什么搁置'
                }
                onClick={() => setParking((v) => !v)}
              >
                停放
              </Button>
            )}
            {fact && (fact.kind === 'vuln' || fact.kind === 'cred' || (premisePolicy.data?.effective === 'verified_only' && ['host','service','loot','artifact'].includes(fact.kind))) && fact.status === 'unverified' && (
              <Button size="xs" variant="soft" disabled={busy} onClick={() => run(() => api.verifyFact(fact.id), '已排入复核')}>
                <ShieldCheck className="h-3.5 w-3.5" /> 复核
              </Button>
            )}
            {/* A human may only take a conclusion down or put it back up: 证实
                is a verifier rerun, never a button (D41). */}
            {fact && (fact.kind === 'vuln' || fact.kind === 'cred' || (premisePolicy.data?.effective === 'verified_only' && ['host','service','loot','artifact'].includes(fact.kind))) && fact.status !== 'rejected' && fact.status !== 'withdrawn' && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => setRejecting((v) => !v)}>
                <ShieldX className="h-3.5 w-3.5" /> 判定不成立
              </Button>
            )}
            {fact && fact.status === 'rejected' && (
              <Button
                size="xs"
                variant="ghost"
                disabled={busy}
                onClick={() =>
                  void submitReview('needs_evidence', '人工撤销驳回：回到未验证', '已撤销驳回')
                }
              >
                <RotateCcw className="h-3.5 w-3.5" /> 撤销驳回
              </Button>
            )}
            {/* Withdrawal is for records that should not stand at all (a write
                probe, a duplicated observation): the ledger keeps the row and
                its history, but it leaves the work surfaces. */}
            {fact && fact.status !== 'withdrawn' && (
              <Button size="xs" variant="ghost" disabled={busy} onClick={() => setWithdrawing((v) => !v)}>
                <ShieldX className="h-3.5 w-3.5" /> 撤回记录
              </Button>
            )}
            </div>
          </div>

          {intervening && intent && intent.status === 'running' && (
            <div className="space-y-2 rounded-lg border border-primary/25 bg-primary/[0.05] p-2.5">
              <div className="flex gap-1.5">
                <Button
                  size="xs"
                  variant={interveneMode === 'say' ? 'soft' : 'ghost'}
                  onClick={() => setInterveneMode('say')}
                >
                  插话
                </Button>
                <Button
                  size="xs"
                  variant={interveneMode === 'steer' ? 'soft' : 'ghost'}
                  onClick={() => setInterveneMode('steer')}
                >
                  中断续话
                </Button>
              </div>
              <div className="text-ink-3">
                {interveneMode === 'say'
                  ? '进入当前 run，不取消、不重开；默认在下一轮模型调用前生效。不设硬上限，但每条会多一轮模型步。'
                  : '取消当前 run，用同一轨迹开一个新 turn；已写出的证据保留。steer 每意图最多 3 次。'}
              </div>
              <Input
                value={interveneText}
                onChange={(e) => setInterveneText(e.target.value)}
                placeholder={
                  interveneMode === 'say'
                    ? '例如：停止铺路径字典，收窄到登录口'
                    : '例如：先停，换到 /portal/login 登录口做单变量差分'
                }
                className="h-8 text-[12px]"
                maxLength={500}
              />
              <Input
                value={refFact}
                onChange={(e) => setRefFact(e.target.value)}
                placeholder="依据事实 id（可选，如 f_xxx）"
                className="h-8 font-mono text-[11px]"
              />
              {interveneMode === 'say' && (
                <label className="flex items-center gap-2 text-[12px] text-ink-2">
                  <input
                    type="checkbox"
                    className="h-3.5 w-3.5 accent-primary"
                    checked={interrupt}
                    onChange={(e) => setInterrupt(e.target.checked)}
                  />
                  打断当前模型请求（只跳过尚未开始的工具）
                </label>
              )}
              <div className="flex items-center gap-1.5">
                <Button size="xs" disabled={busy || !interveneText.trim()} onClick={sendIntervene}>
                  {interveneMode === 'say' ? '发送到当前 run' : '中断并续话'}
                </Button>
                <span className="text-[11px] text-ink-3">
                  {interveneMode === 'say'
                    ? `${intent.say_count ?? 0} 次（不限）`
                    : `${intent.steer_count ?? 0}/3`}
                </span>
              </div>
            </div>
          )}

          {rejecting && fact && (
            <div className="space-y-1">
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="为什么这条不成立（必填）"
                className="h-8 text-[12px]"
              />
              <Button
                size="xs"
                disabled={busy || !reason.trim()}
                onClick={async () => {
                  if (await submitReview('reject', reason.trim(), '已判定不成立')) {
                    setRejecting(false)
                    setReason('')
                  }
                }}
              >
                确认判定
              </Button>
            </div>
          )}

          {withdrawing && fact && (
            <div className="space-y-1">
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="为什么这条记录不应保留（必填）"
                className="h-8 text-[12px]"
              />
              <Button
                size="xs"
                disabled={busy || !reason.trim()}
                onClick={() =>
                  run(async () => {
                    await api.withdrawFact(fact.id, reason.trim())
                    setWithdrawing(false)
                    setReason('')
                  }, '已撤回')
                }
              >
                确认撤回
              </Button>
            </div>
          )}

          {parking && intent && (
            <div className="space-y-1">
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="停放理由（必填）"
                className="h-8 text-[12px]"
              />
              <Button
                size="xs"
                disabled={busy || !reason.trim() || intent.status === 'running' || intent.status === 'claimed'}
                title={
                  intent.status === 'running' || intent.status === 'claimed'
                    ? '先停止，等回到 pending 再停放'
                    : undefined
                }
                onClick={() => run(() => api.parkIntent(intent.id, reason.trim()), '已停放')}
              >
                确认停放
              </Button>
            </div>
          )}

          {note && <div className="rounded-lg border border-hairline bg-surface-2/50 px-2.5 py-2 text-[12px] leading-relaxed text-ink-2">{note}</div>}
        </div>
      </ScrollArea>

      {intent && (
        <RetryIntentDialog
          open={retrying}
          onOpenChange={setRetrying}
          intentId={intent.id}
          title={intent.title}
          onRetried={(r) => {
            setNote(
              `已重试（${r.resume ? '续跑原会话' : '重派新会话'}）：新 run ${r.run_id}，重投 ${r.redelivered} 条未确认消息。`,
            )
            onChanged()
          }}
        />
      )}

      <EvidencePreview file={preview} onOpenChange={(open) => !open && setPreview(null)} />
    </div>
  )
}

function isnStatus(s: string): boolean {
  return ['pending', 'claimed', 'running', 'waiting', 'reviewing', 'done', 'failed', 'review_failed', 'parked', 'unverified', 'verified', 'rejected'].includes(s)
}

export default GraphNodePanel

