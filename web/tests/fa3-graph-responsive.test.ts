// FA3 responsive contract for the graph's overlays.
//
// The graph is not a page: it is a canvas with floating cards. Their usable
// space is the *map's* box, which the rail and the operator's own dock width
// shrink well below the window width, so every breakpoint that used to be
// `sm:`/`md:` has to be a container query against the graph stage. This suite
// renders the real components through the app's Vite transform and checks the
// classes they emit — a regression here silently re-introduces the viewport
// coupling without breaking any behaviour test.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import { defaultGraphFilters } from '../src/lib/graph-model-cache.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphToolbar: React.ComponentType<any>
let GraphChatDock: React.ComponentType<any>
let GraphListView: React.ComponentType<any>
let GraphLegend: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GraphToolbar = (await server.ssrLoadModule('/src/components/graph/GraphToolbar.tsx')).GraphToolbar
  GraphChatDock = (await server.ssrLoadModule('/src/components/GraphChatDock.tsx')).GraphChatDock
  GraphListView = (await server.ssrLoadModule('/src/components/graph/GraphListView.tsx')).GraphListView
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

function html(): string {
  return JSON.stringify(renderer!.toJSON())
}

const noop = () => {}
const dock = {
  width: 360,
  min: 300,
  max: 760,
  resizing: false,
  onResizeStart: noop,
  onResizeKey: noop,
}
const model = { nodes: [], edges: [], byId: new Map(), edgesById: new Map() } as any

test('toolbar wraps by container width, not by window width', async () => {
  await act(async () => {
    renderer = create(
      React.createElement(GraphToolbar, {
        chatDock: false,
        onToggleChatDock: noop,
        layout: 'layered',
        onLayout: noop,
        showLabels: false,
        onToggleLabels: noop,
        listView: false,
        onToggleListView: noop,
        onZoom: noop,
        onFit: noop,
        onRelayout: noop,
      }),
    )
  })
  const toolbar = renderer!.root.findByProps({ role: 'toolbar' })
  const className = String(toolbar.props.className)
  assert.ok(className.includes('tw-graph-toolbar'), 'toolbar uses the container-driven layout')
  assert.ok(className.includes('tw-graph-corner-tr'), 'toolbar is placed by the safe-area corner recipe')
  assert.ok(html().includes('tw-graph-corner-tr'))
  // The old viewport breakpoints must be gone from this element.
  assert.equal(/\bsm:|\bmd:/.test(className), false, 'toolbar has no viewport breakpoints')
})

test('the chat dock is clamped by the map width and the safe area', async () => {
  await act(async () => {
    renderer = create(React.createElement(GraphChatDock, { dock, onClose: noop, onOpenConversation: noop }))
  })
  const root = renderer!.root.findByProps({ 'data-graph-chat': true })
  const className = String(root.props.className)
  assert.ok(className.includes('tw-graph-dock'), 'dock width is clamped against the stage (100cqi)')
  assert.ok(className.includes('tw-graph-dock-right'), 'right inset includes the device safe area')
  assert.equal(className.includes('100vw'), false, 'no viewport-width clamp on the dock')
  // The resize handle is a touch target on a coarse pointer (styled in app.css).
  assert.ok(html().includes('tw-dock-handle'))
})

test('the list view switches its wide columns with container queries', async () => {
  await act(async () => {
    renderer = create(
      React.createElement(GraphListView, {
        model,
        selected: null,
        matches: null,
        onSelect: noop,
        onClose: noop,
      }),
    )
  })
  const markup = html()
  for (const cls of ['tw-graph-list-id', 'tw-graph-list-hint']) {
    assert.ok(markup.includes(cls), `list view emits ${cls}`)
  }
  // Scroll region: reserved gutter + contained scroll.
  assert.ok(markup.includes('tw-scroll'))
  // No column may still answer to the window width.
  assert.equal(/\bsm:|\bmd:|\bxl:/.test(markup), false, 'list view has no viewport breakpoints')
})

test('the legend pins its title row and scrolls its body inside the bounded stack', async () => {
  await act(async () => {
    renderer = create(React.createElement(GraphLegend))
  })
  const legend = renderer!.root.findByProps({ 'data-testid': 'graph-legend' })
  const card = String(legend.props.className)
  assert.ok(card.includes('tw-graph-dock'), 'width is clamped by the map')
  assert.ok(card.includes('flex'), 'the card is a column so its body can shrink')
  assert.ok(card.includes('min-h-10'), 'it shrinks inside the stack, but never below its title row')

  // The header is the collapse control; it must never scroll away with the body.
  const header = () => renderer!.root.findAllByType('button')[0]
  assert.equal(header().props['aria-expanded'], false)
  assert.ok(String(header().props.className).includes('shrink-0'), 'the title row is pinned')
  await act(async () => {
    header().props.onClick()
  })
  assert.equal(header().props['aria-expanded'], true, 'the same row collapses it again')

  const markup = html()
  assert.ok(markup.includes('flex-1'), 'the body takes the height the stack leaves it')
  assert.ok(markup.includes('overflow-y-auto'))
  assert.ok(markup.includes('tw-scroll'), 'the body is a contained scroll region')
  assert.equal(
    markup.includes('tw-graph-overlay-scroll'),
    false,
    'the legend is bounded by its stack, not by a fixed height the banner can cover',
  )
})

test('returning to the graph restores its cards for the same project', async (t) => {
  const { CommanderCard } = await server.ssrLoadModule('/src/components/CommanderCard.tsx')
  const { GraphFilters } = await server.ssrLoadModule('/src/components/graph/GraphFilters.tsx')
  const { api } = await server.ssrLoadModule('/src/lib/api.ts')
  const savedRetrievalStatus = api.retrievalStatus
  const savedWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const storage = new Map<string, string>()
  const location = { pathname: '/p/first/', search: '?view=graph' }
  api.retrievalStatus = async () => ({ configured: false })
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location,
      sessionStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
      },
    },
  })
  t.after(() => {
    api.retrievalStatus = savedRetrievalStatus
    if (savedWindow) Object.defineProperty(globalThis, 'window', savedWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  })

  const renderView = (graph: boolean) => graph
    ? React.createElement(
      React.Fragment,
      null,
      React.createElement(CommanderCard, { onOpen: noop }),
      React.createElement(GraphLegend),
      React.createElement(GraphFilters, {
        current: location.pathname.split('/')[2],
        q: '',
        hits: [],
        activeIndex: -1,
        counts: {},
        filters: defaultGraphFilters(),
        coldDim: false,
        onQueryChange: noop,
        onActiveIndex: noop,
        onOpenHit: noop,
        onActivate: noop,
        onFiltersChange: noop,
        onFocusFact: noop,
        onToggleColdDim: noop,
      }),
    )
    : React.createElement('main', null, '概览')
  const visit = async (graph: boolean) => {
    location.search = graph ? '?view=graph' : '?view=overview'
    await act(async () => renderer!.update(renderView(graph)))
  }
  const toggles = () => [
    renderer!.root.findByType(CommanderCard).findAllByType('button')[0],
    renderer!.root.findByType(GraphLegend).findAllByType('button')[0],
    renderer!.root.findAllByType('button').find(button => button.props['aria-label'] === '筛选与检索')!,
  ]
  const details = () => renderer!.root.findByType('details')
  const expanded = () => [...toggles().map(button => button.props['aria-expanded']), details().props.open]

  await act(async () => { renderer = create(renderView(true)) })
  assert.deepEqual(expanded(), [true, false, false, false])
  await act(async () => {
    const [, legend, filters] = toggles()
    legend.props.onClick()
    filters.props.onClick()
    details().props.onToggle({ currentTarget: { open: true } })
  })

  // Leaving the route unmounts the cards, so ordinary useState lost their
  // expanded flags. The same project's first render must restore them.
  await visit(false)
  await visit(true)
  assert.deepEqual(expanded(), [true, true, true, true])
  await act(async () => toggles()[0].props.onClick())
  await visit(false)
  await visit(true)
  assert.deepEqual(expanded(), [false, true, true, true], 'a deliberate close is remembered independently')

  await visit(false)
  location.pathname = '/p/second/'
  await visit(true)
  assert.deepEqual(expanded(), [true, false, false, false], 'another project starts with its own defaults')
})

test('重新布局 sits on the toolbar row, the labels switch states its state, the focus overlay is gone', async () => {
  await act(async () => {
    renderer = create(React.createElement(GraphToolbar, {
      chatDock: false, onToggleChatDock: noop,
      layout: 'layered', onLayout: noop,
      showLabels: true, onToggleLabels: noop,
      listView: false, onToggleListView: noop,
      onZoom: noop, onFit: noop, onRelayout: noop,
      timelineOpen: false, onToggleTimeline: noop,
      hoverPreview: false, onToggleHoverPreview: noop,
    }))
  })
  const row = renderer!.root.findByProps({ role: 'toolbar' })
  // A high-frequency action must not hide behind the 视图 popover: it is on the
  // row itself, between 适配画布 and 视图.
  const labels = row
    .findAll((node: any) => node.type === 'button')
    .map((node: any) => node.props['aria-label'] || String(node.props.children ?? '').trim())
  assert.deepEqual(labels, ['下发', '放大', '缩小', '适配画布', '重新布局', '视图'])
  // 低频开关搬进「视图」的「显示」段之后，条上不许再有时钟和箭头。
  assert.equal(labels.includes('时间轴'), false, '时间轴 moved into the 视图 panel')
  assert.equal(labels.includes('悬停预览'), false, '悬停预览 moved into the 视图 panel')
  const all = JSON.stringify(renderer!.toJSON())
  assert.doesNotMatch(all, /焦点叠层/, 'the overlay was deleted, not just hidden')
})
