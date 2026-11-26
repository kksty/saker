// Shell/navigation semantics: the skip link must point at the one <main>
// landmark, the rail must be a labelled landmark, and the tab strip must keep
// its roving tab stop and arrow-key contract.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let renderer: ReactTestRenderer | undefined
let Page: any, ConsoleRail: any, SideNav: any, TabBar: any
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  Page = (await server.ssrLoadModule('/src/components/Page.tsx')).Page
  ConsoleRail = (await server.ssrLoadModule('/src/components/ConsoleRail.tsx')).ConsoleRail
  SideNav = (await server.ssrLoadModule('/src/components/SideNav.tsx')).SideNav
  TabBar = (await server.ssrLoadModule('/src/components/TabBar.tsx')).TabBar
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/g/', search: '', hash: '' },
      addEventListener() {},
      removeEventListener() {},
      setInterval,
      clearInterval,
    },
  })
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
})

after(async () => {
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  await server?.close()
})

test('the page body is the main landmark and the heading names it', async () => {
  await act(async () => {
    renderer = create(React.createElement(Page, { title: '态势', subtitle: '舰队与运行态' }, '正文'))
  })
  const main = renderer!.root.findByType('main')
  assert.equal(main.props.id, 'saker-main')
  assert.equal(main.props.tabIndex, -1)
  const heading = renderer!.root.findByType('h1')
  assert.ok(heading.props.id, 'the h1 must carry an id for aria-labelledby')
  assert.equal(main.props['aria-labelledby'], heading.props.id)
  assert.equal(heading.children.join(''), '态势')
})

test('the rail is a labelled landmark with a skip link as its first tab stop', async () => {
  await act(async () => {
    renderer = create(React.createElement(ConsoleRail, {
      title: '全局控制台',
      active: 'fleet',
      footer: React.createElement('div', null, 'footer'),
    }, React.createElement('nav', { 'aria-label': '全局导航' }, 'links')))
  })
  const link = renderer!.root.findByType('a')
  assert.equal(link.props.href, '#saker-main')
  assert.ok(String(link.children.join('')).includes('跳到主要内容'))
  const aside = renderer!.root.findByType('aside')
  assert.equal(aside.props['aria-label'], '全局控制台导航')
  assert.equal(renderer!.root.findAllByType('header').length, 1, 'the compact bar is a header landmark')
  assert.equal(renderer!.root.findAllByType('nav').length, 1)
})

test('rail items announce the current page and stay native buttons', async () => {
  const Icon = () => React.createElement('svg')
  await act(async () => {
    renderer = create(React.createElement('div', null,
      React.createElement(SideNav, { active: true, icon: Icon, label: '态势', onClick() {} }),
      React.createElement(SideNav, { active: false, icon: Icon, label: '项目', onClick() {} }),
    ))
  })
  const buttons = renderer!.root.findAllByType('button')
  assert.equal(buttons[0].props['aria-current'], 'page')
  assert.equal(buttons[1].props['aria-current'], undefined)
  assert.equal(buttons[0].props.type, 'button')
})

test('reaching for a rail row warms its chunk without activating it', async () => {
  const Icon = () => React.createElement('svg')
  const intents: string[] = []
  const clicks: string[] = []
  await act(async () => {
    renderer = create(React.createElement(SideNav, {
      active: false,
      icon: Icon,
      label: '证据',
      onClick: () => clicks.push('evidence'),
      onIntent: () => intents.push('evidence'),
    }))
  })
  const button = renderer!.root.findByType('button')
  // A pointer press lands before the click (so a touch device, which never
  // fires mouseenter, still starts the download early), and keyboard focus
  // covers activation from the keyboard.
  for (const handler of ['onMouseEnter', 'onFocus', 'onPointerDown']) {
    assert.equal(typeof button.props[handler], 'function', `${handler} must reach for the chunk`)
    await act(async () => { button.props[handler]() })
  }
  assert.equal(intents.length, 3, 'hover, focus and a press all warm the chunk')
  assert.deepEqual(clicks, [], 'warming never activates the row')
  await act(async () => { button.props.onClick() })
  assert.deepEqual(clicks, ['evidence'])
})

test('the tab strip keeps one tab stop and moves it with the arrow keys', async () => {  const selected: string[] = []
  const tabs = [{ id: 'one', label: '一' }, { id: 'two', label: '二' }, { id: 'three', label: '三' }] as const
  await act(async () => {
    renderer = create(React.createElement(TabBar, { tabs, tab: 'one', onTab: (id: string) => selected.push(id) }))
  })
  const list = renderer!.root.findByProps({ role: 'tablist' })
  assert.equal(list.props['aria-orientation'], 'horizontal')
  const tabNodes = () => renderer!.root.findAllByProps({ role: 'tab' })
  assert.deepEqual(tabNodes().map((node: any) => node.props.tabIndex), [0, -1, -1])
  assert.deepEqual(tabNodes().map((node: any) => node.props['aria-selected']), [true, false, false])

  await act(async () => { list.props.onKeyDown({ key: 'ArrowRight', preventDefault() {} }) })
  assert.deepEqual(selected, ['two'], 'the neighbour is activated, not just focused')
  await act(async () => { list.props.onKeyDown({ key: 'End', preventDefault() {} }) })
  assert.deepEqual(selected, ['two', 'three'])
  await act(async () => { list.props.onKeyDown({ key: 'Home', preventDefault() {} }) })
  assert.deepEqual(selected, ['two', 'three', 'one'])
})
