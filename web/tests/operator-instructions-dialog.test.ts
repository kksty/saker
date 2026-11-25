// The 持续指令 management dialog's inline body edit.
//
// The version compare-and-set is the point: the server refuses a stale write
// (409) instead of overwriting a revision another window wrote, so the dialog
// must send the row snapshot it showed when 编辑 was clicked, keep the draft
// when the server refuses, and discard it on 取消. A revoked row must not offer
// the editor at all — reviving it is a create decision, not a body change.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import react from '@vitejs/plugin-react-swc'
import { createTestServer } from './helpers/vite.ts'

// @cyber/ui's Dialog is a Radix portal and needs a DOM; the surface only uses
// plain primitives, so the stub renders native elements and exposes the close
// control Dialog's onOpenChange would call. `@cyber/theme` is stubbed too
// because Page.tsx (imported for Panel) pulls `cn` from it, and the real
// package is source-only under `configFile: false`.
const uiStub = {
  name: 'stub-cyber-ui-instructions',
  enforce: 'pre' as const,
  resolveId(id: string) {
    if (id === '@cyber/ui') return '\0stub-cyber-ui-instructions'
    if (id === '@cyber/theme') return '\0stub-cyber-theme-instructions'
    return null
  },
  load(id: string) {
    if (id === '\0stub-cyber-theme-instructions') {
      return "export const cn = (...args) => args.flat().filter(Boolean).join(' ')"
    }
    if (id !== '\0stub-cyber-ui-instructions') return null
    return [
      "import React from 'react'",
      "export const Badge = ({ children }) => React.createElement('span', null, children)",
      "export const Button = ({ children, variant, size, ...props }) => React.createElement('button', props, children)",
      "export const Callout = ({ children }) => React.createElement('div', null, children)",
      "export const Dialog = ({ open, onOpenChange, children }) => open ? React.createElement('div', { role: 'dialog' }, children, React.createElement('button', { onClick: () => onOpenChange ? onOpenChange(false) : undefined }, '关闭弹窗')) : null",
      "export const DialogContent = ({ children, ...props }) => React.createElement('div', props, children)",
      "export const DialogHeader = ({ children }) => React.createElement('div', null, children)",
      "export const DialogTitle = ({ children }) => React.createElement('h2', null, children)",
      "export const DialogDescription = ({ children }) => React.createElement('p', null, children)",
      "export const EmptyState = ({ title }) => React.createElement('div', null, title)",
      "export const Field = ({ label, error, children }) => React.createElement('div', null, React.createElement('span', null, label), children, error ? React.createElement('span', null, error) : null)",
      "export const Select = ({ children }) => React.createElement('div', null, children)",
      "export const SelectContent = ({ children }) => React.createElement('div', null, children)",
      "export const SelectItem = ({ children }) => React.createElement('div', null, children)",
      "export const SelectTrigger = ({ children }) => React.createElement('div', null, children)",
      "export const SelectValue = () => null",
      "export const SkeletonList = () => null",
      "export const Spinner = () => React.createElement('span', null, '…')",
      "export const Textarea = (props) => React.createElement('textarea', props)",
      "export const ToggleGroup = ({ children }) => React.createElement('div', null, children)",
      "export const ToggleGroupItem = ({ children }) => React.createElement('button', { type: 'button' }, children)",
    ].join('\n')
  },
}

let server: Awaited<ReturnType<typeof createTestServer>>
let OperatorInstructionsDialog: React.ComponentType<any>
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
  OperatorInstructionsDialog = (await server.ssrLoadModule('/src/components/OperatorInstructions.tsx')).OperatorInstructionsDialog
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
    id: 'instr_1',
    text: '整个项目只读优先',
    version: 3,
    state: 'active',
    created_by: 'operator',
    created_at: '2026-11-21T21:00:00+08:00',
    updated_at: '2026-11-21T21:00:00+08:00',
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

function rowButtons(r: ReactTestRenderer, id: string) {
  return r.root.findByProps({ 'data-instruction': id }).findAllByType('button')
}

/** The inline editor is the textarea the surface labels for it (the create form
 *  has its own textarea, so locate by the edit label instead of by position). */
function editors(r: ReactTestRenderer) {
  return r.root.findAll((node) => node.type === 'textarea' && node.props['aria-label'] === '修改持续指令正文')
}
function editor(r: ReactTestRenderer) {
  const found = editors(r)
  assert.equal(found.length, 1, 'expected exactly one inline editor')
  return found[0]
}

interface Events { closed: number; changed: number }

async function mount(rows: unknown[], events: Events, stub: { instructions?: unknown[] } = {}): Promise<ReactTestRenderer> {
  const visible = stub.instructions ?? rows
  api.instructions = async () => ({ instructions: visible })
  api.intents = async () => []
  api.instructionHistory = async () => ({ history: [] })
  await act(async () => {
    renderer = create(
      React.createElement(OperatorInstructionsDialog, {
        onClose: () => { events.closed++ },
        onChanged: () => { events.changed++ },
      }),
    )
  })
  return renderer!
}

test('saving an edit sends the version and state the row had when 编辑 was clicked', async () => {
  const target = row({ id: 'instr_7', version: 4, text: '只读优先' })
  const updates: unknown[][] = []
  api.updateInstruction = async (...args: unknown[]) => {
    updates.push(args)
    return row({ id: 'instr_7', version: 5, text: '只读与最小改动' })
  }
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([target], events)

  await act(async () => { button(r, '编辑').props.onClick() })
  const editorNode = editor(r)
  assert.equal(editorNode.props.value, '只读优先', 'the editor starts from the stored body')
  // Trimming back to the stored body is not a change: the save stays disabled.
  assert.equal(button(r, '保存修改').props.disabled, true)
  await act(async () => { editorNode.props.onChange({ target: { value: '  只读优先  ' } }) })
  assert.equal(button(r, '保存修改').props.disabled, true, 'a trimmed no-op must not save')
  await act(async () => { editorNode.props.onChange({ target: { value: '  只读与最小改动  ' } }) })
  assert.equal(button(r, '保存修改').props.disabled, false)

  await act(async () => {
    button(r, '保存修改').props.onClick()
    await Promise.resolve()
  })
  assert.deepEqual(
    updates,
    [['instr_7', { version: 4, text: '只读与最小改动', state: 'active' }]],
    'the CAS must be the row snapshot from the click, with its own state',
  )
  assert.equal(events.changed, 1, 'the sidebar count is refreshed from the same change')
  assert.equal(editors(r).length, 0, 'success closes the editor')
  assert.match(pageText(r), /已修订至 v5，指挥官下一轮可见。/)
})

test('取消 discards the draft so reopening the editor shows the stored body', async () => {
  let updates = 0
  api.updateInstruction = async () => { updates++; return row() }
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([row({ id: 'instr_1', text: '整个项目只读优先' })], events)

  await act(async () => { button(r, '编辑').props.onClick() })
  await act(async () => {
    editor(r).props.onChange({ target: { value: '改到一半的正文' } })
  })
  await act(async () => { button(r, '取消').props.onClick() })

  assert.equal(updates, 0, '取消 must not write anything')
  assert.equal(editors(r).length, 0)
  assert.match(pageText(r), /整个项目只读优先/, 'the stored body stays on the row')
  assert.doesNotMatch(pageText(r), /改到一半的正文/)

  await act(async () => { button(r, '编辑').props.onClick() })
  assert.equal(
    editor(r).props.value,
    '整个项目只读优先',
    'the discarded draft is not resurrected',
  )
})

test('a 409 keeps the editor and the draft, and reports the version conflict', async () => {
  api.updateInstruction = async () => { throw new ApiError(409, '指令版本或幂等键冲突') }
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([row({ id: 'instr_3', version: 2, text: '旧正文' })], events)

  await act(async () => { button(r, '编辑').props.onClick() })
  await act(async () => {
    editor(r).props.onChange({ target: { value: '我改的新正文' } })
  })
  await act(async () => {
    button(r, '保存修改').props.onClick()
    await Promise.resolve()
  })

  const editorNode = editor(r)
  assert.equal(editorNode.props.value, '我改的新正文', 'the typed body survives the refusal')
  assert.equal(button(r, '保存修改').props.disabled, false, 'the operator can retry or cancel')
  assert.match(pageText(r), /正文已被其他窗口改过（版本冲突）：刷新后再改。/)
  assert.equal(events.changed, 0, 'a refused edit must not report success upward')
  assert.equal(editors(r).length, 1)
})

test('a revoked row offers no edit, so the dialog cannot silently revive it', async () => {
  const r = await mount(
    [
      row({ id: 'instr_active', text: '生效中的正文' }),
      row({ id: 'instr_revoked', state: 'revoked', version: 2, text: '已撤销的正文' }),
    ],
    { closed: 0, changed: 0 },
  )
  // Revoked rows are hidden by default; show the history group before asserting
  // that the editor is still absent from it.
  await act(async () => { button(r, '显示已撤销').props.onClick() })
  const editButtons = (id: string) => rowButtons(r, id).filter((node) => textOf(node.children).trim() === '编辑')
  assert.equal(editButtons('instr_active').length, 1)
  assert.equal(editButtons('instr_revoked').length, 0)
  assert.equal(
    rowButtons(r, 'instr_revoked').filter((node) => textOf(node.children).trim() === '撤销').length,
    0,
    'a revoked row has nothing to revoke either',
  )
  // The only editor that can open belongs to the active row.
  await act(async () => { editButtons('instr_active')[0].props.onClick() })
  assert.equal(editor(r).props.value, '生效中的正文')
})

test('a save in flight counts as busy, so the dialog cannot be closed mid-write', async () => {
  let release: (value: unknown) => void = () => {}
  api.updateInstruction = () => new Promise((resolve) => { release = resolve })
  const events: Events = { closed: 0, changed: 0 }
  const r = await mount([row({ id: 'instr_5', version: 3, text: '原正文' })], events)

  await act(async () => { button(r, '编辑').props.onClick() })
  await act(async () => {
    editor(r).props.onChange({ target: { value: '新正文' } })
  })
  await act(async () => {
    button(r, '保存修改').props.onClick()
    await Promise.resolve()
  })

  const content = r.root.findByProps({ 'data-testid': 'operator-instructions-dialog' })
  assert.equal(content.props['aria-busy'], true)
  assert.equal(button(r, '保存中…').props.disabled, true, 'a second click must not double-write')
  await act(async () => { button(r, '关闭弹窗').props.onClick() })
  assert.equal(events.closed, 0, 'closing is refused while the write is in flight')

  await act(async () => {
    release(row({ id: 'instr_5', version: 4, text: '新正文' }))
    await Promise.resolve()
    await Promise.resolve()
  })
  assert.equal(
    r.root.findByProps({ 'data-testid': 'operator-instructions-dialog' }).props['aria-busy'],
    false,
  )
  assert.match(pageText(r), /已修订至 v4/)
  await act(async () => { button(r, '关闭弹窗').props.onClick() })
  assert.equal(events.closed, 1, 'once the write settles the dialog closes normally')
  assert.equal(events.changed, 1)
})
