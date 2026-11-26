// The time axis is an occasional tool. It renders nothing at all until the
// operator opens it from the map toolbar: no footer, no summary line, no strip of
// height taken from every graph screen. The open flag belongs to the page, so the
// toolbar switch and the strip's own collapse control drive the same value — and
// a reload starts closed because nothing is remembered outside React state.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphTimeline: React.ComponentType<any>
let GraphToolbar: React.ComponentType<any>
let GraphViewPanel: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GraphTimeline = (await server.ssrLoadModule('/src/components/graph/GraphTimeline.tsx')).GraphTimeline
  GraphToolbar = (await server.ssrLoadModule('/src/components/graph/GraphToolbar.tsx')).GraphToolbar
  // 时间轴的开关在「视图」面板里；面板单独导出，测试不必穿 Radix 的 Portal。
  GraphViewPanel = (await server.ssrLoadModule('/src/components/graph/GraphToolbar.tsx')).GraphViewPanel
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

const axis = {
  open: true,
  onClose() {},
  total: 12,
  cursor: null,
  revealed: 12,
  pending: 0,
  untimed: 3,
  atLabel: '12:00:00',
  onCursor() {},
  playing: false,
  onTogglePlay() {},
}

const toolbar = {
  chatDock: false,
  onToggleChatDock() {},
  layout: 'force',
  onLayout() {},
  showLabels: true,
  onToggleLabels() {},
  listView: false,
  onToggleListView() {},
  onZoom() {},
  onFit() {},
  onRelayout() {},
  timelineOpen: false,
  onToggleTimeline() {},
}

function buttons(): any[] {
  return renderer!.root.findAll((node: any) => node.type === 'button')
}

function timelineSwitch(): any {
  return buttons().find((node: any) => node.props['aria-label'] === '时间轴')
}

test('closed: the axis renders nothing, not even a summary line', async () => {
  await act(async () => {
    renderer = create(React.createElement(GraphTimeline, { ...axis, open: false }))
  })
  assert.equal(renderer!.toJSON(), null, 'a closed axis must not occupy the footer')
})

test('open: the strip carries the scrubber, and its own close calls back', async () => {
  let closed = 0
  await act(async () => {
    renderer = create(React.createElement(GraphTimeline, { ...axis, onClose: () => (closed += 1) }))
  })
  assert.equal(renderer!.root.findAll((node: any) => node.type === 'input').length, 1, 'the slider is there')
  const close = buttons().find((node: any) => node.props['aria-label'] === '收起时间轴')
  assert.ok(close, 'the strip keeps its own collapse control')
  await act(async () => close.props.onClick())
  assert.equal(closed, 1, 'closing is the page state, so the strip reports it instead of hiding itself')
})

test('the toolbar switch carries the open state and reports clicks', async () => {
  let toggled = 0
  await act(async () => {
    renderer = create(
      React.createElement(GraphViewPanel, {
        ...toolbar,
        timelineCounts: '已揭示 12/12',
        onToggleTimeline: () => (toggled += 1),
      }),
    )
  })
  const closed = timelineSwitch()
  assert.ok(closed, 'the 视图 panel owns the time axis entry')
  assert.equal(closed.props['aria-expanded'], false)
  assert.equal(closed.props['aria-controls'], undefined, 'nothing to point at while closed')
  assert.match(String(closed.props.title), /已揭示 12\/12/, 'the closed tooltip can state the revealed count')
  await act(async () => closed.props.onClick())
  assert.equal(toggled, 1)

  await act(async () => {
    renderer!.update(React.createElement(GraphViewPanel, { ...toolbar, timelineOpen: true }))
  })
  const open = timelineSwitch()
  assert.equal(open.props['aria-expanded'], true)
  assert.equal(open.props['aria-controls'], 'saker-graph-timeline-body', 'the switch names the strip it opens')
  assert.match(String(open.props.title), /收起/, 'an open switch says how to close it')
})
