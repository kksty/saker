// The pre-dispatch plan editor: the version it opened with travels with the
// save; a stale save keeps the draft and can move to the latest version; a
// direction that acquired a run renders no editor at all.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import react from '@vitejs/plugin-react-swc'
import { createTestServer } from './helpers/vite.ts'

// Plain form primitives only: the real @cyber/ui kit is not needed to exercise
// the editor's own state and version handling.
const uiStub = {
  name: 'stub-cyber-ui',
  enforce: 'pre' as const,
  resolveId(id: string) { return id === '@cyber/ui' ? '\0stub-cyber-ui' : null },
  load(id: string) {
    if (id !== '\0stub-cyber-ui') return null
    return [
      "import React from 'react'",
      "const without = (props, keys) => { const out = { ...props }; for (const k of keys) delete out[k]; return out }",
      "export const Button = ({ children, variant, size, ...props }) => React.createElement('button', props, children)",
      "export const Badge = ({ children, variant, size, ...props }) => React.createElement('span', props, children)",
      "export const Input = (props) => React.createElement('input', props)",
      "export const Textarea = (props) => React.createElement('textarea', props)",
      "export const Callout = ({ children }) => React.createElement('div', null, children)",
    ].join('\n')
  },
}

let server: Awaited<ReturnType<typeof createTestServer>>
let IntentPlanEditor: React.ComponentType<any>
let api: any
let ApiErrorClass: any
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
  IntentPlanEditor = (await server.ssrLoadModule('/src/components/IntentPlanEditor.tsx')).IntentPlanEditor
  const apiModule = await server.ssrLoadModule('/src/lib/api.ts')
  api = apiModule.api
  ApiErrorClass = apiModule.ApiError
})
afterEach(async () => { await act(async () => { renderer?.unmount(); renderer = undefined }) })
after(async () => { await server?.close() })

function intent(over: Record<string, unknown> = {}) {
  return {
    id: 'in_1', title: '旧标题', description: '旧说明', eval: '旧验收',
    status: 'pending', attempts: 0, edit_version: 0,
    created_at: '2026-11-27T00:00:00Z', updated_at: '2026-11-27T00:00:00Z',
    ...over,
  }
}

function button(label: string) {
  return renderer!.root.findAllByType('button').find((node) => node.children.join('') === label)!
}

test('saving sends the edit_version the editor opened with and the changed plan text', async () => {
  const calls: unknown[][] = []
  const events: string[] = []
  api.editIntent = async (...args: unknown[]) => {
    calls.push(args)
    return intent({ title: '新标题', eval: '新验收', edit_version: 1, edited_by: 'operator' })
  }
  await act(async () => {
    renderer = create(React.createElement(IntentPlanEditor, {
      intent: intent(), onSaved: () => events.push('saved'),
    }))
  })
  await act(async () => { button('编辑').props.onClick() })
  await act(async () => {
    renderer!.root.findByProps({ 'aria-label': '意图标题' }).props.onChange({ target: { value: '新标题' } })
  })
  await act(async () => {
    renderer!.root.findByProps({ 'aria-label': '验收标准' }).props.onChange({ target: { value: '新验收' } })
  })
  await act(async () => { await button('保存正文').props.onClick() })
  assert.deepEqual(calls, [[
    'in_1',
    { version: 0, title: '新标题', eval: '新验收' },
  ]])
  assert.deepEqual(events, ['saved'])
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已保存，计划正文 v1'))
})

test('rebasing a stale save keeps only local edits and sends only changed fields', async () => {
  api.editIntent = async () => {
    throw new ApiErrorClass(409, '意图计划正文版本已变化', 'intent_edit_conflict')
  }
  await act(async () => {
    renderer = create(React.createElement(IntentPlanEditor, { intent: intent() }))
  })
  await act(async () => { button('编辑').props.onClick() })
  await act(async () => {
    renderer!.root.findByProps({ 'aria-label': '验收标准' }).props.onChange({ target: { value: '我的草稿' } })
  })
  await act(async () => { await button('保存正文').props.onClick() })
  let text = JSON.stringify(renderer!.toJSON())
  assert.ok(text.includes('正文已被他人修改'), 'a stale save must explain the conflict')
  assert.equal(renderer!.root.findByProps({ 'aria-label': '验收标准' }).props.value, '我的草稿', 'the draft must survive')

  const calls: unknown[][] = []
  // The original editor kept the whole stale draft and resent every field.
  // Only the acceptance criterion was edited locally. The title and
  // description from the latest version must survive the next save.
  api.intent = async () => ({ intent: intent({ edit_version: 2, title: '他人标题', description: '他人说明', eval: '他人验收' }) })
  api.editIntent = async (...args: unknown[]) => {
    calls.push(args)
    return intent({ edit_version: 3, title: '他人标题', description: '他人说明', eval: '我的草稿' })
  }
  await act(async () => { await button('载入最新版本（保留草稿）').props.onClick() })
  text = JSON.stringify(renderer!.toJSON())
  assert.ok(text.includes('已载入最新版本 v2'))
  assert.equal(renderer!.root.findByProps({ 'aria-label': '意图标题' }).props.value, '他人标题')
  assert.equal(renderer!.root.findByProps({ 'aria-label': '意图说明' }).props.value, '他人说明')
  assert.equal(renderer!.root.findByProps({ 'aria-label': '验收标准' }).props.value, '我的草稿')
  await act(async () => { await button('保存正文').props.onClick() })
  assert.deepEqual(calls, [[
    'in_1',
    { version: 2, eval: '我的草稿' },
  ]])
})

test('a direction with a run gets no editor, so history cannot be rewritten from the panel', async () => {
  await act(async () => {
    renderer = create(React.createElement(IntentPlanEditor, {
      intent: intent({ status: 'running', run_id: 'r_1', edit_version: 2 }),
    }))
  })
  assert.equal(renderer!.toJSON(), null)
})
