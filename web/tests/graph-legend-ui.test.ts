// The legend must describe the drawing and the backend contract, not a past
// version of it. This renders the real expanded legend and pins the rows added
// after the audit: the four boundary states emitted by graph_page.go, the search
// rings the canvas now paints, and the absence of the retired 发起线.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

const renderSource = await readFile(
  fileURLToPath(new URL('../src/graph/render.ts', import.meta.url)),
  'utf8',
)

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphLegend: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GraphLegend = (await server.ssrLoadModule('/src/components/graph/GraphLegend.tsx')).GraphLegend
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
})

after(async () => {
  await server?.close()
})

async function openLegend(): Promise<string> {
  await act(async () => {
    renderer = create(React.createElement(GraphLegend))
  })
  const toggle = renderer!.root.findAllByType('button')[0]
  await act(async () => {
    toggle.props.onClick()
  })
  return JSON.stringify(renderer!.toJSON())
}

test('the legend names every boundary state the backend can return', async () => {
  const html = await openLegend()
  for (const label of ['未载入（可展开）', '引用缺失', '已否定引用', '已撤回引用']) {
    assert.ok(html.includes(label), `legend is missing ${label}`)
  }
})

test('the legend explains the rings the canvas paints for selection, hover and search', async () => {
  const html = await openLegend()
  for (const label of ['选中（白环）', '悬停（灰环）', '键盘焦点（蓝环）', '搜索命中（红环）', '当前搜索（双环）']) {
    assert.ok(html.includes(label), `legend is missing ${label}`)
  }
  assert.ok(html.includes('否定/撤回'), 'the fact status cross is explained')
  assert.ok(html.includes('未产出（虚环）'), 'a direction with no produced fact is explained')
})

test('the status labels stay scannable and the retired 发起线 is gone', async () => {
  const html = await openLegend()
  assert.ok(html.includes('排队/认领/等待'), 'the pre-run family still reads as one status')
  assert.ok(html.includes('复核未通过'), 'review failure keeps its own row')
  assert.equal(html.includes('发起线'), false, 'no legend row describes a line the canvas does not draw')
})

test('the host edge and the fact-status cross are drawn as the legend says', () => {
  // The legend promises a dotted host grouping and a true × for rejected or
  // withdrawn facts. Both were off by one shape before the audit.
  assert.match(renderSource, /kind === 'host'\s*\?\s*\[2 \/ cam\.k, 3 \/ cam\.k\]/)
  assert.match(renderSource, /function drawStatusCross/)
  const cross = renderSource.slice(renderSource.indexOf('function drawStatusCross'))
  const body = cross.slice(0, cross.indexOf('\n}'))
  assert.equal((body.match(/lineTo\(/g) ?? []).length, 2, 'the cross has both diagonals')
})
