import { Badge } from '@cyber/ui'
import { GuardrailApprovalButtons } from '@/components/GuardrailCard'
import {
  guardrailActionLabel,
  guardrailActionVariant,
  guardrailEventStateLabel,
  parseGuardrailEvent,
} from '@/lib/guardrail'
import type { Event } from '@/lib/types'

/**
 * One guardrail journal row in the run timeline: `guardrail_decision` (record /
 * review / block) and `guardrail_review` (an intercepted call). The action badge
 * carries the visual language — record is quiet grey, review yellow, block red —
 * and the row spells out the tool and the policy's reason.
 *
 * A pending review is also an actionable approval: when the row names its
 * session and operation, the same confirmation dialog as the approval card is
 * one explicit "查看审批状态…" away. The event alone cannot promise the host
 * still waits, so the buttons appear only after that read; rows without ids stay
 * read-only. `record`/`block` never mount the approval controls.
 */
export function GuardrailEventRow({
  event,
  sessionID,
  onChanged,
}: {
  event: Event
  /** The run's session: required before an approval can target it. */
  sessionID?: string
  onChanged?: () => void
}) {
  const view = parseGuardrailEvent(event)
  if (!view) return null
  const stateLabel = guardrailEventStateLabel(view.state)
  const actionable = view.action === 'review' && view.state === 'pending'
  return (
    <div className="flex flex-wrap items-center gap-1.5" data-testid="guardrail-event">
      <Badge size="sm" variant={guardrailActionVariant(view.action)}>{guardrailActionLabel(view.action)}</Badge>
      {view.tool && <span className="font-mono text-[11px] text-ink">{view.tool}</span>}
      {stateLabel && <Badge size="sm" variant="outline">{stateLabel}</Badge>}
      <span className="saker-break text-[11px] leading-relaxed text-ink-2">{view.reason || '（没有记录原因）'}</span>
      {view.operationID && <span className="font-mono text-[10px] text-ink-3">操作 {view.operationID}</span>}
      {view.callID && <span className="font-mono text-[10px] text-ink-3">调用 {view.callID}</span>}
      {actionable && view.operationID && sessionID && (
        <GuardrailApprovalButtons sessionID={sessionID} operationID={view.operationID} tool={view.tool} onChanged={onChanged} />
      )}
      {actionable && (!view.operationID || !sessionID) && (
        <span className="text-[11px] text-ink-3">只读：事件缺少会话或操作 id，无法在此批准。</span>
      )}
    </div>
  )
}
