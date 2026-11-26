const navigationEvent = 'saker:navigate'

export function locationPath(): string {
  return window.location.pathname + window.location.search + window.location.hash
}

/**
 * Same-document navigation must notify React as well as the browser history.
 *
 * Both happen in the same task, synchronously, and nothing animates the swap:
 * the click, the URL and the React render land in one commit, so the new view is
 * painted on the very next frame. That is the whole contract — a rail click, a
 * tab click and a graph node selection all go through here.
 *
 * It used to be wrapped in `document.startViewTransition` so the old and new
 * views cross-faded. That is gone on purpose:
 *
 *   - the browser captures its "new" snapshot on the rendering opportunity that
 *     follows the callback, but React commits the state change later, so the
 *     cross-fade ran between two *identical* frames and the real change appeared
 *     after it — a page-wide flash on every switch, worst on a node click where
 *     only one dock panel changes;
 *   - the snapshot pass plus the 120–160ms fade is pure added latency on a
 *     data-dense page, and it blocks the paint the operator is waiting for;
 *   - the console's feel is "instant", not "animated": motion that does not
 *     report something is noise.
 */
export function navigate(url: string, replace = false): void {
  if (url === locationPath()) return
  if (replace) window.history.replaceState(null, '', url)
  else window.history.pushState(null, '', url)
  window.dispatchEvent(new Event(navigationEvent))
}

export function subscribeLocation(changed: () => void): () => void {
  const events = ['popstate', 'hashchange', navigationEvent]
  for (const event of events) window.addEventListener(event, changed)
  return () => { for (const event of events) window.removeEventListener(event, changed) }
}

/** A damaged or hand-written escape must not prevent the workspace rendering. */
export function decodeLocationPart(value: string): string {
  try { return decodeURIComponent(value) }
  catch { return value }
}
