// The console's proposal queue: what executors suggested, what is waiting for a
// decision, and what the operator/commander already decided.
//
// The chain has two separate steps on purpose. An approval creates a pending
// intent but starts nothing; the dispatch button is the second step that starts
// the run. Keeping them apart in the UI is what makes "approved but not yet
// dispatched" visible instead of collapsing both into one click.
import { useState } from 'react'
import { Check, ListChecks, ScrollText, Send, X } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Field,
  SkeletonList,
  Spinner,
  Textarea,
} from '@cyber/ui'
import { Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { useLoad, usePoll } from '@/lib/hooks'
import {
  activeProposalCount,
  orderProposals,
  PROPOSAL_EMPTY,
  PROPOSAL_REJECT_PLACEHOLDER,
  proposalAcceptance,
  proposalApprovedReceipt,
  proposalApproveBody,
  proposalDecidedByLabel,
  proposalDecisionError,
  proposalDispatchedReceipt,
  proposalDispatchBody,
  proposalRationale,
  proposalRejectedReceipt,
  proposalRejectBody,
  proposalRejectDraftError,
  proposalScopeLabel,
  proposalStatusLabel,
  proposalStatusVariant,
  proposalTimeLine,
} from '@/lib/proposals'
import type { Proposal } from '@/lib/types'

/**
 * The 通信 sidebar card: how many proposals still need an action, the first few,
 * and the one button that opens the management dialog. A 248px column cannot
 * show a decision form and a grouped queue at once, so management stays in the
 * dialog.
 */
export function OperatorProposalsCard() {
  const [open, setOpen] = useState(false)
  const polled = usePoll(() => api.proposals(), 9_000)
  const rows = orderProposals(polled.data?.proposals ?? [])
  const count = activeProposalCount(rows)
  const openRows = rows.filter((row) => row.status === 'proposed' || row.status === 'approved')

  return (
    <>
      <Panel
        data-testid="operator-proposals"
        title={`提议（${count}）`}
        extra={
          <Button size="xs" variant="outline" onClick={() => setOpen(true)}>
            <ListChecks className="h-3.5 w-3.5" /> 管理
          </Button>
        }
        bodyClassName="p-3"
      >
        <p className="text-[10.5px] leading-4 text-ink-3">{PROPOSAL_EMPTY}</p>
        {polled.error && (
          <p role="alert" className="mt-2 text-[11px] leading-4 text-destructive">
            读取提议失败（{polled.error}）
          </p>
        )}
        {polled.loading && !polled.data && <SkeletonList count={2} />}
        {!polled.loading && !polled.error && openRows.length === 0 && (
          <p className="mt-2 text-[11px] leading-4 text-ink-3">当前没有待处理的提议。</p>
        )}
        {openRows.length > 0 && (
          <ul role="list" aria-label="当前待处理提议" className="mt-2 space-y-1.5">
            {openRows.slice(0, 3).map((row) => (
              <li key={row.id} className="rounded-md border border-hairline bg-surface-2/40 px-2 py-1.5">
                <div className="flex items-center gap-1.5 text-[10.5px] text-ink-3">
                  <Badge size="sm" variant={proposalStatusVariant(row.status)}>
                    {proposalStatusLabel(row.status)}
                  </Badge>
                  <span className="min-w-0 truncate">{proposalScopeLabel(row)}</span>
                  <span className="tw-nums ml-auto shrink-0 font-mono">v{row.version}</span>
                </div>
                <p className="tw-break mt-1 line-clamp-2 whitespace-pre-wrap text-[11px] leading-4 text-ink-2">
                  {row.title}
                </p>
              </li>
            ))}
            {openRows.length > 3 && (
              <li className="text-[10.5px] text-ink-3">另有 {openRows.length - 3} 条，打开「管理」查看。</li>
            )}
          </ul>
        )}
      </Panel>
      {open && (
        <OperatorProposalsDialog
          onClose={() => setOpen(false)}
          onChanged={() => polled.reload()}
        />
      )}
    </>
  )
}

/** The proposal queue grouped by the action it is waiting for. Exported so the
 *  component test can render it with a stubbed api. */
export function OperatorProposalsDialog({ onClose, onChanged }: { onClose: () => void; onChanged: () => void }) {
  const rows = useLoad(() => api.proposals(), [])
  const sorted = orderProposals(rows.data?.proposals ?? [])
  const pending = sorted.filter((row) => row.status === 'proposed')
  const approved = sorted.filter((row) => row.status === 'approved')
  const history = sorted.filter((row) => row.status !== 'proposed' && row.status !== 'approved')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState('')
  const [rejecting, setRejecting] = useState('')
  const [rejectReason, setRejectReason] = useState('')
  const [historyShown, setHistoryShown] = useState(false)
  const locked = busy !== ''

  function clearFeedback() {
    setError('')
    setReceipt('')
  }

  function startReject(proposal: Proposal) {
    if (locked) return
    setRejecting(proposal.id)
    setRejectReason('')
    clearFeedback()
  }

  function cancelReject() {
    setRejecting('')
    setRejectReason('')
  }

  async function approve(proposal: Proposal) {
    if (locked) return
    setBusy(`approve:${proposal.id}`)
    clearFeedback()
    try {
      const result = await api.approveProposal(proposal.id, proposalApproveBody(proposal))
      setReceipt(proposalApprovedReceipt(result))
      rows.reload()
      onChanged()
    } catch (e) {
      setError(proposalDecisionError(e, 'approve'))
    } finally {
      setBusy('')
    }
  }

  async function reject(proposal: Proposal) {
    if (locked) return
    const invalid = proposalRejectDraftError(rejectReason)
    if (invalid) {
      setError(invalid)
      return
    }
    setBusy(`reject:${proposal.id}`)
    clearFeedback()
    try {
      const result = await api.rejectProposal(proposal.id, proposalRejectBody(proposal, rejectReason))
      setReceipt(proposalRejectedReceipt(result))
      cancelReject()
      rows.reload()
      onChanged()
    } catch (e) {
      setError(proposalDecisionError(e, 'reject'))
    } finally {
      setBusy('')
    }
  }

  async function dispatch(proposal: Proposal) {
    if (locked) return
    setBusy(`dispatch:${proposal.id}`)
    clearFeedback()
    try {
      const result = await api.dispatchProposal(proposal.id, proposalDispatchBody(proposal))
      setReceipt(proposalDispatchedReceipt(result))
      rows.reload()
      onChanged()
    } catch (e) {
      setError(proposalDecisionError(e, 'dispatch'))
    } finally {
      setBusy('')
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next && !locked) onClose() }}>
      <DialogContent
        className="max-h-[90dvh] max-w-2xl overflow-y-auto overscroll-contain"
        aria-busy={locked}
        data-testid="operator-proposals-dialog"
      >
        <DialogHeader>
          <DialogTitle>提议</DialogTitle>
          <DialogDescription>{PROPOSAL_EMPTY}</DialogDescription>
        </DialogHeader>

        {error && (
          <div role="alert">
            <Callout tone="destructive">{error}</Callout>
          </div>
        )}
        {receipt && (
          <p data-proposal-receipt className="text-[11.5px] leading-5 text-ink-2">
            {receipt}
          </p>
        )}

        <section aria-label="待批准提议" className="space-y-2">
          <div className="flex items-center gap-2">
            <div className="tw-section-title">待批准</div>
            <span className="tw-nums font-mono text-[11px] text-ink-3">{pending.length} 条</span>
            <Button size="xs" variant="ghost" className="ml-auto" onClick={() => rows.reload()} disabled={rows.loading}>
              刷新
            </Button>
          </div>
          {rows.loading && !rows.data && <SkeletonList count={2} />}
          {rows.error && (
            <p role="alert" className="text-[11px] leading-4 text-destructive">
              读取提议失败（{rows.error}）
            </p>
          )}
          {!rows.loading && !rows.error && pending.length === 0 && (
            <EmptyState compact icon={ListChecks} title="没有待批准的提议" description={PROPOSAL_EMPTY} />
          )}
          <ul role="list" aria-label="待批准提议列表" className="space-y-2">
            {pending.map((proposal) => (
              <ProposalRow
                key={proposal.id}
                proposal={proposal}
                busy={locked}
                rejecting={rejecting === proposal.id}
                rejectingBusy={busy === `reject:${proposal.id}`}
                approvingBusy={busy === `approve:${proposal.id}`}
                rejectReason={rejecting === proposal.id ? rejectReason : ''}
                onApprove={() => void approve(proposal)}
                onStartReject={() => startReject(proposal)}
                onCancelReject={cancelReject}
                onRejectReason={setRejectReason}
                onReject={() => void reject(proposal)}
              />
            ))}
          </ul>
        </section>

        <section aria-label="待派发提议" className="space-y-2">
          <div className="flex items-center gap-2">
            <div className="tw-section-title">待派发</div>
            <span className="tw-nums font-mono text-[11px] text-ink-3">{approved.length} 条</span>
          </div>
          {!rows.loading && !rows.error && approved.length === 0 && (
            <p className="text-[11px] leading-4 text-ink-3">没有已批准待派发的提议：批准只生成意图，派发才起 run。</p>
          )}
          <ul role="list" aria-label="待派发提议列表" className="space-y-2">
            {approved.map((proposal) => (
              <ProposalRow
                key={proposal.id}
                proposal={proposal}
                busy={locked}
                dispatchingBusy={busy === `dispatch:${proposal.id}`}
                onDispatch={() => void dispatch(proposal)}
              />
            ))}
          </ul>
        </section>

        <section aria-label="已处理的提议" className="space-y-2">
          <div className="flex items-center gap-2">
            <div className="tw-section-title">已处理</div>
            <span className="tw-nums font-mono text-[11px] text-ink-3">{history.length} 条</span>
            <Button
              size="xs"
              variant="ghost"
              className="ml-auto"
              aria-pressed={historyShown}
              onClick={() => setHistoryShown((shown) => !shown)}
            >
              <ScrollText className="h-3.5 w-3.5" />
              {historyShown ? '隐藏历史' : '显示历史'}
            </Button>
          </div>
          {historyShown && (
            <ul role="list" aria-label="已处理提议列表" className="space-y-2">
              {history.map((proposal) => (
                <ProposalRow key={proposal.id} proposal={proposal} busy readOnly />
              ))}
              {history.length === 0 && <li className="text-[11px] text-ink-3">还没有处理过的提议。</li>}
            </ul>
          )}
        </section>
      </DialogContent>
    </Dialog>
  )
}

function ProposalRow({
  proposal,
  busy,
  readOnly = false,
  rejecting = false,
  rejectingBusy = false,
  approvingBusy = false,
  dispatchingBusy = false,
  rejectReason = '',
  onApprove,
  onStartReject,
  onCancelReject,
  onRejectReason,
  onReject,
  onDispatch,
}: {
  proposal: Proposal
  busy: boolean
  readOnly?: boolean
  rejecting?: boolean
  rejectingBusy?: boolean
  approvingBusy?: boolean
  dispatchingBusy?: boolean
  rejectReason?: string
  onApprove?: () => void
  onStartReject?: () => void
  onCancelReject?: () => void
  onRejectReason?: (value: string) => void
  onReject?: () => void
  onDispatch?: () => void
}) {
  const acceptance = proposalAcceptance(proposal)
  const rationale = proposalRationale(proposal)
  const premises = proposal.from_fact_ids?.length ?? 0
  return (
    <li data-proposal={proposal.id} className="rounded-lg border border-hairline p-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px] text-ink-3">
        <Badge size="sm" variant={proposalStatusVariant(proposal.status)}>
          {proposalStatusLabel(proposal.status)}
        </Badge>
        <span>{proposalScopeLabel(proposal)}</span>
        <span className="tw-nums font-mono">{proposalTimeLine(proposal)}</span>
      </div>
      <p className="tw-break mt-1.5 text-[12.5px] font-medium leading-relaxed text-ink">{proposal.title}</p>
      <p className="tw-break mt-1 whitespace-pre-wrap text-[11.5px] leading-relaxed text-ink-2">
        <span className="text-ink-3">验收：</span>
        {acceptance || '（未写验收标准）'}
      </p>
      {rationale && (
        <p className="tw-break mt-0.5 whitespace-pre-wrap text-[11.5px] leading-relaxed text-ink-2">
          <span className="text-ink-3">理由：</span>
          {rationale}
        </p>
      )}
      {premises > 0 && (
        <p className="tw-break mt-0.5 text-[10.5px] text-ink-3">
          必要前提 {premises} 条：{proposal.from_fact_ids?.join('、')}
        </p>
      )}
      {proposal.decided_by && (
        <p className="tw-break mt-0.5 text-[10.5px] text-ink-3">
          {proposalDecidedByLabel(proposal.decided_by)}处理
          {proposal.decision_note ? `：${proposal.decision_note}` : ''}
          {proposal.intent_id ? ` · 意图 ${proposal.intent_id}` : ''}
          {proposal.run_id ? ` · run ${proposal.run_id}` : ''}
        </p>
      )}

      {rejecting && (
        <div className="mt-1.5 space-y-1.5">
          <Field label="驳回理由">
            <Textarea
              rows={2}
              value={rejectReason}
              disabled={rejectingBusy}
              onChange={(event) => onRejectReason?.(event.target.value)}
              aria-label="驳回提议的理由"
              placeholder={PROPOSAL_REJECT_PLACEHOLDER}
              className="min-h-[56px] text-[12px] leading-relaxed"
            />
          </Field>
          <div className="flex flex-wrap gap-1.5">
            <Button size="xs" onClick={onReject} disabled={busy}>
              {rejectingBusy && <Spinner size="sm" className="text-primary-foreground" />}
              {rejectingBusy ? '驳回中…' : '确认驳回'}
            </Button>
            <Button size="xs" variant="ghost" onClick={onCancelReject} disabled={busy}>
              取消
            </Button>
          </div>
        </div>
      )}

      {!readOnly && !rejecting && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          {proposal.status === 'proposed' && (
            <>
              <Button size="xs" onClick={onApprove} disabled={busy}>
                {approvingBusy && <Spinner size="sm" className="text-primary-foreground" />}
                <Check className="h-3.5 w-3.5" /> {approvingBusy ? '批准中…' : '批准'}
              </Button>
              <Button size="xs" variant="ghost" onClick={onStartReject} disabled={busy}>
                <X className="h-3.5 w-3.5" /> 驳回
              </Button>
            </>
          )}
          {proposal.status === 'approved' && (
            <Button size="xs" onClick={onDispatch} disabled={busy}>
              {dispatchingBusy && <Spinner size="sm" className="text-primary-foreground" />}
              <Send className="h-3.5 w-3.5" /> {dispatchingBusy ? '派发中…' : '派发'}
            </Button>
          )}
        </div>
      )}
    </li>
  )
}
