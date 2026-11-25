// P0-9c UI: the provenance second channel and the helper badge are visible in
// the three surfaces the operator reads (图例 / 列表 / 节点面板). Loaded through
// the app's own Vite transform so JSX, aliases and @cyber/ui behave as in the
// console; there is no browser — the renderer is enough for static copy.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import type { GraphModel, GraphNode } from '../src/graph/model.ts'
import type { Intent } from '../src/lib/types.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphLegend: React.ComponentType<any>
let GraphListView: React.ComponentType<any>
let NodePanel: React.ComponentType<any>
let api: any
let renderer: ReactTestRenderer | undefined
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GraphLegend = (await server.ssrLoadModule('/src/components/graph/GraphLegend.tsx')).GraphLegend
  GraphListView = (await server.ssrLoadModule('/src/components/graph/GraphListView.tsx')).GraphListView
  NodePanel = (await server.ssrLoadModule('/src/components/graph/GraphNodePanel.tsx')).GraphNodePanel
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  api.fact = async () => null
  api.children = async () => []
  api.intentUsage = async () => null
  api.intentReadiness = async () => null
  api.premisePolicy = async () => ({ premise_policy: 'verified_only', effective: 'verified_only' })
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

function mountedJSON(): string {
  return JSON.stringify(renderer!.toJSON())
}


const dock = {
  width: 360,
  min: 300,
  max: 760,
  resizing: false,
  onResizeStart() {},
  onResizeKey() {},
}

const panelProps = {
  facts: [],
  runs: [],
  dock,
  onSelectFact() {},
  onClose() {},
  onChanged() {},
}

function stepNode(over: Partial<GraphNode> = {}, intentOver: Partial<Intent> = {}): GraphNode {
  const intent: Intent = {
    id: 'in_peer',
    title: '平级意图',
    description: '',
    status: 'running',
    attempts: 1,
    created_at: '2026-11-11T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
    ...intentOver,
  }
  return {
    id: 's:in_peer',
    type: 'step',
    label: intent.title,
    status: intent.status,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 60,
    h: 20,
    depth: 2,
    attempts: 1,
    origin: 'executor',
    helpers: { calls: 2, active: 1, durationMs: 1500 },
    meta: { intent },
    ...over,
  }
}




test('没有发起者记录、也没有助手调用时，面板不编造这两块', async () => {
  const node = stepNode({
    origin: undefined,
    helpers: undefined,
  })
  await act(async () => {
    renderer = create(React.createElement(NodePanel, { ...panelProps, node }))
  })
  const html = mountedJSON()
  assert.ok(html.includes('未记录'), 'legacy created_by reads 未记录')
  assert.equal(html.includes('本轮助手'), false, 'no helper block without a recorded call')
  assert.equal(html.includes('平级发布'), false, 'no peer note without an executor origin')
})
