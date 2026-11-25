// Executor proposals: the control surface where a suggestion becomes a
// direction. The executor proposes, the commander or the operator decides, and
// approval only creates a pending intent — a separate dispatch starts the run.
//
// This file owns the wording and the request/response mapping; the component
// only draws it. The version carried back is the row the console actually read:
// a decision from a stale tab is refused (409) instead of overwriting a
// decision another window already made.
import { ApiError } from './api.ts'
import { fmtTime } from './format.ts'
import type { Proposal, ProposalDecisionResponse } from './types.ts'

export const PROPOSAL_EMPTY =
  '执行者可以提议下一步；批准只生成待派发意图，派发才起 run。'

export const PROPOSAL_REJECT_PLACEHOLDER =
  '驳回必须写理由：说明为什么不采纳这一步，提议者会按这条理由调整或继续原意图。'

const STATUS_LABEL: Record<Proposal['status'], string> = {
  proposed: '待批准',
  approved: '待派发',
  rejected: '已驳回',
  dispatched: '已派发',
  withdrawn: '已撤回',
}

export function proposalStatusLabel(status: Proposal['status']): string {
  return STATUS_LABEL[status] ?? status
}

/** Badge tone per status: waiting for a decision (warning), waiting for the
 *  second dispatch step (info), done (success), or closed history (muted). */
export function proposalStatusVariant(
  status: Proposal['status'],
): 'warning' | 'info' | 'success' | 'muted' {
  switch (status) {
    case 'proposed':
      return 'warning'
    case 'approved':
      return 'info'
    case 'dispatched':
      return 'success'
    default:
      return 'muted'
  }
}

/** Active rows are the ones that still wait for a console/commander action. */
export function proposalOpen(proposal: Pick<Proposal, 'status'>): boolean {
  return proposal.status === 'proposed' || proposal.status === 'approved'
}

export function activeProposalCount(rows: readonly Proposal[]): number {
  return rows.filter(proposalOpen).length
}

/** The acceptance criterion, falling back to the description: the row must
 *  always show what "done" means, even when the proposer wrote no eval. */
export function proposalAcceptance(proposal: Proposal): string {
  return (proposal.eval ?? '').trim() || (proposal.description ?? '').trim()
}

/** The proposer's reason, kept as its own line so the acceptance criterion
 *  stays readable next to it. */
export function proposalRationale(proposal: Proposal): string {
  return (proposal.rationale || '').trim()
}

export function proposalScopeLabel(proposal: Proposal): string {
  return proposal.from_intent_id ? `来自意图 ${proposal.from_intent_id}` : '来自执行者'
}

export function proposalDecidedByLabel(decidedBy?: string): string {
  if (!decidedBy) return ''
  if (decidedBy === 'commander') return '指挥官'
  if (decidedBy === 'operator') return '操作员'
  return decidedBy
}

export function proposalTimeLine(proposal: Proposal): string {
  return `v${proposal.version} · ${fmtTime(proposal.updated_at || proposal.created_at)}`
}

/** Open rows first (待批准 then 待派发), then history newest-first. A terminal
 *  row must not push a still-dispatchable direction below the fold. */
export function orderProposals(rows: readonly Proposal[]): Proposal[] {
  const rank = (p: Proposal) => (p.status === 'proposed' ? 0 : p.status === 'approved' ? 1 : 2)
  const at = (p: Proposal) => p.updated_at || p.created_at || ''
  return [...rows].sort((a, b) => {
    const r = rank(a) - rank(b)
    if (r !== 0) return r
    return at(b).localeCompare(at(a))
  })
}

/** Approval may carry a note; the CAS version is the row snapshot from the read
 *  that rendered the button. */
export function proposalApproveBody(proposal: Proposal, note = ''): { expected_version: number; note?: string } {
  const trimmed = note.trim()
  return trimmed ? { expected_version: proposal.version, note: trimmed } : { expected_version: proposal.version }
}

/** Rejection always carries its reason; the server refuses an unexplained
 *  rejection because the proposer has nothing to act on without it. */
export function proposalRejectBody(proposal: Proposal, reason: string): { expected_version: number; reason: string } {
  return { expected_version: proposal.version, reason: reason.trim() }
}

/** Dispatch closes the chain; a retry of an already dispatched proposal reports
 *  the existing run instead of starting a second one. */
export function proposalDispatchBody(proposal: Proposal): { expected_version: number } {
  return { expected_version: proposal.version }
}

export function proposalRejectDraftError(reason: string): string | undefined {
  return reason.trim() ? undefined : '写一条驳回理由再提交。'
}

export function proposalDecisionError(error: unknown, action: 'approve' | 'reject' | 'dispatch'): string {
  if (error instanceof ApiError) {
    if (error.code === 'proposal_premise') {
      return '必要前提当前不可用：先按 readiness 处理，再批准或驳回。'
    }
    if (error.code === 'proposal_conflict' || error.status === 409) {
      // A refused dispatch names the admission blocker (paused, capacity, a lost
      // claim); that sentence is actionable, unlike a generic "conflict".
      if (action === 'dispatch' && error.message.includes('未获准入')) return error.message
      return '提议已被其他窗口处理（版本或状态已变）：刷新后按现状处理。'
    }
    if (error.status === 404) return '这条提议不在了：刷新后再试。'
    if (error.status === 403) return '只有指挥官或操作员可以处理提议。'
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}

function notificationReceipt(result: ProposalDecisionResponse): string {
  const notification = result.notification
  if (!notification) return ''
  return notification.notified
    ? '通知已登记。'
    : `通知未确认${notification.error ? `（${notification.error}）` : ''}。`
}

export function proposalApprovedReceipt(result: ProposalDecisionResponse): string {
  const intent = result.intent_id || result.proposal.intent_id || ''
  return `已批准 v${result.proposal.version}，已生成待派发意图 ${intent}；点「派发」才会起 run。${notificationReceipt(result)}`
}

export function proposalRejectedReceipt(result: ProposalDecisionResponse): string {
  return `已驳回 v${result.proposal.version}。${notificationReceipt(result)}`
}

export function proposalDispatchedReceipt(result: ProposalDecisionResponse): string {
  return `已派发：intent=${result.intent_id || result.proposal.intent_id || ''} run=${result.run_id || result.proposal.run_id || ''}。${notificationReceipt(result)}`
}
