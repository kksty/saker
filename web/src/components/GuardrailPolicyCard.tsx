import { useRef, useState, type FormEvent } from 'react'
import { ShieldCheck, ShieldOff, TriangleAlert } from 'lucide-react'
import { Badge, Button, Callout, Input } from '@cyber/ui'
import { Panel } from '@/components/Page'
import { ApiError, api } from '@/lib/api'
import {
  guardrailAllowPrefixesExplainText,
  guardrailAllowPrefixesSummary,
  guardrailEnableText,
  guardrailFailOpenText,
  guardrailLLMLabel,
  guardrailLLMSemanticsText,
  guardrailPolicyFlags,
  guardrailProviderLabel,
  guardrailScopeExplainText,
  guardrailScopeSummary,
  guardrailUsesLLM,
  settingsApplyNotice,
} from '@/lib/settings'
import type { SettingsGuardrailSummary, SettingsView } from '@/lib/types'

/**
 * Summary of the saved `guardrail` policy and its judge connection.
 *
 *  - provider none / configured=false is "守门未启用" in neutral grey — a
 *    configuration state, not an error; JEV is optional (llm-only is valid);
 *  - on_error=allow is the explicit fail-open tradeoff and must be called out
 *    prominently: a let-through is not a passed judgement (docs §10.3);
 *  - shadow=true is "只判定不拦截" — grey rollout, never enforcement;
 *  - scope is a reduction of the checked surface, NOT isolation: unlisted tools
 *    are never judged, and bash/subagent can still do the same work;
 *  - allow_prefixes is literal prefix matching in safe mode only (`ls` matches
 *    `lsof`) — a noise reducer, not a semantic firewall;
 *  - JEV/LLM show only whether they are configured (plus model/base_url), never
 *    a key.
 */
export function GuardrailPolicyCard({ guardrail, onSaved }: {
  guardrail?: SettingsGuardrailSummary
  onSaved?: (view: SettingsView) => void
}) {
  // An older server has no guardrail projection at all: render nothing instead
  // of inventing a "not configured" state that could be wrong.
  if (!guardrail) return null
  const flags = guardrailPolicyFlags(guardrail)
  const allowPrefixes = guardrail.allow_prefixes_count ?? 0
  const usesLLM = guardrailUsesLLM(guardrail.provider)
  return (
    <Panel
      title="守门策略"
      extra={<Badge variant="outline" size="sm">{onSaved ? '已保存配置' : '只读'} · cyber.yaml guardrail</Badge>}
      bodyClassName="space-y-3"
      data-testid="guardrail-policy"
    >
      <div className="flex flex-wrap items-center gap-2">
        {flags.enabled ? (
          <ShieldCheck className="h-4 w-4 text-success" aria-hidden="true" />
        ) : (
          <ShieldOff className="h-4 w-4 text-ink-3" aria-hidden="true" />
        )}
        <Badge size="sm" variant={flags.enabled ? 'success' : 'muted'}>{flags.label}</Badge>
        {flags.shadow && <Badge size="sm" variant="info">影子模式：只判定不拦截</Badge>}
        {!onSaved && <span className="text-[11px] text-ink-3">配置由操作员直接写在 cyber.yaml。</span>}
      </div>

      {!flags.enabled && guardrail.reason && <Callout tone="info">{guardrail.reason}</Callout>}

      {!flags.enabled && !guardrail.reason && (
        <p className="text-[12px] leading-relaxed text-ink-2">
          守门未启用：工具调用前不会经过自动判定，也不会进入人工审批流程。<code className="mx-1 rounded bg-surface-2 px-1 font-mono text-[11px]">provider=none</code>
          或未配置就是默认状态；只配置凭据不会启用策略。需要时可以在
          <code className="mx-1 rounded bg-surface-2 px-1 font-mono text-[11px]">guardrail</code>
          配 <code className="mx-1 rounded bg-surface-2 px-1 font-mono text-[11px]">provider: llm</code>（加 llm 子段，不需要 JEV）或 jev / hybrid，重启宿主后生效。
        </p>
      )}

      {flags.failOpen && (
        <Callout tone="warning" icon={<TriangleAlert className="h-4 w-4" aria-hidden="true" />}>
          <span className="font-medium">守门未生效时自动放行（fail-open）</span>：裁判不可用、超时或输出不合法时会直接放行被拦截的工具调用。这是显式的安全取舍，放行不等于已判定通过。
        </Callout>
      )}

      <dl className="grid grid-cols-1 gap-x-4 gap-y-1.5 text-[12px] leading-relaxed sm:grid-cols-2">
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">裁判</dt>
          <dd className="saker-break min-w-0 text-ink-2">{guardrailProviderLabel(guardrail.provider)}</dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">模式</dt>
          <dd className="saker-break min-w-0 text-ink-2">{guardrail.mode?.trim() || '默认'}</dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">检查面（scope）</dt>
          <dd className="saker-break min-w-0 text-ink-2">{guardrailScopeSummary(guardrail.scope)}</dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">前缀放行（allow_prefixes）</dt>
          <dd className="saker-break min-w-0 text-ink-2">{guardrailAllowPrefixesSummary(allowPrefixes)}</dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">异常处理（on_error）</dt>
          <dd className={`saker-break min-w-0 ${flags.failOpen ? 'font-medium text-warning' : 'text-ink-2'}`}>
            {guardrail.on_error?.trim() || (usesLLM ? '未设置（默认 review）' : '不适用')}
            {flags.failOpen ? ' · 自动放行' : guardrail.on_error === 'review' ? ' · 转人工复核' : guardrail.on_error === 'block' ? ' · 直接拦截' : ''}
          </dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">JEV 端点</dt>
          <dd className="text-ink-2">{guardrail.jev_custom_endpoint === undefined ? '未知' : guardrail.jev_custom_endpoint ? '自定义' : '默认'}</dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">JEV 凭据（可选复核裁判）</dt>
          <dd className={`saker-break min-w-0 ${guardrail.jev_configured ? 'text-ink-2' : 'text-ink-3'}`}>
            {guardrail.jev_configured ? '已配置' : '未配置（llm 模式不需要）'}
          </dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-1.5">
          <dt className="shrink-0 text-ink-3">小模型裁判</dt>
          <dd className="saker-break min-w-0 font-mono text-[11px] text-ink-2">{guardrailLLMLabel(guardrail.llm)}</dd>
        </div>
      </dl>

      {/* How to read the two operator-facing switches. These are the same
          caveats the harness documentation states; the card must not imply that
          scope isolates executions or that prefix matching understands shell. */}
      <div className="space-y-1.5 border-t border-hairline pt-2 text-[11px] leading-relaxed text-ink-3">
        <p className="saker-break">
          <span className="font-medium text-ink-2">scope（只缩小检查面）</span>：{guardrailScopeExplainText}
        </p>
        <p className="saker-break">
          <span className="font-medium text-ink-2">allow_prefixes（仅 safe，字面匹配）</span>：{guardrailAllowPrefixesExplainText}
        </p>
        <p className="saker-break">
          <span className="font-medium text-ink-2">如何启用</span>：{guardrailEnableText}
        </p>
        {usesLLM && (
          <p className="saker-break">
            <span className="font-medium text-ink-2">LLM 判定语义</span>：{guardrailLLMSemanticsText}
          </p>
        )}
      </div>

      {flags.failOpen && (
        <p className="text-[11px] leading-relaxed text-ink-3">{guardrailFailOpenText}</p>
      )}
      {onSaved && <GuardrailConnection guardrail={guardrail} onSaved={onSaved} />}
    </Panel>
  )
}

function GuardrailConnection({ guardrail, onSaved }: {
  guardrail: SettingsGuardrailSummary
  onSaved: (view: SettingsView) => void
}) {
  const activeProvider = guardrail.provider !== 'none' ? guardrail.provider : ''
  const [selected, setSelected] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<ReturnType<typeof settingsApplyNotice>>(null)
  const [error, setError] = useState('')
  const saving = useRef(false)
  const provider = activeProvider || selected
  const usesLLM = guardrailUsesLLM(provider)
  const usesJEV = provider === 'jev' || provider === 'hybrid'

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (saving.current || !provider) return
    const form = event.currentTarget
    const values = Object.fromEntries(new FormData(form).entries())
    saving.current = true
    setBusy(true)
    setError('')
    setNotice(null)
    try {
      const next = await api.saveGuardrail({ ...values, provider })
      onSaved(next)
      form.reset()
      setNotice(settingsApplyNotice(next) ?? { tone: 'info', text: '配置与已保存内容一致。' })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      if (err instanceof ApiError && err.code === 'settings_saved_apply_failed' &&
          err.body && typeof err.body === 'object' && 'settings' in err.body) {
        onSaved(err.body.settings as SettingsView)
        form.reset()
        setNotice({ tone: 'warning', text: message })
      } else if (err instanceof ApiError && err.status < 500) {
        setError(message)
      } else {
        setError(`保存结果未确认。${message}`)
      }
    } finally {
      saving.current = false
      setBusy(false)
    }
  }

  return <form onSubmit={save} className="space-y-3 border-t border-hairline pt-3">
    <fieldset disabled={busy} className="grid gap-2 sm:grid-cols-2">
      {!activeProvider && <label className="grid gap-1 text-xs text-ink-2 sm:col-span-2">
        审批裁判
        <select value={selected} aria-label="审批裁判" onChange={event => setSelected(event.target.value)} className="h-9 rounded-md border border-hairline bg-surface-2 px-2 text-ink">
          <option value="">请选择</option>
          <option value="llm">LLM</option>
          <option value="jev">JEV</option>
          <option value="hybrid">LLM + JEV</option>
        </select>
      </label>}
      {usesLLM && <>
        <label className="grid gap-1 text-xs text-ink-2">LLM 裁判端点
          <Input name="llm_base_url" type="url" placeholder={guardrail.llm?.base_url || 'https://example.com/v1'} required={!guardrail.llm?.base_url} />
        </label>
        <label className="grid gap-1 text-xs text-ink-2">LLM 裁判模型
          <Input name="llm_model" placeholder={guardrail.llm?.model || '模型名称'} required={!guardrail.llm?.model} />
        </label>
        <label className="grid gap-1 text-xs text-ink-2 sm:col-span-2">LLM 裁判 API Key
          <Input name="llm_api_key" type="password" autoComplete="new-password" placeholder="选填，留空保留已保存的凭据" />
        </label>
      </>}
      {usesJEV && <>
        <label className="grid gap-1 text-xs text-ink-2">JEV 端点
          <Input name="jev_base_url" type="url" placeholder={guardrail.jev_custom_endpoint ? '留空保留自定义端点' : '留空使用默认端点'} />
        </label>
        <label className="grid gap-1 text-xs text-ink-2">JEV API Key
          <Input name="jev_api_key" type="password" autoComplete="new-password" placeholder={guardrail.jev_configured ? '留空保留已配置的凭据' : '输入 JEV API Key'} required={!guardrail.jev_configured} />
        </label>
      </>}
    </fieldset>
    <p className="text-[11px] text-ink-3">留空的字段保留原值。配置变更会重启本机宿主并结束其当前会话。</p>
    <Button type="submit" size="sm" disabled={busy || !provider}>{busy ? '保存中…' : '保存并应用守门策略'}</Button>
    {error && <Callout tone="destructive">{error}</Callout>}
    {notice && <Callout tone={notice.tone}>{notice.text}</Callout>}
  </form>
}
