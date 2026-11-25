// 操作员指令: the standing-guidance surface.
//
// These cases pin the parts a screenshot cannot: a persistent directive is
// composed by this surface (never by the one-shot composer), the copy keeps
// saying "guidance, not a boundary", revoke carries the version it read, and the
// history rows show the instruction's own version.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ApiError } from '../src/lib/api.ts'
import * as instructionLib from '../src/lib/operator-instructions.ts'
import {
  MAX_INSTRUCTION_RUNES,
  INSTRUCTION_EMPTY,
  INSTRUCTION_GUIDANCE,
  activeInstructionCount,
  instructionCreateBody,
  instructionCreateError,
  instructionCreatedReceipt,
  instructionDraftError,
  instructionFingerprint,
  instructionIntentOptions,
  instructionKeyFor,
  instructionOperationLabel,
  instructionRevokedReceipt,
  instructionRevokeBody,
  instructionRevokeError,
  instructionRevisions,
  instructionScopeLabel,
  instructionStateLabel,
  instructionUpdateBody,
  instructionUpdateDraftError,
  instructionUpdateError,
  instructionUpdatedReceipt,
  orderInstructions,
  type InstructionDraft,
} from '../src/lib/operator-instructions.ts'
import type { DirectiveResponse, Instruction, InstructionHistoryResponse } from '../src/lib/types.ts'

function draft(over: Partial<InstructionDraft> = {}): InstructionDraft {
  return {
    text: '新派意图的标题、说明与验收标准一律用简体中文',
    scope: 'project',
    intentID: '',
    ...over,
  }
}

function row(over: Partial<Instruction> = {}): Instruction {
  return {
    id: 'instr_1',
    text: '整个项目只读优先',
    version: 1,
    state: 'active',
    created_by: 'operator',
    created_at: '2026-11-21T21:00:00+08:00',
    updated_at: '2026-11-21T21:00:00+08:00',
    ...over,
  }
}

test('a persistent instruction is the same directive endpoint with its own lifetime', () => {
  assert.deepEqual(instructionCreateBody(draft(), 'key-1'), {
    text: '新派意图的标题、说明与验收标准一律用简体中文',
    target: 'project',
    persistent: true,
    steer: false,
    idempotency_key: 'key-1',
  })
  assert.deepEqual(
    instructionCreateBody(draft({ scope: 'intent', intentID: 'in_x' }), 'key-2'),
    {
      text: '新派意图的标题、说明与验收标准一律用简体中文',
      target: 'intent',
      intent_id: 'in_x',
      persistent: true,
      steer: false,
      idempotency_key: 'key-2',
    },
  )
  // Trimming is the surface's job: the server stores exactly what it reads.
  assert.equal(instructionCreateBody(draft({ text: '  hi  ' }), 'k').text, 'hi')
})

test('the form refuses what the server would refuse, before the request', () => {
  assert.match(instructionDraftError(draft({ text: '   ' })) ?? '', /写一条消息/)
  assert.equal(instructionDraftError(draft({ text: 'x'.repeat(MAX_INSTRUCTION_RUNES) })), undefined)
  assert.match(
    instructionDraftError(draft({ text: 'x'.repeat(MAX_INSTRUCTION_RUNES + 1) })) ?? '',
    new RegExp(String(MAX_INSTRUCTION_RUNES)),
  )
  assert.match(
    instructionDraftError(draft({ scope: 'intent', intentID: '' })) ?? '',
    /选一个关联意图/,
  )
  assert.equal(instructionDraftError(draft()), undefined)
  assert.deepEqual(instructionCreateBody(draft({ scope: 'intent', intentID: ' in_x ' }), 'k').intent_id, 'in_x')
})

test('a retried save reuses the key; an edited instruction gets a new one', () => {
  const first = instructionFingerprint(draft())
  assert.equal(first, instructionFingerprint(draft({ text: `  ${draft().text}  ` })))
  assert.notEqual(first, instructionFingerprint(draft({ text: '换一句' })))
  assert.notEqual(first, instructionFingerprint(draft({ scope: 'intent', intentID: 'in_x' })))

  const kept = instructionKeyFor({ fingerprint: first, key: 'stable-key' }, first, () => 'unused')
  assert.equal(kept.key, 'stable-key')
  let calls = 0
  const regenerated = instructionKeyFor({ fingerprint: first, key: 'stable-key' }, instructionFingerprint(draft({ text: '换一句' })), () => {
    calls++
    return 'new-key'
  })
  assert.equal(calls, 1)
  assert.equal(regenerated.key, 'new-key')
})

test('the copy describes permanent messages to the commander', () => {
  assert.match(INSTRUCTION_GUIDANCE, /保存给指挥官的偏好、待办和提醒/)
  // Editing is part of the surface, not a hidden API: the same card says so.
  assert.match(INSTRUCTION_GUIDANCE, /可修改/)
  assert.match(INSTRUCTION_EMPTY, /下发/)
  // The one-shot composer must not grow a lifetime switch back: persistent
  // instructions are created only by the dedicated surface.
  const composer = readFileSync(
    fileURLToPath(new URL('../src/components/ChatComposer.tsx', import.meta.url)),
    'utf8',
  )
  assert.doesNotMatch(composer, /persistent:\s*true/)
  const surface = readFileSync(
    fileURLToPath(new URL('../src/components/OperatorInstructions.tsx', import.meta.url)),
    'utf8',
  )
  assert.match(surface, /api\.instructions\('all'\)/)
  assert.match(surface, /api\.instructionHistory/)
  assert.match(surface, /api\.revokeInstruction/)
  // Editing lives in the management dialog's rows, and only there: the inline
  // body editor is the one labelled control.
  assert.match(surface, /api\.updateInstruction/)
  assert.match(surface, /aria-label="修改持续指令正文"/)
  assert.match(surface, /保存修改/)
  assert.match(surface, /export function OperatorInstructionsDialog/)
  // The surface is reachable from the 通信 sidebar, not buried in a settings card.
  const messages = readFileSync(fileURLToPath(new URL('../src/views/Messages.tsx', import.meta.url)), 'utf8')
  assert.match(messages, /import \{ OperatorInstructionsCard \}/)
  assert.match(messages, /<OperatorInstructionsCard \/>/)
  // The sidebar card stays read-only: it opens the dialog and never draws an
  // editor of its own.
  const card = /export function OperatorInstructionsCard\(\) \{([\s\S]*?)\n\}\n/.exec(surface)
  assert.ok(card, 'the sidebar card must stay its own component')
  assert.doesNotMatch(card[1], /<Textarea/)
  assert.doesNotMatch(card[1], /api\.updateInstruction/)
})

test('the standing list keeps active rows visible and revoked rows as history', () => {
  assert.equal(instructionStateLabel('active'), '生效中')
  assert.equal(instructionStateLabel('revoked'), '已撤销')

  const rows = [
    row({ id: 'old_active', updated_at: '2026-11-20T10:00:00+08:00' }),
    row({ id: 'new_active', updated_at: '2026-11-22T10:00:00+08:00' }),
    row({ id: 'revoked_newer', state: 'revoked', updated_at: '2026-11-23T10:00:00+08:00' }),
  ]
  assert.deepEqual(orderInstructions(rows).map((r) => r.id), ['new_active', 'old_active', 'revoked_newer'])
  assert.equal(activeInstructionCount(rows), 2)
})

test('scope labels name the direction when the ledger can, and keep the id when it cannot', () => {
  assert.equal(instructionScopeLabel(row()), '整个项目')
  const scoped = row({ intent_id: 'in_x' })
  assert.equal(instructionScopeLabel(scoped), '意图 in_x')
  assert.equal(instructionScopeLabel(scoped, new Map([['in_x', '打点门户']])), '意图「打点门户」')
  assert.equal(instructionScopeLabel(scoped, { in_x: '打点门户' }), '意图「打点门户」')
  // The picker keeps every intent: a persistent scope also covers a direction
  // that is retried or continued later.
  assert.deepEqual(
    instructionIntentOptions([{ id: 'in_x', title: 'A', status: 'done' }]),
    [{ id: 'in_x', title: 'A', status: 'done' }],
  )
})

test('revoking carries the version and text this page read, so a stale revocation is refused', () => {
  const target = row({ id: 'instr_7', version: 4, text: '只读优先' })
  assert.deepEqual(instructionRevokeBody(target), { version: 4, text: '只读优先', state: 'revoked' })
  assert.match(instructionRevokeError(new ApiError(409, '冲突', 'instruction_conflict')), /版本冲突/)
  assert.match(instructionRevokeError(new ApiError(403, '禁止', 'forbidden')), /只有操作员/)
  assert.match(instructionRevokeError(new ApiError(404, '不存在', 'not_found')), /不在了/)
  assert.equal(instructionRevokeError(new Error('网络错误')), '网络错误')
})

test('an edit carries the clicked row version and its state, not the new text', () => {
  const target = row({ id: 'instr_7', version: 4, text: '只读优先', state: 'active' })
  // The CAS travels back as the version the operator read: saving a second time
  // after a background reload must still name that row version.
  assert.deepEqual(instructionUpdateBody(target, '  只读与最小改动  '), {
    version: 4,
    text: '只读与最小改动',
    state: 'active',
  })
  // An edit is not a revoke and never changes the state; a revoked row reaching
  // this helper would keep its state instead of being silently revived.
  assert.equal(instructionUpdateBody(row({ version: 9, state: 'revoked' }), 'x').state, 'revoked')
})

test('an edit refuses an empty or overlong body before the server sees it', () => {
  assert.match(instructionUpdateDraftError('   ') ?? '', /写一条消息/)
  assert.equal(instructionUpdateDraftError(`  ${'x'.repeat(MAX_INSTRUCTION_RUNES)}  `), undefined)
  assert.match(
    instructionUpdateDraftError('x'.repeat(MAX_INSTRUCTION_RUNES + 1)) ?? '',
    new RegExp(String(MAX_INSTRUCTION_RUNES)),
  )
  assert.equal(instructionUpdateDraftError('新的正文'), undefined)
})

test('edit failures say what to do: conflict, missing row, or the server reason', () => {
  assert.match(
    instructionUpdateError(new ApiError(409, '指令版本或幂等键冲突', 'instruction_conflict')),
    /正文已被其他窗口改过（版本冲突）/,
  )
  assert.match(
    instructionUpdateError(new ApiError(409, '冲突')),
    /刷新后再改/,
  )
  assert.match(
    instructionUpdateError(new ApiError(404, '指令不存在', 'not_found')),
    /这条指令不在了：刷新后再试/,
  )
  // Anything else keeps the server's wording instead of inventing local copy.
  assert.equal(
    instructionUpdateError(new ApiError(403, '只有操作员能写持续指令', 'forbidden')),
    '只有操作员能写持续指令',
  )
  // A 400 keeps the server's own reason (the capacity body is not generic).
  assert.equal(
    instructionUpdateError(new ApiError(400, '有效指令上下文合计超过 64 KiB，请先整理或撤销旧指令')),
    '有效指令上下文合计超过 64 KiB，请先整理或撤销旧指令',
  )
  assert.equal(instructionUpdateError(new Error('网络错误')), '网络错误')
})

test('the edit receipt names the new version and when it takes effect', () => {
  assert.equal(
    instructionUpdatedReceipt(row({ version: 5 })),
    '已修订至 v5，指挥官下一轮可见。',
  )
})

test('the unused time-line formatter is gone, so nobody renders a second time format', () => {
  assert.equal('instructionTimeLine' in instructionLib, false)
})

test('create errors say what to do instead of echoing a status code', () => {
  assert.match(
    instructionCreateError(new ApiError(400, '下发正文必须是 1–4000 字', 'directive_invalid')),
    /1–4000/,
  )
  assert.match(
    instructionCreateError(new ApiError(404, '意图不存在', 'directive_target_missing')),
    /刷新后再选/,
  )
  assert.match(instructionCreateError(new ApiError(403, '禁止', 'forbidden')), /只有操作员/)
  assert.equal(instructionCreateError(new Error('网络错误')), '网络错误')
})

test('the history list reads versions newest first and labels the operation', () => {
  const page: InstructionHistoryResponse = {
    history: [
      { seq: 1, operation: 'baseline', version: 1, at: '2026-11-21T20:00:00+08:00', value: row({ version: 1, text: '旧文' }) },
      { seq: 2, operation: 'update', version: 2, at: '2026-11-21T21:00:00+08:00', value: row({ version: 2, text: '新文', state: 'revoked' }) },
    ],
  }
  const revisions = instructionRevisions(page)
  assert.deepEqual(revisions.map((r) => r.version), [2, 1])
  assert.deepEqual(revisions.map((r) => r.text), ['新文', '旧文'])
  assert.equal(revisions[0].state, 'revoked')
  assert.equal(revisions[1].operation, '初始')
  assert.equal(instructionOperationLabel('update'), '修订')
  assert.equal(instructionOperationLabel(''), '修订')
  assert.equal(instructionOperationLabel('自定义'), '自定义')
  assert.deepEqual(instructionRevisions(null), [])
})

test('receipts say what became standing and what was withdrawn', () => {
  const created: DirectiveResponse = {
    kind: 'instruction',
    target: 'project',
    persistent: true,
    created: true,
    instruction: row(),
  }
  assert.match(instructionCreatedReceipt(created), /已保存，指挥官后续可见/)
  assert.match(
    instructionCreatedReceipt({ ...created, created: false }),
    /重复提交/,
  )
  assert.match(instructionRevokedReceipt(row({ version: 3 })), /已撤销 v3/)
  assert.match(instructionRevokedReceipt(row({ version: 3 })), /可查阅历史版本/)
})
