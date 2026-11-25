// The proposal queue's wording and request shapes. The dialog is only a view:
// every CAS version, rejection requirement and error sentence lives here so the
// component test can stay about interaction.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError, api } from '../src/lib/api.ts'
import {
  activeProposalCount,
  orderProposals,
  PROPOSAL_EMPTY,
  proposalAcceptance,
  proposalApprovedReceipt,
  proposalApproveBody,
  proposalDecidedByLabel,
  proposalDecisionError,
  proposalDispatchedReceipt,
  proposalDispatchBody,
  proposalOpen,
  proposalRejectedReceipt,
  proposalRejectBody,
  proposalRejectDraftError,
  proposalScopeLabel,
  proposalStatusLabel,
  proposalStatusVariant,
} from '../src/lib/proposals.ts'
import type { Proposal } from '../src/lib/types.ts'

function row(over: Partial<Proposal> = {}): Proposal {
  return {
    id: 'pr_1',
    status: 'proposed',
    version: 3,
    title: '验证 /api/admin 的未授权读取',
    description: '确认 /api/admin 能否在未登录下读取，并保存最小复现',
    eval: '能复现一次未授权读取并留住响应原文',
    rationale: '上一轮已确认管理端可达',
    from_fact_ids: ['f_1', 'f_2'],
    from_participant_id: 'pt_in_a',
    from_intent_id: 'in_a',
    from_run_id: 'r_a',
    created_at: '2026-11-25T10:00:00+08:00',
    updated_at: '2026-11-25T10:00:00+08:00',
    ...over,
  }
}

test('status labels, tones and the open set follow the chain states', () => {
  assert.equal(proposalStatusLabel('proposed'), '待批准')
  assert.equal(proposalStatusLabel('approved'), '待派发')
  assert.equal(proposalStatusLabel('rejected'), '已驳回')
  assert.equal(proposalStatusLabel('dispatched'), '已派发')
  assert.equal(proposalStatusLabel('withdrawn'), '已撤回')
  assert.equal(proposalStatusVariant('proposed'), 'warning')
  assert.equal(proposalStatusVariant('approved'), 'info')
  assert.equal(proposalStatusVariant('dispatched'), 'success')
  assert.equal(proposalStatusVariant('rejected'), 'muted')
  assert.equal(proposalOpen({ status: 'proposed' }), true)
  assert.equal(proposalOpen({ status: 'approved' }), true)
  assert.equal(proposalOpen({ status: 'rejected' }), false)
})

test('open rows stay above closed history, and the count counts only actions left', () => {
  const rows = [
    row({ id: 'pr_hist', status: 'dispatched', at: '2026-11-25T12:00:00+08:00', updated_at: '2026-11-25T12:00:00+08:00' }),
    row({ id: 'pr_dispatch', status: 'approved', updated_at: '2026-11-25T11:00:00+08:00' }),
    row({ id: 'pr_decide', status: 'proposed', updated_at: '2026-11-25T10:00:00+08:00' }),
  ] as Proposal[]
  assert.deepEqual(orderProposals(rows).map((p) => p.id), ['pr_decide', 'pr_dispatch', 'pr_hist'])
  assert.equal(activeProposalCount(rows), 2)
  assert.match(PROPOSAL_EMPTY, /批准只生成待派发意图/)
})

test('each decision carries the version the row was read at, and rejections carry a reason', () => {
  const proposal = row({ id: 'pr_9', version: 7, status: 'proposed' })
  assert.deepEqual(proposalApproveBody(proposal), { expected_version: 7 })
  assert.deepEqual(proposalApproveBody(proposal, '  值得做  '), { expected_version: 7, note: '值得做' })
  assert.deepEqual(proposalRejectBody(proposal, '  与现有意图重叠  '), {
    expected_version: 7,
    reason: '与现有意图重叠',
  })
  assert.deepEqual(proposalDispatchBody(row({ version: 8, status: 'approved' })), { expected_version: 8 })
  assert.equal(proposalRejectDraftError('   '), '写一条驳回理由再提交。')
  assert.equal(proposalRejectDraftError('不做'), undefined)
})

test('decision errors name the conflict, the premise gate and the vanished row', () => {
  assert.match(
    proposalDecisionError(new ApiError(409, '提议状态或版本已变化', 'proposal_conflict'), 'approve'),
    /已被其他窗口处理/,
  )
  assert.match(
    proposalDecisionError(new ApiError(409, '必要前提失效', 'proposal_premise'), 'approve'),
    /必要前提当前不可用/,
  )
  // A refused dispatch carries the admission blocker, which is more useful than
  // a generic conflict sentence.
  assert.equal(
    proposalDecisionError(new ApiError(409, '提议已批准，但这次派发未获准入：项目已暂停', 'proposal_conflict'), 'dispatch'),
    '提议已批准，但这次派发未获准入：项目已暂停',
  )
  assert.match(proposalDecisionError(new ApiError(404, '提议不存在', 'proposal_not_found'), 'reject'), /不在了/)
  assert.match(proposalDecisionError(new ApiError(403, 'forbidden', 'proposal_forbidden'), 'approve'), /指挥官或操作员/)
})

test('receipts name the intent an approval created and the run a dispatch started', () => {
  const approved = {
    proposal: row({ status: 'approved', version: 4, intent_id: 'in_new' }),
    intent_id: 'in_new',
  }
  assert.match(proposalApprovedReceipt(approved), /已批准 v4/)
  assert.match(proposalApprovedReceipt(approved), /in_new/)
  assert.match(proposalApprovedReceipt(approved), /点「派发」才会起 run/)
  assert.match(
    proposalDispatchedReceipt({ proposal: row({ status: 'dispatched', intent_id: 'in_new', run_id: 'r_new' }), intent_id: 'in_new', run_id: 'r_new' }),
    /run=r_new/,
  )
  assert.match(proposalRejectedReceipt({ proposal: row({ status: 'rejected', version: 5 }) }), /已驳回 v5/)
})

test('acceptance falls back to the description, and the scope/decider labels stay plain', () => {
  assert.equal(proposalAcceptance(row()), '能复现一次未授权读取并留住响应原文')
  assert.equal(proposalAcceptance(row({ eval: '  ' })), '确认 /api/admin 能否在未登录下读取，并保存最小复现')
  assert.equal(proposalScopeLabel(row()), '来自意图 in_a')
  assert.equal(proposalScopeLabel(row({ from_intent_id: '' })), '来自执行者')
  assert.equal(proposalDecidedByLabel('commander'), '指挥官')
  assert.equal(proposalDecidedByLabel('operator'), '操作员')
  assert.equal(proposalDecidedByLabel(''), '')
})

test('the five proposal calls use the project endpoints and carry the CAS body', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string; body?: unknown }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({
      url: String(url),
      method: init?.method ?? 'GET',
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    })
    return new Response(JSON.stringify({ proposals: [], proposal: row() }))
  }
  try {
    await api.proposals('proposed')
    await api.proposals()
    await api.approveProposal('pr_1', { expected_version: 3 })
    await api.rejectProposal('pr_1', { expected_version: 3, reason: '重复' })
    await api.dispatchProposal('pr_1', { expected_version: 4 })
    assert.deepEqual(calls, [
      { url: '/p/fixture/api/proposals?status=proposed&limit=100', method: 'GET', body: undefined },
      { url: '/p/fixture/api/proposals?limit=100', method: 'GET', body: undefined },
      { url: '/p/fixture/api/proposals/pr_1/approve', method: 'POST', body: { expected_version: 3 } },
      { url: '/p/fixture/api/proposals/pr_1/reject', method: 'POST', body: { expected_version: 3, reason: '重复' } },
      { url: '/p/fixture/api/proposals/pr_1/dispatch', method: 'POST', body: { expected_version: 4 } },
    ])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
