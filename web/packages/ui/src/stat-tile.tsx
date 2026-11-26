import type { ReactNode } from 'react'
import { cn } from '@cyber/theme'

export type StatTone = 'default' | 'primary' | 'success' | 'warning' | 'caution' | 'destructive' | 'muted'

const valueTone: Record<StatTone, string> = {
  default: 'text-ink',
  primary: 'text-primary',
  success: 'text-success',
  warning: 'text-warning',
  caution: 'text-caution',
  destructive: 'text-destructive',
  muted: 'text-ink-2',
}

export interface StatTileProps {
  label: ReactNode
  value: ReactNode
  tone?: StatTone
  /** Optional leading glyph next to the label. */
  icon?: ReactNode
  /** Optional micro caption under the value ("+12 since 09:00", "of 4 slots"). */
  hint?: ReactNode
  /** Renders the value at the page-metric size (26px) instead of the tile size. */
  size?: 'default' | 'lg'
  className?: string
}

/**
 * A compact metric tile — a quiet label over a mono, tabular value. The
 * building block of the scan/asset metric grids (hosts / services / loots / …)
 * and of the project overview board. Promoted out of AssetResultView's private
 * `Metric`, which recurred there (×9) and in FindingsSummary.
 */
export function StatTile({
  label,
  value,
  tone = 'default',
  icon,
  hint,
  size = 'default',
  className,
}: StatTileProps) {
  return (
    <div className={cn('min-w-0', className)}>
      <div className="flex items-center gap-1.5 text-[11px] font-medium tracking-[0.03em] text-ink-3">
        {icon && <span className="flex shrink-0 items-center">{icon}</span>}
        <span className="truncate">{label}</span>
      </div>
      <div className={cn(
        'saker-nums mt-1.5 font-mono font-semibold leading-none tracking-tight',
        size === 'lg' ? 'text-stat' : 'text-[20px]',
        valueTone[tone],
      )}>
        {value}
      </div>
      {hint && <div className="mt-1.5 truncate text-[11px] text-ink-3">{hint}</div>}
    </div>
  )
}
