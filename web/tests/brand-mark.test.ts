// The Saker wordmark lives in three renderers: the React component, the tab icon
// (public/favicon.svg) and the login page, which is Go HTML with its own copy in
// cmd/saker/auth.go. Nothing at build time ties them together, so the copies can
// drift silently: a changed glyph would show up on the rail and not in the tab,
// and no other suite would notice.
//
// This pins the drawing itself (the path strings the art is made of), not the
// styling around it: colour, size and the surrounding markup stay free.
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

test('the rail mark renders as one labelled image with no text fallback', () => {
  const html = renderToStaticMarkup(React.createElement(Wordmark, { width: 148 }))
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

test('the mark keeps one aspect ratio and respects the 120px floor', () => {
  const size = (html: string) => ({
    w: Number(/width="(\d+)"/.exec(html)?.[1]),
    h: Number(/height="(\d+)"/.exec(html)?.[1]),
  })
  const small = size(renderToStaticMarkup(React.createElement(Wordmark, { width: 148 })))
  const large = size(renderToStaticMarkup(React.createElement(Wordmark, { width: 296 })))
  assert.ok(large.h > small.h)
  // Same ratio: the rail and the login card show one mark, not two. Height is
  // rounded to whole pixels, so compare with that rounding allowance.
  const ratio = (s: { w: number; h: number }) => s.w / s.h
  assert.ok(Math.abs(ratio(small) - ratio(large)) < 0.2, `${ratio(small)} vs ${ratio(large)}`)
  assert.ok(Math.abs(ratio(small) - width / 100) < 0.2)
  // Below this the S curves blur, per the artwork's own spec; the rail and the
  // compact header must both stay at or above it.
  assert.ok(minWidth >= 120)
  assert.ok(size(renderToStaticMarkup(React.createElement(Wordmark, {}))).w >= minWidth)
  assert.ok(size(renderToStaticMarkup(React.createElement(Brand, {}))).w >= minWidth)
  assert.ok(size(renderToStaticMarkup(React.createElement(Brand, { width: minWidth }))).w >= minWidth)
})

test('public/favicon.svg carries the same S as the rail', () => {
  const favicon = readFileSync(`${webRoot}public/favicon.svg`, 'utf8')
  assert.match(favicon, /<svg[^>]*role="img"[^>]*aria-label="Saker"/)
  // A tab strip can be light or dark, and an icon has no tile of its own: the
  // letter has to pick its own ink in both.
  assert.match(favicon, /prefers-color-scheme:\s*dark/)
  assert.ok(favicon.includes(glyphs[0].stroke!), 'favicon is missing the S path')
  assert.match(favicon, new RegExp(`stroke-width="${stroke}"`))
})

test('the Go login page carries the whole wordmark', () => {
  const auth = readFileSync(`${repoRoot}cmd/saker/auth.go`, 'utf8')
  for (const glyph of glyphs) {
    const d = glyph.fill ?? glyph.stroke!
    assert.ok(auth.includes(d), `login page is missing ${glyph.ch}`)
  }
  assert.match(auth, /aria-label="Saker"/)
  // The Kaiti/gradient lockup this replaced must not linger: it made the name
  // look like the host's font inventory.
  assert.doesNotMatch(auth, /Kaiti SC/)
})
