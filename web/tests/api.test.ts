import { test } from 'node:test'
import assert from 'node:assert/strict'
import { File } from 'node:buffer'
import { ApiError, api } from '../src/lib/api.ts'

test('native IOA project and instance views use distinct read-only endpoints', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response('{"records":[],"sessions":{}}')
  }
  try {
    await api.ioaObservations()
    await api.instanceIOAObservations()
    assert.deepEqual(calls, [
      { url: '/p/fixture/api/ioa/events', method: 'GET' },
      { url: '/api/ioa/events', method: 'GET' },
    ])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('expired login preserves the selected view, record and graph selection', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  let redirect = ''
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: {
    pathname: '/p/example/', search: '?view=runs&run=r_example', hash: '#selection',
    assign: (url: string) => { redirect = url },
  } } })
  globalThis.fetch = async () => new Response('{}', { status: 401 })
  try {
    await assert.rejects(api.health(), /需要登录/)
    assert.equal(new URL(redirect, 'http://localhost').searchParams.get('next'), '/p/example/?view=runs&run=r_example#selection')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('unavailable human action is sent as a distinct receipt, not completed login', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  let sent: { url?: string; body?: unknown } = {}
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    sent = {url: String(url), body: JSON.parse(String(init?.body))}
    return new Response('{"accepted":true}')
  }
  try {
    await api.completeOperator('in_example', 'op_example', '没有账号，本次仅测未登录面', 'unavailable')
    assert.equal(sent.url, '/p/fixture/api/intents/in_example/operator/complete')
    assert.deepEqual(sent.body, {request_id:'op_example', response:'没有账号，本次仅测未登录面', resolution:'unavailable'})
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('cancelIntent refuses an empty reason before any request, and sends a trimmed reason otherwise', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; body: unknown }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : null })
    return new Response('{"intent_id":"in_example","run_id":"r_1","held":true}')
  }
  try {
    await assert.rejects(api.cancelIntent('in_example', '   '), /没有理由的停止等于丢工作/)
    assert.equal(calls.length, 0, 'an empty reason must not reach the wire')
    const result = await api.cancelIntent('in_example', '  意图越界  ')
    assert.equal(calls.length, 1)
    assert.equal(calls[0].url, '/p/fixture/api/intents/in_example/cancel')
    assert.deepEqual(calls[0].body, { reason: '意图越界' })
    assert.equal(result.held, true)
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('a 409 stop keeps the server reason and status for the dialog to show', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async () =>
    new Response('{"error":"意图 in_example 已经是 failed：用 retry 或 park，不要 cancel"}', { status: 409 })
  try {
    await assert.rejects(api.cancelIntent('in_example', '重复停止'), (error: unknown) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 409)
      assert.match(error.message, /用 retry 或 park/)
      return true
    })
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('project export is a JSON POST and import uploads the archive as the body', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string; type: string; body: unknown }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/g/' } } })
  globalThis.fetch = async (url, init) => {
    const headers = (init?.headers ?? {}) as Record<string, string>
    calls.push({ url: String(url), method: init?.method ?? 'GET', type: headers['Content-Type'] ?? '', body: init?.body })
    return new Response('{"name":"p-1.sakerproj","url":"/api/exports/p-1.sakerproj","size":1,"manifest":{},"project":{}}')
  }
  try {
    await api.exportProject('p-1')
    // Import uploads the archive byte-for-byte: the page never inspects the
    // suffix, so the server stays the one place that validates the format.
    const archive = new File(['PK'], 'p-1.sakerproj', { type: 'application/zip' })
    await api.importProject(archive)
    assert.deepEqual(calls.map(c => ({ url: c.url, method: c.method, type: c.type })), [
      { url: '/api/projects/p-1/export', method: 'POST', type: 'application/json' },
      { url: '/api/projects/import', method: 'POST', type: 'application/zip' },
    ])
    // The archive must stay a binary body: base64-in-JSON would triple its size
    // in the page and defeat the server's temp-file streaming path.
    assert.ok(calls[1].body instanceof File)
    assert.equal(typeof calls[1].body, 'object')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('the review switch is project-scoped and distinct from the project pause', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/acme/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response('{"paused":false,"enabled":true}', { status: 200 })
  }
  try {
    await api.verifierStatus()
    await api.pauseReviews()
    await api.resumeReviews()
    // /api/project/pause stays a different endpoint: pausing reviews never
    // resumes or pauses the engagement (D137).
    assert.deepEqual(calls, [
      { url: '/p/acme/api/verifier', method: 'GET' },
      { url: '/p/acme/api/verifier/pause', method: 'POST' },
      { url: '/p/acme/api/verifier/resume', method: 'POST' },
    ])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('editing an instruction is a versioned PUT carrying the body this page read', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string; type: string; body: unknown }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    const headers = (init?.headers ?? {}) as Record<string, string>
    calls.push({
      url: String(url),
      method: init?.method ?? 'GET',
      type: headers['Content-Type'] ?? '',
      body: init?.body,
    })
    return new Response('{"id":"instr_7","text":"只读","version":5,"state":"active","created_by":"operator","created_at":"2026-11-21T00:00:00Z","updated_at":"2026-11-22T00:00:00Z"}')
  }
  try {
    // Not a revoke: the same PUT path with the edited text and the state the page
    // read, so the server's compare-and-set can refuse a stale write.
    const updated = await api.updateInstruction('instr_7', { version: 4, text: '只读', state: 'active' })
    assert.deepEqual(calls, [{
      url: '/p/fixture/api/instructions/instr_7',
      method: 'PUT',
      type: 'application/json',
      body: JSON.stringify({ version: 4, text: '只读', state: 'active' }),
    }])
    assert.equal(updated.version, 5)
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
test('editing an intent is a PATCH of the plan text carrying the edit_version read', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string; body: unknown }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : null })
    return new Response('{"id":"in_1","title":"新标题","description":"旧说明","eval":"新验收","edit_version":1,"status":"pending"}')
  }
  try {
    const updated = await api.editIntent('in_1', { version: 0, title: '新标题', eval: '新验收' })
    assert.deepEqual(calls, [{
      url: '/p/fixture/api/intents/in_1',
      method: 'PATCH',
      body: { version: 0, title: '新标题', eval: '新验收' },
    }])
    assert.equal(updated.edit_version, 1)
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('a refused intent edit keeps its stable code so the editor can explain the lock', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async () =>
    new Response('{"error":"意图已有运行记录，计划正文不能再改","code":"intent_not_editable"}', { status: 409 })
  try {
    await assert.rejects(api.editIntent('in_1', { version: 2, eval: 'x' }), (error: unknown) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 409)
      assert.equal(error.code, 'intent_not_editable')
      return true
    })
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('project-scoped health and bundle download stay under the project base', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url) => {
    calls.push(String(url))
    return new Response('{"ok":true,"executor":"fixture"}')
  }
  try {
    await api.health()
    assert.deepEqual(calls, ['/p/fixture/api/health'])
    // The download handler only exists on the project mux; an instance-level
    // /api/bundles/... URL would fall through to the instance 404.
    assert.equal(api.bundleURL('b-1.zip'), '/p/fixture/api/bundles/b-1.zip')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
