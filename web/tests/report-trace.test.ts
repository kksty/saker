// The report → fact trace (lib/report-trace.ts).
//
// Two rules these cases pin: a fact has one address (`?view=graph&fact=`), and a
// precheck that did not answer `unreported` is absent, not zero. A failed
// precheck read is its own verdict, so the console cannot start a report run as
// if the check had passed.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { factHref, precheckVerdict, unreportedView, withReportParam } from '../src/lib/report-trace.ts'
import type { ReportPrecheck } from '../src/lib/types.ts'

function precheck(over: Partial<ReportPrecheck> = {}): ReportPrecheck {
  return { reportable: 3, unverified: 1, rejected: 0, running: false, ...over }
}

test('a fact address is one record param, and the id is escaped', () => {
  assert.equal(factHref('f_3f3a04db'), '?view=graph&fact=f_3f3a04db')
  assert.equal(factHref('f a/b'), '?view=graph&fact=f%20a%2Fb')
})

test('the open report is a record param that keeps the rest of the URL', () => {
  assert.equal(
    withReportParam('?view=reports&case=fixture', 'rep_1'),
    '?view=reports&case=fixture&report=rep_1',
  )
  // Closing removes only the report, never the view.
  assert.equal(withReportParam('?view=reports&case=fixture&report=rep_1', null), '?view=reports&case=fixture')
  assert.equal(withReportParam('?view=reports&report=rep_1', ''), '?view=reports')
  // No search at all, and a bare search string without the leading `?`.
  assert.equal(withReportParam('', 'rep_1'), '?report=rep_1')
  assert.equal(withReportParam('view=reports', 'rep_1'), '?view=reports&report=rep_1')
})

test('an absent unreported block is absent, never "0 facts"', () => {
  const view = unreportedView(precheck())
  assert.equal(view.present, false)
  assert.equal(view.total, 0)
  assert.deepEqual(view.items, [])
  // Same for no precheck at all.
  assert.equal(unreportedView(undefined).present, false)
})

test('the unreported list keeps the server rows, caps and the two named lists', () => {
  const view = unreportedView(precheck({
    unreported: {
      total: 25,
      limit: 20,
      items: [
        { fact: 'f_a', title: 'A', previous: [{ report: 'rep_1', claim_version: 1 }] },
        { fact: 'f_b', title: '' },
        { fact: '', title: 'dropped: no id' },
      ],
      note: '边界说明',
      unreadable: ['rep_1'],
      uncounted: ['rep_2', ''],
      unknown_version_reports: ['rep_legacy'],
    },
  }))
  assert.equal(view.present, true)
  assert.deepEqual(view.items, [
    { fact: 'f_a', title: 'A', previous: [{ report: 'rep_1', claimVersion: 1 }] },
    { fact: 'f_b', title: '', previous: [] },
  ])
  assert.equal(view.total, 25)
  assert.equal(view.hidden, 23)
  assert.equal(view.limit, 20)
  assert.equal(view.note, '边界说明')
  assert.deepEqual(view.unreadable, ['rep_1'])
  assert.deepEqual(view.uncounted, ['rep_2'])
  assert.deepEqual(view.unknownVersionReports, ['rep_legacy'])
})

test('an empty unreported list is a real answer, distinct from a missing block', () => {
  const view = unreportedView(precheck({ unreported: { total: 0, limit: 20, items: [] } }))
  assert.equal(view.present, true)
  assert.equal(view.total, 0)
  assert.equal(view.hidden, 0)
})

test('a failed precheck read is unavailable, not "nothing to warn about"', () => {
  assert.deepEqual(precheckVerdict(undefined, '预检请求超时'), { kind: 'unavailable', reason: '预检请求超时' })
  // The failure wins even if a stale payload is still around.
  assert.deepEqual(precheckVerdict(precheck(), '502'), { kind: 'unavailable', reason: '502' })
})

test('a running report and a loaded precheck stay distinct', () => {
  assert.deepEqual(precheckVerdict(precheck({ running: true }), undefined), { kind: 'running' })
  const loaded = precheckVerdict(precheck(), undefined)
  assert.equal(loaded.kind, 'ok')
  assert.equal(precheckVerdict(undefined, undefined).kind, 'loading')
})
