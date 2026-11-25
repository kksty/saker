// The right-hand fact panel used to show only the title + summary: the body
// (复现过程) lives in `doc`, which the graph's lightweight fact record usually
// does not carry — it comes back on the detail read. These tests pin both halves:
// the choice rule (detail first, record fallback, blank means no block at all)
// and that the panel really renders the body, expanded by default, offering
// 收起 only for a genuinely long one.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let NodePanel: React.ComponentType<any>
let api: any
let factDocText: (primary?: string | null, fallback?: string | null) => string
let factDocLong: (doc: string) => boolean
let FACT_DOC_EXPAND_CHARS: number
let renderer: ReactTestRenderer | undefined
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  const factDoc = await server.ssrLoadModule('/src/lib/fact-doc.ts')
  factDocText = factDoc.factDocText
  factDocLong = factDoc.factDocLong
  FACT_DOC_EXPAND_CHARS = factDoc.FACT_DOC_EXPAND_CHARS
  NodePanel = (await server.ssrLoadModule('/src/components/graph/GraphNodePanel.tsx')).GraphNodePanel
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  // The panel's neighbours: nothing here is about them, they just must not fetch.
  api.facts = async () => []
  api.children = async () => []
  api.intentUsage = async () => null
  api.intentReadiness = async () => null
  api.premisePolicy = async () => ({ mode: 'recorded' })
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { pathname: '/p/fixture/', search: '', hash: '' }, setInterval, clearInterval },
  })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      visibilityState: 'visible',
      addEventListener() {},
      removeEventListener() {},
      getElementById() {
        return null
      },
    },
  })
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
})

after(async () => {
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (originalDocument) Object.defineProperty(globalThis, 'document', originalDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  await server?.close()
})

const dock = { width: 360, min: 300, max: 760, resizing: false, onResizeStart() {}, onResizeKey() {} }

const panelProps = {
  facts: [],
  runs: [],
  dock,
  onSelectFact() {},
  onClose() {},
  onChanged() {},
}

function factRecord(id: string, over: Record<string, unknown> = {}) {
  return { id, title: `事实 ${id}`, kind: 'note', status: 'unverified', confidence: 'observed', created_at: '', ...over }
}

/** A fact node as the graph builds it: the record, not the detail read. */
function factNode(id: string, over: Record<string, unknown> = {}) {
  return { id: `f:${id}`, type: 'fact', label: `事实 ${id}`, meta: { fact: factRecord(id, over) } }
}

function mountedJSON(): string {
  return JSON.stringify(renderer!.toJSON())
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))
  })
}

async function renderPanel(node: any) {
  await act(async () => {
    renderer = create(React.createElement(NodePanel, { ...panelProps, node }))
  })
  await flush()
}

function bodyPre(): any {
  return renderer!.root.findAllByType('pre')[0]
}

function toggleButton(label: string): any | undefined {
  return renderer!.root.findAllByType('button').find((n) => n.children.join('') === label)
}

// 30 lines is comfortably past the threshold (each line is ~45 chars).
const LONG_DOC = `复现步骤：\n${'curl -s -i http://10.0.0.5/portal/login | head -n 20\n'.repeat(30)}`

test('详情 / 复现: the detail read wins, the graph record is the fallback', () => {
  assert.equal(factDocText('detail body', 'graph body'), 'detail body')
  assert.equal(factDocText(undefined, 'graph body'), 'graph body')
  assert.equal(factDocText('', 'graph body'), 'graph body')
  // A whitespace-only detail doc must not shadow a real body.
  assert.equal(factDocText('  \n\t ', 'graph body'), 'graph body')
  // Blank on both sides is "no body": the caller renders nothing, not an empty box.
  assert.equal(factDocText(undefined, undefined), '')
  assert.equal(factDocText('', ''), '')
  assert.equal(factDocText(null, '   '), '')
  // Legacy/non-string payloads are treated as absent, never as the text "undefined".
  assert.equal(factDocText(undefined, 'graph body'), 'graph body')
})

test('a body offers the collapse toggle only past the length threshold', () => {
  assert.ok(FACT_DOC_EXPAND_CHARS >= 400 && FACT_DOC_EXPAND_CHARS <= 1000, 'about 600 characters')
  assert.equal(factDocLong('x'.repeat(FACT_DOC_EXPAND_CHARS)), false)
  assert.equal(factDocLong('x'.repeat(FACT_DOC_EXPAND_CHARS + 1)), true)
  assert.equal(factDocLong(''), false)
})

test('a fact whose detail carries a doc renders the body under 详情 / 复现', async () => {
  api.fact = async () => ({
    fact: factRecord('a', { doc: '复现：curl http://10.0.0.5/portal/login → 200，随后可读 /admin' }),
    evidence: [],
    reviews: [],
  })
  // The record's own doc is the weaker source: it must lose to the detail read.
  await renderPanel(factNode('a', { doc: 'GRAPH-RECORD-BODY' }))
  const html = mountedJSON()
  assert.ok(html.includes('详情 / 复现'), 'the body section has a title')
  assert.ok(html.includes('curl http://10.0.0.5/portal/login'), 'the detail body is rendered')
  assert.equal(html.includes('GRAPH-RECORD-BODY'), false, 'the detail doc should win over the record')
  // Short body: no collapse handle, and it is not clipped to the preview height.
  assert.equal(html.includes('收起'), false)
  assert.equal(html.includes('展开全文'), false)
  assert.ok(!String(bodyPre().props.className).includes('max-h-40'))
})

test('the record doc is the fallback when the detail read has none', async () => {
  api.fact = async () => ({ fact: factRecord('b', { doc: '' }), evidence: [], reviews: [] })
  await renderPanel(factNode('b', { doc: 'GRAPH-ONLY-BODY：复现步骤见记录' }))
  assert.ok(mountedJSON().includes('GRAPH-ONLY-BODY'))
})

test('a blank doc renders no 详情 / 复现 section at all', async () => {
  api.fact = async () => ({ fact: factRecord('c'), evidence: [], reviews: [] })
  await renderPanel(factNode('c'))
  const html = mountedJSON()
  assert.equal(html.includes('详情 / 复现'), false)
  assert.equal(renderer!.root.findAllByType('pre').length, 0)
})

test('a long body starts expanded and offers 收起, which collapses it to a preview', async () => {
  api.fact = async () => ({ fact: factRecord('d', { doc: LONG_DOC }), evidence: [], reviews: [] })
  await renderPanel(factNode('d'))
  const html = mountedJSON()
  assert.ok(html.includes('详情 / 复现'))
  // Expanded by default: the body is readable without a click, and the handle
  // that is offered is 收起 — not a gate in front of the text.
  assert.ok(html.includes('复现步骤'))
  assert.ok(toggleButton('收起'), 'a long body offers 收起')
  assert.equal(html.includes('展开全文'), false)
  assert.ok(String(bodyPre().props.className).includes('max-h-[70vh]'))
  assert.ok(!String(bodyPre().props.className).includes('max-h-40'))

  await act(async () => {
    toggleButton('收起')!.props.onClick()
  })
  assert.ok(toggleButton('展开全文'), 'collapsed state offers 展开全文 back')
  assert.ok(String(bodyPre().props.className).includes('max-h-40'), 'collapsed body is a preview')

  await act(async () => {
    toggleButton('展开全文')!.props.onClick()
  })
  assert.ok(toggleButton('收起'))
  assert.ok(String(bodyPre().props.className).includes('max-h-[70vh]'))
})

test('while the detail read is pending no body is shown, even if the record has one', async () => {
  api.fact = () => new Promise(() => {})
  await renderPanel(factNode('e', { doc: 'GRAPH-EARLY-BODY' }))
  const html = mountedJSON()
  assert.equal(html.includes('详情 / 复现'), false)
  assert.equal(html.includes('GRAPH-EARLY-BODY'), false)
})

test('a failed detail read keeps the 没读全 hint and never invents a body', async () => {
  api.fact = async () => {
    throw new Error('boom')
  }
  await renderPanel(factNode('g'))
  const html = mountedJSON()
  assert.ok(html.includes('详情没读全'))
  assert.equal(html.includes('详情 / 复现'), false)
})
