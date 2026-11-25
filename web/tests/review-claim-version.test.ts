// 人工复核要绑在操作员看到的那一版断言上：请求带 claim_version，断言在期间
// 改过时服务端回 409，界面刷新后让人重判，不静默重试。
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let NodePanel: React.ComponentType<any>
let api: any
let ApiError: any
let renderer: ReactTestRenderer | undefined
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  const mod = await server.ssrLoadModule('/src/lib/api.ts')
  api = mod.api
  ApiError = mod.ApiError
  NodePanel = (await server.ssrLoadModule('/src/components/graph/GraphNodePanel.tsx')).GraphNodePanel
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

const fact = {
  id: 'f_claim', title: '示例结论', kind: 'vuln', status: 'unverified',
  confidence: 'observed', created_at: '', claim_version: 7, doc: '',
}
const node = { id: 'f:f_claim', type: 'fact', label: '示例结论', meta: { fact } }

function panel(element = node) {
  return React.createElement(NodePanel, {
    node: element,
    facts: [],
    runs: [],
    dock,
    onSelectFact() {},
    onClose() {},
    onChanged() {},
  })
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))
  })
}

function button(label: string): any {
  // Icon children render as elements; the label is the string part.
  return renderer!.root.findAllByType('button').find((n: any) => {
    const text = n.children
      .map((child: any) => (typeof child === 'string' ? child : ''))
      .join('')
      .trim()
    return text === label
  })
}

async function openReject(): Promise<void> {
  const target = button('判定不成立')
  assert.ok(target, 'reject button must render; saw: ' + renderer!.root.findAllByType('button').map((n: any) => n.children.join('')).join('|'))
  await act(async () => { target.props.onClick() })
  await flush()
  const input = renderer!.root.findAllByType('input').find((n: any) => String(n.props.placeholder ?? '').includes('为什么这条不成立'))
  assert.ok(input, 'reject input must be present')
  await act(async () => { input.props.onChange({ target: { value: '示例理由：证据不足' } }) })
  await flush()
}

function noticeText(): string {
  return JSON.stringify(renderer!.toJSON())
}

test('a human reject carries the assertion version the operator read', async () => {
  const calls: any[] = []
  let reads = 0
  api.fact = async () => {
    reads += 1
    return { fact, version: 7, evidence: [], reviews: [] }
  }
  api.reviewFact = async (...args: any[]) => {
    calls.push(args)
    return { review: { id: 'rv_1' } }
  }
  await act(async () => { renderer = create(panel()) })
  await flush()
  assert.equal(reads, 1, 'the panel reads the fact (and its version) once')
  await openReject()
  await act(async () => { button('确认判定').props.onClick() })
  await flush()
  assert.deepEqual(calls, [['f_claim', 'reject', '示例理由：证据不足', 7]], 'version travels with the verdict')
})

test('a 409 refreshes the fact and asks for a new verdict instead of retrying', async () => {
  let reads = 0
  let reviews = 0
  api.fact = async () => {
    reads += 1
    return { fact, version: 7, evidence: [], reviews: [] }
  }
  api.reviewFact = async () => {
    reviews += 1
    throw new ApiError(409, 'fact changed since this verdict was prepared', 'review_conflict')
  }
  await act(async () => { renderer = create(panel()) })
  await flush()
  await openReject()
  await act(async () => { button('确认判定').props.onClick() })
  await flush()
  assert.equal(reviews, 1, 'a conflict is not retried behind the operator')
  assert.equal(reads, 2, 'the panel re-reads the fact so the next verdict uses the new version')
  assert.match(noticeText(), /断言在你判定期间改过/)
  assert.match(noticeText(), /重新判定/)
  // The reject form stays open: the operator has to look at the new claim.
  assert.ok(button('确认判定'), 'the form is still there after the refresh')
})

test('an unbound verdict still binds when the detail read carries no version', async () => {
  const calls: any[] = []
  api.fact = async () => ({ fact, version: undefined, evidence: [], reviews: [] })
  api.reviewFact = async (...args: any[]) => {
    calls.push(args)
    return { review: { id: 'rv_1' } }
  }
  await act(async () => { renderer = create(panel()) })
  await flush()
  await openReject()
  await act(async () => { button('确认判定').props.onClick() })
  await flush()
  assert.equal(calls[0][3], 7, 'the record\u2019s own claim_version is the fallback')
})
