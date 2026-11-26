// FA3 global stylesheet contract.
//
// `app.css` is the design system's single file: the cyber-ui tokens, the one
// card/type ramp, and the global behaviours every panel inherits. It is also the
// file where a well-meaning cleanup can silently move the product's visual
// identity, so this suite pins two things at once:
//
//   1. the tokens (and therefore the cyber-ui look) are byte-for-value stable;
//   2. the modern platform behaviours the graph/global pass added are present,
//      and the patterns the guidance explicitly warns against are absent.
//
// It is a contract test, not a rendering test: jsdom is not part of this repo's
// test stack, so the file is the unit under test.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Comments are stripped first: the file documents the pitfalls it avoids, so a
// naive `includes('100vh')` would match the explanation rather than the code.
const css = readFileSync(fileURLToPath(new URL('../src/app.css', import.meta.url)), 'utf8').replace(
  /\/\*[\s\S]*?\*\//g,
  '',
)

function token(name: string, scope: 'light' | 'dark'): string | null {
  // The light tokens are the first `:root { … }`, the dark ones are `.dark { … }`.
  const start = scope === 'light' ? css.indexOf(':root {') : css.indexOf('.dark {')
  assert.ok(start >= 0, `${scope} token block is reachable`)
  const end = css.indexOf('\n}', start)
  const block = css.slice(start, end)
  const match = new RegExp(`${name}:\\s*([^;]+);`).exec(block)
  return match ? match[1].trim() : null
}

test('the cyber-ui tokens keep their exact values in both themes', () => {
  // A representative cross-section: background/card/primary/ink/radius. If a
  // component pass ever needs a new colour it adds one; it never retunes these.
  assert.equal(token('--radius', 'light'), '0.5rem')
  assert.equal(token('--background', 'light'), '#f4f5f7')
  assert.equal(token('--card', 'light'), '#ffffff')
  assert.equal(token('--primary', 'light'), '#c92a2a')
  assert.equal(token('--ink-1', 'light'), '#14161b')
  assert.equal(token('--border', 'light'), '#dfe2e8')

  assert.equal(token('--background', 'dark'), '#0a0c10')
  assert.equal(token('--surface-1', 'dark'), '#0d1015')
  assert.equal(token('--card', 'dark'), '#11151c')
  assert.equal(token('--primary', 'dark'), '#f2554d')
  assert.equal(token('--primary-solid', 'dark'), '#c93430')
  assert.equal(token('--ink-1', 'dark'), '#e7eaf0')
  assert.equal(token('--review', 'dark'), '#2dd4bf')
})

test('the shell declares its colour scheme and never reintroduces viewport units', () => {
  assert.match(css, /color-scheme:\s*dark/)
  assert.match(css, /html:not\(\.dark\)\s*\{[^}]*color-scheme:\s*light/)
  // 100vh was the bug: it ignores the page header and the mobile URL bar. The
  // stage uses container units instead, so the file must not contain it at all.
  assert.equal(css.includes('100vh'), false, 'no 100vh in the shell stylesheet')
  assert.equal(css.includes('100vw'), false, 'no 100vw in the shell stylesheet')
})

test('the app shell handles the dynamic viewport and the device safe area', () => {
  // Dynamic viewport units, with the static 100% kept as the fallback.
  assert.match(css, /@supports \(height: 100dvh\)\s*\{[\s\S]*?height:\s*100dvh/)
  assert.match(css, /env\(safe-area-inset-bottom, 0px\)/)
  assert.match(css, /env\(safe-area-inset-top, 0px\)/)
  assert.match(css, /env\(safe-area-inset-right, 0px\)/)
})

test('scroll regions reserve their gutter and never chain their scroll', () => {
  // The viewport stays fixed and leaves the full width to the shell.
  const root = /html\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
  assert.match(root, /overflow:\s*hidden/)
  assert.match(root, /overscroll-behavior:\s*none/)
  assert.doesNotMatch(root, /scrollbar-gutter/)
  // Nested regions contain instead: inner scrolling must not move the page.
  assert.match(css, /\.saker-scroll\s*\{[^}]*overscroll-behavior:\s*contain/)
  assert.match(css, /\.saker-scroll\s*\{[^}]*scrollbar-gutter:\s*stable/)
  assert.match(css, /\.saker-graph-overlay-scroll\s*\{[^}]*overscroll-behavior:\s*contain/)
})

test('the graph stage is a size container, not a viewport breakpoint', () => {
  assert.match(css, /container:\s*graph\s*\/\s*size/)
  // Inline- and block-size container units for the overlays.
  assert.match(css, /100cqi/)
  assert.match(css, /100cqb/)
  // Every graph overlay decision the old code made with `sm:`/`md:` is a
  // container query now.
  // (The toolbar has no wide-map variant any more: it is the same icon row at
  // every width, so there is no >= 42rem rule left to pin.)
  for (const query of ['inline-size < 40rem', 'inline-size < 44rem', 'inline-size < 38rem']) {
    assert.ok(css.includes(query), `container query for ${query}`)
  }
  assert.match(css, /@container graph \(inline-size >= 44rem\)/)
  // Coarse pointers get the 24px minimum target for the dock resize handle.
  assert.match(css, /@media \(pointer: coarse\)\s*\{[^}]*saker-dock-handle/)
  // The bottom-left overlay stack is bounded to the map box, so an expanded
  // legend scrolls inside the map instead of being clipped under the page
  // banner with its collapse control unreachable.
  assert.match(css, /--graph-overlay-height:\s*calc\(100cqb[^;]*--graph-footer-space/)
  assert.match(css, /\.saker-graph-stack\s*\{[^}]*max-block-size:[^;]*--graph-overlay-height/)
})

test('reduced motion is a global backstop, not the forbidden duration override', () => {
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)/)
  // Persistent (infinite) decorative motion stops...
  assert.match(css, /animation-iteration-count:\s*1 !important/)
  // ...while colour feedback stays, which is what the guidance asks for.
  assert.match(css, /transition:\s*color 120ms ease, background-color 120ms ease, border-color 120ms ease/)
  // The guidance calls out a global `animation-duration: 0.01ms` as actively
  // harmful; the backstop must never use it.
  assert.equal(/animation-duration:\s*0?\.0*1m?s/.test(css), false, 'no global 0.01ms duration override')
})

test('navigation and page bodies are not animated', () => {
  // View Transitions are gone on purpose: the browser snapshotted the page
  // before React committed the route change, so the cross-fade ran between two
  // identical frames and the real change appeared after it (a page-wide flash),
  // while adding a snapshot pass plus 120–160ms to every switch.
  assert.equal(css.includes('view-transition'), false, 'no View Transitions rules')
  // The decorative primitives that used to fade/stagger/scan panel content in.
  // Panels, rows and notices now appear in place, instantly.
  for (const legacy of [
    'saker-rise',
    'saker-stagger',
    'saker-scan-track',
    'saker-slide-in',
    'saker-live',
    'saker-breath',
    'saker-ripple',
    'saker-operation-dialog',
    'saker-operation-success',
  ]) {
    assert.equal(css.includes(`.${legacy}`), false, `.${legacy} must stay removed`)
  }
  // One animation is left, and it reports a real in-flight state: the progress
  // bar of a pending destructive action.
  const animations = [...css.matchAll(/animation:\s*(saker-[\w-]+)/g)].map((match) => match[1])
  assert.deepEqual(animations, ['saker-progress'], 'the pending-action progress bar is the only keyframe animation')
  // It still parks under reduced motion.
  assert.match(css, /\.saker-progress-indeterminate\s*\{[^}]*animation:\s*none/)
})
