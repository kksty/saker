import { ShieldCheck, ShieldOff, TriangleAlert } from 'lucide-react'
import { Badge, Callout } from '@cyber/ui'
import { Panel } from '@/components/Page'
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
} from '@/lib/settings'
import type { SettingsGuardrailSummary } from '@/lib/types'

/**
 * Read-only summary of the operator's `extensions.guardrail` policy.
 *
 * The settings page does not edit the judge (the operator writes the section in
 * cyber.yaml directly), so this card only reports what is in effect — and, just
 * as important, how to read it:
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
export function GuardrailPolicyCard({ guardrail }: { guardrail?: SettingsGuardrailSummary }) {
  // An older server has no guardrail projection at all: render nothing instead
  // of inventing a "not configured" state that could be wrong.
  if (!guardrail) return null
  const flags = guardrailPolicyFlags(guardrail)
  const allowPrefixes = guardrail.allow_prefixes_count ?? 0
  const usesLLM = guardrailUsesLLM(guardrail.provider)
  return (
    <Panel
      title="守门策略"
      extra={<Badge variant="outline" size="sm">只读 · cyber.yaml extensions.guardrail</Badge>}
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
        <span className="text-[11px] text-ink-3">控制台只读展示，配置由操作员直接写在 cyber.yaml。</span>
      </div>

      {!flags.enabled && (
        <p className="text-[12px] leading-relaxed text-ink-2">
          守门未启用：工具调用前不会经过自动判定，也不会进入人工审批流程。<code className="mx-1 rounded bg-surface-2 px-1 font-mono text-[11px]">provider=none</code>
          或未配置就是默认状态；只配置凭据不会启用策略。需要时可以在
          <code className="mx-1 rounded bg-surface-2 px-1 font-mono text-[11px]">extensions.guardrail</code>
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
            {guardrail.on_error?.trim() || '未设置（默认 review）'}
            {flags.failOpen ? ' · 自动放行' : guardrail.on_error === 'review' ? ' · 转人工复核' : guardrail.on_error === 'block' ? ' · 直接拦截' : ''}
          </dd>
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
    </Panel>
  )
}
