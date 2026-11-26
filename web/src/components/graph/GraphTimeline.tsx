// The graph's time axis control (docs/33 §4.6): a footer strip inside the map.
//
// What it answers is "when did this happen", and nothing else: it reveals the
// ledger in `created_at` order and never re-ranks, filters or colours anything.
// The strip is a footer rather than a floating card for one reason: it must stay
// visible while the map, the legend and the minimap are all in use, and a
// floating card in the bottom centre collides with the legend on a narrow map.
//
// It is closed until the operator asks for it: the entry sits in the map toolbar
// (「时间轴」), so a graph screen no longer spends a strip of height on an
// occasional tool. Nothing renders at all while closed — no footer, no summary
// line. The open state is the page's, not this component's, so the toolbar and
// the strip's own collapse button drive the same switch; a reload starts closed.
//
// Undated nodes are never hidden: the axis only conceals rows it can put in
// order. Their count sits in the strip so a scrub never looks complete.

import type { CSSProperties, Ref } from 'react'
import { ChevronDown, Clock, Pause, Play, SkipForward } from 'lucide-react'
import { Button } from '@cyber/ui'

export interface GraphTimelineProps {
  /** Rendered at all? The toolbar's 时间轴 switch owns this. */
  open: boolean
  /** Close from inside the strip (the toolbar switch does the same). */
  onClose: () => void
  /** Timed rows on the axis; 0 = nothing to order. */
  total: number
  /** Row index currently revealed; null = "latest" (all rows). */
  cursor: number | null
  /** Rows the axis has revealed, including the cursor row. */
  revealed: number
  /** Timed rows not revealed yet. */
  pending: number
  /** Drawn nodes with no usable timestamp: always visible, counted here. */
  untimed: number
  /** Clock label of the cursor row (or of the last row when cursor is null). */
  atLabel: string
  firstLabel?: string
  lastLabel?: string
  stripRef?: Ref<HTMLDivElement>
  onCursor: (cursor: number | null) => void
  playing: boolean
  onTogglePlay: () => void
  /** Why the axis cannot move (no timestamps, or a single row). */
  disabledReason?: string
}

const BODY_ID = 'saker-graph-timeline-body'
/** The toolbar button points `aria-controls` at the same id. */
export const TIMELINE_BODY_ID = BODY_ID

export function GraphTimeline({
  open,
  onClose,
  total,
  cursor,
  revealed,
  pending,
  untimed,
  atLabel,
  firstLabel,
  lastLabel,
  stripRef,
  onCursor,
  playing,
  onTogglePlay,
  disabledReason,
}: GraphTimelineProps) {
  const last = Math.max(0, total - 1)
  const value = cursor ?? last
  const usable = total > 1

  if (!open) return null

  return (
    <div
      ref={stripRef}
      id={BODY_ID}
      className="saker-graph-timeline absolute z-10 rounded-xl border border-hairline bg-card/95 shadow-elevated backdrop-blur"
      role="group"
      aria-label="时间轴：按入账顺序揭示节点"
      tabIndex={usable ? 0 : undefined}
      onKeyDown={(event) => {
        // Space toggles playback while the strip has focus; the range input keeps
        // its own arrow-key behaviour, so this never swallows a scrub.
        if (event.target !== event.currentTarget || event.key !== ' ') return
        if (!usable) return
        event.preventDefault()
        onTogglePlay()
      }}
    >
      <div className="saker-graph-timeline-heading flex min-w-0 items-center gap-2.5">
        <Clock className="size-4 shrink-0 text-info" aria-hidden="true" />
        <span className="shrink-0 text-ui font-semibold text-ink">时间轴</span>
        {usable && (
          <span className="saker-nums whitespace-nowrap rounded-md bg-surface-2 px-2 py-1 font-mono text-caption text-ink">
            {atLabel}
          </span>
        )}
      </div>
      {usable && (
        <p className="saker-graph-timeline-counts saker-nums min-w-0 truncate text-caption text-ink-3" aria-live={playing ? 'off' : 'polite'}>
          已揭示 <strong className="font-semibold text-ink-2">{revealed}/{total}</strong>
          {pending > 0 && ` · 未到 ${pending}`}
          {untimed > 0 && ` · ${untimed} 无时间`}
        </p>
      )}
      <div className="saker-graph-timeline-actions flex items-center gap-1">
        {usable && (
          <>
            <Button
              size="icon-sm"
              variant="soft"
              aria-label={playing ? '暂停' : '播放'}
              title={playing ? '暂停' : '按入账顺序播放'}
              onClick={onTogglePlay}
            >
              {playing ? <Pause className="size-4" /> : <Play className="size-4" />}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="gap-1.5"
              disabled={cursor === null && !playing}
              aria-label="回到最新"
              title="回到最新：显示全部已入账节点"
              onClick={() => onCursor(null)}
            >
              <SkipForward className="size-4 shrink-0" />
              <span className="saker-graph-timeline-latest-label">最新</span>
            </Button>
          </>
        )}
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label="收起时间轴"
          aria-expanded={true}
          aria-controls={BODY_ID}
          title="收起时间轴"
          onClick={onClose}
        >
          <ChevronDown className="size-4" />
        </Button>
      </div>
      {usable ? (
          <div className="saker-graph-timeline-track grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2.5">
            <span className="saker-graph-timeline-endpoint saker-nums whitespace-nowrap font-mono text-[11px] text-ink-3">{firstLabel}</span>
            <input
              type="range"
              min={0}
              max={last}
              step={1}
              value={value}
              onChange={(event) => {
                const next = Number(event.target.value)
                onCursor(next >= last ? null : next)
              }}
              aria-label="按入账顺序揭示节点"
              aria-valuetext={`第 ${value + 1} 条，共 ${total} 条，${atLabel}`}
              className="saker-graph-timeline-range block h-6 w-full min-w-0 cursor-pointer"
              style={{ '--timeline-progress': `${(value / last) * 100}%` } as CSSProperties}
            />
            <span className="saker-graph-timeline-endpoint saker-nums whitespace-nowrap font-mono text-[11px] text-ink-3">{lastLabel}</span>
          </div>
        ) : (
          <p className="saker-graph-timeline-track text-caption text-ink-3">
            {disabledReason ?? '没有可排序的入账时间，所有节点按当前顺序显示。'}
            {untimed > 0 && `（${untimed} 个节点没有入账时间，始终显示）`}
          </p>
        )}
    </div>
  )
}
