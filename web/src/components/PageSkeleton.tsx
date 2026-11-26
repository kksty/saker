import { Skeleton, SkeletonList } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { pageGutter } from './Page'

/**
 * Suspense fallback shaped like <Page>: the same header strip and card body, so
 * the frame does not jump when the lazy chunk lands and only the content
 * appears. Shared by both shells (global pages and the project rail's panels) so
 * the two do not drift into two different loading looks.
 *
 * It is deliberately still: no entrance fade, no scan beam, no staggered lift.
 * The fallback is only on screen while a chunk downloads, which on this console
 * is a moment, and animating the placeholder made a switch feel slower than it
 * was. The only thing that moves is the component library's own `Skeleton` pulse, the design
 * system's loading affordance, and that stops under `prefers-reduced-motion`
 * through the shared stylesheet backstop.
 */
export function PageSkeleton() {
  return (
    <div className="flex h-full min-h-0 flex-col" aria-busy="true" aria-label="页面加载中">
      {/* Header strip: the title row's three placeholder lines and the action
          block, at the sizes <Page> uses. */}
      <div className={cn('saker-page-header flex shrink-0 items-center gap-3 border-b border-hairline py-3 sm:py-5', pageGutter)}>
        <div className="min-w-0 space-y-2">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-7 w-28" />
          <Skeleton className="h-3.5 w-32 sm:w-72" />
        </div>
        <div className="ml-auto shrink-0">
          <Skeleton className="h-8 w-24" />
        </div>
      </div>
      {/* Body: the three metric cards and then the list card, already in the
          final geometry. */}
      <div className={cn('min-h-0 flex-1 overflow-hidden py-4 sm:py-5', pageGutter)}>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
          {[0, 1, 2].map((index) => (
            <div key={index}>
              <Skeleton className="h-20 rounded-xl" />
            </div>
          ))}
        </div>
        <div className="mt-4">
          <div className="rounded-xl border border-hairline bg-card p-4">
            <SkeletonList count={5} />
          </div>
        </div>
      </div>
    </div>
  )
}
