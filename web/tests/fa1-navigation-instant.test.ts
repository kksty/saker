// Navigation contract: the visible change is synchronous.
//
// A rail click, a tab click and a graph node selection all go through
// `navigate()`. It must mutate history and notify React *in the same task* and
// never ask the browser to animate the swap.
//
// That `never` is the part worth pinning. The View Transitions wrapper that used
// to sit here captured the page's snapshot on the rendering opportunity that
// followed the callback, but React commits the state change after it, so every
// switch cross-faded two identical frames and the real change appeared
// afterwards — a page-wide flash, most obvious when clicking a graph node, where
// only one dock panel changes. It also added a snapshot pass and 120–160ms of
// fade to every click on a data-dense page.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { navigate, subscribeLocation } from '../src/lib/navigation.ts'

const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

interface Harness {
  calls: { mode: string; url: string }[]
  events: number
  /** How often the app asked the platform to animate the swap. */
  transitions: number
}

function harness(): Harness {
  const state: Harness = { calls: [], events: 0, transitions: 0 }
  const location = { pathname: '/g/', search: '', hash: '' }
  const events = new EventTarget()
  const update = (mode: string, url: string) => {
    state.calls.push({ mode, url })
    const next = new URL(url, 'http://localhost')
    Object.assign(location, { pathname: next.pathname, search: next.search, hash: next.hash })
  }
  const fakeWindow = {
    location,
    history: {
      pushState: (_s: unknown, _t: string, url: string) => update('push', url),
      replaceState: (_s: unknown, _t: string, url: string) => update('replace', url),
    },
    matchMedia: () => ({ matches: false }),
    addEventListener: events.addEventListener.bind(events),
    removeEventListener: events.removeEventListener.bind(events),
    dispatchEvent: (event: Event) => {
      if (event.type === 'saker:navigate') state.events++
      return events.dispatchEvent(event)
    },
  }
  Object.defineProperty(globalThis, 'window', { configurable: true, value: fakeWindow })
  // A browser that does expose the API: the app must still never use it.
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      startViewTransition() {
        state.transitions++
        return { ready: Promise.resolve(), finished: Promise.resolve() }
      },
    },
  })
  return state
}

afterEach(() => {
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (originalDocument) Object.defineProperty(globalThis, 'document', originalDocument)
  else Reflect.deleteProperty(globalThis, 'document')
})

test('a route change commits history and notifies readers before navigate returns', () => {
  const state = harness()
  let changed = 0
  const unsubscribe = subscribeLocation(() => changed++)
  try {
    const before = changed
    navigate('/g/?view=settings')
    // No tick, no promise, no frame: the click paints the new route next frame.
    assert.deepEqual(state.calls, [{ mode: 'push', url: '/g/?view=settings' }])
    assert.equal(changed, before + 1, 'readers see the new path synchronously')
  } finally {
    unsubscribe()
  }
})

test('a replace navigation (selection, tab alias) is one synchronous swap', () => {
  const state = harness()
  navigate('/p/demo/?view=graph#f%3Afact_1', true)
  assert.deepEqual(state.calls, [{ mode: 'replace', url: '/p/demo/?view=graph#f%3Afact_1' }])
  assert.equal(state.events, 1)
})

test('the page is never asked to animate the swap, even where the API exists', () => {
  const state = harness()
  navigate('/p/demo/?view=runs')
  navigate('/p/demo/?view=runs#s%3Aintent_1', true)
  navigate('/p/demo/?view=state')
  assert.equal(state.transitions, 0, 'View Transitions must not be used for navigation')
  assert.equal(state.events, 3)
})

test('clicking the active route adds no history entry and no second render', () => {
  const state = harness()
  navigate('/g/?view=projects')
  const callsAfterFirst = state.calls.length
  const eventsAfterFirst = state.events
  navigate('/g/?view=projects')
  assert.equal(state.calls.length, callsAfterFirst, 'the active route is a no-op')
  assert.equal(state.events, eventsAfterFirst, 'readers are not woken for a no-op')
})

test('selecting a graph node is a hash-only commit, so the map is not re-rooted', () => {
  const state = harness()
  navigate('/p/demo/?view=graph')
  const before = state.calls.length
  // This is exactly what GraphView.select does: one replaceState with the hash.
  navigate('/p/demo/?view=graph#s%3Aintent_7', true)
  assert.deepEqual(state.calls.slice(before), [{ mode: 'replace', url: '/p/demo/?view=graph#s%3Aintent_7' }])
  assert.equal(state.transitions, 0)
  assert.equal(state.events, 2)
})
