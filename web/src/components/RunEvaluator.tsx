import { useMemo } from 'react'
import { CircleCheck, CircleHelp, CircleX, LoaderCircle } from 'lucide-react'
import { Badge, Callout } from '@cyber/ui'
import {
  evaluatorBadgeVariant,
  evaluatorProvenanceText,
  evaluatorRoundText,
  evaluatorSummaryText,
  evaluatorVerdictLabel,
  latestEvaluatorResult,
  parseEvaluatorResults,
} from '@/lib/run-evaluator'
import type { Event } from '@/lib/types'

/**
 * The evaluator's last judged round, surfaced above the raw event stream.
 *
 * The status text already carries the verdict (`eval_end 第2/3轮：通过 原因`,
 * written by internal/aopexec/eval.go), so the run page shows a pass/fail/error
 * callout instead of making the operator find that line among hundreds of
 * events. A stream with only eval_start shows "评估进行中" — never a verdict.
 */
export function RunEvaluator({ events }: { events: Event[] }) {
  const results = useMemo(() => parseEvaluatorResults(events), [events])
  const latest = useMemo(() => latestEvaluatorResult(results), [results])
  if (!latest) return null
  const tone =
    latest.verdict === 'pass' ? 'success'
      : latest.verdict === 'fail' ? 'warning'
        : latest.verdict === 'error' ? 'destructive'
          : 'info'
  const Icon =
    latest.verdict === 'pass' ? CircleCheck
      : latest.verdict === 'fail' ? CircleX
        : latest.verdict === 'error' ? CircleX
          : LoaderCircle
  const rounds = evaluatorRoundText(latest)
  return (
    <Callout tone={tone} icon={<Icon className="h-3.5 w-3.5" aria-hidden="true" />}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Badge size="sm" variant={evaluatorBadgeVariant(latest)}>{evaluatorVerdictLabel(latest)}</Badge>
        {rounds && <span className="saker-nums font-mono text-[11px] text-ink-3">{rounds}</span>}
        {results.length > 1 && <span className="text-[11px] text-ink-3">共 {results.length} 次评估</span>}
        {latest.verdict === 'unknown' && <CircleHelp className="h-3 w-3 text-ink-3" aria-hidden="true" />}
      </div>
      <p className="saker-break mt-0.5 text-[12px] leading-relaxed text-ink-2">{evaluatorSummaryText(latest)}</p>
      {/* Who judged: same model connection as the executor, not JEV. Without
          this line an operator reads "评估通过/未通过" as a guardrail verdict. */}
      <p className="saker-break mt-1 border-t border-hairline pt-1 text-[11px] leading-relaxed text-ink-3">
        {evaluatorProvenanceText}
      </p>
    </Callout>
  )
}
