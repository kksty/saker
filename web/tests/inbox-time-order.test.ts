// The overview card mixes every pending row into one newest-first list instead
// of budgeting per category: a stack of authorization requests used to push the
// handoffs out of the visible three. Category headings stay, and rows without a
// time (older servers, an unstamped receipt) sort after the timed ones.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'
import type { OperatorInbox } from '../src/lib/types.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let OperatorInboxCard: React.ComponentType<any>

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  OperatorInboxCard = (await server.ssrLoadModule('/src/components/OperatorInboxCard.tsx')).OperatorInboxCard
})

after(async () => {
  await server?.close()
})

const noop = () => {}
const render = (inbox: OperatorInbox): string =>
  renderToStaticMarkup(
    React.createElement(OperatorInboxCard, {
      inbox, onNavigate: noop, onChanged: noop, onOpenIntent: noop,
      onOpenRun: noop, onOpenAuthorization: noop, onOpenGoal: noop,
    }),
  )
const before_ = (html: string, a: string, b: string) => {
  const ia = html.indexOf(a)
  const ib = html.indexOf(b)
  assert.ok(ia >= 0 && ib >= 0, `both rows render: ${a} / ${b}`)
  assert.ok(ia < ib, `${a} should come before ${b}`)
}

const base = (over: Partial<OperatorInbox>): OperatorInbox => ({
  authorization: [], handoff: [], acceptance: [], warnings: [], total: 0, ...over,
})

test('the newest pending row leads, across categories', () => {
  const html = render(base({
    authorization: [{ id: 'req_old', action: 'OLD_AUTH', status: 'pending', created_at: '2026-11-21T01:00:00Z', updated_at: '2026-11-21T01:00:00Z' }],
    handoff: [{ intent_id: 'in_1', title: 'NEW_HANDOFF', status: 'pending', at: '2026-11-21T04:00:00Z', request: { id: 'rq_1', request: '请登录' } }],
    acceptance: [{ goal_id: 'g_1', statement: 'MID_ACCEPT', at: '2026-11-21T03:00:00Z' }],
    warnings: [{ run: 'r_1', warnings: ['OLD_WARN'], at: '2026-11-21T02:00:00Z' }],
    total: 4,
  }))
  before_(html, 'NEW_HANDOFF', 'MID_ACCEPT')
  before_(html, 'MID_ACCEPT', 'OLD_WARN')
  // The oldest row is behind the expander, not silently dropped.
  assert.doesNotMatch(html, /OLD_AUTH/, 'the fourth, oldest row is capped')
  assert.match(html, /展开全部/)
})

test('a newer handoff is not crowded out by older authorization requests', () => {
  const html = render(base({
    authorization: [
      { id: 'req_1', action: 'AUTH_ONE', status: 'pending', created_at: '2026-11-21T01:00:00Z', updated_at: '2026-11-21T01:00:00Z' },
      { id: 'req_2', action: 'AUTH_TWO', status: 'pending', created_at: '2026-11-21T02:00:00Z', updated_at: '2026-11-21T02:00:00Z' },
    ],
    handoff: [{ intent_id: 'in_1', title: 'LATE_HANDOFF', status: 'pending', at: '2026-11-21T05:00:00Z', request: { id: 'rq_1', request: '请确认' } }],
    total: 3,
  }))
  before_(html, 'LATE_HANDOFF', 'AUTH_TWO')
  assert.doesNotMatch(html, /展开全部/, 'three rows fit, so the handoff is visible without expanding')
})

test('rows without a time sort after the timed ones and still group by category', () => {
  const html = render(base({
    authorization: [{ id: 'req_1', action: 'TIMED_AUTH', status: 'pending', created_at: '2026-11-21T05:00:00Z', updated_at: '2026-11-21T05:00:00Z' }],
    handoff: [{ intent_id: 'in_1', title: 'UNTIMED_HANDOFF', status: 'pending', request: { id: 'rq_1', request: '请确认' } }],
    acceptance: [{ goal_id: 'g_1', statement: 'TIMED_ACCEPT', at: '2026-11-21T03:00:00Z' }],
    total: 3,
  }))
  before_(html, 'TIMED_AUTH', 'TIMED_ACCEPT')
  before_(html, 'TIMED_ACCEPT', 'UNTIMED_HANDOFF')
  // Every category keeps its heading even though the list is interleaved.
  assert.match(html, /需要你授权/)
  assert.match(html, /等你操作/)
  assert.match(html, /需要你验收/)
})

test('the expander still counts rows, not the inbox total', () => {
  const html = render(base({
    authorization: [{ id: 'req_1', action: 'A1', status: 'pending', created_at: '2026-11-21T04:00:00Z', updated_at: '2026-11-21T04:00:00Z' }],
    handoff: [
      { intent_id: 'in_1', title: 'H1', status: 'pending', at: '2026-11-21T03:00:00Z', request: { id: 'rq_1', request: 'x' } },
      { intent_id: 'in_2', title: 'H2', status: 'pending', at: '2026-11-21T02:00:00Z', request: { id: 'rq_2', request: 'y' } },
    ],
    acceptance: [{ goal_id: 'g_1', statement: 'G1', at: '2026-11-21T01:00:00Z' }],
    total: 9,
  }))
  assert.match(html, /展开全部/)
  assert.match(html, /还有\s*<span class="font-mono tw-nums">1<\/span>/)
})
