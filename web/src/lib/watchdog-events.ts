import type { Run } from './types.ts'

/** One watchdog cancellation as the overview lists it. `canceledAt` is the
 *  cancellation event (`ended_at`), not the run's start. */
export interface WatchdogCancel {
  run: Run
  canceledAt: number
}

/** One cancellation that a successor run followed. The successor's row time is
 *  the ordering key: a redispatch happened when that run was admitted, not when
 *  the old run had started. */
export interface WatchdogRedispatch {
  run: Run
  successor: Run
  at: number
}

export interface WatchdogEvents {
  canceled: WatchdogCancel[]
  redispatched: WatchdogRedispatch[]
}

function parseTime(value?: string): number | null {
  if (!value) return null
  const at = Date.parse(value)
  return Number.isFinite(at) ? at : null
}

/**
 * Split watchdog cancellations from the redispatch pairs they started.
 *
 * The old list sorted by the original run's `started_at`, so a run that began
 * early and was canceled a minute ago fell behind an older cancellation. Here
 * cancellations sort by `ended_at`, and a redispatch sorts by the successor
 * run's row time (`started_at`), which is the admission receipt the ledger has.
 * A successor is the earliest later run on the same intent; if the restart
 * failed, the cancellation is listed without inventing one.
 */
export function watchdogEvents(runs: readonly Run[]): WatchdogEvents {
  const canceled: WatchdogCancel[] = []
  for (const run of runs) {
    if (run.cancel_cause !== 'watchdog') continue
    canceled.push({ run, canceledAt: parseTime(run.ended_at) ?? 0 })
  }
  canceled.sort((a, b) => b.canceledAt - a.canceledAt || a.run.id.localeCompare(b.run.id))

  const redispatched: WatchdogRedispatch[] = []
  for (const item of canceled) {
    const canceledAt = parseTime(item.run.ended_at)
    // A cancellation time that is unknown cannot safely date a successor: the
    // run's start would be the very value this list must not order by.
    if (canceledAt === null) continue
    let successor: Run | undefined
    let successorAt = Number.POSITIVE_INFINITY
    for (const run of runs) {
      if (run.id === item.run.id || run.intent_id !== item.run.intent_id) continue
      const at = parseTime(run.started_at)
      if (at === null || at <= canceledAt || at >= successorAt) continue
      successor = run
      successorAt = at
    }
    if (successor) redispatched.push({ run: item.run, successor, at: successorAt })
  }
  redispatched.sort((a, b) => b.at - a.at || a.successor.id.localeCompare(b.successor.id))
  return { canceled, redispatched }
}
