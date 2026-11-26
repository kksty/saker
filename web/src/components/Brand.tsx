import { cn } from '@cyber/theme'
import { Wordmark } from './Wordmark'

/**
 * The rail's brand lockup: the drawn wordmark, either shell, both sizes.
 *
 * It used to be live text — a 楷体/衬线 stack with a warm gradient and a seal dot
 * — which made the name look like whatever fonts the host had. The mark is now
 * geometry (see Wordmark.tsx), so the rail, the compact header and the login
 * page show the same lettering. `width` is the ink width in CSS pixels: the
 * 216px rail takes the default (148), the 52px mobile bar the 120px floor below
 * which the S curves blur.
 */
export function Brand({ width = 148, className }: { width?: number; className?: string }) {
  return (
    <div className={cn('flex min-w-0 items-center', className)}>
      <Wordmark width={width} />
    </div>
  )
}
