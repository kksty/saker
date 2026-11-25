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
 *  cited, so "declared done" can be checked without opening the ledger. */
export function doneDeclaredDetail(
  done: Pick<Intent, 'done_note' | 'done_fact_ids' | 'done_run_ids' | 'done_at'>,
): string {
  const lines: string[] = []
  if (done.done_note) lines.push(`说明：${done.done_note}`)
  if (done.done_fact_ids?.length) lines.push(`证据事实：${done.done_fact_ids.join('，')}`)
  if (done.done_run_ids?.length) lines.push(`证据运行：${done.done_run_ids.join('，')}`)
  if (done.done_at) lines.push(`判定于 ${fmtTime(done.done_at)}`)
  return lines.join('\n')
}
