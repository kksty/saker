import { cn } from '@cyber/theme'
import { Wordmark } from './Wordmark'

/**
 * The rail's brand: the drawn wordmark alone, in either shell, both sizes.
 *
 * It used to be live text — a 楷体/衬线 stack with a warm gradient and a seal dot
 * — which made the name look like whatever fonts the host had. The mark is now
 * geometry (see Wordmark.tsx), so the rail, the compact header and the login
 * page show the same lettering. There is deliberately no icon beside it: the
 * name is the brand, and a second mark in the corner competes with the nav.
 *
 * `width` is the ink width in CSS pixels: the 216px rail takes the default (120),
 * the 52px compact header a smaller 104. Both stay above the 96px floor below
 * which the S curves blur.
 */
export function Brand({ width = 120, className }: { width?: number; className?: string }) {
  return (
    <div className={cn('flex min-w-0 items-center', className)}>
      <Wordmark width={width} />
    </div>
  )
}
