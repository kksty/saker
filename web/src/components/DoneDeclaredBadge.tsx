import { Badge } from '@cyber/ui'

import { doneDeclaredByLabel, doneDeclaredDetail } from '@/lib/settle'
import type { Intent } from '@/lib/types'

type DeclaredDone = Pick<
  Intent,
  'status' | 'done_declared_by' | 'done_note' | 'done_fact_ids' | 'done_run_ids' | 'done_at'
>

/**
 * The provenance chip for a declared completion. It sits beside 完成 instead of
 * replacing it: the status is still `done`, but who decided it is the part a
 * reader must not guess. Hover names the note and the cited facts/runs.
 *
 * The chip belongs to the completed turn only: a retried or continued direction
 * is pending/claimed again, and an older declared completion must not ride
 * along (the settle note stays in the history for audit).
 */
export function DoneDeclaredBadge({ intent, className }: { intent?: DeclaredDone | null; className?: string }) {
  if (!intent?.done_declared_by || (intent.status && intent.status !== 'done')) return null
  const label = doneDeclaredByLabel(intent.done_declared_by)
  const detail = doneDeclaredDetail(intent)
  return (
    <Badge size="sm" variant="outline" className={className} title={detail || label}>
      {label}
    </Badge>
  )
}
