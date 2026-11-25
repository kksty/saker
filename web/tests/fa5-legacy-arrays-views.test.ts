// FA5 (task-17), view half: the inline `[...x].sort(...)` / `[...x].reverse()`
// expressions on the data-dense pages became `toSorted()` / `toReversed()`.
//
// Rendering the real view is the only way to observe those inline expressions,
// so each test renders the page with a mocked API payload and asserts:
//   - the rendered row order is exactly what the old copy-then-sort produced;
//   - the API payload array the component received is still in its original
//     order afterwards (the component only ever owns a sorted copy).
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let renderer: ReactTestRenderer | undefined
let EvidenceView: any, ExternalActions: any, Workers: any, Overview: any, ProjectState: any, ConfirmProvider: any, api: any
const timers = new Map<number, () => void>()
const storage = new Map<string, string>()
let timerID = 0
const oldWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const oldDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const oldFormElement = Object.getOwnPropertyDescriptor(globalThis, 'HTMLFormElement')

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  EvidenceView = (await server.ssrLoadModule('/src/views/Evidence.tsx')).EvidenceView
  ExternalActions = (await server.ssrLoadModule('/src/views/ExternalActions.tsx')).ExternalActions
  Workers = (await server.ssrLoadModule('/src/views/Workers.tsx')).Workers
  Overview = (await server.ssrLoadModule('/src/views/Overview.tsx')).Overview
  ProjectState = (await server.ssrLoadModule('/src/views/ProjectState.tsx')).ProjectState
  ConfirmProvider = (await server.ssrLoadModule('@cyber/ui')).ConfirmProvider
  api = (await server.ssrLoadModule('/src/lib/api.ts')).api
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/p/alpha/', search: '', hash: '', origin: 'http://console.test', assign() {} },
      history: { replaceState() {}, pushState() {} },
      sessionStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => void storage.set(key, value),
      },
      setInterval: (fn: () => void) => { timers.set(++timerID, fn); return timerID },
      clearInterval: (id: number) => void timers.delete(id),
      setTimeout, clearTimeout,
      matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
      addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
    },
  })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      title: '',
      visibilityState: 'visible',
      addEventListener() {}, removeEventListener() {},
    },
  })
  Object.defineProperty(globalThis, 'HTMLFormElement', { configurable: true, value: class {} })
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
  timers.clear()
  storage.clear()
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

/** Let every mounted resource resolve and run one polling tick. */
async function settle() {
  for (let i = 0; i < 3; i++) {
    await act(async () => { await Promise.resolve() })
  }
  await act(async () => { for (const tick of timers.values()) tick() })
  for (let i = 0; i < 3; i++) {
    await act(async () => { await Promise.resolve() })
  }
}

async function mount(element: React.ReactElement) {
  await act(async () => { renderer = create(element) })
  await settle()
}

/** Concatenated text of the rendered tree (props are skipped on purpose). */
function textContent(node: any): string {
  if (node == null) return ''
  if (typeof node === 'string') return node
  if (Array.isArray(node)) return node.map(textContent).join('')
  if (typeof node === 'object') return textContent(node.children)
  return ''
}

function pageText(): string {
  return textContent(renderer!.toJSON())
}

/** Index-of order for unique marker strings across the rendered tree. */
function assertRenderedOrder(markers: string[]) {
  const text = pageText()
  const positions = markers.map((marker) => {
    const at = text.indexOf(marker)
    assert.notEqual(at, -1, `expected "${marker}" to be rendered`)
    return at
  })
  assert.deepEqual(
    [...positions].sort((a, b) => a - b),
    positions,
    `rendered order must be ${markers.join(' < ')}`,
  )
}

function ids(rows: readonly { id: string }[]): string[] {
  return rows.map((row) => row.id)
}

// ---------------------------------------------------------------- Evidence ----

test('Evidence rows render newest-first from an unsorted API payload and the payload stays untouched', async () => {
  const evidence = [
    { id: 'e_old', run_id: 'r_1', intent_id: 'in_1', name: 'EVID_OLD', size: 1, uri: 'p/old.txt', created_at: '2026-11-10T01:00:00Z' },
    { id: 'e_new', run_id: 'r_1', intent_id: 'in_1', name: 'EVID_NEW', size: 2, uri: 'p/new.txt', created_at: '2026-11-10T03:00:00Z' },
    { id: 'e_mid', run_id: 'r_1', intent_id: 'in_1', name: 'EVID_MID', size: 3, uri: 'p/mid.txt', created_at: '2026-11-10T02:00:00Z' },
  ]
  const before = ids(evidence)
  api.evidence = async () => evidence
  api.evidenceDownloadURL = (id: string) => `/download/${id}`
  await mount(React.createElement(EvidenceView))
  const list = renderer!.root.findAllByType('ul').find((node: any) => node.props['aria-label'] === '证据文件')
  assert.ok(list, 'evidence list must render')
  assert.equal(list.findAllByType('li').length, 3)
  assertRenderedOrder(['EVID_NEW', 'EVID_MID', 'EVID_OLD'])
  assert.deepEqual(ids(evidence), before, 'the API payload must not be reordered in place')
})

// ---------------------------------------------------------- ExternalActions ----

test('ExternalActions renders the payload reversed and the payload stays untouched', async () => {
  const action = (id: string, name: string): any => ({
    id, name, intent_id: 'in_1', run_id: 'r_1', state: 'completed', version: 1,
    created_at: '2026-11-10T01:00:00Z', updated_at: '2026-11-10T01:00:00Z',
  })
  const actions = [action('a1', 'ACT_FIRST'), action('a2', 'ACT_SECOND'), action('a3', 'ACT_THIRD')]
  const before = ids(actions)
  api.externalActions = async () => ({ actions })
  await mount(React.createElement(ExternalActions))
  const list = renderer!.root.findAllByType('ul').find((node: any) => node.props['aria-label'] === '外部动作')
  assert.ok(list, 'external action list must render')
  assert.equal(list.findAllByType('li').length, 3)
  assertRenderedOrder(['ACT_THIRD', 'ACT_SECOND', 'ACT_FIRST'])
  assert.deepEqual(ids(actions), before, 'the API payload must not be reversed in place')
})

// ----------------------------------------------------------------- Workers ----

test('Workers ranks running first, newest second, and never reorders the payload', async () => {
  const worker = (id: string, status: string, created: string): any => ({
    intent_id: id,
    participant_id: `pt_${id}`,
    title: `W_${id.toUpperCase()}`,
    status,
    running: status === 'running',
    can_kill: status === 'running',
    run_id: `r_${id}`,
    created_at: created,
  })
  const workers = [
    worker('w_done', 'done', '2026-11-10T04:00:00Z'),
    worker('w_run_old', 'running', '2026-11-10T01:00:00Z'),
    worker('w_run_new', 'running', '2026-11-10T02:00:00Z'),
    worker('w_failed', 'failed', '2026-11-10T03:00:00Z'),
  ]
  const before = workers.map((row) => row.intent_id)
  api.workers = async () => ({ workers, count: workers.length })
  api.peers = async () => ({ mode: 'open', peer_policy: { mode: 'open', allow: [] } })
  api.participants = async () => []
  await mount(React.createElement(ConfirmProvider, null, React.createElement(Workers, {})))
  const list = renderer!.root.findAllByType('ul').find((node: any) => node.props['aria-label'] === '执行者名单')
  assert.ok(list, 'worker roster must render')
  assert.equal(list.findAllByType('li').length, 4)
  assertRenderedOrder(['W_W_RUN_NEW', 'W_W_RUN_OLD', 'W_W_FAILED', 'W_W_DONE'])
  assert.deepEqual(workers.map((row) => row.intent_id), before, 'the API payload must not be sorted in place')
})

// ------------------------------------------------------------- ProjectState ----

function boardFixture(over: Record<string, unknown> = {}): any {
  return {
    generated_at: '2026-11-10T12:00:00Z',
    project_paused: false,
    digest: '',
    decider: { enabled: false, paused: false, phase: 'idle', tokens_total: 0 },
    intents: { total: 0, pending: 0, running: 0, done: 0, waiting: 0, held: 0 },
    facts: { total: 0, verified: 0, with_evidence: 0, reviewable_unverified: 0, notes: 0 },
    runs: { total: 0, running: 0, done: 0, failed: 0, canceled: 0 },
    goals: { total: 0, met: 0 },
    evidence: 0,
    playbooks: { confirmed: 0, candidates: 0, retired: 0, archived: 0 },
    frontier: [], waiting_intents: [], recent_facts: [], running_intents: [], ports: [], coverage: [],
    handoff_warnings: [],
    ...over,
  }
}

const intentRow = (id: string, status: string, created: string, updated: string, over: Record<string, unknown> = {}): any => ({
  id,
  title: `INTENT_${id.toUpperCase()}`,
  description: '',
  status,
  attempts: 1,
  created_at: created,
  updated_at: updated,
  ...over,
})

const runRow = (id: string, intent_id: string, started: string, over: Record<string, unknown> = {}): any => ({
  id,
  intent_id,
  node: 'local',
  status: 'done',
  events: 0,
  declared_facts: 0,
  started_at: started,
  ...over,
})

test('ProjectState roster keeps live (newest updated) before queued (oldest created) and attempts keep chronological order', async () => {
  const intents = [
    intentRow('live_new', 'running', '2026-11-10T01:00:00Z', '2026-11-10T03:00:00Z'),
    intentRow('queued_late', 'pending', '2026-11-10T02:00:00Z', '2026-11-10T02:00:00Z'),
    intentRow('live_old', 'running', '2026-11-10T01:00:00Z', '2026-11-10T02:00:00Z'),
    intentRow('queued_early', 'pending', '2026-11-10T01:00:00Z', '2026-11-10T01:00:00Z'),
  ]
  // live_new has two attempts, listed newest-first: the chronological sort must
  // pick r_late as "latest" (tokens 999), not the array order (r_early, 111).
  const runs = [
    runRow('r_late', 'live_new', '2026-11-10T05:00:00Z', { tokens: 999 }),
    runRow('r_early', 'live_new', '2026-11-10T04:00:00Z', { tokens: 111 }),
    runRow('r_only_old', 'live_old', '2026-11-10T04:30:00Z', { tokens: 5 }),
  ]
  const intentBefore = ids(intents)
  const runBefore = ids(runs)
  api.board = async () => boardFixture()
  api.intents = async () => intents
  api.runs = async () => runs
  api.intentReadiness = async () => null
  api.runContext = async () => null
  api.events = async () => []
  // One direction's detail reads its own facts from /api/intents/{id}; this is the
  // same shape the server returns (facts_total included).
  api.intent = async (id: string) => ({
    intent: intents.find((row) => row.id === id),
    facts: [
      {
        id: 'f_live', intent_id: id, kind: 'host', title: 'FACT_LIVE', summary: '',
        confidence: 'observed', status: 'verified', claim_version: 2,
        evidence_ids: ['e_1'], created_at: '2026-11-10T05:00:00Z',
      },
    ],
    facts_total: 1,
  })
  await mount(React.createElement(ConfirmProvider, null, React.createElement(ProjectState, {})))
  const roster = renderer!.root.findByProps({ 'data-testid': 'exec-roster' })
  const rows = roster
    .findAll((node: any) => typeof node.type === 'string' && typeof node.props?.['data-testid'] === 'string' && node.props['data-testid'].startsWith('intent-tab-'))
    .map((node: any) => node.props['data-testid'] as string)
  // Roster groups: live (updated_at desc), then queued (created_at asc).
  assert.deepEqual(rows, ['intent-tab-live_new', 'intent-tab-live_old', 'intent-tab-queued_early', 'intent-tab-queued_late'])
  // The selected/latest attempt is chronological, not API-array order.
  const latestText = pageText()
  assert.match(latestText, /999 tok/, 'the later-started attempt must be treated as latest')
  assert.doesNotMatch(latestText, /111 tok/)
  // 历次运行 and 关联证据 belong to the page, not to a disclosure: opening one
  // panel must not be the only way to see that a direction was retried or what
  // it declared.
  assert.match(latestText, /历次运行/)
  assert.match(latestText, /关联证据/)
  assert.match(latestText, /FACT_LIVE/)
  assert.match(latestText, /已复核/)
  const more = renderer!.root.findByProps({ 'data-testid': 'detail-more' })
  assert.equal(
    more.findAll((node: any) => String(node.props?.['data-testid'] ?? '').startsWith('attempt-tab-')).length,
    0,
    'the attempt chain must not sit inside the folded panel',
  )
  assert.deepEqual(ids(intents), intentBefore, 'the intent payload must not be reordered in place')
  assert.deepEqual(ids(runs), runBefore, 'the run payload must not be reordered in place')
})

test('a settled direction says who declared it, in the roster and in the detail', async () => {
  const settled = intentRow('settled_one', 'done', '2026-11-10T01:00:00Z', '2026-11-10T06:00:00Z', {
    done_declared_by: 'commander',
    done_note: '执行者已拿到 shell，run 收口失败但证据齐全',
    done_fact_ids: ['f_shell'],
    done_run_ids: ['r_9f2'],
    done_at: '2026-11-10T06:00:00Z',
  })
  const runs = [runRow('r_9f2', 'settled_one', '2026-11-10T05:00:00Z', { status: 'failed' })]
  api.board = async () => boardFixture()
  api.intents = async () => [settled]
  api.runs = async () => runs
  api.intentReadiness = async () => null
  api.runContext = async () => null
  api.events = async () => []
  api.intent = async () => ({ intent: settled, facts: [], facts_total: 0 })
  await mount(React.createElement(ConfirmProvider, null, React.createElement(ProjectState, {})))
  assert.match(pageText(), /指挥官判定/, 'the roster line must not read as a run-produced 完成')
  const row = renderer!.root.findByProps({ 'data-testid': 'intent-tab-settled_one' })
  await act(async () => { row.props.onClick?.({}) })
  await settle()
  const text = pageText()
  assert.match(text, /指挥官判定完成/)
  const badge = renderer!.root.findAll((node: any) =>
    typeof node.type === 'string' && String(node.props?.title ?? '').includes('说明：执行者已拿到 shell')).at(-1)
  assert.ok(badge, 'hover text names the settler note and its evidence')
  assert.match(String(badge.props.title), /证据事实：f_shell/)
  assert.match(String(badge.props.title), /证据运行：r_9f2/)
  // A run-produced completion must not grow the extra chip: exactly one badge in
  // the detail panel carries the settled provenance.
  const detail = renderer!.root.findByProps({ 'data-testid': 'exec-detail' })
  assert.equal(
    detail.findAll((node: any) => typeof node.type === 'string' && String(node.props?.title ?? '').startsWith('说明：执行者')).length,
    1,
  )
})

test('a canceled run says its category once and gives the operator words the full width', async () => {
  const stopped = intentRow('stopped_one', 'failed', '2026-11-10T01:00:00Z', '2026-11-10T05:00:00Z', {
    hold: true,
    held_by: 'commander',
    hold_reason: '止损',
  })
  const runs = [runRow('r_stop', 'stopped_one', '2026-11-10T04:00:00Z', {
    status: 'canceled',
    cancel_cause: 'stop_loss',
    ended_at: '2026-11-10T04:30:00Z',
    exit_reason:
      '止损：操作员指令：白盒优先（本来就是想复现）。先停黑盒摸索，等白盒给出各级 file:line 判定',
  })]
  api.board = async () => boardFixture()
  api.intents = async () => [stopped]
  api.runs = async () => runs
  api.intentReadiness = async () => null
  api.runContext = async () => null
  api.events = async () => []
  api.intent = async () => ({ intent: stopped, facts: [], facts_total: 0 })
  await mount(React.createElement(ConfirmProvider, null, React.createElement(ProjectState, {})))
  const row = renderer!.root.findByProps({ 'data-testid': 'intent-tab-stopped_one' })
  await act(async () => { row.props.onClick?.({}) })
  await settle()
  const text = pageText()
  assert.match(text, /取消原因/, 'the row keeps its own label')
  assert.match(text, /操作员指令：白盒优先（本来就是想复现）/, "the operator's words are shown verbatim")
  assert.doesNotMatch(text, /止损：/, 'the category is not repeated inside the reason')
  const body = renderer!.root.findAll((node: any) =>
    typeof node.type === 'string' &&
    String(node.props?.className ?? '').includes('tw-break') &&
    String(node.props?.title ?? '').includes('止损')).at(-1)
  assert.ok(body, 'the cancel reason body is rendered')
  assert.doesNotMatch(String(body.props.className), /flex-1/, 'the reason must not share its row with the label')
})

// ----------------------------------------------------------------- Overview ----
test('Overview distinguishes parked, held, failed, and prerequisite blockers', async () => {
  api.goals = async () => []
  api.runs = async () => []
  api.nodes = async () => ({ nodes: [], project_default: '' })
  api.operatorInbox = async () => ({ total: 0 })
  api.usageBreakdown = async () => null
  const rows = [
    { id: 'wait', title: 'Awaiting verdict', status: 'pending', ready: false,
      premise_issues: [{ fact_id: 'f_review', reason: '断言尚未通过独立核验' }] },
    { id: 'parked', title: 'Parked direction', status: 'parked', ready: false, held: true,
      premise_issues: [{ fact_id: 'f_missing', reason: '前提缺少保全的原始证据' }] },
    { id: 'held', title: 'Held direction', status: 'pending', ready: false, held: true, no_premise: true },
    { id: 'last_failed', title: 'Failed direction', status: 'failed', ready: false, last_stop: 'error' },
  ]
  await mount(React.createElement(ConfirmProvider, null, React.createElement(Overview, {
    current: 'alpha',
    projects: [],
    board: boardFixture({
      intents: { ...boardFixture().intents, total: rows.length, pending: 2, held: 1, parked: 1 },
      frontier: [{ id: 'legacy', title: 'Legacy fallback', ready: true }],
      operator_frontier: rows,
    }),
    onRefresh() {},
    onOpenNode() {},
  })))
  const text = pageText()
  assert.match(text, /已停放Parked direction.*f_missing.*缺少保全的原始证据/)
  assert.match(text, /前提未就绪Awaiting verdict.*f_review.*尚未通过独立核验/)
  assert.match(text, /已挂起Held direction无前提/)
  assert.match(text, /上轮失败Failed direction上轮 error/)
  assert.doesNotMatch(text, /Legacy fallback/)
})

test('Overview renders open goals and recent runs in the migrated order without touching the payloads', async () => {
  const goals = [
    { id: 'g_met', statement: 'GOAL_MET', met: true, created_at: '2026-11-10T01:00:00Z' },
    { id: 'g_open', statement: 'GOAL_OPEN', met: false, created_at: '2026-11-10T02:00:00Z' },
  ]
  const books = [
    { id: 'b_retired', title: 'BOOK_RETIRED', when: 'w', origin: 'executor', status: 'retired', created_at: '2026-11-10T01:00:00Z', updated_at: '2026-11-10T01:00:00Z' },
    { id: 'b_candidate', title: 'BOOK_CANDIDATE', when: 'w', origin: 'commander', status: 'candidate', created_at: '2026-11-10T01:00:00Z', updated_at: '2026-11-10T01:00:00Z' },
    { id: 'b_confirmed', title: 'BOOK_CONFIRMED', when: 'w', origin: 'executor', status: 'confirmed', created_at: '2026-11-10T01:00:00Z', updated_at: '2026-11-10T01:00:00Z' },
  ]
  const runs = [
    runRow('run-mid', 'in_1', '2026-11-10T02:00:00Z', { tokens: 20 }),
    runRow('run-old', 'in_1', '2026-11-10T01:00:00Z', { tokens: 10 }),
    runRow('run-new', 'in_1', '2026-11-10T03:00:00Z', { tokens: 30 }),
  ]
  const goalBefore = ids(goals)
  const bookBefore = ids(books)
  const runBefore = ids(runs)
  api.goals = async () => goals
  api.runs = async () => runs
  api.nodes = async () => ({ nodes: [], project_default: '' })
  api.playbooksPage = async () => ({ playbooks: books, count: books.length })
  api.decisions = async () => []
  api.state = async () => ({ state: '' })
  api.operatorInbox = async () => ({ total: 0 })
  api.intents = async () => []
  await mount(React.createElement(ConfirmProvider, null, React.createElement(Overview, {
    current: 'alpha',
    projects: [],
    board: boardFixture(),
    onRefresh() {},
  })))
  // Open goals first (met=false), then met ones — the old sort key is unchanged.
  assertRenderedOrder(['GOAL_OPEN', 'GOAL_MET'])
  // The playbook summary is not part of the overview anymore: it is reference
  // material and lives on 战场知识. Keeping it here would re-add a block that
  // never changes the operator's next action.
  assert.ok(!pageText().includes('BOOK_CONFIRMED'), 'knowledge panel must stay off the overview')
  // Recent runs: newest started_at first, capped at 5 (the table shows id.slice(-6)).
  assertRenderedOrder(['un-new', 'un-mid', 'un-old'])
  assert.deepEqual(ids(goals), goalBefore, 'the goal payload must not be sorted in place')
  assert.deepEqual(ids(books), bookBefore, 'the playbook payload must not be sorted in place')
  assert.deepEqual(ids(runs), runBefore, 'the run payload must not be sorted in place')
})
test('失败运行 is a chip in the status row, not a 26px metric tile', async () => {
  api.goals = async () => []
  api.runs = async () => []
  api.nodes = async () => ({ nodes: [], project_default: '' })
  api.playbooksPage = async () => ({ playbooks: [], count: 0 })
  api.decisions = async () => []
  api.state = async () => ({ state: '' })
  api.operatorInbox = async () => ({ total: 0 })
  api.intents = async () => []
  await mount(React.createElement(ConfirmProvider, null, React.createElement(Overview, {
    current: 'alpha',
    projects: [],
    board: boardFixture({
      runs: { total: 341, running: 0, done: 332, failed: 9, canceled: 0 },
      intents: { total: 57, pending: 7, running: 0, done: 43, waiting: 0, held: 1, parked: 6 },
      facts: { total: 303, verified: 51, with_evidence: 260, reviewable_unverified: 12, notes: 4 },
      goals: { total: 2, met: 1 },
    }),
    onRefresh() {},
  })))
  const status = renderer!.root.findByProps({ 'data-testid': 'overview-status' })
  const chip = status.findByProps({ 'data-testid': 'overview-failed-runs' })
  assert.equal(typeof chip.props.onClick, 'function', 'the count has to open the run list')
  assert.match(pageText(), /失败运行\s*9/)
  // The big tile is gone: the block it used to occupy pushed 阻塞原因 off the
  // first screen, so the number now sits on the status line as a label.
  const metrics = renderer!.root.findByProps({ 'aria-label': '关键指标' })
  const metricsText = metrics.findAll((node: any) => typeof node.children?.[0] === 'string')
    .map((node: any) => node.children.join('')).join('')
  assert.doesNotMatch(metricsText, /失败运行/, '关键指标 must not carry the failed tile anymore')
})
