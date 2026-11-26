// 图谱里「关掉浮层」的两条硬要求（2026-11-24 巡查）：
//   1) 按钮上写「关闭（Esc）」就真接 Esc，焦点在浮层里时也生效；
//   2) 关掉之后焦点回主画布，别掉到 BODY（否则下一次 Tab 从文档头开始）。
//
// 断言同时覆盖 `isTypingTarget` 的输入边界，以及节点详情上实际的关闭与焦点回交。
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import react from '@vitejs/plugin-react-swc'
import { createTestServer } from './helpers/vite.ts'

class FakeElement {
  tagName: string
  isContentEditable: boolean
  focusedWith: unknown = undefined
  constructor(tagName: string, isContentEditable = false) {
    this.tagName = tagName
    this.isContentEditable = isContentEditable
  }
  focus(options?: unknown) {
    this.focusedWith = options
  }
}

let server: Awaited<ReturnType<typeof createTestServer>>
let GraphNodePanel: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined
let isTypingTarget: (target: unknown) => boolean
let isEscapeToClose: (event: unknown) => boolean
let closeAndFocusCanvas: (close: () => void) => void
const frames: Array<() => void> = []
let canvas: FakeElement | undefined
let queriedSelector = ''
const savedGlobals = new Map<string, PropertyDescriptor | undefined>()

before(async () => {
  for (const name of ['window', 'document', 'HTMLElement', 'requestAnimationFrame']) {
    savedGlobals.set(name, Object.getOwnPropertyDescriptor(globalThis, name))
  }
  Object.defineProperty(globalThis, 'HTMLElement', { configurable: true, value: FakeElement })
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/p/fixture/' },
      requestAnimationFrame: (cb: () => void) => frames.push(cb),
      setInterval,
      clearInterval,
    },
  })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      visibilityState: 'visible',
      addEventListener() {},
      removeEventListener() {},
      querySelector(selector: string) {
        queriedSelector = selector
        return canvas ?? null
      },
    },
  })

  server = await createTestServer({
    configFile: false,
    root: fileURLToPath(new URL('..', import.meta.url)),
    plugins: [react()],
    esbuild: false,
    resolve: {
      alias: [
        { find: '@cyber/ui', replacement: fileURLToPath(new URL('../packages/ui/src', import.meta.url)) },
        { find: '@cyber/theme', replacement: fileURLToPath(new URL('../packages/theme/src', import.meta.url)) },
        { find: '@', replacement: fileURLToPath(new URL('../src', import.meta.url)) },
      ],
      dedupe: ['react', 'react-dom', 'react/jsx-runtime', 'react/jsx-dev-runtime'],
    },
  })
  GraphNodePanel = (await server.ssrLoadModule('/src/components/graph/GraphNodePanel.tsx')).GraphNodePanel
  const { api } = await server.ssrLoadModule('/src/lib/api.ts')
  api.premisePolicy = async () => ({ premise_policy: 'verified_only' })
  const focus = await server.ssrLoadModule('/src/components/graph/focus.ts')
  isTypingTarget = focus.isTypingTarget
  isEscapeToClose = focus.isEscapeToClose
  closeAndFocusCanvas = focus.closeAndFocusCanvas
})

afterEach(async () => {
  await act(async () => { renderer?.unmount() })
  renderer = undefined
})

after(async () => {
  for (const [name, descriptor] of savedGlobals) {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor)
    else delete (globalThis as Record<string, unknown>)[name]
  }
  await server?.close()
})

test('isTypingTarget only claims elements that really take text', () => {
  assert.equal(isTypingTarget(new FakeElement('INPUT')), true)
  assert.equal(isTypingTarget(new FakeElement('TEXTAREA')), true)
  assert.equal(isTypingTarget(new FakeElement('SELECT')), true)
  assert.equal(isTypingTarget(new FakeElement('DIV', true)), true)
  assert.equal(isTypingTarget(new FakeElement('BUTTON')), false, 'a button does not swallow Esc')
  assert.equal(isTypingTarget(new FakeElement('CANVAS')), false)
  assert.equal(isTypingTarget(null), false)
})

function panelProps(onClose: () => void) {
  return {
    node: { id: 'p:fixture', type: 'project', label: '项目简报' },
    facts: [],
    runs: [],
    dock: { width: 360, min: 300, max: 760, resizing: false, onResizeStart() {}, onResizeKey() {} },
    onSelectFact() {},
    onClose,
    onChanged() {},
  }
}

async function mountPanel(props: ReturnType<typeof panelProps>) {
  await act(async () => {
    renderer = create(
      React.createElement(GraphNodePanel, props),
    )
  })
  assert.ok(renderer)
}

const panel = () =>
  renderer!.root.findAll(
    (node) => node.type === 'div' && String(node.props.className ?? '').split(' ').includes('saker-graph-panel'),
  )[0]

function escapeEvent(target: unknown) {
  const event = {
    key: 'Escape',
    target,
    defaultPrevented: false,
    prevented: false,
    preventDefault() {
      event.prevented = true
      event.defaultPrevented = true
    },
  }
  return event
}

test('isEscapeToClose leaves Esc to a layer above, and to the draft being typed', () => {
  const button = new FakeElement('BUTTON')
  assert.equal(isEscapeToClose({ key: 'Escape', defaultPrevented: false, target: button }), true)
  assert.equal(isEscapeToClose({ key: 'Enter', defaultPrevented: false, target: button }), false)
  // 证据预览/确认框这类 Portal 弹层：Radix 在 document 捕获阶段已经处理并 preventDefault，
  // 面板不能再关一次（2026-11-24 实测：否则一次 Esc 关掉两层）。
  assert.equal(
    isEscapeToClose({ key: 'Escape', defaultPrevented: true, target: button }),
    false,
    'a portal dialog that already handled Esc must win',
  )
  assert.equal(isEscapeToClose({ key: 'Escape', defaultPrevented: false, target: new FakeElement('INPUT') }), false)
})

test('closeAndFocusCanvas closes once and hands focus to the canvas', () => {
  let closed = 0
  canvas = new FakeElement('CANVAS')
  frames.length = 0
  closeAndFocusCanvas(() => {
    closed += 1
  })
  assert.equal(closed, 1)
  assert.equal(frames.length, 1, 'focus is handed back on the next frame')
  act(() => {
    frames.shift()?.()
  })
  assert.match(queriedSelector, /graph-canvas/)
  assert.deepEqual(canvas?.focusedWith, { preventScroll: true })
})

test('Escape in the node panel clears the selection and returns focus to the canvas', async () => {
  let cleared = 0
  await mountPanel(panelProps(() => {
    cleared += 1
  }))
  canvas = new FakeElement('CANVAS')
  frames.length = 0
  const event = escapeEvent(new FakeElement('BUTTON'))

  act(() => {
    panel().props.onKeyDown(event)
  })

  assert.equal(cleared, 1, 'Esc must clear the selection')
  assert.equal(event.prevented, true, 'Esc is handled here, so the page must not also act on it')
  assert.equal(frames.length, 1, 'focus is handed back on the next frame')
  act(() => {
    frames.shift()?.()
  })
  assert.match(queriedSelector, /graph-canvas/, 'focus goes to the canvas, not to BODY')
  assert.deepEqual(canvas?.focusedWith, { preventScroll: true })

  // 面板关闭按钮也交回焦点。
  frames.length = 0
  act(() => {
    renderer!.root
      .findAll((node) => node.type === 'button')
      .find((node) => node.props['aria-label'] === '关闭')
      ?.props.onClick()
  })
  assert.equal(cleared, 2)
  assert.equal(frames.length, 1)
})

test('Escape while typing a note does not close the node panel', async () => {
  let cleared = 0
  await mountPanel(panelProps(() => {
    cleared += 1
  }))
  const event = escapeEvent(new FakeElement('TEXTAREA'))
  act(() => {
    panel().props.onKeyDown(event)
  })
  assert.equal(cleared, 0, 'a draft must survive Esc')
  assert.equal(event.prevented, false)
})

test('tree controls stay available without a main-chain list', async () => {
  let filtered = false
  let restored = false
  const props = {
    ...panelProps(() => {}),
    onOnlyTree: () => { filtered = true },
    onShowAll: () => { restored = true },
    // A selected chain previously put a numbered list before the node's
    // actual details. The tree controls now belong to the panel header.
    chain: {
      hops: [{ id: 'p:fixture', index: 1, title: '项目简报', relation: null, otherUses: [] }],
      near: new Set(),
    },
  }
  await mountPanel(props)
  assert.doesNotMatch(JSON.stringify(renderer!.toJSON()), /主干|旁支|data-hop/)
  const button = (label: string) =>
    panel().findAllByType('button').find(node =>
      node.children.some(child => typeof child === 'string' && child.trim() === label))!
  await act(async () => button('只看这棵树').props.onClick())
  assert.equal(filtered, true)
  await act(async () => { renderer!.update(React.createElement(GraphNodePanel, { ...props, treeActive: true })) })
  await act(async () => button('看全图').props.onClick())
  assert.equal(restored, true)
})
