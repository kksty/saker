import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let renderer: ReactTestRenderer | undefined
let useSessionState: any, usePoll: any, Reports: any, SettingsPage: any, Workers: any, ConfirmProvider: any, api: any, ApiError: any
let EvidencePreview: any, Playbooks: any
const storage = new Map<string, string>()
const timers = new Map<number, () => void>()
let timerID = 0
const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const oldFormElement = Object.getOwnPropertyDescriptor(globalThis, 'HTMLFormElement')

before(async () => {
  server = await createTestServer({
    root: fileURLToPath(new URL('..', import.meta.url)),
    plugins: [{
      name: 'saker-test:inline-sheet',
      enforce: 'pre',
      transform(_, id) {
        if (!id.endsWith('/packages/ui/src/sheet.tsx')) return
        // The test renderer has no DOM to mount the Radix portal into.
        return `
          import { createElement } from 'react'
          export const Sheet = ({open, children}) => open ? children : null
          export const SheetTrigger = 'button'
          export const SheetClose = 'button'
          export const SheetContent = ({children}) => createElement('div', null, children)
          export const SheetHeader = 'header'
          export const SheetFooter = 'footer'
          export const SheetTitle = 'h2'
          export const SheetDescription = 'p'
        `
      },
    }],
  })
  useSessionState = (await server.ssrLoadModule('/src/lib/session-state.ts')).useSessionState
  usePoll = (await server.ssrLoadModule('/src/lib/hooks.ts')).usePoll
  Reports = (await server.ssrLoadModule('/src/views/Reports.tsx')).Reports
  Playbooks = (await server.ssrLoadModule('/src/views/Playbooks.tsx')).Playbooks
  SettingsPage = (await server.ssrLoadModule('/src/views/Settings.tsx')).SettingsPage
  Workers = (await server.ssrLoadModule('/src/views/Workers.tsx')).Workers
  EvidencePreview = (await server.ssrLoadModule('/src/components/EvidencePreview.tsx')).EvidencePreview
  ConfirmProvider = (await server.ssrLoadModule('@cyber/ui')).ConfirmProvider
  const client = await server.ssrLoadModule('/src/lib/api.ts')
  api = client.api
  ApiError = client.ApiError
  const listeners = new Map<string, Set<() => void>>()
  const applyURL = (url: string) => {
    const next = new URL(url, 'http://console.test')
    window.location.pathname = next.pathname
    window.location.search = next.search
    window.location.hash = next.hash
  }
  Object.defineProperty(globalThis, 'window', {configurable: true, value: {
    location: {pathname: '/p/alpha/', search: '', hash: '', origin: 'http://console.test'},
    history: {pushState(_state: unknown, _title: string, url: string) { applyURL(url) }, replaceState(_state: unknown, _title: string, url: string) { applyURL(url) }},
    sessionStorage: {getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value)},
    setInterval: (fn: () => void) => { timers.set(++timerID, fn); return timerID },
    clearInterval: (id: number) => timers.delete(id),
    setTimeout, clearTimeout,
    addEventListener(type: string, fn: () => void) {
      const set = listeners.get(type) ?? new Set<() => void>()
      set.add(fn)
      listeners.set(type, set)
    },
    removeEventListener(type: string, fn: () => void) { listeners.get(type)?.delete(fn) },
    dispatchEvent(event: {type: string}) { for (const fn of listeners.get(event.type) ?? []) fn(); return true },
  }})
  Object.defineProperty(globalThis, 'document', {configurable: true, value: {
    visibilityState: 'visible', addEventListener() {}, removeEventListener() {},
  }})
  Object.defineProperty(globalThis, 'HTMLFormElement', {configurable: true, value: class {}})
})
afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  storage.clear()
  timers.clear()
  window.location.pathname = '/p/alpha/'
  window.location.search = ''
  window.location.hash = ''
})
after(async () => {
  if (oldWindow) Object.defineProperty(globalThis, 'window', oldWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (oldDocument) Object.defineProperty(globalThis, 'document', oldDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  if (oldFormElement) Object.defineProperty(globalThis, 'HTMLFormElement', oldFormElement)
  else Reflect.deleteProperty(globalThis, 'HTMLFormElement')
  await server.close()
})

test('draft edits survive immediate navigation, stay project-scoped and clear on successful submission', async () => {
  let state: any
  function Draft() { state = useSessionState('message', ''); return null }
  await act(async () => { renderer = create(React.createElement(Draft)) })
  await act(async () => { state[1]('unfinished'); renderer!.unmount() })
  window.location.pathname = '/p/beta/'
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], '')
  await act(async () => { renderer!.unmount() })
  window.location.pathname = '/p/alpha/'
  window.location.search = ''
  window.location.hash = ''
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], 'unfinished')
  await act(async () => { state[1](''); renderer!.unmount() })
  await act(async () => { renderer = create(React.createElement(Draft)) })
  assert.equal(state[0], '')
})

test('damaged persisted filters fall back to defaults instead of breaking the graph', async () => {
  storage.set('saker.viewMemory.v1', JSON.stringify({'draft:/p/alpha/': {filters: null}}))
  let value: any
  function Filters() { [value] = useSessionState('filters', {hiddenKinds: [], hideDone: false}); return null }
  await act(async () => { renderer = create(React.createElement(Filters)) })
  assert.deepEqual(value, {hiddenKinds: [], hideDone: false})
})

test('failed refresh preserves data and its timestamp, then a retry clears the error', async () => {
  let fail = false, state: any
  const load = async () => { if (fail) throw new Error('offline'); return ['run-1'] }
  function Monitor() { state = usePoll(load, 4000); return null }
  await act(async () => { renderer = create(React.createElement(Monitor)) })
  const timestamp = state.updatedAt
  fail = true
  await act(async () => { state.reload() })
  assert.deepEqual(state.data, ['run-1'])
  assert.equal(state.updatedAt, timestamp)
  assert.equal(state.error, 'offline')
  fail = false
  await act(async () => { state.reload() })
  assert.equal(state.error, undefined)
})

test('execution supervision starts empty and follows polled directions to their graph and run', async () => {
  let rows: any[] = []
  const opened: string[] = []
  api.workers = async () => ({workers: rows, count: rows.length})
  api.peers = async () => ({mode: 'open', peer_policy: {mode: 'open', allow: []}})
  api.participants = async () => []
  await act(async () => { renderer = create(React.createElement(ConfirmProvider, null,
    React.createElement(Workers, {current: 'alpha', projects: [], onRefresh() {},
      onOpenGraphIntent: (id: string) => opened.push(`graph:${id}`),
      onOpenRun: (id: string) => opened.push(`run:${id}`)}))) })
  const empty = JSON.stringify(renderer!.toJSON())
  assert.ok(empty.includes('还没有执行者'))
  assert.ok(empty.includes('意图开始执行后会出现在这里'))
  assert.ok(!empty.includes('造人') && !empty.includes('造一个人'))
  rows = [{intent_id: 'in_fixture', participant_id: 'pt_fixture', title: 'Fixture direction',
    status: 'running', running: true, can_kill: true, run_id: 'r_fixture', created_at: '2026-11-15T00:00:00Z'}]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('Fixture direction'))
  await act(async () => {
    renderer!.root.findByProps({'data-testid': 'worker-open-graph'}).props.onClick()
    renderer!.root.findByProps({'data-testid': 'worker-open-run'}).props.onClick()
  })
  assert.deepEqual(opened, ['graph:in_fixture', 'run:r_fixture'])
})

test('report polling continues past the first refresh and finds a completed report despite server clock skew', async () => {
  let rows: any[] = [], runs: any[] = [], running = false, posts = 0
  api.reports = async () => rows
  api.runs = async () => runs
  api.reportPrecheck = async () => ({running, unverified: 0})
  api.createReport = async () => { posts++; return {queued: true, run_id: 'new-run'} }
  await act(async () => { renderer = create(React.createElement(Reports)) })
  const generate = () => renderer!.root.findAllByType('button').find(node => node.children.includes('生成报告'))!
  await act(async () => { await generate().props.onClick() })
  assert.equal(posts, 1)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已排队'))
  for (let i = 0; i < 3; i++) await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已排队'))
  running = true
  runs = [{id: 'new-run', purpose: 'report', status: 'running', started_at: '2020-01-01T00:00:00Z'}]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('生成中'))
  running = false
  runs = [{...runs[0], status: 'done'}]
  rows = [{id: 'new-report', run_id: 'new-run', status: 'ready', findings: 2, created_at: '2020-01-01T00:00:00Z'}]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('new-report'))
  assert.equal(generate().props.disabled, false)
})

test('report preview drops the old body when a report changes status or the operator opens another', async () => {
  let rows = ['report-a', 'report-b'].map(id => ({
    id, status: 'ready', findings: 0, created_at: '2026-11-20T00:00:00Z',
  }))
  let rejectBody!: (reason: unknown) => void
  const delayedBody = new Promise<string>((_, reject) => { rejectBody = reject })
  api.reports = async () => rows
  api.runs = async () => []
  api.reportPrecheck = async () => ({running: false, unverified: 0})
  api.reportFindings = async () => []
  api.reportFileText = async (id: string) => {
    if (id === 'report-b') return 'Body of report B'
    return rows[0].status === 'ready' ? 'Body of report A' : delayedBody
  }
  const text = () => JSON.stringify(renderer!.toJSON())
  const open = (id: string) => renderer!.root.findAllByType('button').find(node => node.children.includes(id))!.props.onClick()
  await act(async () => { renderer = create(React.createElement(Reports)) })
  await act(async () => { open('report-a') })
  assert.ok(text().includes('Body of report A'))

  rows = [{...rows[0], status: 'stale'}, rows[1]]
  await act(async () => { for (const tick of timers.values()) tick() })
  assert.ok(!text().includes('Body of report A'), 'stale preview still shows the previous body')
  await act(async () => { rejectBody(new ApiError(409, '报告已过期')) })
  assert.ok(text().includes('报告已过期'))

  await act(async () => { open('report-b') })
  assert.ok(text().includes('Body of report B'))
  assert.ok(!/Body of report A|报告已过期/.test(text()))
})

test('evidence preview reloads when the same ledger ID points to a different project file', async (t) => {
  const paths: string[] = []
  t.mock.method(globalThis, 'fetch', async (url: string) => {
    paths.push(url)
    return new Response(url === '/p/alpha/api/evidence/e_1' ? 'Alpha body' : 'Beta body')
  })
  const preview = (id: string) => {
    const contentURL = `/p/${id}/api/evidence/e_1`
    const file = {key: 'e_1', name: 'example.txt', uri: 'example.txt', contentURL, downloadURL: contentURL + '?download=1'}
    return React.createElement(EvidencePreview, {file, onOpenChange() {}})
  }
  await act(async () => { renderer = create(preview('alpha')) })
  assert.equal(renderer!.root.findByType('pre').children.join(''), 'Alpha body')
  await act(async () => { renderer!.update(preview('beta')) })
  assert.equal(renderer!.root.findByType('pre').children.join(''), 'Beta body')
  assert.deepEqual(paths, ['/p/alpha/api/evidence/e_1', '/p/beta/api/evidence/e_1'])
})

test('expired login during evidence preview keeps the selected view and record', async (t) => {
  const redirects: string[] = []
  const location = window.location
  const original = {...location}
  const next = '/p/alpha/?view=evidence&record=e_1#details'
  Object.assign(location, {search: '?view=evidence&record=e_1', hash: '#details', assign: (url: string) => redirects.push(url)})
  t.after(() => {
    Object.assign(location, original)
    Reflect.deleteProperty(location, 'assign')
  })
  t.mock.method(globalThis, 'fetch', async () => new Response(null, {status: 401}))
  const file = {key: 'e_1', name: 'example.txt', uri: 'example.txt', contentURL: '/e_1', downloadURL: '/e_1?download=1'}
  await act(async () => {
    renderer = create(React.createElement(EvidencePreview, {file, onOpenChange() {}}))
  })
  assert.equal(redirects.length, 1)
  const redirect = new URL(redirects[0], 'http://localhost')
  assert.equal(redirect.pathname, '/login')
  assert.equal(redirect.searchParams.get('next'), next)
})

test('single model gateway saves one key and connectivity runs only on explicit click', async () => {
 let saved:any={base_url:'http://gateway.test/v1',model:'route',provider:'openai',api_key_set:true,configured:true}
 let checks=0, writes:any[]=[]
 api.settings=async()=>saved
 api.projectIndex=async()=>({projects:[]})
 api.runtimeSettings=async()=>({auxiliary_concurrency:1,max_auxiliary_concurrency:8})
 api.saveSettings=async(body:any)=>{writes.push(body);saved={...saved,...body,api_key:undefined};return saved}
 api.checkModel=async()=>{checks++;return {available:true,returned_model:'actual-model'}}
 await act(async()=>{renderer=create(React.createElement(ConfirmProvider,null,React.createElement(SettingsPage)))})
 assert.equal(checks,0)
 assert.equal(renderer!.root.findAllByProps({'aria-label':'暂停使用 one'}).length,0)
 await act(async()=>{renderer!.root.findByProps({'aria-label':'模型名称'}).props.onChange({target:{value:'new-route'}})})
 const button=(label:string)=>renderer!.root.findAllByType('button').find(node=>node.children.includes(label))!
 assert.equal(button('检测连接').props.disabled,true)
 await act(async()=>{await button('保存并应用').props.onClick()})
 assert.equal(writes.length,1)
 assert.equal(writes[0].model,'new-route')
 assert.equal(writes[0].api_key,'')
 assert.deepEqual(Object.keys(writes[0]).sort(),['api_key','base_url','context_window','max_tokens','model','provider'])
 assert.equal(button('检测连接').props.disabled,false)
 await act(async()=>{await button('检测连接').props.onClick()})
 assert.equal(checks,1)
 assert.ok(JSON.stringify(renderer!.toJSON()).includes('actual-model'))
})

test('a failed precheck read stops generation and says 预检不可用 instead of generating', async () => {
  let posts = 0
  api.reports = async () => []
  api.runs = async () => []
  api.reportPrecheck = async () => { throw new Error('预检请求超时') }
  api.createReport = async () => { posts++; return {queued: true, run_id: 'new-run'} }
  await act(async () => { renderer = create(React.createElement(Reports)) })
  const generate = () => renderer!.root.findAllByType('button').find(node => node.children.includes('生成报告'))!
  await act(async () => { await generate().props.onClick() })
  const text = JSON.stringify(renderer!.toJSON())
  assert.equal(posts, 0, 'a failed precheck must not silently fall back to generating')
  assert.ok(text.includes('预检不可用'))
  assert.ok(text.includes('预检请求超时'))
  // 仍要生成 is the operator's explicit way through the missing check.
  const proceed = renderer!.root.findAllByType('button').find(node => node.children.includes('仍要生成'))!
  assert.ok(proceed, 'the unavailable branch must offer an explicit proceed')
  await act(async () => { await proceed.props.onClick() })
  assert.equal(posts, 1)
})

test('the precheck names the facts no report cites and the reports it could not count', async () => {
  api.reports = async () => []
  api.runs = async () => []
  api.reportPrecheck = async () => ({
    reportable: 3, unverified: 0, rejected: 0, running: false,
    unreported: {
      total: 2, limit: 20,
      items: [
        {fact: 'f_never', title: '示例：从未进过报告的结论'},
        {fact: 'f_also', title: '示例：另一条'},
      ],
      note: '只按 ready 报告算差',
      unreadable: ['rep_bad'],
      uncounted: ['rep_failed'],
    },
  })
  api.createReport = async () => ({queued: true, run_id: 'new-run'})
  await act(async () => { renderer = create(React.createElement(Reports)) })
  const text = JSON.stringify(renderer!.toJSON())
  assert.ok(text.includes('未进过报告的事实'))
  const link = renderer!.root.findAllByType('a').find(node => String(node.props.href).includes('fact=f_never'))
  assert.ok(link, 'an unreported fact row must carry its record address')
  assert.equal(link!.props.href, '?view=graph&fact=f_never')
  // A report that could not be read is named, not counted as zero.
  assert.ok(text.includes('rep_bad'))
  assert.ok(text.includes('rep_failed'))
})

test('a linked report opens from ?report=, so Back from a fact lands on that report', async () => {
  api.reports = async () => [{id: 'rep_live', status: 'ready', findings: 1, created_at: '2026-11-20T00:00:00Z'}]
  api.runs = async () => []
  api.reportPrecheck = async () => ({running: false, unverified: 0, reportable: 1, rejected: 0})
  api.reportFileText = async () => 'REPORT BODY'
  api.reportFindings = async () => [{fact: 'f_a', verdict: 'confirmed', title: '示例结论'}]
  const before = window.location.search
  window.location.search = '?view=reports&report=rep_live'
  try {
    await act(async () => { renderer = create(React.createElement(Reports)) })
    await act(async () => { for (const tick of timers.values()) tick() })
    const text = JSON.stringify(renderer!.toJSON())
    assert.ok(text.includes('REPORT BODY'), 'the linked report must open its preview')
    const row = renderer!.root.findAllByType('button').find(node => node.children.includes('rep_live'))!
    assert.equal(row.props['aria-expanded'], true, 'the list row must show which report is open')
  } finally {
    window.location.search = before
  }
})

test('a report id that is not in the list says so instead of opening the first report', async () => {
  const fetched: string[] = []
  api.reports = async () => [{id: 'rep_first', status: 'ready', findings: 1, created_at: '2026-11-20T00:00:00Z'}]
  api.runs = async () => []
  api.reportPrecheck = async () => ({running: false, unverified: 0, reportable: 1, rejected: 0})
  api.reportFileText = async (id: string) => { fetched.push(id); return 'FIRST BODY' }
  api.reportFindings = async () => []
  const before = window.location.search
  window.location.search = '?view=reports&report=rep_gone'
  try {
    await act(async () => { renderer = create(React.createElement(Reports)) })
    await act(async () => { for (const tick of timers.values()) tick() })
    const text = JSON.stringify(renderer!.toJSON())
    renderer!.root.findByProps({'data-testid': 'report-not-found'})
    assert.ok(text.includes('没有找到这份报告'))
    assert.ok(text.includes('rep_gone'))
    // A stale link must not swap in the first row or fetch another report's body.
    assert.deepEqual(fetched, [])
    assert.ok(!text.includes('FIRST BODY'))
    const row = renderer!.root.findAllByType('button').find(node => node.children.includes('rep_first'))!
    assert.equal(row.props['aria-expanded'], false)
  } finally {
    window.location.search = before
  }
})

// Explicit review-off mode still produces a report, with candidates kept unverified.
test('report generation can queue unverified candidates when reviews are disabled', async () => {
  let posts = 0
  api.reports = async () => []
  api.runs = async () => []
  api.reportPrecheck = async () => ({running: false, unverified: 2, reviews_disabled: true})
  api.createReport = async () => { posts++; return {queued: true, run_id: 'candidates-run'} }
  await act(async () => { renderer = create(React.createElement(Reports)) })
  const generate = renderer!.root.findAllByType('button').find(node => node.children.includes('生成报告'))!
  await act(async () => { await generate.props.onClick() })
  assert.equal(posts, 1)
  assert.match(JSON.stringify(renderer!.toJSON()), /已排队/)
})

// Candidates from an executor can already have an execution record. The detail
// must explain the source requirements without attributing them to the commander.
test('battlefield knowledge explains each candidate by its recorded origin', async () => {
  const candidate = {
    status: 'candidate', when: '示例条件', outcome: 'success', tags: [],
    created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
  }
  api.playbooksPage = async () => ({
    playbooks: [
      { ...candidate, id: 'pb_a', title: '执行者样例', origin: 'executor', from_fact_ids: ['f_a'] },
      { ...candidate, id: 'pb_b', title: '指挥官样例', origin: 'commander' },
    ],
    counts: { candidate: 2 }, total: 2,
  })
  await act(async () => {
    renderer = create(React.createElement(ConfirmProvider, null, React.createElement(Playbooks)))
  })
  const body = () => JSON.stringify(renderer!.toJSON())
  assert.match(body(), /执行者已声明这条知识/)
  assert.match(body(), /缺少原始证据登记/)
  const commander = renderer!.root.findAllByType('button').find(node =>
    node.findAllByType('span').some(span => span.children.includes('指挥官样例')))
  assert.ok(commander)
  await act(async () => { commander.props.onClick() })
  assert.match(body(), /指挥官提名，目前显示为候选/)
  assert.doesNotMatch(body(), /执行者已声明这条知识/)
})
