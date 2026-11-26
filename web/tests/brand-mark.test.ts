// The Saker brand is one wordmark, drawn in two renderers: the React component
// and the login page, which is Go HTML with its own copy in cmd/saker/auth.go.
// Nothing at build time ties the copies together, so they can drift silently:
// the rail and the login card would disagree and no other suite would notice.
//
// The tab icon is deliberately empty (no link in either page), so this file also
// pins that neither renderer re-adds one.
//
// This pins the drawing itself (the path strings and numbers the art is made
// of), not the styling around it: the surrounding markup stays free.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const repoRoot = fileURLToPath(new URL('../..', import.meta.url))

type Glyph = { ch: string; offset: number; width: number; fill?: string; stroke?: string }

// The login card draws the wordmark at 132px of ink (36px tall) inside its
// 340px card; the rail draws it at 120 and the compact header at 104.
const LOGIN_WORDMARK_WIDTH = 132
const LOGIN_WORDMARK_HEIGHT = 36

let Wordmark: React.ComponentType<{ width?: number }>
let Brand: React.ComponentType<{ width?: number }>
let glyphs: Glyph[]
let width: number
let stroke: number
let minWidth: number

before(async () => {
  const server = await createTestServer({ root: webRoot })
  try {
    const mod = await server.ssrLoadModule('/src/components/Wordmark.tsx')
    Wordmark = mod.Wordmark
    glyphs = mod.WORDMARK_GLYPHS
    width = mod.WORDMARK_WIDTH
    stroke = mod.WORDMARK_STROKE
    minWidth = mod.WORDMARK_MIN_WIDTH
    Brand = (await server.ssrLoadModule('/src/components/Brand.tsx')).Brand
  } finally {
    await server.close()
  }
})

test('the wordmark spells SAKER as five drawn glyphs', () => {
  assert.deepEqual(glyphs.map(g => g.ch), ['S', 'A', 'K', 'E', 'R'])
  // Every glyph is geometry, never a text node: the mark must not depend on a
  // font the operator's machine happens to have.
  for (const glyph of glyphs) {
    assert.ok(glyph.fill || glyph.stroke, `${glyph.ch} has no drawing`)
    assert.ok(glyph.width > 0, `${glyph.ch} has no box`)
  }
  // The boxes tile left to right and end exactly at the documented ink width.
  assert.equal(glyphs[0].offset, 0)
  for (let i = 1; i < glyphs.length; i++) {
    assert.ok(glyphs[i].offset >= glyphs[i - 1].offset + glyphs[i - 1].width, `${glyphs[i].ch} overlaps`)
  }
  const last = glyphs[glyphs.length - 1]
  assert.equal(last.offset + last.width, width)
})

test('the brand renders as one labelled image with no text fallback', () => {
  const html = renderToStaticMarkup(React.createElement(Wordmark, { width: 120 }))
  assert.match(html, /role="img"/)
  assert.match(html, /aria-label="Saker"/)
  assert.doesNotMatch(html, />Saker</, 'the name must not also be typeset beside the drawing')
  for (const glyph of glyphs) {
    const d = glyph.fill ?? glyph.stroke!
    assert.ok(html.includes(d), `${glyph.ch} is missing from the render`)
  }
  // Each glyph is clipped to its own box so the letters sit flush on the cap
  // line and baseline; losing a clip lets K's diagonals and R's leg overshoot.
  assert.equal((html.match(/<clipPath/g) ?? []).length, glyphs.length)
})

test('the wordmark keeps one aspect ratio and respects the 96px floor', () => {
  const size = (html: string) => ({
    w: Number(/width="(\d+)"/.exec(html)?.[1]),
    h: Number(/height="(\d+)"/.exec(html)?.[1]),
  })
  const small = size(renderToStaticMarkup(React.createElement(Wordmark, { width: 120 })))
  const large = size(renderToStaticMarkup(React.createElement(Wordmark, { width: 296 })))
  assert.ok(large.h > small.h)
  // Same ratio: the rail and the login card show one mark, not two. Height is
  // rounded to whole pixels, so compare with that rounding allowance.
  const ratio = (s: { w: number; h: number }) => s.w / s.h
  assert.ok(Math.abs(ratio(small) - ratio(large)) < 0.2, `${ratio(small)} vs ${ratio(large)}`)
  assert.ok(Math.abs(ratio(small) - width / 100) < 0.2)
  // Below this the S curves blur, per the artwork's own spec; the rail, the
  // compact header and the login card must all stay at or above it.
  assert.equal(minWidth, 96)
  const defaultInk = size(renderToStaticMarkup(React.createElement(Wordmark, {}))).w
  assert.equal(defaultInk, 120)
  assert.ok(defaultInk >= minWidth)
  const brandInk = size(renderToStaticMarkup(React.createElement(Brand, {}))).w
  assert.equal(brandInk, 120)
  assert.ok(brandInk >= minWidth)
  // One drawing only: the corner shows the name, not a name beside a mark.
  const brandHTML = renderToStaticMarkup(React.createElement(Brand, {}))
  assert.equal((brandHTML.match(/<svg/g) ?? []).length, 1, 'the brand must be the wordmark alone')
  // The rail's brand is never aria-hidden: it is the one thing that announces
  // the console's name on the page.
  assert.match(brandHTML, /role="img"/)
  assert.match(brandHTML, /aria-label="Saker"/)
  assert.ok(size(renderToStaticMarkup(React.createElement(Brand, { width: minWidth }))).w >= minWidth)
})

test('the tab icon is left empty in both HTML entry points', () => {
  // No favicon means the browser shows its own default page tab. A custom icon
  // was tried and dropped; the important part is that neither entry point
  // points at a file that no longer exists (a broken <link> still costs a 404
  // on every page load).
  for (const file of [`${webRoot}index.html`, `${repoRoot}cmd/saker/auth.go`]) {
    const html = readFileSync(file, 'utf8')
    assert.doesNotMatch(html, /rel="icon"/, `${file} still links a tab icon`)
    assert.doesNotMatch(html, /favicon/, `${file} still references a favicon file`)
  }
})

test('the Go login page carries the whole wordmark and no mark beside it', () => {
  const auth = readFileSync(`${repoRoot}cmd/saker/auth.go`, 'utf8')
  for (const glyph of glyphs) {
    const d = glyph.fill ?? glyph.stroke!
    assert.ok(auth.includes(d), `login page is missing ${glyph.ch}`)
  }
  assert.ok(auth.includes(`width="${LOGIN_WORDMARK_WIDTH}" height="${LOGIN_WORDMARK_HEIGHT}"`))
  assert.match(auth, /aria-label="Saker"/)
  // The login card shows the name alone, like the rail: no icon beside it.
  assert.doesNotMatch(auth, /\.lockup\{/, 'the lockup wrapper was only there to pair the wordmark with a mark')
  // The Kaiti/gradient lockup this replaced must not linger: it made the name
  // look like the host's font inventory.
  assert.doesNotMatch(auth, /Kaiti SC/)
})
