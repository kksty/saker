import type { ReviewAttemptDetail } from './types.ts'

/** The review fields the overview reads. Kept structural so the page can pass
 *  the board's `reviews` object without a second mapping. */
export interface ReviewSummaryState {
  automatic: boolean
  paused: boolean
  disabled?: boolean
  pending: number
  processing: number
  cooling: number
  blocked?: number
  rejected?: number
  details?: ReviewAttemptDetail[]
}

/**
 * One sentence for the overview's review strip. The total is always
 * "未核验": only the queued set is called "待复核", and a closed or paused
 * review switch keeps its own state instead of being flattened into the queue.
 * Cooling (run failed), blocked (needs material) and rejected (independent
 * veto) are separate counts; they are not pending work.
 */
export function reviewSummaryText(r: ReviewSummaryState, unverified: number): string {
  const parts: string[] = [`复核：${unverified} 条未核验。`]
  if (r.processing > 0) parts.push(`${r.processing} 项复核中。`)
  if (r.pending > 0) parts.push(`${r.pending} 项待复核。`)
  if (r.cooling > 0) parts.push(`${r.cooling} 项运行失败，等待重试。`)
  if ((r.blocked ?? 0) > 0) parts.push(`${r.blocked} 项缺少可复现材料，等待证据或断言变化。`)
  if ((r.rejected ?? 0) > 0) parts.push(`${r.rejected} 项已被独立复核否决。`)
  parts.push(
    r.disabled
      ? '复核已关闭：不会再有自动复核 run，可在结论页开启。'
      : r.paused
        ? '复核已暂停，可在结论页恢复。'
        : r.automatic
          ? '队列会自动继续推进。'
          : '自动复核未开启，可在结论页发起复核。',
  )
  return parts.join(' ')
}

/** The attempts the overview lists under the summary, bounded by the caller. */
export function visibleReviewAttempts(r: ReviewSummaryState, limit = 3): ReviewAttemptDetail[] {
  return (r.details ?? []).slice(0, limit)
}
