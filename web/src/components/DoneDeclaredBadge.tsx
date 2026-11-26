import { Badge } from '@cyber/ui'

import { doneDeclaredByLabel, doneDeclaredDetail, doneReceiptStaleLabel } from '@/lib/settle'
import type { Intent } from '@/lib/types'

type DeclaredDone = Pick<
  Intent,
  | 'status'
  | 'done_declared_by'
  | 'done_note'
  | 'done_fact_ids'
  | 'done_run_ids'
  | 'done_at'
  | 'done_fact_claims'
  | 'done_fact_statuses'
  | 'done_unverified'
  | 'done_receipt_stale'
  | 'done_receipt_changed'
>

/**
 * The provenance chip for a declared completion. It sits beside 完成 instead of
 * replacing it: the status is still `done`, but who decided it is the part a
 * reader must not guess. Hover names the note and the cited facts/runs, with the
 * claim version and review status each had at settlement. A later correction
 * turns the chip into a stale marker instead of rewriting the receipt.
 *
 * The chip belongs to the completed turn only: a retried or continued direction
 * is pending/claimed again, and an older declared completion must not ride
 * along (the settle note stays in the history for audit).
 */
export function DoneDeclaredBadge({ intent, className }: { intent?: DeclaredDone | null; className?: string }) {
  if (!intent?.done_declared_by || (intent.status && intent.status !== 'done')) return null
  const label = doneDeclaredByLabel(intent.done_declared_by)
  const stale = doneReceiptStaleLabel(intent)
  const detail = doneDeclaredDetail(intent)
  return (
    <Badge
      size="sm"
      variant={intent.done_receipt_stale ? 'warning' : 'outline'}
      className={className}
      title={detail || label}
    >
      {label}
      {stale ? ` · ${stale}` : ''}
    </Badge>
  )
}
