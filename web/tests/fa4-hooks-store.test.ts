// FA4: the console location is an external store now. The old
// useState+useEffect pair only subscribed after paint, so a same-document
// navigation fired in between could be missed; useSyncExternalStore re-reads the
// snapshot on subscribe and tears the listener down on unmount. The hook is
// loaded through the app's own Vite transform (it imports extensionless lib
// modules) and rendered with react-test-renderer against a fake history store.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let useConsoleLocation: () => URL
let renderer: ReactTestRenderer | undefined
const listeners = new Map<string, Set<() => void>>()
const location = { pathname: '/p/alpha/', search: '', hash: '', origin: 'http://console.test' }
const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  useConsoleLocation = (await server.ssrLoadModule('/src/lib/hooks.ts')).useConsoleLocation
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location,
      addEventListener(type: string, listener: () => void) {
        const set = listeners.get(type) ?? new Set<() => void>()
        set.add(listener)
        listeners.set(type, set)
      },
      removeEventListener(type: string, listener: () => void) {
        listeners.get(type)?.delete(listener)
      },
      setInterval,
      clearInterval,
      setTimeout,
      clearTimeout,
    },
  })
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
  listeners.clear()
  location.pathname = '/p/alpha/'
  location.search = ''
  location.hash = ''
})

after(async () => {
  if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  await server?.close()
})

function emit(type: string) {
  for (const listener of listeners.get(type) ?? []) listener()
}

function listenerCount(): number {
  return [...listeners.values()].reduce((total, set) => total + set.size, 0)
}

test('the location hook re-reads the store on subscribe and follows navigation', async () => {
  // A navigation that lands after the first render but before the passive
  // subscription must not be lost: useSyncExternalStore checks the snapshot when
  // it subscribes, unlike the old one-shot state initializer.
  location.pathname = '/p/beta/'
  let url: URL | undefined
  function Route() {
    url = useConsoleLocation()
    return null
  }
  await act(async () => {
    renderer = create(React.createElement(Route))
  })
  assert.equal(url?.pathname, '/p/beta/')

  location.pathname = '/p/beta/runs'
  location.search = '?view=runs'
  location.hash = '#r_1'
  await act(async () => {
    emit('saker:navigate')
  })
  assert.equal(url?.pathname, '/p/beta/runs')
  assert.equal(url?.search, '?view=runs')
  assert.equal(url?.hash, '#r_1')
  assert.equal(url?.origin, 'http://console.test')

  // Back/Forward arrives as popstate; the hook follows it too.
  location.pathname = '/p/gamma/'
  location.search = ''
  location.hash = ''
  await act(async () => {
    emit('popstate')
  })
  assert.equal(url?.pathname, '/p/gamma/')
})

test('unmount unsubscribes from every location source', async () => {
  function Route() {
    useConsoleLocation()
    return null
  }
  await act(async () => {
    renderer = create(React.createElement(Route))
  })
  assert.equal(listenerCount(), 3, 'popstate, hashchange and the app navigation event')
  await act(async () => {
    renderer!.unmount()
    renderer = undefined
  })
  assert.equal(listenerCount(), 0)
})
