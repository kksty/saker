import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

// The operator inbox poller: unread count, tab-title badge and the read cursor.
// It runs on window.setInterval/sessionStorage, so the test drives one poll at a
// time with fake timers instead of waiting four real seconds.
let server: Awaited<ReturnType<typeof createTestServer>>
let OperatorAlerts: React.ComponentType<any>
let api: any
let renderer: ReactTestRenderer | undefined

const storage = new Map<string, string>()
const timers = new Map<number, () => void>()
const listeners = new Map<string, (event: any) => void>()
let timerID = 0
let sentSeen: number[] = []
let inbox: any[] = []
let requests: number[] = []

const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const oldMutation = Object.getOwnPropertyDescriptor(globalThis, 'MutationObserver')

before(async () => {
  server = await createTestServer({
    root: fileURLToPath(new URL('..', import.meta.url)),
  })
  OperatorAlerts = (await server.ssrLoadModule('/src/components/OperatorAlerts.tsx')).OperatorAlerts
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  Object.defineProperty(globalThis, 'window', { configurable: true, value: {
    location: { pathname: '/p/alpha/', search: '', hash: '' },
    sessionStorage: { getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value) },
    setInterval: (fn: () => void) => { timers.set(++timerID, fn); return timerID },
    clearInterval: (id: number) => timers.delete(id),
    addEventListener: (name: string, fn: (event: any) => void) => listeners.set(name, fn),
    removeEventListener: (name: string) => listeners.delete(name),
    dispatchEvent: () => true,
  } })
  Object.defineProperty(globalThis, 'document', { configurable: true, value: {
    hidden: false,
    visibilityState: 'visible',
    title: 'Saker',
    querySelector: () => null,
    head: {},
    addEventListener() {},
    removeEventListener() {},
  } })
  Object.defineProperty(globalThis, 'MutationObserver', { configurable: true, value: class { observe() {} disconnect() {} } })
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  storage.clear(); timers.clear(); listeners.clear(); timerID = 0
  sentSeen = []; inbox = []; requests = []
})

after(async () => {
  if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (oldDocument) Object.defineProperty(globalThis, 'document', oldDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  if (oldMutation) Object.defineProperty(globalThis, 'MutationObserver', oldMutation)
  else Reflect.deleteProperty(globalThis, 'MutationObserver')
  await server?.close()
})

function message(over: Record<string, unknown> = {}) {
  return { id: 'm_1', seq: 2, kind: 'message', text: '请补充授权范围', status: 'accepted', from_participant_id: 'commander', to_participant_id: 'operator', created_at: '2026-11-17T00:00:00Z', ...over }
}

test('operator alerts count unread inbox messages, badge the tab title and flush the viewed cursor', async () => {
  inbox = [message({ id: 'm_ack', seq: 1, status: 'confirmed', text: '已确认的旧消息' }), message({ id: 'm_new', seq: 2 })]
  api.operatorMessages = async (after: number) => {
    requests.push(after)
    return { messages: inbox.filter((m) => m.seq > after), next: 2, more: false }
  }
  api.operatorMessagesSeen = async (through: number) => { sentSeen.push(through); return { read: 1 } }

  await act(async () => { renderer = create(React.createElement(OperatorAlerts, { projectID: 'alpha' })) })
  // First poll: an already-confirmed row is not unread, so the badge says one.
  assert.deepEqual(requests, [0])
  assert.equal(document.title, '(1) Saker')
  const text = JSON.stringify(renderer!.toJSON())
  assert.ok(text.includes('条发给操作员的消息'), text)

  // Viewing the 通信 page fires the shell event: the toast clears, the title is
  // restored and the highest seen seq is remembered for the next poll to ack.
  await act(async () => { listeners.get('saker:operator-messages-read')!({}) })
  assert.equal(document.title, 'Saker')
  assert.equal(renderer!.toJSON(), null)
  assert.ok(JSON.stringify([...storage.entries()]).includes('pending-read'))

  await act(async () => { for (const tick of timers.values()) tick() })
  assert.deepEqual(sentSeen, [2])
  assert.deepEqual(requests, [0, 2], 'after acknowledging, the poll resumes from the flushed cursor')
  const stored = storage.get('saker.viewMemory.v1') ?? ''
  assert.ok(stored.includes('"seen":2'), stored)
})
