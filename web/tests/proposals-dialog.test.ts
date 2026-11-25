// The 提议 management dialog.
//
// The chain has two separate steps (approval creates a pending intent, dispatch
// starts the run), and every decision is a version compare-and-set: the dialog
// must send the row snapshot it rendered, refuse an unexplained rejection, keep
// the queue visible when the server answers 409, and not close mid-write.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import react from '@vitejs/plugin-react-swc'
import { createTestServer } from './helpers/vite.ts'

const uiStub = {
  name: 'stub-cyber-ui-proposals',
  enforce: 'pre' as const,
  resolveId(id: string) {
    if (id === '@cyber/ui') return '\0stub-cyber-ui-proposals'
    if (id === '@cyber/theme') return '\0stub-cyber-theme-proposals'
    return null
  },
  load(id: string) {
    if (id === '\0stub-cyber-theme-proposals') {
      return "export const cn = (...args) => args.flat().filter(Boolean).join(' ')"
    }
    if (id !== '\0stub-cyber-ui-proposals') return null
    return [
      "import React from 'react'",
      "export const Badge = ({ children }) => React.createElement('span', null, children)",
      "export const Button = ({ children, ...props }) => React.createElement('button', props, children)",
      "export const Callout = ({ children }) => React.createElement('div', null, children)",
      "export const Dialog = ({ open, onOpenChange, children }) => open ? React.createElement('div', { role: 'dialog' }, children, React.createElement('button', { onClick: () => onOpenChange ? onOpenChange(false) : undefined }, '关闭弹窗')) : null",
      "export const DialogContent = ({ children, ...props }) => React.createElement('div', props, children)",
      "export const DialogHeader = ({ children }) => React.createElement('div', null, children)",
      "export const DialogTitle = ({ children }) => React.createElement('h2', null, children)",
      "export const DialogDescription = ({ children }) => React.createElement('p', null, children)",
      "export const EmptyState = ({ title }) => React.createElement('div', null, title)",
      "export const Field = ({ label, children, error }) => React.createElement('div', null, React.createElement('span', null, label), children, error ? React.createElement('span', null, error) : null)",
      "export const SkeletonList = () => null",
      "export const Spinner = () => React.createElement('span', null, '…')",
      "export const Textarea = (props) => React.createElement('textarea', props)",
    ].join('\n')
  },
}

let server: Awaited<ReturnType<typeof createTestServer>>
let OperatorProposalsDialog: React.ComponentType<any>
let api: any
let ApiError: any
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createTestServer({
    configFile: false,
    root: fileURLToPath(new URL('..', import.meta.url)),
    plugins: [react(), uiStub],
    esbuild: false,
    resolve: {
      alias: [{ find: '@', replacement: fileURLToPath(new URL('../src', import.meta.url)) }],
      dedupe: ['react', 'react-dom', 'react/jsx-runtime', 'react/jsx-dev-runtime'],
    },
  })
  OperatorProposalsDialog = (await server.ssrLoadModule('/src/components/OperatorProposals.tsx')).OperatorProposalsDialog
  const mod = await server.ssrLoadModule('/src/lib/api.ts')
  api = mod.api
  ApiError = mod.ApiError
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
})

after(async () => { await server?.close() })

function row(over: Record<string, unknown> = {}) {
  return {
    id: 'pr_1',
    status: 'proposed',
    version: 3,
    title: '验证 /api/admin 的未授权读取',
    description: '确认 /api/admin 能否在未登录下读取，并保存最小复现',
    eval: '能复现一次未授权读取并留住响应原文',
    rationale: '上一轮已确认管理端可达',
    from_fact_ids: ['f_1'],
    from_participant_id: 'pt_in_a',
    from_intent_id: 'in_a',
    from_run_id: 'r_a',
    created_at: '2026-11-25T10:00:00+08:00',
    updated_at: '2026-11-25T10:00:00+08:00',
    ...over,
  }
}

const textOf = (node: any): string => {
  if (node == null) return ''
  if (typeof node === 'string') return node
  if (Array.isArray(node)) return node.map(textOf).join('')
  return textOf(node.children)
}
const pageText = (r: ReactTestRenderer) => textOf(r.toJSON())

function button(r: ReactTestRenderer, label: string) {
  const found = r.root.findAllByType('button').filter((node) => textOf(node.children).trim() === label)
  assert.ok(found.length > 0, `missing button ${label}`)
  return found[found.length - 1]
}

/** Busy labels sit next to an icon/Spinner, so match the label inside the
 *  rendered text instead of requiring the button's whole text to equal it. */
function buttonContaining(r: ReactTestRenderer, label: string) {
  const found = r.root.findAllByType('button').filter((node) => textOf(node.children).includes(label))
  assert.ok(found.length > 0, `missing button containing ${label}`)
  return found[found.length - 1]
}

function rowButton(r: ReactTestRenderer, id: string, label: string) {
  const node = r.root.findByProps({ 'data-proposal': id })
  const found = node.findAllByType('button').filter((b) => textOf(b.children).trim() === label)
  assert.ok(found.length > 0, `missing ${label} on ${id}`)
  return found[0]
}

function rowButtons(r: ReactTestRenderer, id: string) {
  return r.root.findByProps({ 'data-proposal': id }).findAllByType('button')
}

interface Events { closed: number; changed: number }

async function mount(rows: unknown[], events: Events): Promise<ReactTestRenderer> {
  api.proposals = async () => ({ proposals: rows, total: rows.length })
  await act(async () => {
    renderer = create(
      React.createElement(OperatorProposalsDialog, {
        onClose: () => { events.closed++ },
        onChanged: () => { events.changed++ },
      }),
    )
  })
  return renderer!
}

test('批准 sends the version and the row the operator read', async () => {
  const target = row({ id: 'pr_pending', version: 3 })
  const decided: unknown[][] = []
  api.approveProposal = async (...args: unknown[]) => {
    decided.push(args)
    return { proposal: { ...target, status: 'approved', version: 4, intent_id: 'in_new' }, intent_id: 'in_new' }
  }
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([target], events)

  await act(async () => {
    rowButton(r, 'pr_pending', '批准').props.onClick()
    await Promise.resolve()
  })
  assert.deepEqual(decided, [['pr_pending', { expected_version: 3 }]])
  assert.match(pageText(r), /已批准 v4/)
  assert.match(pageText(r), /in_new/)
  assert.match(pageText(r), /点「派发」才会起 run/)
  assert.equal(events.changed, 1, 'the sidebar count is refreshed from the same change')
})

test('派发 is only offered on an approved proposal and carries its own version', async () => {
  const approved = row({ id: 'pr_approved', status: 'approved', version: 5, intent_id: 'in_new' })
  const proposed = row({ id: 'pr_pending', version: 2 })
  const dispatched: unknown[][] = []
  api.dispatchProposal = async (...args: unknown[]) => {
    dispatched.push(args)
    return { proposal: { ...approved, status: 'dispatched', version: 6, run_id: 'r_new' }, intent_id: 'in_new', run_id: 'r_new' }
  }
  const r = await mount([approved, proposed], { closed: 0, changed: 0 })

  // The pending row can be approved/rejected, never dispatched; the approved row
  // offers dispatch and no approve/reject — the two steps do not collapse.
  assert.equal(rowButtons(r, 'pr_pending').filter((b) => textOf(b.children).trim() === '派发').length, 0)
  assert.equal(rowButtons(r, 'pr_approved').filter((b) => textOf(b.children).trim() === '批准').length, 0)
  await act(async () => {
    rowButton(r, 'pr_approved', '派发').props.onClick()
    await Promise.resolve()
  })
  assert.deepEqual(dispatched, [['pr_approved', { expected_version: 5 }]])
  assert.match(pageText(r), /run=r_new/)
})

test('驳回 requires a reason and does not call the API without one', async () => {
  let rejected: unknown[][] = []
  api.rejectProposal = async (...args: unknown[]) => {
    rejected.push(args)
    return { proposal: row({ status: 'rejected', version: 4 }) }
  }
  const r = await mount([row({ id: 'pr_reject', version: 3 })], { closed: 0, changed: 0 })

  await act(async () => { rowButton(r, 'pr_reject', '驳回').props.onClick() })
  const editor = r.root.findAll((node) => node.type === 'textarea' && node.props['aria-label'] === '驳回提议的理由')
  assert.equal(editor.length, 1, 'the rejection editor appears inline')
  await act(async () => { button(r, '确认驳回').props.onClick() })
  assert.equal(rejected.length, 0, 'an unexplained rejection must not reach the server')
  assert.match(pageText(r), /写一条驳回理由再提交。/)

  await act(async () => { editor[0].props.onChange({ target: { value: '  与现有意图重叠  ' } }) })
  await act(async () => {
    button(r, '确认驳回').props.onClick()
    await Promise.resolve()
  })
  assert.deepEqual(rejected, [['pr_reject', { expected_version: 3, reason: '与现有意图重叠' }]])
  assert.match(pageText(r), /已驳回 v4/)
})

test('a 409 keeps the queue and names the version/state conflict', async () => {
  api.approveProposal = async () => {
    throw new ApiError(409, '提议状态或版本已变化', 'proposal_conflict')
  }
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([row({ id: 'pr_stale', version: 2 })], events)

  await act(async () => {
    rowButton(r, 'pr_stale', '批准').props.onClick()
    await Promise.resolve()
  })
  assert.match(pageText(r), /已被其他窗口处理（版本或状态已变）：刷新后按现状处理。/)
  assert.equal(r.root.findAllByProps({ 'data-proposal': 'pr_stale' }).length > 0, true, 'the row stays visible')
  assert.equal(events.changed, 0, 'a refused decision must not report success upward')
})

test('a decision in flight counts as busy, so the dialog cannot be closed mid-write', async () => {
  let release: (value: unknown) => void = () => {}
  api.approveProposal = () => new Promise((resolve) => { release = resolve })
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([row({ id: 'pr_busy', version: 3 })], events)

  await act(async () => {
    rowButton(r, 'pr_busy', '批准').props.onClick()
    await Promise.resolve()
  })
  const content = r.root.findByProps({ 'data-testid': 'operator-proposals-dialog' })
  assert.equal(content.props['aria-busy'], true)
  assert.equal(buttonContaining(r, '批准中…').props.disabled, true, 'a second click must not double-decide')
  await act(async () => { button(r, '关闭弹窗').props.onClick() })
  assert.equal(events.closed, 0, 'closing is refused while the decision is in flight')

  await act(async () => {
    release({ proposal: row({ id: 'pr_busy', status: 'approved', version: 4, intent_id: 'in_new' }), intent_id: 'in_new' })
    await Promise.resolve()
    await Promise.resolve()
  })
  assert.equal(
    r.root.findByProps({ 'data-testid': 'operator-proposals-dialog' }).props['aria-busy'],
    false,
  )
  await act(async () => { button(r, '关闭弹窗').props.onClick() })
  assert.equal(events.closed, 1, 'once the decision settles the dialog closes normally')
  assert.equal(events.changed, 1)
})
