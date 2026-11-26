import type { LucideIcon } from 'lucide-react'
import { cn } from '@cyber/theme'

/** One rail item: icon + label, with the active row reading as "you are here". */
export function SideNav({
  active,
  icon: Icon,
  label,
  onClick,
  onIntent,
  badge,
  badgeTitle,
}: {
  active: boolean
  icon: LucideIcon
  label: string
  onClick: () => void
  /**
   * Fired when the row is reached for (hover, focus, pointer down), not only
   * when it activates. The shell uses it to start downloading the page's chunk,
   * so the click swaps content instead of showing the fallback. Must stay
   * side-effect-only: it never changes the row.
   */
  onIntent?: () => void
  /** A live count, or one of the unknown-state words (未查询 / 不可用). A count of
   *  0 is never passed: the badge renders nothing instead of a zero. */
  badge?: number | string
  /** Why the badge is not an exact number; read on hover and by screen readers. */
  badgeTitle?: string
}) {
  return (
    <button
      type="button"
      aria-current={active ? 'page' : undefined}
      // The label already carries the name; the badge is a live count appended to
      // it, and the active row announces "current page" through aria-current, so
      // no extra aria-label can drift out of sync with the visible text.
      onClick={onClick}
      // "Reached for" covers hover, keyboard focus and a finger: a touch device
      // never fires mouseenter, and pointerdown lands before the click, so the
      // chunk is already on its way when the row activates.
      onMouseEnter={onIntent}
      onFocus={onIntent}
      onPointerDown={onIntent}
      className={cn(
        'saker-nav-item group relative flex min-h-10 w-full items-center gap-2.5 rounded-lg px-3 py-1.5 text-left text-ui transition-colors min-[2200px]:min-h-11',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
        active ? 'font-semibold text-ink' : 'font-normal text-ink-2 hover:bg-surface-2 hover:text-ink',
      )}
    >
      <Icon aria-hidden="true" className={cn('h-4 w-4 shrink-0', active ? 'text-primary' : 'text-ink-3 group-hover:text-ink-2')} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {/* A number is a count; a word is the backend saying "not known", which
          must not be rendered as a zero. Both sit in the same slot so the row
          never reflows when the state changes. */}
      {typeof badge === 'string' ? (
        <span
          className="shrink-0 rounded-full border border-hairline px-1.5 py-0.5 text-[10px] leading-none text-ink-3"
          title={badgeTitle}
        >
          {badge}
        </span>
      ) : badge !== undefined && badge > 0 ? (
        <span
          title={badgeTitle}
          className="saker-nums rounded-full bg-warning/15 px-1.5 py-0.5 font-mono text-[11px] leading-none text-warning"
        >
          {badge}
        </span>
      ) : null}
    </button>
  )
}
