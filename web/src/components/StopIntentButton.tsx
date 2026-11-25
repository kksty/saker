import { CircleStop } from 'lucide-react'
import { Button } from '@cyber/ui'
import { cn } from '@cyber/theme'

/**
 * The operator's sticky stop for one direction. A bare square icons reads as a
 * checkbox, so this always uses the stop glyph and never relies on `title`:
 * narrow screens show "停止" beside it, wide screens reveal the word on hover.
 * The aria-label states the consequence because the visible row does not.
 */
export function StopIntentButton({
  title,
  onStop,
  className,
  alwaysShowLabel = false,
}: {
  /** Direction title, only for the hover tooltip. */
  title: string
  onStop: () => void
  className?: string
  /** Detail headers have room for the word at every width. */
  alwaysShowLabel?: boolean
}) {
  return (
    <Button
      type="button"
      size="xs"
      variant="ghost"
      className={cn(
        'group/stop shrink-0 px-1.5 text-ink-3 hover:bg-destructive/10 hover:text-destructive',
        className,
      )}
      aria-label="停止意图：会取消整棵下游意图树"
      title={`停止「${title}」（人工）：会取消整棵下游意图树，理由必填`}
      onClick={onStop}
    >
      <CircleStop className="h-4 w-4" aria-hidden="true" />
      {alwaysShowLabel ? (
        <span className="text-[11px]">停止</span>
      ) : (
        <>
          <span className="text-[11px] sm:hidden">停止</span>
          <span className="hidden text-[11px] sm:group-hover/stop:inline">停止</span>
        </>
      )}
    </Button>
  )
}
