// FA4 transport contract: the shared request layer keeps its existing error
// semantics (ApiError status/code, 401 redirect) while gaining two modern
// capabilities: every query string is built with URLSearchParams, and a read can
// be cancelled with an AbortSignal (optionally with an AbortSignal.timeout
// deadline). These tests mock fetch; no network and no browser are involved.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api, ApiError } from '../src/lib/api.ts'
import { putIntentContext } from '../src/lib/context.ts'
import type { IntentContextRequest } from '../src/lib/types.ts'

interface Call {
  url: string
  init?: RequestInit
}

/** A minimal window: the console only reads the project from the pathname and
 *  uses location.assign for the login redirect. */
function withWindow<T>(pathname: string, fn: (redirects: string[]) => Promise<T>): Promise<T> {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const redirects: string[] = []
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: {
        pathname,
        search: '',
        hash: '',
        assign: (url: string) => redirects.push(url),
      },
    },
  })
  const done = () => {
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
  return fn(redirects).finally(done)
}

function mockFetch(handler: (url: string, init?: RequestInit) => Promise<Response>) {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) =>
    handler(String(input), init)) as typeof fetch
  return () => {
    globalThis.fetch = originalFetch
  }
}

function recordingFetch(calls: Call[], body = '{"events":[]}'): (url: string, init?: RequestInit) => Promise<Response> {
  return async (url, init) => {
    calls.push({ url, init })
    return new Response(body)
  }
}

/** Rejects with the signal reason as soon as the signal aborts. */
function abortableFetch(): (url: string, init?: RequestInit) => Promise<Response> {
  return (_url, init) =>
    new Promise<Response>((_resolve, reject) => {
      const signal = init?.signal
      assert.ok(signal instanceof AbortSignal)
      if (signal.aborted) {
        reject(signal.reason)
        return
      }
      signal.addEventListener('abort', () => reject(signal.reason), { once: true })
    })
}

test('reads forward the caller signal and encode every query with URLSearchParams', async () => {
  const calls: Call[] = []
  const restore = mockFetch(recordingFetch(calls))
  try {
    await withWindow('/p/fixture/', async () => {
      const controller = new AbortController()
      await api.events('r_1', 100, true, { signal: controller.signal })
      await api.httpFlows('a b&c=d', 0, { signal: controller.signal })
      await api.factsPage({ q: 'x y', limit: 20 })
      await api.artifacts('')

      assert.equal(calls[0].url, '/p/fixture/api/runs/r_1/events?limit=100&include_input=true')
      assert.equal(calls[0].init?.signal, controller.signal)
      // Space becomes +, & and = inside a value stay encoded: no hand-built
      // query string can lose them.
      assert.equal(
        calls[1].url,
        '/p/fixture/api/traffic/flows?q=a+b%26c%3Dd&offset=0&limit=100',
      )
      assert.equal(calls[1].init?.signal, controller.signal)
      assert.equal(calls[2].url, '/p/fixture/api/facts?q=x+y&limit=20')
      assert.equal(calls[3].url, '/p/fixture/api/artifacts?limit=100')
    })
  } finally {
    restore()
  }
})

test('an aborted read rejects with the signal reason (AbortError)', async () => {
  const restore = mockFetch(abortableFetch())
  try {
    await withWindow('/p/fixture/', async () => {
      const controller = new AbortController()
      const pending = api.health({ signal: controller.signal })
      controller.abort()
      await assert.rejects(
        pending,
        (error: unknown) => error instanceof DOMException && error.name === 'AbortError',
      )
    })
  } finally {
    restore()
  }
})

test('timeoutMs aborts with TimeoutError, not AbortError', async () => {
  const restore = mockFetch(abortableFetch())
  try {
    await withWindow('/p/fixture/', async () => {
      // AbortSignal.timeout's timer is unref'd. Under `node --test` nothing else
      // holds the event loop while this awaits, so the deadline could be cut off
      // before it fires and the awaited rejection never arrived ("Promise
      // resolution is still pending but the event loop has already resolved").
      // The keepalive is harness plumbing: it stands in for the timer loop a
      // browser keeps alive, and does not change the code under test.
      const keepalive = setInterval(() => {}, 1000)
      try {
        await assert.rejects(
          api.health({ timeoutMs: 10 }),
          (error: unknown) => error instanceof DOMException && error.name === 'TimeoutError',
        )
      } finally {
        clearInterval(keepalive)
      }
    })
  } finally {
    restore()
  }
})

test('request options never change the HTTP error contract', async () => {
  const restore = mockFetch(
    async () => new Response('{"error":"conflict","code":"peer_denied"}', { status: 409 }),
  )
  try {
    await withWindow('/p/fixture/', async () => {
      await assert.rejects(
        api.health({ timeoutMs: 100 }),
        (error: unknown) =>
          error instanceof ApiError && error.status === 409 && error.code === 'peer_denied',
      )
    })
  } finally {
    restore()
  }
})

test('the context write forwards the signal and keeps the 409 code and 401 redirect', async () => {
  const calls: Call[] = []
  const body: IntentContextRequest = { version: 3, references: ['f_1'], pins: [], entities: [] }
  let restore = mockFetch(async (url, init) => {
    calls.push({ url, init })
    return new Response('{"error":"changed","code":"context_denied"}', { status: 409 })
  })
  try {
    await withWindow('/p/fixture/', async () => {
      const controller = new AbortController()
      await assert.rejects(
        putIntentContext('in_1', body, { signal: controller.signal }),
        (error: unknown) =>
          error instanceof ApiError && error.status === 409 && error.code === 'context_denied',
      )
      assert.equal(calls[0].url, '/p/fixture/api/intents/in_1/context')
      assert.equal(calls[0].init?.method, 'PUT')
      assert.equal(calls[0].init?.body, JSON.stringify(body))
      assert.equal(calls[0].init?.signal, controller.signal)
    })
  } finally {
    restore()
  }

  // 401 is a login redirect, and the return path survives URL encoding whole.
  restore = mockFetch(async () => new Response('{}', { status: 401 }))
  try {
    await withWindow('/p/fixture/', async (redirects) => {
      await assert.rejects(putIntentContext('in_1', body), /需要登录/)
      assert.equal(redirects.length, 1)
      assert.equal(
        new URL(redirects[0], 'http://localhost').searchParams.get('next'),
        '/p/fixture/',
      )
    })
  } finally {
    restore()
  }
})
