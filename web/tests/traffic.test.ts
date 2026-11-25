import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import { bodyPreview, capturePreviewBadge, capturePreviewFlags, capturePreviewNote } from '../src/lib/traffic.ts'

test('HTTP bodies preserve UTF-8 text and render binary as hex', () => {
  assert.equal(bodyPreview(Buffer.from('中文<script>文本</script>').toString('base64')).text, '中文<script>文本</script>')
  const binary = bodyPreview(Buffer.from([0, 255, 128]).toString('base64'))
  assert.equal(binary.binary, true)
  assert.equal(binary.text, '00 ff 80')
})

test('a text preview boundary does not misclassify a split UTF-8 character', () => {
  const preview = bodyPreview(Buffer.from('中文测试').toString('base64'), 4)
  assert.equal(preview.text, '中')
  assert.equal(preview.truncated, true)
  assert.equal(preview.binary, false)
  assert.equal(preview.size, 12)
})

test('capture body flags label preview-only and truncated bytes', () => {
  const complete = capturePreviewFlags({ preview_only: false, truncated: false })
  assert.deepEqual(complete, { previewOnly: false, truncated: false, partial: false })
  assert.equal(capturePreviewBadge(complete), '')
  assert.equal(capturePreviewNote(complete), '')

  const preview = capturePreviewFlags({ preview_only: true, truncated: false })
  assert.equal(capturePreviewBadge(preview), '仅预览')
  assert.match(capturePreviewNote(preview), /4 KiB/)
  assert.match(capturePreviewNote(preview), /body_storage=disk/)

  const truncated = capturePreviewFlags({ preview_only: false, truncated: true })
  assert.equal(capturePreviewBadge(truncated), '已截断')
  assert.match(capturePreviewNote(truncated), /8 MiB/)

  const both = capturePreviewFlags({ preview_only: true, truncated: true })
  assert.equal(capturePreviewBadge(both), '仅预览 · 已截断')
  assert.match(capturePreviewNote(both), /仅预览/)
  assert.match(capturePreviewNote(both), /已截断/)
})

test('a legacy capture without flags is reconciled from its preserved error text', () => {
  // The server does exactly this derivation on read; an older index row must
  // not be presented as a complete body in the console either.
  const legacy = capturePreviewFlags({ error: 'response body truncated (10/20 bytes retained) (preview only; local storage disabled or unavailable)' })
  assert.equal(legacy.previewOnly, true)
  assert.equal(legacy.truncated, true)
  assert.equal(capturePreviewFlags({ error: 'connect timeout' }).partial, false)
})

// ---- render: preview/truncated labels only; no project-quota marker ----------

// There is no project-level body quota in the console any more: retention is a
// single scan-node setting. A legacy/unknown `quota_exceeded` field must be
// ignored (no badge/callout), while preview/truncated labels stay.
let server: Awaited<ReturnType<typeof createTestServer>>
let Traffic: any
let api: any
let renderer: ReactTestRenderer | undefined
const timers = new Map<number, () => void>()
let timerID = 0
const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const oldFormElement = Object.getOwnPropertyDescriptor(globalThis, 'HTMLFormElement')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  Traffic = (await server.ssrLoadModule('/src/views/Traffic.tsx')).Traffic
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/p/alpha/', search: '', hash: '' },
      sessionStorage: { getItem: () => null, setItem: () => {} },
      setInterval: (fn: () => void) => { timers.set(++timerID, fn); return timerID },
      clearInterval: (id: number) => void timers.delete(id),
      setTimeout, clearTimeout,
      addEventListener() {}, removeEventListener() {},
    },
  })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} },
  })
  Object.defineProperty(globalThis, 'HTMLFormElement', { configurable: true, value: class {} })
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  timers.clear()
})

after(async () => {
  if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (oldDocument) Object.defineProperty(globalThis, 'document', oldDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  if (oldFormElement) Object.defineProperty(globalThis, 'HTMLFormElement', oldFormElement)
  await server.close()
})

test('the packet block scrolls itself but does not swallow the wheel', async () => {
  const body = Buffer.from(
    Array.from({ length: 80 }, (_, i) => `"row_${i + 1}": "keep scrolling past this line"`).join('\n'),
  ).toString('base64')
  const record = {
    ref: 'r_1/f_9', run_id: 'r_1', intent_id: 'in_1', event_id: 'ev_9', flow_id: 'f_9',
    method: 'GET', url: 'https://target.test/', status: 200, at: '2026-11-15T00:00:00Z',
    complete: true, request_bytes: 10, response_bytes: 20,
  }
  api.httpFlows = async () => ({ flows: [record], total: 1 })
  api.httpFlow = async () => ({
    record,
    flow: {
      request: { method: 'GET', url: 'https://target.test/', protocol: 'HTTP/1.1', headers: [], body: '' },
      response: { status_code: 200, reason_phrase: 'OK', headers: [], body },
    },
  })
  await act(async () => { renderer = create(React.createElement(Traffic)) })
  await act(async () => { for (const fn of timers.values()) fn() })
  const pres = renderer!.root.findAll((node: any) => node.type === 'pre')
  assert.equal(pres.length, 4, 'request and response each render a start block and a body block')
  for (const pre of pres) {
    const cls = String(pre.props.className)
    assert.match(cls, /overflow-auto/, 'the packet block keeps its own scrolling')
    // overscroll-contain stops the wheel at the block's end: the pointer sits on
    // the body most of the time, so the whole panel read as unscrollable.
    assert.doesNotMatch(cls, /overscroll-contain/, 'the packet block must not cut the scroll chain')
  }
  assert.equal(
    pres.filter((pre: any) => String(pre.props.className).includes('max-h-[420px]')).length,
    2,
    'both bodies keep their own height cap',
  )
})

test('the traffic detail keeps preview labels and ignores the retired quota field', async () => {
  const record = {
    ref: 'r_1/f_1', run_id: 'r_1', intent_id: 'in_1', event_id: 'ev_1', flow_id: 'f_1',
    method: 'GET', url: 'https://target.test/', status: 200, at: '2026-11-15T00:00:00Z',
    complete: true, request_bytes: 10, response_bytes: 20,
    preview_only: true, truncated: true,
    // A record written while the removed project-quota UI existed: unknown now.
    quota_exceeded: true,
  }
  api.httpFlows = async () => ({ flows: [record], total: 1 })
  api.httpFlow = async () => ({
    record,
    flow: { request: { method: 'GET', url: 'https://target.test/', protocol: 'HTTP/1.1', headers: [], body: '' }, response: null },
  })
  await act(async () => { renderer = create(React.createElement(Traffic)) })
  await act(async () => { for (const fn of timers.values()) fn() })
  const text = JSON.stringify(renderer!.toJSON())
  assert.ok(text.includes('仅预览 · 已截断'), text)
  assert.ok(text.includes('body_storage=disk'), text)
  assert.ok(!text.includes('配额'), text)
  assert.ok(!text.includes('仅元数据'), text)
})
