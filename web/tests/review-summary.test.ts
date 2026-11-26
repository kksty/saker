// The overview's review strip: total is "未核验", only the queued set is
// "待复核", and closed/paused keep their own state. The numbers must survive
// the switch change, not disappear with it.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { reviewSummaryText, visibleReviewAttempts } from '../src/lib/review-summary.ts'

const base = { automatic: true, paused: false, pending: 2, processing: 0, cooling: 0 }

test('the total is 未核验 and only queued rows are 待复核', () => {
  const text = reviewSummaryText(base, 7)
  assert.match(text, /7 条未核验/)
  assert.match(text, /2 项待复核/)
  assert.doesNotMatch(text, /7 项待复核/)
  assert.match(text, /自动继续推进/)
})

test('paused and closed keep their state while the numbers stay readable', () => {
  const paused = reviewSummaryText({ ...base, paused: true }, 7)
  assert.match(paused, /7 条未核验/)
  assert.match(paused, /2 项待复核/)
  assert.match(paused, /复核已暂停/)

  const closed = reviewSummaryText({ ...base, automatic: false, disabled: true, pending: 0, cooling: 1, blocked: 2 }, 7)
  assert.match(closed, /7 条未核验/)
  assert.match(closed, /复核已关闭/)
  assert.match(closed, /1 项运行失败，等待重试/)
  assert.match(closed, /2 项缺少可复现材料/)
  // A blocked/cooling row is not queued work and must not be called 待复核.
  assert.doesNotMatch(closed, /项待复核/)
})

test('attempts are bounded and keep the material a blocked row waits for', () => {
  const details = [1, 2, 3, 4].map((n) => ({
    fact: `f_${n}`,
    claim_version: 1,
    outcome: n === 4 ? 'rejected' : 'needs_evidence',
    reason: `why ${n}`,
    missing_evidence: [`proof-${n}.txt`],
  }))
  const visible = visibleReviewAttempts({ ...base, details })
  assert.equal(visible.length, 3)
  assert.equal(visible[0].missing_evidence?.[0], 'proof-1.txt')
})
