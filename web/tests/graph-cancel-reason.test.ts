// Clicking a stopped direction on the graph has to answer "why did it stop" in
// the same words the run page uses. A held intent settles to failed+hold, so the
// status chip alone cannot say "the commander stopped it to reclaim the slot";
// the right panel carries the run's recorded cancel reason.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'
import type { Run } from '../src/lib/types.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let CancelReasonCallout: React.ComponentType<{ run?: Run; heldBy?: string; holdReason?: string }>

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  CancelReasonCallout = (await server.ssrLoadModule('/src/components/graph/CancelReasonCallout.tsx'))
    .CancelReasonCallout
})

after(async () => {
  await server?.close()
})

function run(over: Partial<Run> = {}): Run {
  return {
    id: 'r_d05stop',
    intent_id: 'in_d05',
    node: 'local',
    status: 'canceled',
    cancel_cause: 'stop_loss',
    exit_reason:
      '止损：d-05 flag 已 submit 且平台 is_completed=true（400 分），loot f_rkvxog5 已入库，回收槽位起新题。',
    started_at: '2026-11-24T22:00:56+08:00',
    ended_at: '2026-11-24T22:02:15+08:00',
    ...over,
  } as Run
}

test('the graph panel renders a held direction cancel reason', () => {
  const html = renderToStaticMarkup(
    React.createElement(CancelReasonCallout, { run: run(), heldBy: 'operator' }),
  )
  assert.match(html, /取消原因/)
  assert.match(html, /操作员/, 'who stopped it is part of the reason header')
  assert.match(html, /止损取消/, 'the sentence starts with the same status label as the run page')
  // The tooltip keeps the mechanism text; the visible line must not say it twice.
  const visible = html.replace(/title="[^"]*"/g, '')
  assert.doesNotMatch(visible, /止损：/, 'the category is not said twice in one line')
  assert.match(html, /d-05 flag 已 submit/, 'the recorded reason, not a re-worded summary')
  assert.match(html, /f_rkvxog5/, 'the evidence id stays readable')
  // Label and body are stacked: side by side, a long reason is squeezed into a
  // column of six or seven characters inside the 360px panel.
  assert.doesNotMatch(html, /tw-break[^"]*flex-1/, 'the reason owns the full width')
})

test('an executor stop names the initiator role, not its raw participant id', () => {
  const html = renderToStaticMarkup(
    React.createElement(CancelReasonCallout, { run: run(), heldBy: 'pt_in_parent' }),
  )
  assert.match(html, /发起执行者/)
  assert.doesNotMatch(html, /pt_in_parent/)
})

test('a stop before dispatch falls back to the intent hold reason', () => {
  const html = renderToStaticMarkup(
    React.createElement(CancelReasonCallout, { heldBy: 'operator', holdReason: '派发前叫停，等操作员确认' }),
  )
  assert.match(html, /派发前叫停/)
  assert.match(html, /操作员/)
  assert.doesNotMatch(html, /原因未记录/)
})

test('a missing reason is said, not invented', () => {
  const html = renderToStaticMarkup(React.createElement(CancelReasonCallout, { run: run({ exit_reason: '' }) }))
  assert.match(html, /原因未记录/)
})

test('the node panel shows it for any held direction and prefers the cancelled run', async () => {
  const panel = await readFile(
    fileURLToPath(new URL('../src/components/graph/GraphNodePanel.tsx', import.meta.url)),
    'utf8',
  )
  assert.match(panel, /node\.stopped && \(\s*<CancelReasonCallout/)
  assert.match(panel, /run=\{latestRun\?\.status === 'canceled' \? latestRun : undefined\}/)
  assert.match(panel, /holdReason=\{intent\.hold_reason\}/)
})
