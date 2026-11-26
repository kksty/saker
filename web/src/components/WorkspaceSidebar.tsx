import { useId, type ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from '@cyber/theme'
import { Button } from '@cyber/ui'

/** A side pane beside the main content, with a disclosure in single-column layouts. */
export function WorkspaceSidebar({ label, summary, expanded, onToggle, children }: {
  label: string
  summary?: ReactNode
  expanded: boolean
  onToggle: () => void
  children: ReactNode
}) {
  const contentID = useId()

  return (
    <div className="flex min-h-0 min-w-0 shrink-0 flex-col gap-2">
      <Button
        size="sm"
        variant="outline"
        aria-expanded={expanded}
        aria-controls={contentID}
        onClick={onToggle}
        className="h-9 w-full justify-start gap-2 rounded-lg bg-card text-left @3xl:hidden"
      >
        <span className="min-w-0 flex-1 truncate">{label}</span>
        {summary && <span className="saker-nums truncate text-[11px] text-ink-3">{summary}</span>}
        <ChevronDown aria-hidden="true" className={cn('size-4 shrink-0 text-ink-3', expanded && 'rotate-180')} />
      </Button>
      <div id={contentID} className={cn('min-h-0 flex-1 flex-col gap-3 @3xl:flex', expanded ? 'flex' : 'hidden')}>
        {children}
      </div>
    </div>
  )
}
