// A declared completion is not a run result: every surface that shows 完成 has
// to say who settled it, and hover has to name the note and the cited records.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let DoneDeclaredBadge: React.ComponentType<any>

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  DoneDeclaredBadge = (await server.ssrLoadModule('/src/components/DoneDeclaredBadge.tsx')).DoneDeclaredBadge
})

after(async () => {
  await server?.close()
})

function render(intent: Record<string, unknown>): string {
  return renderToStaticMarkup(React.createElement(DoneDeclaredBadge, { intent }))
}

const done = {
  status: 'done',
  done_note: '执行者已拿到 shell，run 收口失败但证据齐全',
  done_fact_ids: ['f_shell', 'f_priv'],
  done_fact_claims: { f_shell: 1, f_priv: 2 },
  done_fact_statuses: { f_shell: 'verified', f_priv: 'unverified' },
  done_run_ids: ['r_9f2'],
  done_at: '2026-11-25T01:30:00Z',
}

test('a commander-declared completion is labelled as one, with note, versions and evidence on hover', () => {
  const html = render({ ...done, done_declared_by: 'commander' })
  assert.match(html, /指挥官判定完成/)
  assert.match(html, /说明：执行者已拿到 shell/)
  assert.match(html, /证据事实：f_shell（断言 v1 \/ 已核验），f_priv（断言 v2 \/ 未核验）/)
  assert.match(html, /证据运行：r_9f2/)
})

// The stored receipt keeps the versions it accepted. A later correction is a
// warning chip ("依据已变"), not a rewrite of the note or the cited records.
test('a receipt whose claim moved is marked stale while its original note stays', () => {
  const html = render({
    ...done,
    done_declared_by: 'operator',
    done_receipt_stale: true,
    done_receipt_changed: ['f_shell'],
  })
  assert.match(html, /操作员判定完成 · 依据已变/)
  assert.match(html, /依据已变化：f_shell/)
  assert.match(html, /证据事实：f_shell（断言 v1 \/ 已核验）/)
  assert.match(html, /warning/)
})

test('an operator-declared completion says 操作员, not the generic word', () => {
  const html = render({ ...done, done_declared_by: 'operator' })
  assert.match(html, /操作员判定完成/)
  assert.doesNotMatch(html, /指挥官/)
})

test('a run-produced completion renders no provenance chip at all', () => {
  assert.equal(render({ status: 'done' }), '')
})

test('a completion declared without a note still names its evidence', () => {
  const html = render({ done_declared_by: 'commander', done_fact_ids: ['f_only'] })
  assert.match(html, /指挥官判定完成/)
  assert.match(html, /证据事实：f_only/)
})
