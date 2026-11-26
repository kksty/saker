// The Saker wordmark lives in three renderers: the React component, the tab icon
// (public/favicon.svg) and the login page, which is Go HTML with its own copy in
// cmd/saker/auth.go. Nothing at build time ties them together, so the copies can
// drift silently: a changed glyph would show up on the rail and not in the tab,
// and no other suite would notice.
//
// This pins the drawing itself (the point/stroke strings the art is made of),
// not the styling around it: colour, size and the surrounding markup stay free.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const repoRoot = fileURLToPath(new URL('../..', import.meta.url))

let server: Awaited<ReturnType<typeof createTestServer>>
let Wordmark: React.ComponentType<{ cap?: number }>
let Monogram: React.ComponentType<{ cap?: number }>
let glyphs: { ch: string; polys: string[]; strokes: { d: string; w: number }[] }[]

before(async () => {
  server = await createTestServer({ root: webRoot })
  const mod = await server.ssrLoadModule('/src/components/Wordmark.tsx')
  Wordmark = mod.Wordmark
  Monogram = mod.Monogram
  glyphs = mod.WORDMARK_GLYPHS
})

after(async () => {
  await server?.close()
})

const drawing = () => glyphs.flatMap(g => [...g.polys, ...g.strokes.map(s => `${s.d}@${s.w}`)])

test('the wordmark spells SAKER as five drawn glyphs', () => {
  assert.deepEqual(glyphs.map(g => g.ch), ['S', 'A', 'K', 'E', 'R'])
  // Every glyph is geometry, never a text node: the mark must not depend on a
  // font the operator's machine happens to have.
  for (const glyph of glyphs) {
    assert.ok(glyph.polys.length + glyph.strokes.length > 0, `${glyph.ch} has no drawing`)
  }
})

test('the rail mark renders as one labelled image with no text fallback', () => {
  const html = renderToStaticMarkup(React.createElement(Wordmark, { cap: 20 }))
  assert.match(html, /role="img"/)
  assert.match(html, /aria-label="Saker"/)
  assert.match(html, /<svg/)
  assert.doesNotMatch(html, />Saker</, 'the name must not also be typeset beside the drawing')
  assert.equal((html.match(/<polygon/g) ?? []).length, glyphs.reduce((n, g) => n + g.polys.length, 0))
})

test('the drawn cap height scales the whole lockup', () => {
  const small = renderToStaticMarkup(React.createElement(Wordmark, { cap: 16 }))
  const large = renderToStaticMarkup(React.createElement(Wordmark, { cap: 32 }))
  const height = (html: string) => Number(/height="(\d+)"/.exec(html)?.[1])
  assert.ok(height(large) > height(small))
  // Same aspect ratio: the rail and the login card show one mark, not two.
  const ratio = (html: string) => Number(/width="(\d+)"/.exec(html)?.[1]) / height(html)
  assert.equal(ratio(small).toFixed(2), ratio(large).toFixed(2))
})

test('the monogram is the wordmark S, not a second drawing', () => {
  const html = renderToStaticMarkup(React.createElement(Monogram, { cap: 16 }))
  assert.match(html, /aria-label="Saker"/)
  for (const points of glyphs[0].polys) assert.ok(html.includes(points), `monogram is missing ${points}`)
})

test('public/favicon.svg carries the same S as the rail', () => {
  const favicon = readFileSync(`${webRoot}public/favicon.svg`, 'utf8')
  assert.match(favicon, /<svg[^>]*role="img"[^>]*aria-label="Saker"/)
  // A tab strip can be light or dark, and an icon has no tile of its own: the
  // letter has to pick its own ink in both.
  assert.match(favicon, /prefers-color-scheme:\s*dark/)
  for (const points of glyphs[0].polys) assert.ok(favicon.includes(points), `favicon is missing ${points}`)
})

test('the Go login page carries the whole wordmark', () => {
  const auth = readFileSync(`${repoRoot}cmd/saker/auth.go`, 'utf8')
  for (const glyph of glyphs) {
    for (const points of glyph.polys) assert.ok(auth.includes(points), `login page is missing ${glyph.ch}: ${points}`)
    for (const stroke of glyph.strokes) {
      assert.ok(auth.includes(`d="${stroke.d}"`), `login page is missing ${glyph.ch} stroke ${stroke.d}`)
    }
  }
  assert.match(auth, /aria-label="Saker"/)
  // The Kaiti/gradient lockup this replaced must not linger: it made the name
  // look like the host's font inventory.
  assert.doesNotMatch(auth, /Kaiti SC/)
})
