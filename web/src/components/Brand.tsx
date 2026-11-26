import type { ReactNode } from 'react'
import { cn } from '@cyber/theme'
import { Wordmark } from './Wordmark'

/**
 * The rail's brand lockup: the drawn wordmark, either shell, both sizes.
 *
 * It used to be live text — a 楷体/衬线 stack with a warm gradient and a seal dot
 * — which made the name look like whatever fonts the host had. The mark is now
 * geometry (see Wordmark.tsx), so the rail, the compact header and the login
 * page show the same lettering. `cap` is the drawn cap height: the 216px rail
 * takes the default, the 52px mobile bar takes the smaller one.
 *
 * `logo` stays as an escape hatch for a real supplied asset; until one exists
 * the wordmark is the mark, and `public/favicon.svg` is its S for the tab.
 */
export function Brand({
  badge,
  logo,
  cap = 20,
  className,
}: {
  badge?: ReactNode
  /** A supplied logo asset; replaces the drawn wordmark when passed. */
  logo?: ReactNode
  /** Drawn cap height in px. */
  cap?: number
  className?: string
}) {
  return (
    <div className={cn('flex min-w-0 items-center', className)}>
      {logo ? (
        <div className="flex min-w-0 items-center" data-brand-logo>
          {logo}
        </div>
      ) : (
        <Wordmark cap={cap} />
      )}
      {badge ? <div className="ml-auto shrink-0">{badge}</div> : null}
    </div>
  )
}
