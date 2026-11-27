// Settings surface: one tab stop for the tablist, autofill/input hints on the
// credential and address-like fields, and submit-time validation that marks the
// field instead of only showing a payload-level error.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import { installFormElementShim } from './helpers/dom.ts'

let server: Awaited<ReturnType<typeof createTestServer>>
let renderer: ReactTestRenderer | undefined
let SettingsPage: any, ConfirmProvider: any, api: any, ApiError: any, Callout: any
const storage = new Map<string, string>()
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')

before(async () => {
  // The settings page now contains a Switch; Radix reads `HTMLFormElement`
  // directly, and react-test-renderer has no DOM to provide it.
  installFormElementShim()
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  SettingsPage = (await server.ssrLoadModule('/src/views/Settings.tsx')).SettingsPage
  ConfirmProvider = (await server.ssrLoadModule('@cyber/ui')).ConfirmProvider
  ;({ Callout } = await server.ssrLoadModule('@cyber/ui'))
  ;({ api, ApiError } = await server.ssrLoadModule('/src/lib/api.ts'))
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { pathname: '/g/', search: '', hash: '' },
      sessionStorage: { getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value) },
      addEventListener() {},
      removeEventListener() {},
      setInterval,
      clearInterval,
    },
  })
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} } })
})

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  storage.clear()
})

after(async () => {
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
  if (originalDocument) Object.defineProperty(globalThis, 'document', originalDocument)
  else Reflect.deleteProperty(globalThis, 'document')
  await server?.close()
})

async function mountSettings(saved: Record<string, unknown> = {}, egress: Record<string, unknown> = { mode: 'direct', configured: false, capture: true }) {
  const settings = { base_url: 'http://gateway.test/v1', model: 'route', provider: 'openai', api_key_set: true, configured: true, ...saved }
  api.settings = async () => settings
  api.projectIndex = async () => ({ projects: [] })
  api.runtimeSettings = async () => ({ auxiliary_concurrency: 1, max_auxiliary_concurrency: 8, reviews_disabled: false })
  api.egress = async () => egress
  await act(async () => { renderer = create(React.createElement(ConfirmProvider, null, React.createElement(SettingsPage))) })
  return settings
}

/** Text under one rendered component, for finding a callout by its sentence. */
function renderedText(node: any): string {
  if (typeof node === 'string') return node
  return ((node?.children ?? []) as any[]).map(renderedText).join('')
}

function button(label: string) {
  return renderer!.root.findAllByType('button').find((node) => node.children.includes(label))!
}

test('settings shows saved gateway and proxy configuration', async () => {
  // Gateway settings already reached the host, but the page had no way to
  // distinguish an enabled instance or a selected proxy from an empty file.
  await mountSettings({ gateway: { instances: 2, enabled: 1, proxy_configured: true } })
  const page = JSON.stringify(renderer!.toJSON())
  assert.match(page, /工具 API 网关/)
  assert.match(page, /2 个实例，1 个启用/)
  assert.match(page, /Cyberhub 代理/)
  assert.match(page, /已保存配置 · 只读/)
})

test('the gateway panel edits masked YAML and applies it', async () => {
  // The card used to be read-only while the harness section could only be
  // changed by hand-editing cyber.yaml. Exercise the editor and the save
  // outcome: a changed gateway section is read at scan start, so the notice
  // must be the restart, not a false "online".
  const document = "gateway:\n  fofa:\n    base_url: https://gw.test/fofa\n    key: '***'\ncyberhub:\n  proxy: office\n"
  const original = await mountSettings({
    gateway: { instances: 1, enabled: 1, proxy_configured: true, editable: true, document },
  })
  const field = () => renderer!.root.findAllByProps({ 'aria-label': '工具 API 网关配置' }).find(node => typeof node.type === 'string')!
  assert.equal(field().props.value, document)
  assert.match(JSON.stringify(renderer!.toJSON()), /已保存配置 · 可编辑/)

  const writes: { document: string }[] = []
  const edited = document.replace('https://gw.test/fofa', 'https://new.test/fofa')
  api.saveGateway = async (body: { document: string }) => {
    writes.push(body)
    return {
      ...original,
      gateway: { ...original.gateway, document: body.document },
      applied: 'saved',
      requires_restart: true,
      restart_reason: '工具 API 网关只在 scan 启动时读取',
    }
  }
  await act(async () => { field().props.onChange({ target: { value: edited } }) })
  await act(async () => { await button('保存并应用网关配置').props.onClick() })
  assert.deepEqual(writes, [{ document: edited }])
  assert.equal(field().props.value, edited, 'the draft is reconciled with the saved document')
  const page = JSON.stringify(renderer!.toJSON())
  assert.match(page, /需要重启后生效/)
  assert.match(page, /工具 API 网关只在 scan 启动时读取/)
})

test('a rejected gateway document shows the server reason', async () => {
  const document = "gateway:\n  fofa:\n    key: '***'\ncyberhub:\n  proxy: ''\n"
  await mountSettings({ gateway: { instances: 1, enabled: 1, proxy_configured: false, editable: true, document } })
  api.saveGateway = async () => { throw new ApiError(400, '工具 API 网关配置无效：gateway.fofa.enabled 必须是 true 或 false') }
  const field = () => renderer!.root.findAllByProps({ 'aria-label': '工具 API 网关配置' }).find(node => typeof node.type === 'string')!
  await act(async () => { field().props.onChange({ target: { value: document.replace("key: '***'", "enabled: 'false'") } }) })
  await act(async () => { await button('保存并应用网关配置').props.onClick() })
  assert.equal(field().props['aria-invalid'], true)
  assert.match(JSON.stringify(renderer!.toJSON()), /enabled 必须是 true 或 false/)
})

test('guardrail settings can save a judge and display the saved outcome', async () => {
  // The settings page previously exposed only the read-only summary even when
  // the operator needed to enable a judge. Exercise the form and API boundary.
  const original = await mountSettings({ guardrail_editable: true, guardrail: { provider: 'none', configured: false } })
  await act(async () => renderer!.root.findAllByType('select').find(node => node.props['aria-label'] === '审批裁判')!.props.onChange({ target: { value: 'llm' } }))
  assert.ok(renderer!.root.findAllByType('input').some(node => node.props.name === 'llm_model'))
  const writes: unknown[] = []
  api.saveGuardrail = async (body: unknown) => {
    writes.push(body)
    return { ...original, guardrail: { provider: 'llm', configured: true }, applied: 'restart' }
  }
  const previous = globalThis.FormData
  let reset = false
  try {
    globalThis.FormData = class {
      entries() { return [['llm_base_url', 'http://fixture.test/v1'], ['llm_model', 'judge'], ['llm_api_key', 'fixture-key']] }
    } as unknown as typeof FormData
    const form = renderer!.root.findAllByType('form').find(node => node.findAllByType('input').some(input => input.props.name === 'llm_model'))!
    await act(async () => form.props.onSubmit({ preventDefault() {}, currentTarget: { reset() { reset = true } } }))
  } finally {
    globalThis.FormData = previous
  }
  assert.deepEqual(writes, [{ provider: 'llm', llm_base_url: 'http://fixture.test/v1', llm_model: 'judge', llm_api_key: 'fixture-key' }])
  assert.ok(reset)
  assert.match(JSON.stringify(renderer!.toJSON()), /正在按新配置重启/)
})

test('a detected model can be selected before explicitly saving it', async () => {
  const original = await mountSettings()
  api.checkModel = async () => ({ available: true, source: 'host', returned_model: 'route', models: ['route', 'gateway/second'] })
  const writes: unknown[] = []
  api.saveSettings = async (patch: { model: string }) => { writes.push(patch.model); return { ...original, model: patch.model, applied: 'online' } }
  await act(async () => button('检测连接').props.onClick())
  const option = () => renderer!.root.findAllByType('button').find(node =>
    node.findAllByType('span').some(label => label.children.includes('gateway/second')))!
  // The catalogue used to render joined text, with no way to use a result.
  assert.ok(option())
  await act(async () => option().props.onClick())
  assert.equal(option().props['aria-pressed'], true)
  assert.equal(renderer!.root.findByProps({ 'aria-label': '模型名称' }).props.value, 'gateway/second')
  assert.deepEqual(writes, [], 'selecting fills the draft without changing the stored model')
  await act(async () => button('保存并应用').props.onClick())
  assert.deepEqual(writes, ['gateway/second'])
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('已在线应用'), 'reported only after a confirmed online apply')
})

test('a failed online apply displays the model that was already saved', async () => {
  const original = await mountSettings()
  api.saveSettings = async () => {
    throw new ApiError(500, '设置已保存，但准备宿主运行配置失败。', 'settings_saved_apply_failed', {
      settings: { ...original, model: 'stored-model' },
    })
  }
  // The request may fail after writing to disk. The draft should be reconciled
  // with the persisted model while the runtime failure remains visible.
  const field = () => renderer!.root.findByProps({ 'aria-label': '模型名称' })
  await act(async () => field().props.onChange({ target: { value: 'stored-model' } }))
  await act(async () => button('保存并应用').props.onClick())
  assert.equal(button('保存并应用').props.disabled, true, 'this is already saved')
  const output = JSON.stringify(renderer!.toJSON())
  assert.ok(output.includes('已保存的模型') && output.includes('stored-model'))
  assert.ok(output.includes('准备宿主运行配置失败'))
  assert.ok(!output.includes('已在线应用'))
})

test('an uncertain save reads back the stored model without retrying the write', async () => {
  const original = await mountSettings()
  let saves = 0
  api.saveSettings = async () => { saves += 1; throw new Error('连接中断') }
  api.settings = async () => ({ ...original, model: 'stored-after-disconnect' })
  const field = () => renderer!.root.findByProps({ 'aria-label': '模型名称' })
  await act(async () => field().props.onChange({ target: { value: 'stored-after-disconnect' } }))
  await act(async () => button('保存并应用').props.onClick())
  const stored = renderer!.root.findAllByType('strong').find(node => node.children.includes('stored-after-disconnect'))
  assert.ok(stored, 'readback updates the saved model shown to the operator')
  assert.equal(saves, 1)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('应用状态未确认'))
})

test('saving model settings preserves drafts in initially empty settings fields', async () => {
  const original = await mountSettings()
  api.saveSettings = async (patch: { model: string }) => ({ ...original, model: patch.model })
  const field = (label: string) => renderer!.root.findAllByProps({ 'aria-label': label }).find((node) => typeof node.type === 'string')!
  const draft = '{"local":{"command":"local-preview"}}'
  // Blank stored values are valid baselines. Refreshing settings after a save
  // used to treat them as unloaded and replace unsaved changes in other cards.
  await act(async () => {
    field('MCP 服务器 JSON').props.onChange({ target: { value: draft } })
    field('捕获保留上限（MiB）').props.onChange({ target: { value: '4096' } })
    field('模型名称').props.onChange({ target: { value: 'changed-route' } })
  })
  await act(async () => { await button('保存并应用').props.onClick() })
  assert.equal(field('MCP 服务器 JSON').props.value, draft)
  assert.equal(field('捕获保留上限（MiB）').props.value, '4096')
})

test('changing the default review switch preserves an unsaved concurrency edit', async () => {
  await mountSettings()
  api.saveRuntimeSettings = async () => ({ auxiliary_concurrency: 1, max_auxiliary_concurrency: 8, reviews_disabled: true })
  const field = () => renderer!.root.findByProps({ 'aria-label': '复核与报告并发' })
  const toggle = renderer!.root.findAllByProps({ id: 'review-default' }).find((node) => typeof node.props.onCheckedChange === 'function')!
  // Updating the shared runtime response used to reset the other panel's input.
  await act(async () => field().props.onChange({ target: { value: '3' } }))
  await act(async () => toggle.props.onCheckedChange(false))
  assert.equal(field().props.value, '3')
})

test('the settings tablist has one tab stop and arrow keys move it with the panel', async () => {
  await mountSettings()
  const list = renderer!.root.findByProps({ role: 'tablist' })
  const tabs = () => renderer!.root.findAll((node) => node.type === 'button' && node.props.role === 'tab')
  assert.equal(tabs().length, 2)
  assert.deepEqual(tabs().map((node: any) => node.props['aria-selected']), [true, false])
  assert.deepEqual(tabs().map((node: any) => node.props.tabIndex), [0, -1])
  assert.equal(tabs()[0].props['aria-controls'], 'settings-panel-model')

  await act(async () => { list.props.onKeyDown({ key: 'ArrowRight', preventDefault() {} }) })
  assert.deepEqual(tabs().map((node: any) => node.props['aria-selected']), [false, true])
  assert.deepEqual(tabs().map((node: any) => node.props.tabIndex), [-1, 0])
  assert.equal(renderer!.root.findByProps({ id: 'settings-panel-model' }).props.hidden, true)
  assert.equal(renderer!.root.findByProps({ id: 'settings-panel-projects' }).props.hidden, false)

  await act(async () => { list.props.onKeyDown({ key: 'Home', preventDefault() {} }) })
  assert.deepEqual(tabs().map((node: any) => node.props['aria-selected']), [true, false])
})

test('connection fields carry the right autofill and input hints, and the key can be revealed', async () => {
  await mountSettings()
  const baseURL = renderer!.root.findByProps({ 'aria-label': '接口地址' })
  assert.equal(baseURL.props.autoComplete, 'url')
  assert.equal(baseURL.props.inputMode, 'url')
  assert.equal(baseURL.props.spellCheck, false)
  assert.equal(baseURL.props.enterKeyHint, 'next')
  assert.equal(renderer!.root.findByProps({ 'aria-label': '模型名称' }).props.autoComplete, 'off')
  const key = renderer!.root.findByProps({ 'aria-label': 'API Key' })
  assert.equal(key.props.type, 'password')
  assert.equal(key.props.autoComplete, 'new-password')
  await act(async () => { renderer!.root.findByProps({ 'aria-label': '显示 API Key' }).props.onClick() })
  assert.equal(renderer!.root.findByProps({ 'aria-label': 'API Key' }).props.type, 'text')
  assert.equal(renderer!.root.findByProps({ 'aria-label': '隐藏 API Key' }).props['aria-pressed'], true)
})

test('an invalid model-number draft is caught before the request and marked on the field', async () => {
  const writes: any[] = []
  api.saveSettings = async (body: any) => { writes.push(body); return { ...body, api_key_set: true, configured: true } }
  await mountSettings()
  const field = () => renderer!.root.findByProps({ 'aria-label': '上下文窗口' })
  await act(async () => { field().props.onChange({ target: { value: 'abc' } }) })
  await act(async () => { await button('保存并应用').props.onClick() })
  assert.equal(writes.length, 0, 'the invalid draft must not reach the API')
  assert.equal(field().props['aria-invalid'], true)
  assert.match(String(field().props['aria-describedby']), /-context_window-error$/)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('上下文窗口必须是非负整数'))

  await act(async () => { field().props.onChange({ target: { value: '128000' } }) })
  assert.equal(field().props['aria-invalid'], undefined, 'editing the field clears its error state')
  await act(async () => { await button('保存并应用').props.onClick() })
  assert.equal(writes.length, 1)
  assert.equal(writes[0].context_window, 128000)
})

test('runtime concurrency rejects an out-of-range value and focuses the field', async () => {
  const writes: any[] = []
  api.saveRuntimeSettings = async (body: any) => { writes.push(body); return { auxiliary_concurrency: body.auxiliary_concurrency, max_auxiliary_concurrency: 8 } }
  await mountSettings()
  const field = () => renderer!.root.findByProps({ 'aria-label': '复核与报告并发' })
  assert.equal(field().props.required, true)
  assert.equal(field().props.inputMode, 'numeric')
  await act(async () => { field().props.onChange({ target: { value: '99' } }) })
  await act(async () => { await button('保存并发').props.onClick() })
  assert.deepEqual(writes, [])
  assert.equal(field().props['aria-invalid'], true)
  assert.ok(JSON.stringify(renderer!.toJSON()).includes('1–8'))

  await act(async () => { field().props.onChange({ target: { value: '3' } }) })
  await act(async () => { await button('保存并发').props.onClick() })
  assert.deepEqual(writes, [{ auxiliary_concurrency: 3 }])
})

test('the egress panel separates the saved selection from the probe', async () => {
  // Two different questions: what is configured (an instance or a URL) and
  // whether this machine can still get out through it. A failed probe is a
  // measurement, not a configuration error.
  await mountSettings(
    { gateway: { instances: 1, enabled: 1, proxy_configured: true, editable: true, document: "gateway:\n  office:\n    kind: clash\n    url: '***'\ncyberhub:\n  proxy: office\n" } },
    {
      mode: 'subscription', configured: true, egress: 'office', capture: true,
      probe: { ok: false, duration_ms: 183, error: 'dial tcp 127.0.0.1:7890: connectex: refused', at: '2026-11-27T06:00:00Z', target: 'https://example.com/' },
    },
  )
  const page = JSON.stringify(renderer!.toJSON())
  assert.match(page, /出口状态/)
  assert.match(page, /订阅 · office/)
  assert.match(page, /不通/)
  assert.match(page, /dial tcp 127\.0\.0\.1:7890/)
})

test('the egress panel flags a selection the scan would reject', async () => {
  await mountSettings({}, { mode: 'subscription', configured: true, capture: true, invalid: '有 2 个 clash 实例（a、b）但 cyberhub.proxy 未指定' })
  assert.match(JSON.stringify(renderer!.toJSON()), /有 2 个 clash 实例/)
})

test('a check that cannot run is a warning, not a red egress', async () => {
  // The distinction is the point of probe_error: the check did not reach the
  // network, so a destructive callout would claim a measurement nobody made.
  await mountSettings({}, {
    mode: 'direct', configured: false, capture: true,
    probe_error: '本机宿主没有可用的 scan 二进制',
  })
  assert.match(JSON.stringify(renderer!.toJSON()), /检测无法运行：本机宿主没有可用的 scan 二进制/)
  const summary = renderer!.root.findAllByType(Callout)
    .find((node) => renderedText(node).includes('检测无法运行'))
  assert.equal(summary?.props.tone, 'warning')
})

test('a config the scan refuses cannot be probed from the panel', async () => {
  await mountSettings({}, {
    mode: 'subscription', configured: false, capture: true, refused: true,
    invalid: '有 2 个 clash 实例（a、b）但 cyberhub.proxy 未指定：scan 启动会拒绝',
  })
  const probeButton = renderer!.root.findAllByType('button')
    .find((node) => String(node.children.join('')) === '检测')
  assert.equal(probeButton?.props.disabled, true, '检测 would start a scan that exits before dialing')
})

test('the model protocol is a choice, not free text', async () => {
  // A typed protocol let a typo reach the harness; the supported protocols are
  // openai and anthropic, and the field is now a select with an explicit name.
  await mountSettings({ provider: 'anthropic' })
  const inputs = renderer!.root.findAllByType('input')
  assert.equal(inputs.some(node => node.props['aria-label'] === '模型接口协议'), false, 'protocol must not be a text input')
  const trigger = renderer!.root.findAllByType('button').find(node => node.props['aria-label'] === '模型接口协议')
  assert.ok(trigger, 'protocol select trigger is missing')
})

test('every select label is inside a select group', async () => {
  // Radix throws "`SelectLabel` must be used within `SelectGroup`", and a
  // closed SelectContent still renders its children into a hidden fragment —
  // so an ungrouped label takes the whole settings page down on mount. The
  // node renderer does not mount that fragment, which is why only the browser
  // caught it; this guard covers the rule statically.
  const source = await readFile(fileURLToPath(new URL('../src/views/Settings.tsx', import.meta.url)), 'utf8')
  let at = 0
  let labels = 0
  while ((at = source.indexOf('<SelectLabel', at)) >= 0) {
    labels += 1
    const open = source.lastIndexOf('<SelectGroup>', at)
    const close = source.lastIndexOf('</SelectGroup>', at)
    assert.ok(open >= 0 && open > close, 'a SelectLabel sits outside its SelectGroup')
    at += 1
  }
  assert.ok(labels > 0, 'no SelectLabel found to check')
})
