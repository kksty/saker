import { useMemo, useState } from 'react'
import { Calculator, ChevronDown } from 'lucide-react'
import { Badge, Callout } from '@cyber/ui'
import { usageBreakdownView, type UsagePart } from '@/lib/usage-breakdown'
import type { UsageBreakdown } from '@/lib/types'

/**
 * Read-only project token accounting card (project overview).
 *
 * The main row answers "who spent the tokens": commander, executor (including
 * review/report), and their combined total. The second row keeps the guardrail
 * judge apart (JEV / LLM) — a judge call is not engagement work — and the card
 * ends with the all-in total. Everything comes from model-reported usage: an
 * absent bucket renders "—" with a note; the card never estimates or zero-fills.
 *
 * Compact by default: the summary row carries the three main totals and the
 * detail (input/output/runs per bucket) stays behind a disclosure. It is a card
 * in the existing overview, not a new page or tab.
 */
export function TokenBreakdownCard({
  breakdown,
  error,
  loading,
}: {
  breakdown?: UsageBreakdown
  /** Fetch error (e.g. the API is not deployed yet): degraded, not destructive. */
  error?: string
  loading?: boolean
}) {
  const view = useMemo(() => usageBreakdownView(breakdown), [breakdown])
  return (
    <section
      data-testid="token-breakdown"
      aria-label="Token 记账"
      className="saker-panel shrink-0 rounded-xl px-4 py-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Calculator className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" />
        <span className="saker-section-title">Token 记账</span>
        <Badge size="sm" variant="outline">只读 · 模型上报</Badge>
        {loading && <span className="text-[11px] text-ink-3">读取中…</span>}
        <span className="ml-auto text-[11px] text-ink-3">不估算、不补零；未上报显示 —</span>
      </div>

      {/* Main row: 指挥官 / 执行者（含复核·报告）/ 合计（指挥官+执行者）. */}
      <div className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-3">
        {view.main.map((entry) => (
          <div key={entry.key} className="min-w-0 rounded-lg border border-hairline bg-surface-2/30 px-3 py-2">
            <div className="truncate text-[11px] leading-4 text-ink-3" title={entry.label}>{entry.label}</div>
            <div className="saker-nums font-mono text-[16px] font-semibold leading-6 text-ink">{entry.text}</div>
            {entry.detail && <div className="saker-nums truncate font-mono text-[10px] leading-4 text-ink-3" title={entry.detail}>{entry.detail}</div>}
          </div>
        ))}
      </div>

      {/* Second row: judge split, kept apart from engagement work. */}
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-hairline pt-2 text-[12px] text-ink-2">
        <span className="text-ink-3">裁判</span>
        {view.judge.map((entry) => (
          <span key={entry.key} className="inline-flex items-baseline gap-1" title={entry.detail || undefined}>
            <span className="text-ink-3">{entry.label}</span>
            <span className="saker-nums font-mono font-medium text-ink">{entry.text}</span>
          </span>
        ))}
        <span className="inline-flex items-baseline gap-1" title={view.judgeTotal.detail || undefined}>
          <span className="text-ink-3">{view.judgeTotal.label}</span>
          <span className="saker-nums font-mono text-ink">{view.judgeTotal.text}</span>
        </span>
        <span className="ml-auto inline-flex items-baseline gap-1.5">
          <span className="text-ink-3">全部（含裁判）</span>
          <span className="saker-nums font-mono text-[14px] font-semibold text-ink">{view.all.text}</span>
        </span>
      </div>

      {(view.note || view.sources) && (
        <p className="mt-1.5 text-[11px] leading-relaxed text-ink-3">
          {view.sources && <span>来源：{view.sources}{view.note ? ' · ' : ''}</span>}
          {view.note}
        </p>
      )}

      {error && (
        // A not-yet-deployed endpoint is an availability fact, not an alert:
        // keep the em-dash rows and say why they are not numbers.
        <Callout tone="info" className="mt-2">
          Token 分账暂不可用（{error}）。卡片保留 —，不显示估算值；接口就绪后无需改动即可显示。
        </Callout>
      )}

      {view.anyData && (
        <details className="mt-1.5">
          <summary className="flex cursor-pointer select-none list-none items-center gap-1 text-[11px] text-ink-3 [&::-webkit-details-marker]:hidden">
            <ChevronDown className="h-3 w-3" aria-hidden="true" /> 展开每组入/出与 runs
          </summary>
          <dl className="mt-1 grid grid-cols-1 gap-x-4 gap-y-1 text-[11px] leading-5 sm:grid-cols-2">
            {[view.main[0], view.main[1], view.judgeTotal].map((entry: UsagePart) => (
              <div key={`detail-${entry.key}`} className="flex min-w-0 flex-wrap items-baseline gap-x-2">
                <dt className="shrink-0 text-ink-3">{entry.label}</dt>
                <dd className="saker-nums saker-break min-w-0 font-mono text-ink-2">{entry.detail || '未上报明细'}</dd>
              </div>
            ))}
          </dl>
        </details>
      )}
    </section>
  )
}
