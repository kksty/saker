import { fmtTime } from './format'
import type { Intent } from './types'

/**
 * A completion can come from two places: a run closing on its own result, or a
 * person/commander declaring the direction done on recorded evidence. The row
 * carries the second kind in `done_declared_by`, and every surface that shows
 * 完成 must say which one it was — a declared close is not an executor result.
 */
export function doneDeclaredByLabel(by?: string): string {
  switch (by) {
    case 'commander':
      return '指挥官判定完成'
    case 'operator':
      return '操作员判定完成'
    default:
      return by ? '判定完成' : ''
  }
}

/** The same fact in a tight row (roster line, graph list), where the full badge
 *  does not fit: "指挥官判定" still keeps it apart from a run-produced 完成. */
export function doneDeclaredByShort(by?: string): string {
  switch (by) {
    case 'commander':
      return '指挥官判定'
    case 'operator':
      return '操作员判定'
    default:
      return by ? '判定完成' : ''
  }
}

/** Hover text for the badge: the settler's note and exactly which records it
 *  cited, with the claim version and review status each one had at settlement.
 *  A changed claim is named separately; the original judgement is never
 *  rewritten. */
export function doneDeclaredDetail(
  done: Pick<
    Intent,
    | 'done_note'
    | 'done_fact_ids'
    | 'done_run_ids'
    | 'done_at'
    | 'done_fact_claims'
    | 'done_fact_statuses'
    | 'done_unverified'
    | 'done_receipt_stale'
    | 'done_receipt_changed'
  >,
): string {
  const lines: string[] = []
  if (done.done_note) lines.push(`说明：${done.done_note}`)
  if (done.done_fact_ids?.length) {
    const claims = done.done_fact_ids.map((id) => {
      const version = done.done_fact_claims?.[id]
      const status = done.done_fact_statuses?.[id]
      const parts = [version ? `断言 v${version}` : '断言版本未知']
      if (status) parts.push(statusLabel(status))
      return `${id}（${parts.join(' / ')}）`
    })
    lines.push(`证据事实：${claims.join('，')}`)
  }
  if (done.done_run_ids?.length) lines.push(`证据运行：${done.done_run_ids.join('，')}`)
  if (done.done_at) lines.push(`判定于 ${fmtTime(done.done_at)}`)
  if (done.done_unverified) lines.push('判定时并非所有引用事实都已核验')
  if (done.done_receipt_stale) {
    lines.push(`依据已变化：${(done.done_receipt_changed ?? []).join('，') || '引用的断言已更新'}；原回执保留，请按 ID 重读后重新确认`)
  }
  return lines.join('\n')
}

/** A short marker for the chip itself, so a stale receipt is visible without
 *  hovering. Empty when the receipt still rests on what was accepted. */
export function doneReceiptStaleLabel(done: Pick<Intent, 'done_receipt_stale'>): string {
  return done.done_receipt_stale ? '依据已变' : ''
}

function statusLabel(status: string): string {
  switch (status) {
    case 'verified':
      return '已核验'
    case 'unverified':
      return '未核验'
    case 'withdrawn':
      return '已撤回'
    case 'rejected':
      return '已否决'
    default:
      return status
  }
}
