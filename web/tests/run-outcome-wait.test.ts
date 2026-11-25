// 运行结局与等待语义的显示规则：自动续跑次数与关闭原因只能来自账本字段
// （failure_class / auto_resume_attempt），等待句必须说清等的是谁、还差谁。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { intentWaitText } from '../src/lib/format.ts'
import type { IntentWait } from '../src/lib/types.ts'

const runsSrc = await readFile(fileURLToPath(new URL('../src/views/Runs.tsx', import.meta.url)), 'utf8')

test('a transport self-heal run names the attempt from the recorded count', () => {
  assert.match(runsSrc, /auto_resume_attempt/)
  assert.match(
    runsSrc,
    /网络中断，自动重跑第 \{current\.auto_resume_attempt\} 次（上限 3）/,
    'the sentence is the contract wording, built from the field, not from error text',
  )
})

test('an intentional host close is not offered as something to rerun', () => {
  assert.match(runsSrc, /current\.failure_class === 'intentional'/)
  assert.match(runsSrc, /宿主主动关闭，未续跑/)
  // The class is read, never re-derived in the console.
  assert.doesNotMatch(runsSrc, /failure_class.*includes\(/)
})

test('a yielded run does not print its yield as a cancel reason', () => {
  // child_wait is a slot release, not a stop: the wait sentence below already
  // says what it waits for, and 「（原因未记录）」 read as a decision.
  assert.match(runsSrc, /yieldOnly/)
  assert.match(runsSrc, /!yieldOnly/)
  assert.match(runsSrc, /current\?\.cancel_cause === 'child_wait'/)
})

test('the wait sentence names every child with its own state', () => {
  const state: Record<string, { status?: string; hold?: boolean } | undefined> = {
    in_a: { status: 'running' },
    in_b: { status: 'done' },
    in_c: undefined,
  }
  const wait: IntentWait = { children: ['in_a', 'in_b', 'in_c'], mode: 'all' }
  assert.equal(
    intentWaitText(wait, (id) => state[id]),
    '等待 3 条意图（全部完成）：in_a 运行中 · in_b 完成 · in_c（不在账本）',
  )
})

test('a stopped child is named as stopped, not by its last status', () => {
  const wait: IntentWait = { children: ['in_a'], mode: 'any' }
  const text = intentWaitText(wait, () => ({ status: 'failed', hold: true }))
  assert.equal(text, '等待 1 条意图（任一即可）：in_a 已停止')
})

test('a wait the control plane added says so', () => {
  const text = intentWaitText({ children: ['in_a'], implicit: true, mode: 'all' }, () => ({ status: 'running' }))
  assert.match(text, /控制面补的等待/)
  assert.match(text, /in_a 运行中/)
})

test('operator and external-action waits do not read as child waits', () => {
  assert.equal(intentWaitText({ children: [], operator: { request: '登录一次' } }), '等待人工：登录一次')
  assert.equal(
    intentWaitText({ children: [], operator: { request: '登录一次', resolution: 'unavailable' } }),
    '等待人工：登录一次（无法完成）',
  )
  assert.equal(intentWaitText({ children: [], actions: ['a', 'b'], mode: 'any' }), '等待 2 个外部动作（任一结果）')
  assert.equal(intentWaitText(undefined), '')
})
