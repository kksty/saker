import { useEffect, useState, type ReactNode } from 'react'
import { Menu } from 'lucide-react'
import { Button, Sheet, SheetContent, SheetDescription, SheetTitle, SheetTrigger } from '@cyber/ui'
import { Brand } from './Brand'

/** The shell's first tab stop: jump past the rail and its repeated navigation
 *  into the page's <main> landmark (Page renders it with `tabIndex={-1}`). */
function SkipLink() {
  return (
    <a href="#saker-main" className="saker-skip-link">
      跳到主要内容
    </a>
  )
}

/** Both workspaces share the same rail; on small screens it becomes a modal
 * drawer so the operator can still use the page at its full width.
 *
 * The drawer is a Radix dialog, which already supplies the platform modal
 * semantics (focus containment, inert background, Esc/dismiss, focus return to
 * the trigger). What this shell adds is the landmark structure — skip link,
 * header for the compact bar, labelled aside for the rail — and scroll
 * containment so a gesture inside the open drawer cannot chain to the page. */
export function ConsoleRail({ title, active, context, children, footer }: {
  title: string
  active: string
  context?: ReactNode
  children: ReactNode
  footer: ReactNode
}) {
  const [open, setOpen] = useState(false)
  useEffect(() => { setOpen(false) }, [active])
  useEffect(() => {
    // Guarded: tests and restricted embeds can render the shell without
    // matchMedia, and the drawer must not fail just because the breakpoint is
    // unavailable.
    if (typeof window.matchMedia !== 'function') return
    const wide = window.matchMedia('(min-width: 1024px)')
    const resize = () => { if (wide.matches) setOpen(false) }
    wide.addEventListener('change', resize)
    return () => wide.removeEventListener('change', resize)
  }, [])
  const contents = <>
    <div className="saker-rail-brand shrink-0 px-5 pb-4 pt-6">
      <Brand />
    </div>
    <div className="mx-3 mb-2 border-t border-hairline px-2.5 pt-3 text-caption font-medium text-ink-3">{title}</div>
    {context && <div className="mx-3 mb-3 shrink-0">{context}</div>}
    {children}
    <div className="m-3 mt-auto shrink-0 border-t border-hairline pt-3">{footer}</div>
  </>
  return <>
    <SkipLink />
    <header className="saker-rail flex h-[52px] shrink-0 items-center justify-between border-b border-hairline px-3.5 lg:hidden">
      <Brand cap={16} />
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetTrigger asChild><Button size="sm" variant="outline" className="gap-1.5" aria-label="打开导航"><Menu className="h-4 w-4" />导航</Button></SheetTrigger>
        <SheetContent side="left" closeLabel="关闭导航" className="saker-rail flex w-[280px] max-w-[85vw] flex-col gap-0 overscroll-contain p-0" style={{ paddingBottom: 'env(safe-area-inset-bottom, 0px)' }} onClick={event => {
          if (event.target instanceof Element && event.target.closest('nav button')) setOpen(false)
        }}>
          <SheetTitle className="sr-only">{title}导航</SheetTitle>
          <SheetDescription className="sr-only">选择页面；按 Escape 或关闭按钮返回工作区。</SheetDescription>
          {contents}
        </SheetContent>
      </Sheet>
    </header>
    <aside aria-label={`${title}导航`} className="saker-rail hidden w-[216px] shrink-0 flex-col border-r border-hairline lg:flex xl:w-[228px]" style={{ paddingBottom: 'env(safe-area-inset-bottom, 0px)' }}>{contents}</aside>
  </>
}
