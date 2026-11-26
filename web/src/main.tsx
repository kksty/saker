import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ThemeProvider } from '@cyber/theme'
import { ConfirmProvider } from '@cyber/ui'
import App from './App'
import './app.css'

/**
 * Platform behaviour the app relies on but cannot express in Tailwind classes,
 * installed once before the first render:
 *
 * - Navigation is instant (see lib/navigation.ts): the console deliberately does
 *   not start View Transitions, so there is no root cross-fade to shorten or to
 *   gate on reduced motion.
 * - A native modal dialog must not let its scroll chain to the inert page
 *   behind it (dialog:modal + ::backdrop), per the platform overlay guidance.
 *   the component library's overlays are Radix today, so this is for any native <dialog>
 *   added later and costs nothing when none is present.
 */
const PROGRESSIVE_CSS = `
dialog:modal {
  overscroll-behavior: contain;
}
dialog::backdrop {
  overflow: hidden;
  overscroll-behavior: contain;
}
/* Visual error state for a validated control. :user-invalid is the native
   signal (a real form submission / blur marks it), and [data-user-invalid] is
   the same signal for the operator-validated console fields: the attribute is
   written in the same state update as aria-invalid, so the visible state and the
   announced state cannot drift. :is() keeps the rule alive where :user-invalid
   is unsupported. */
:is(:user-invalid, [data-user-invalid="true"]):not(:disabled) {
  border-color: var(--destructive);
}
/* The shell's first tab stop: off-screen until focused, then a real control.
   Tokens only — the palette and type ramp are untouched. */
.saker-skip-link {
  position: fixed;
  left: max(0.75rem, env(safe-area-inset-left, 0px));
  top: -100vh;
  z-index: 100;
  border: 1px solid var(--hairline);
  border-radius: var(--radius, 0.5rem);
  background: var(--popover);
  color: var(--ink-1);
  padding: 0.5rem 0.75rem;
  font-size: 13px;
  line-height: 20px;
  box-shadow: var(--shadow-elevated, 0 10px 30px rgb(0 0 0 / 0.25));
}
.saker-skip-link:focus {
  top: max(0.75rem, env(safe-area-inset-top, 0px));
  outline: 2px solid var(--ring);
  outline-offset: 2px;
}
`

function installProgressiveStyles(): void {
  if (typeof document === 'undefined' || document.getElementById('saker-progressive-css')) return
  const style = document.createElement('style')
  style.id = 'saker-progressive-css'
  style.textContent = PROGRESSIVE_CSS
  document.head.append(style)
}

/**
 * `closedby` is still Limited availability (Safari). Where it is missing, turn
 * `closedby="any"` on a native modal into the documented pointer fallback: only
 * a press *and* release outside the dialog box dismiss it, so selecting text
 * that ends over the backdrop does not close the dialog. Esc/back are native and
 * need no help. The listeners are delegated and inert until such a dialog opens.
 */
function installClosedByFallback(): void {
  if (typeof document === 'undefined' || typeof HTMLDialogElement === 'undefined') return
  if ('closedBy' in HTMLDialogElement.prototype) return
  const outside = (event: PointerEvent, dialog: HTMLDialogElement): boolean => {
    const box = dialog.getBoundingClientRect()
    return event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom
  }
  const target = (event: Event): HTMLDialogElement | null => {
    const dialog = event.target instanceof HTMLDialogElement ? event.target : null
    return dialog && dialog.matches('dialog[closedby="any"]') && dialog.matches(':modal') ? dialog : null
  }
  let pressedOutside = false
  document.addEventListener('pointerdown', (event) => {
    const dialog = target(event)
    pressedOutside = Boolean(dialog && outside(event, dialog))
  }, true)
  document.addEventListener('pointerup', (event) => {
    const dialog = target(event)
    if (dialog && pressedOutside && outside(event, dialog)) dialog.close()
    pressedOutside = false
  }, true)
}

installProgressiveStyles()
installClosedByFallback()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider className="aspect-theme-root h-full">
      <ConfirmProvider labels={{ title: '确认', confirm: '确定', cancel: '取消', close: '关闭' }}>
        <App />
      </ConfirmProvider>
    </ThemeProvider>
  </StrictMode>,
)
