import { after, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'
import {
  guardrailAllowPrefixesExplainText,
  guardrailAllowPrefixesSummary,
  guardrailEnableText,
  guardrailFailOpenText,
  guardrailLLMLabel,
  guardrailLLMSemanticsText,
  guardrailPolicyFlags,
  guardrailProviderLabel,
  guardrailScopeCount,
  guardrailScopeExplainText,
  guardrailScopeLabel,
  guardrailScopeSummary,
  guardrailUsesLLM,
} from '../src/lib/settings.ts'
import type { SettingsGuardrailSummary } from '../src/lib/types.ts'

// ---- pure rules --------------------------------------------------------------

test('guardrail policy flags keep "not enabled" neutral and fail-open explicit', () => {
  const disabled = guardrailPolicyFlags({ configured: false, provider: 'none' })
  assert.deepEqual(disabled, { enabled: false, failOpen: false, shadow: false, label: '守门未启用' })
  // configured=true with provider none is still "not enabled": credentials alone
  // do not enable a policy.
  assert.equal(guardrailPolicyFlags({ configured: true, provider: 'none' }).enabled, false)
  assert.equal(guardrailPolicyFlags({ configured: true, provider: '', on_error: 'review' }).enabled, false)
  const live = guardrailPolicyFlags({ configured: true, provider: 'hybrid', on_error: 'allow', shadow: true })
  assert.deepEqual(live, { enabled: true, failOpen: true, shadow: true, label: '守门已启用' })
  assert.match(guardrailFailOpenText, /守门未生效时自动放行（fail-open）/)
  assert.match(guardrailFailOpenText, /不等于已判定通过/)
})

test('provider/scope/LLM labels stay human and never leak a key', () => {
  assert.match(guardrailProviderLabel('none'), /不做自动判定/)
  assert.match(guardrailProviderLabel('jev'), /两阶段/)
  assert.match(guardrailProviderLabel('llm'), /小模型/)
  assert.match(guardrailProviderLabel('hybrid'), /REVIEW 再交 JEV/)
  assert.match(guardrailProviderLabel(undefined), /等同 none/)
  assert.equal(guardrailScopeLabel([]), '全部工具')
  assert.equal(guardrailScopeLabel(['bash', ' subagent ']), 'bash、subagent')
  assert.equal(guardrailLLMLabel(undefined), '未配置')
  assert.equal(guardrailLLMLabel({ configured: false, model: 'x' }), '未配置')
  assert.equal(guardrailLLMLabel({ configured: true, model: 'judge-1', base_url: 'http://llm/v1' }), '已配置 · judge-1 · http://llm/v1')
})

test('scope is a reduced checked surface, not isolation, and shows its count', () => {
  assert.equal(guardrailScopeCount([]), 0)
  assert.equal(guardrailScopeCount(['bash', ' subagent ', '']), 2)
  assert.equal(guardrailScopeSummary([]), '全部工具（未设置 scope）')
  assert.equal(guardrailScopeSummary(['bash', 'subagent']), 'bash、subagent（2 个工具）')
  // The bypass surface must be stated, not implied: unlisted tools are never
  // judged, and bash/subagent can still perform the same work.
  assert.match(guardrailScopeExplainText, /未列入的工具不会经过检查/)
  assert.match(guardrailScopeExplainText, /不是安全隔离/)
  assert.match(guardrailScopeExplainText, /bash 内部命令|子会话/)
  assert.match(guardrailScopeExplainText, /不能替代宿主权限/)
})

test('allow_prefixes is safe-mode-only literal matching and shows its count', () => {
  assert.equal(guardrailAllowPrefixesSummary(0), '未配置（仅 mode=safe 生效）')
  assert.equal(guardrailAllowPrefixesSummary(2), '2 条（仅 mode=safe 生效）')
  assert.match(guardrailAllowPrefixesExplainText, /仅在 mode=safe 下生效/)
  assert.match(guardrailAllowPrefixesExplainText, /字面前缀/)
  assert.match(guardrailAllowPrefixesExplainText, /不做语义分析/)
  assert.match(guardrailAllowPrefixesExplainText, /ls 会命中 lsof/)
})

test('judge enable paths: none is default, llm-only needs no JEV, JEV is optional', () => {
  assert.match(guardrailEnableText, /不配置或 provider=none 就是不启用/)
  assert.match(guardrailEnableText, /只配 provider: llm/)
  assert.match(guardrailEnableText, /不配 JEV/)
  assert.match(guardrailEnableText, /可选的复核裁判/)
  assert.match(guardrailEnableText, /国内环境无法访问 JEV 出口时可以完全不用/)
  assert.equal(guardrailUsesLLM('llm'), true)
  assert.equal(guardrailUsesLLM('hybrid'), true)
  assert.equal(guardrailUsesLLM('jev'), false)
  assert.equal(guardrailUsesLLM('none'), false)
})

test('LLM judge semantics match the harness docs: allow-biased, on_error on failure', () => {
  assert.match(guardrailLLMSemanticsText, /默认允许/)
  assert.match(guardrailLLMSemanticsText, /不确定或没把握时允许/)
  assert.match(guardrailLLMSemanticsText, /可用性、数据完整性或系统配置/)
  assert.match(guardrailLLMSemanticsText, /on_error/)
  assert.match(guardrailLLMSemanticsText, /默认转人工复核/)
  assert.match(guardrailLLMSemanticsText, /fail-open|block/)
})

// ---- render: the three operator-relevant combinations ------------------------

let server: Awaited<ReturnType<typeof createTestServer>>
let GuardrailPolicyCard: React.ComponentType<any>
let renderer: ReactTestRenderer | undefined

test('guardrail policy card renders not-enabled, fail-open+shadow and normal LLM states', async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  GuardrailPolicyCard = (await server.ssrLoadModule('/src/components/GuardrailPolicyCard.tsx')).GuardrailPolicyCard
  try {
    const render = async (guardrail?: SettingsGuardrailSummary) => {
      await act(async () => {
        renderer = create(React.createElement(GuardrailPolicyCard, { guardrail }))
      })
      return JSON.stringify(renderer!.toJSON())
    }

    // 1) provider none: neutral "not enabled", no fail-open alarm and no badge.
    //    The card must still explain scope/prefix semantics and enable paths.
    const none = await render({ configured: false, provider: 'none', mode: '', on_error: 'review', shadow: false, scope: [], allow_prefixes_count: 0, jev_configured: false })
    assert.ok(none.includes('守门未启用'), none)
    assert.ok(none.includes('provider=none'), none)
    assert.ok(!none.includes('自动放行（fail-open）'), none)
    assert.ok(!none.includes('影子模式'), none)
    assert.ok(!none.includes('text-destructive'), none)
    assert.ok(none.includes('bg-muted'), none)
    assert.ok(none.includes('全部工具（未设置 scope）'), none)
    assert.ok(none.includes('未列入的工具不会经过检查'), none)
    assert.ok(none.includes('不是安全隔离'), none)
    assert.ok(none.includes('仅 mode=safe'), none)
    assert.ok(none.includes('ls 会命中 lsof'), none)
    assert.ok(none.includes('只配 provider: llm'), none)
    assert.ok(none.includes('不配 JEV'), none)
    assert.ok(none.includes('可选复核裁判'), none)
    assert.ok(none.includes('未配置（llm 模式不需要）'), none)

    // The old card rendered only its generic enablement instructions when the
    // saved binding was rejected, leaving the actual failure hidden.
    const invalid = await render({ configured: false, provider: 'jev', reason: '当前裁判缺少 JEV 凭据' })
    assert.ok(invalid.includes('当前裁判缺少 JEV 凭据'), invalid)
    assert.ok(!invalid.includes('extensions.guardrail'), invalid)

    // 2) on_error=allow + shadow: fail-open callout and shadow badge are both
    //    required, plus the scope/prefix summary and LLM semantics.
    const failOpen = await render({
      configured: true, provider: 'hybrid', mode: 'safe', on_error: 'allow', shadow: true,
      scope: ['bash', 'subagent'], allow_prefixes_count: 2, jev_configured: true,
      jev_custom_endpoint: true,
      llm: { configured: true, model: 'judge-1', base_url: 'http://llm/v1' },
    })
    assert.ok(failOpen.includes('守门已启用'), failOpen)
    assert.ok(failOpen.includes('守门未生效时自动放行（fail-open）'), failOpen)
    assert.ok(failOpen.includes('影子模式：只判定不拦截'), failOpen)
    assert.ok(failOpen.includes('bash、subagent（2 个工具）'), failOpen)
    assert.ok(failOpen.includes('2 条（仅 mode=safe 生效）'), failOpen)
    assert.ok(failOpen.includes('LLM 判定语义'), failOpen)
    assert.ok(failOpen.includes('默认允许'), failOpen)
    assert.ok(failOpen.includes('不确定或没把握时允许'), failOpen)
    assert.ok(failOpen.includes('judge-1'), failOpen)
    assert.ok(failOpen.includes('已配置'), failOpen)
    assert.ok(failOpen.includes('自定义'), failOpen)

    // 3) a normal llm-only judge: no fail-open, no shadow, JEV not needed.
    const normal = await render({
      configured: true, provider: 'llm', mode: 'strict', on_error: 'block', shadow: false,
      scope: [], allow_prefixes_count: 0, jev_configured: false,
      llm: { configured: true, model: 'judge-2' },
    })
    assert.ok(normal.includes('守门已启用'), normal)
    assert.ok(!normal.includes('自动放行（fail-open）'), normal)
    assert.ok(!normal.includes('影子模式'), normal)
    assert.ok(normal.includes('judge-2'), normal)
    assert.ok(normal.includes('未配置（llm 模式不需要）'), normal)
    assert.ok(normal.includes('LLM 判定语义'), normal)
    assert.ok(normal.includes('默认转人工复核'), normal)

    // 4) an older server without the field renders nothing instead of a guess.
    const absent = await render(undefined)
    assert.equal(absent, 'null')
  } finally {
    await act(async () => { renderer?.unmount() })
    await server.close()
  }
})
