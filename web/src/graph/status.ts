// DOM tint for intent statuses.
//
// The inspector and node panel use the same semantic tokens as the canvas.

/** Chip classes (border + tint + text) for one intent status. */
export const INTENT_STATUS_TINT: Record<string, string> = {
  // Pre-run / waiting family: no live work yet.
  pending: 'border-warning/20 bg-warning/12 text-warning',
  claimed: 'border-warning/20 bg-warning/12 text-warning',
  waiting: 'border-warning/20 bg-warning/12 text-warning',
  running: 'border-primary/25 bg-primary/12 text-primary',
  reviewing: 'border-review/25 bg-review/12 text-review',
  done: 'border-success/20 bg-success/12 text-success',
  failed: 'border-ai/25 bg-ai/12 text-ai',
  review_failed: 'border-caution/25 bg-caution/12 text-caution',
  parked: 'border-border bg-surface-2 text-ink-3',
}

/** The matching leading-dot class. */
export const INTENT_STATUS_DOT: Record<string, string> = {
  pending: 'bg-warning',
  claimed: 'bg-warning',
  waiting: 'bg-warning',
  running: 'bg-primary',
  reviewing: 'bg-review',
  done: 'bg-success',
  failed: 'bg-ai',
  review_failed: 'bg-caution',
  parked: 'bg-ink-3',
}

/** The chip for an explicitly stopped intent (ledger `hold`). */
const INTENT_STOPPED_TINT = 'border-stopped/25 bg-stopped/12 text-stopped'
const INTENT_STOPPED_DOT = 'bg-stopped'

/** Short label the graph surfaces use for a held direction. */
export const INTENT_STOPPED_LABEL = '已停止'

/**
 * `hold` is the ledger's sticky-stop flag: the direction was explicitly stopped
 * (commander/operator stop, worker kill, model stall) and will not be dispatched
 * again until a retry. It is authoritative over the status — a held direction
 * is stopped even when the row still says `pending` (stopped before it ever
 * ran), and a retry is the only way back.
 */
export function intentStopped(hold?: boolean): boolean {
  return Boolean(hold)
}

export function intentStatusTint(status?: string, hold?: boolean): string | undefined {
  if (!status) return undefined
  if (intentStopped(hold)) return INTENT_STOPPED_TINT
  return INTENT_STATUS_TINT[status]
}

export function intentStatusDot(status?: string, hold?: boolean): string | undefined {
  if (!status) return undefined
  if (intentStopped(hold)) return INTENT_STOPPED_DOT
  return INTENT_STATUS_DOT[status]
}
