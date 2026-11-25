// 运行结局与等待语义的显示规则：自动续跑次数与关闭原因只能来自账本字段
// （failure_class / auto_resume_attempt），等待句必须说清等的是谁、还差谁。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { intentWaitText } from '../src/lib/format.ts'

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
})

test('operator and external-action waits do not read as child waits', () => {
  assert.equal(intentWaitText({ operator: { request: '登录一次' } }), '等待人工：登录一次')
  assert.equal(
    intentWaitText({ operator: { request: '登录一次', resolution: 'unavailable' } }),
    '等待人工：登录一次（无法完成）',
  )
  assert.equal(intentWaitText({ actions: ['a', 'b'], mode: 'any' }), '等待 2 个外部动作（任一结果）')
  assert.equal(intentWaitText(undefined), '')
})
