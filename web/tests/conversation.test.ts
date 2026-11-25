import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildTimeline, instructionNoticeState } from '../src/lib/conversation.ts'
import type { Message } from '../src/lib/types.ts'

function message(patch: Partial<Message> = {}): Message {
  return {
    id: 'm_fixture',
    seq: 1,
    project_id: 'p_fixture',
    from_participant_id: 'commander',
    to_participant_id: 'operator',
    text: 'fixture',
    status: 'confirmed',
    created_at: '2026-11-12T10:00:00+08:00',
    updated_at: '2026-11-12T10:00:00+08:00',
    ...patch,
  }
}

test('the unified timeline keeps ordinary messages in ledger order', () => {
  const timeline = buildTimeline([
    message({ id: 'm_2', seq: 2, text: 'second' }),
    message({ id: 'm_1', seq: 1, text: 'first' }),
  ])
  assert.deepEqual(timeline.map((entry) => entry.kind === 'message' && entry.message.id), ['m_1', 'm_2'])
})

test('one instruction revision fans out to recipients but renders once', () => {
  const timeline = buildTimeline([
    message({ id: 'm_1', seq: 1, kind: 'instruction', instruction_id: 'ins_1', instruction_version: 3, to_participant_id: 'commander' }),
    message({ id: 'm_2', seq: 2, kind: 'instruction', instruction_id: 'ins_1', instruction_version: 3, to_participant_id: 'pt_a' }),
    message({ id: 'm_3', seq: 3, kind: 'instruction', instruction_id: 'ins_1', instruction_version: 3, to_participant_id: 'pt_b' }),
    message({ id: 'm_4', seq: 4, text: 'ack' }),
  ])
  assert.equal(timeline.length, 2)
  const revision = timeline[0]
  assert.equal(revision.kind, 'instruction')
  if (revision.kind !== 'instruction') return
  assert.equal(revision.recipients, 3)
  assert.equal(revision.notice.id, 'm_1')
  assert.equal(revision.seq, 1, 'the revision sits where its first delivery did')
})

test('a later revision is a separate line, and the batch parent is not repeated', () => {
  const timeline = buildTimeline([
    message({ id: 'm_batch', seq: 1, kind: 'directive_batch', to_participant_id: 'executors' }),
    message({ id: 'm_1', seq: 2, kind: 'instruction', instruction_id: 'ins_1', instruction_version: 1 }),
    message({ id: 'm_2', seq: 3, kind: 'instruction', instruction_id: 'ins_1', instruction_version: 2 }),
    message({ id: 'm_3', seq: 4, text: 'hello' }),
  ])
  assert.deepEqual(
    timeline.map((entry) => (entry.kind === 'instruction' ? entry.notice.instruction_version : entry.message.id)),
    [1, 2, 'm_3'],
  )
})

test('an unnamed notice stays its own line instead of collapsing with another', () => {
  const timeline = buildTimeline([
    message({ id: 'm_1', seq: 1, kind: 'instruction' }),
    message({ id: 'm_2', seq: 2, kind: 'instruction' }),
  ])
  assert.equal(timeline.length, 2)
})

test('the notice revision state is read from the server sentence', () => {
  assert.equal(instructionNoticeState('永久消息 ins_1 已更新至 v2（active）。'), 'active')
  assert.equal(instructionNoticeState('永久消息 ins_1 已更新至 v3（revoked）。'), 'revoked')
  assert.equal(instructionNoticeState('something else'), '')
})
