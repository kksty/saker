// The graph search box's interaction contract.
//
// The old box only fed a substring set to the canvas: no list, no keyboard, no
// clear. This suite renders the real component and pins the behaviour an
// operator relies on: combobox semantics, ↑↓ walking the ranked hits, Enter /
// Shift+Enter locating them, Escape/× clearing, IME-safe typing, and Enter
// falling back to the whole-ledger index on a local miss.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import { defaultGraphFilters } from '../src/lib/graph-model-cache.ts'
import type { GraphSearchHit } from '../src/lib/graph-search.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphFilters: React.ComponentType<any>
let Component: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

const savedGlobals = new Map<string, PropertyDescriptor | undefined>()
const listeners = new Map<string, Set<(event: any) => void>>()
const storage = new Map<string, string>()
const nodeMock = {
  focused: 0,
  selected: 0,
  focus() {
    this.focused += 1
  },
  select() {
    this.selected += 1
  },
}

function node(id: string, label: string, over: Record<string, unknown> = {}) {
  return {
    id,
    type: 'fact',
    label,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 6,
    h: 6,
    depth: 2,
    ...over,
  }
}

function hitsOf(...labels: string[]): GraphSearchHit[] {
  return labels.map((label, index) => ({
    node: node(`f:${index}`, label),
    rank: 0,
    field: 'label',
  }))
}

function props(over: Record<string, unknown> = {}) {
  return {
    current: 'fixture',
    q: '',
    hits: [],
    activeIndex: -1,
    pending: false,
    counts: {},
    filters: defaultGraphFilters(),
    coldDim: false,
    onQueryChange() {},
    onActiveIndex() {},
    onOpenHit() {},
    onActivate() {},
    onFiltersChange() {},
    onFocusFact() {},
    onToggleColdDim() {},
    ...over,
  }
}

before(async () => {
  for (const name of ['window', 'document']) {
    savedGlobals.set(name, Object.getOwnPropertyDescriptor(globalThis, name))
  }
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/p/fixture/', search: '' },
      sessionStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
      },
      setTimeout,
      clearTimeout,
      addEventListener(type: string, fn: (event: any) => void) {
        const set = listeners.get(type) ?? new Set()
        set.add(fn)
        listeners.set(type, set)
      },
      removeEventListener(type: string, fn: (event: any) => void) {
        listeners.get(type)?.delete(fn)
      },
    },
  })
  // Only focusGraphCanvas (Escape with an empty box) touches the document.
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      querySelector: () => null,
      addEventListener() {},
      removeEventListener() {},
    },
  })
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GraphFilters = (await server.ssrLoadModule('/src/components/graph/GraphFilters.tsx')).GraphFilters
  Component = GraphFilters
  const { api } = await server.ssrLoadModule('/src/lib/api.ts')
  api.retrievalStatus = async () => ({ configured: false })
  api.factsPage = async () => ({ facts: [], next_cursor: undefined, has_more: false, total: 0 })
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
  listeners.clear()
})

after(async () => {
  for (const [name, descriptor] of savedGlobals) {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor)
    else delete (globalThis as Record<string, unknown>)[name]
  }
  await server?.close()
})

function searchInput() {
  return renderer!.root.findAllByType('input').find((item) => item.props['aria-label'] === '搜索节点')!
}

async function mount(over: Record<string, unknown> = {}) {
  await act(async () => {
    renderer = create(React.createElement(Component, props(over)), {
      createNodeMock: (element) => (element.type === 'input' ? nodeMock : null),
    })
  })
  return searchInput()
}

function key(key: string, over: Record<string, unknown> = {}) {
  const event = {
    key,
    shiftKey: false,
    defaultPrevented: false,
    prevented: false,
    preventDefault() {
      event.prevented = true
      event.defaultPrevented = true
    },
    ...over,
  }
  return event
}

test('an empty box is a combobox with no list and no clear button', async () => {
  const input = await mount()
  assert.equal(input.props.role, 'combobox')
  assert.equal(input.props['aria-expanded'], false)
  assert.equal(input.props['aria-label'], '搜索节点')
  assert.equal(renderer!.root.findAllByProps({ role: 'listbox' }).length, 0)
  assert.equal(renderer!.root.findAll((item) => item.props['aria-label'] === '清空搜索').length, 0)
})

test('a query lists ranked hits with the active row wired to aria-activedescendant', async () => {
  const hits = hitsOf('登录口越权', '验证登录口')
  const input = await mount({ q: '登录', hits, activeIndex: 0 })
  assert.equal(input.props['aria-expanded'], true)
  const list = renderer!.root.findByProps({ role: 'listbox' })
  const options = renderer!.root.findAllByProps({ role: 'option' })
  assert.equal(options.length, 2)
  assert.equal(options[0].props['aria-selected'], true)
  assert.equal(options[1].props['aria-selected'], false)
  assert.ok(String(input.props['aria-controls']).includes('r'))
  assert.equal(input.props['aria-activedescendant'], options[0].props.id)
  assert.equal(list.props['aria-label'], '搜索命中节点')
  // The matched fragment is marked without changing the label text.
  const marks = renderer!.root.findAllByType('mark')
  assert.equal(marks.length, 2)
  assert.equal(marks[0].children.join(''), '登录')
  assert.equal(renderer!.root.findAll((item) => item.props['aria-label'] === '清空搜索').length, 1)
})

test('a hit that matched the description says so instead of the status', async () => {
  const hit: GraphSearchHit = {
    node: node('f:sub', '资产清单', { sub: '含登录口记录', status: 'verified' }),
    rank: 3,
    field: 'sub',
  }
  await mount({ q: '登录', hits: [hit], activeIndex: 0 })
  const text = renderer!.root
    .findAllByType('span')
    .map((span) => span.children.filter((child) => typeof child === 'string').join(''))
    .join('|')
  assert.ok(text.includes('描述'), 'the row names the field that matched')
  assert.equal(text.includes('已复核'), false, 'the status does not crowd out the reason')
  assert.equal(renderer!.root.findAllByType('mark').length, 0, 'the label itself has no query text to mark')
})

test('↑↓ move the active row inside the list', async () => {
  const calls: number[] = []
  const input = await mount({
    q: '登录',
    hits: hitsOf('a', 'b', 'c'),
    activeIndex: 1,
    onActiveIndex: (index: number) => calls.push(index),
  })
  input.props.onKeyDown(key('ArrowDown'))
  input.props.onKeyDown(key('ArrowUp'))
  assert.deepEqual(calls, [2, 0])
})

test('Enter locates the active hit and Shift+Enter goes back', async () => {
  const calls: boolean[] = []
  const input = await mount({
    q: '登录',
    hits: hitsOf('a', 'b'),
    activeIndex: 0,
    onActivate: (backward: boolean) => calls.push(backward),
  })
  input.props.onKeyDown(key('Enter'))
  input.props.onKeyDown(key('Enter', { shiftKey: true }))
  assert.deepEqual(calls, [false, true])
})

test('clicking a row opens that exact hit', async () => {
  const opened: number[] = []
  await mount({ q: '登录', hits: hitsOf('a', 'b'), activeIndex: 0, onOpenHit: (index: number) => opened.push(index) })
  const options = renderer!.root.findAllByProps({ role: 'option' })
  options[1].props.onClick()
  assert.deepEqual(opened, [1])
})

test('Escape clears a non-empty box', async () => {
  const queries: string[] = []
  const active: number[] = []
  const input = await mount({
    q: '登录',
    hits: hitsOf('a'),
    activeIndex: 0,
    onQueryChange: (next: string) => queries.push(next),
    onActiveIndex: (index: number) => active.push(index),
  })
  input.props.onKeyDown(key('Escape'))
  assert.deepEqual(queries, [''])
  assert.deepEqual(active, [-1])
})

test('the × button clears the search and keeps the keyboard in the box', async () => {
  const queries: string[] = []
  const active: number[] = []
  await mount({
    q: '登录',
    hits: hitsOf('a'),
    activeIndex: 0,
    onQueryChange: (next: string) => queries.push(next),
    onActiveIndex: (index: number) => active.push(index),
  })
  nodeMock.focused = 0
  const clear = renderer!.root.find((item) => item.props['aria-label'] === '清空搜索')
  clear.props.onClick()
  assert.deepEqual(queries, [''])
  assert.deepEqual(active, [-1])
  assert.equal(nodeMock.focused, 1, 'clearing keeps the caret in the search box')
})

test('a composing word is not committed to the search until composition ends', async () => {
  const queries: string[] = []
  const input = await mount({ onQueryChange: (next: string) => queries.push(next) })
  input.props.onCompositionStart()
  input.props.onChange({ target: { value: '登' } })
  assert.deepEqual(queries, [], 'the half-typed IME prefix must not search')
  input.props.onChange({ target: { value: '登录' } })
  assert.deepEqual(queries, [])
  input.props.onCompositionEnd({ currentTarget: { value: '登录' } })
  assert.deepEqual(queries, ['登录'])
})

test('a local miss turns Enter into the whole-ledger search', async () => {
  const calls: unknown[] = []
  await mount({ q: '不存在', hits: [], activeIndex: -1 })
  const { api } = await server.ssrLoadModule('/src/lib/api.ts')
  const saved = api.factsPage
  api.factsPage = async (params: unknown) => {
    calls.push(params)
    return { facts: [], next_cursor: undefined, has_more: false, total: 0 }
  }
  try {
    const input = searchInput()
    await act(async () => {
      input.props.onKeyDown(key('Enter'))
    })
    assert.equal(calls.length, 1)
    assert.deepEqual(calls[0], { q: '不存在', mode: 'lexical', limit: 20, cursor: '' })
  } finally {
    api.factsPage = saved
  }
})

test('slash focuses the search box from the map', async () => {
  await mount()
  nodeMock.focused = 0
  nodeMock.selected = 0
  const handlers = [...(listeners.get('keydown') ?? [])]
  assert.ok(handlers.length > 0, 'the shortcut listener is installed while the card is mounted')
  handlers[0]({ key: '/', target: null, metaKey: false, ctrlKey: false, altKey: false, defaultPrevented: false, preventDefault() {} })
  assert.equal(nodeMock.focused, 1)
  assert.equal(nodeMock.selected, 1)
})

test('clicking a whole-ledger hit clears the local search and focuses that fact', async () => {
  const { api } = await server.ssrLoadModule('/src/lib/api.ts')
  const saved = api.factsPage
  api.factsPage = async () => ({
    facts: [{ id: 'f_global', title: '全局事实' }],
    next_cursor: undefined,
    has_more: false,
    total: 1,
  })
  const queries: string[] = []
  const focused: string[] = []
  try {
    const input = await mount({
      q: '登录',
      hits: [],
      activeIndex: -1,
      onQueryChange: (next: string) => queries.push(next),
      onFocusFact: (id: string) => focused.push(id),
    })
    await act(async () => {
      input.props.onKeyDown(key('Enter'))
    })
    const row = renderer!.root
      .findAllByType('button')
      .find((button) => button.children.join('') === '全局事实')
    assert.ok(row, 'the whole-ledger hit is rendered under the local result')
    await act(async () => {
      row!.props.onClick()
    })
    assert.deepEqual(queries, [''])
    assert.deepEqual(focused, ['f_global'])
  } finally {
    api.factsPage = saved
  }
})
