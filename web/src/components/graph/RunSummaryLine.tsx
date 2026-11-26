import { Button } from '@cyber/ui'
import { fmtDuration, fmtTime, runStatusLabel } from '@/lib/format'
import type { Run } from '@/lib/types'

/**
 * One line that ties the map to the run ledger: which attempt is the latest one,
 * how it ended, how long it took and when it started. The panel already carries
 * the intent's frozen evidence; without this line an operator had to open the
 * run page to learn whether the direction is running, failed or was stopped.
 */
export function RunSummaryLine({ run, onOpenRun }: { run: Run; onOpenRun?: (runID: string) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-ink-3">
      <span className="shrink-0">最近运行</span>
      <span className="font-mono saker-nums">{run.id}</span>
      <span className="text-ink-2">{runStatusLabel(run)}</span>
      <span className="font-mono saker-nums">{fmtDuration(run.started_at, run.ended_at)}</span>
      <span className="font-mono saker-nums">{fmtTime(run.started_at)}</span>
      {onOpenRun && (
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => onOpenRun(run.id)}>
          运行详情 →
        </Button>
      )}
    </div>
  )
}
