// The watchdog lists order by what happened, not by when the original run was
// started: a run that began early but was canceled a minute ago leads the
// cancellation list, and a redispatch is sorted by its successor's row time.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { watchdogEvents } from '../src/lib/watchdog-events.ts'

function run(over: Record<string, unknown>) {
  return {
    id: 'r', intent_id: 'in_1', node: 'local', status: 'canceled',
    started_at: '2026-01-01T00:00:00Z', events: 0, declared_facts: 0,
    ...over,
  }
}

test('cancellations sort by ended_at, not by the original start', () => {
  const earlyStartLateCancel = run({
    id: 'r_late_cancel', started_at: '2026-01-01T00:00:00Z',
    ended_at: '2026-01-01T03:00:00Z', cancel_cause: 'watchdog',
  })
  const lateStartEarlyCancel = run({
    id: 'r_early_cancel', started_at: '2026-01-01T02:00:00Z',
    ended_at: '2026-01-01T02:10:00Z', cancel_cause: 'watchdog',
  })
  const events = watchdogEvents([lateStartEarlyCancel, earlyStartLateCancel])
  assert.deepEqual(events.canceled.map((c) => c.run.id), ['r_late_cancel', 'r_early_cancel'])
})

test('redispatches sort by the successor run time', () => {
  const old = run({
    id: 'r_old', started_at: '2026-01-01T00:00:00Z', ended_at: '2026-01-01T01:00:00Z', cancel_cause: 'watchdog',
  })
  const newRun = run({
    id: 'r_new', started_at: '2026-01-01T02:00:00Z', ended_at: '2026-01-01T02:02:00Z', cancel_cause: 'watchdog',
  })
  const successorOld = run({
    id: 'r_succ_old', status: 'done', started_at: '2026-01-01T01:05:00Z',
  })
  const successorNew = run({
    id: 'r_succ_new', status: 'done', started_at: '2026-01-01T02:05:00Z',
  })
  const events = watchdogEvents([old, newRun, successorOld, successorNew])
  assert.deepEqual(events.redispatched.map((r) => r.successor.id), ['r_succ_new', 'r_succ_old'])
})

test('a cancellation without a later run stays a cancellation only', () => {
  const only = run({ id: 'r_only', cancel_cause: 'watchdog', ended_at: '2026-01-01T01:00:00Z' })
  const events = watchdogEvents([only])
  assert.equal(events.redispatched.length, 0)
  assert.equal(events.canceled.length, 1)
})
