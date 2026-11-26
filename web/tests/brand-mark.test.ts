import { before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ConsoleRail as ConsoleRailComponent } from '../src/components/ConsoleRail.tsx'
import { createTestServer } from './helpers/vite.ts'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const index = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const auth = readFileSync(new URL('../../cmd/saker/auth.go', import.meta.url), 'utf8')

let ConsoleRail: typeof ConsoleRailComponent

before(async () => {
  const server = await createTestServer({ root: webRoot })
  try {
    ConsoleRail = (await server.ssrLoadModule('/src/components/ConsoleRail.tsx')).ConsoleRail
  } finally {
    await server.close()
  }
})

test('the rail and compact header expose the shared wordmark at their intended widths', () => {
  // The compact header and desktop rail render separately. Both need an
  // accessible name, even though the artwork is now a CSS mask.
  const html = renderToStaticMarkup(React.createElement(ConsoleRail, {
    title: '项目', active: 'overview', children: null, footer: null,
  }))
  assert.equal((html.match(/role="img" aria-label="Saker"/g) ?? []).length, 2)
  assert.match(html, /class="saker-wordmark[^"]*" role="img" aria-label="Saker" style="width:104px"/)
  assert.match(html, /class="saker-wordmark[^"]*" role="img" aria-label="Saker" style="width:120px"/)

  // The login and console previously had separate drawings that could drift.
  const css = readFileSync(new URL('../src/app.css', import.meta.url), 'utf8')
  const svg = readFileSync(new URL('../public/saker-wordmark.svg', import.meta.url), 'utf8')
  assert.match(css, /mask: url\('\/saker-wordmark\.svg'\)/)
  assert.match(svg, /viewBox="0 0 246 54"/)
  assert.match(css, /aspect-ratio: 246 \/ 54/)
})

test('the Go login uses the shared drawing and preserves its accessible name', () => {
  assert.match(auth, /<img class="wordmark" src="\/static\/saker-wordmark\.svg" width="132" height="29" alt="Saker">/)
})

test('the console and login link to the same tab icon', () => {
  // Both entry points need an explicit icon link or the browser uses its default.
  assert.match(index, /<link rel="icon" type="image\/svg\+xml" href="\/saker-favicon\.svg"/)
  assert.match(auth, /<link rel="icon" type="image\/svg\+xml" href="\/static\/saker-favicon\.svg"/)
  const icon = readFileSync(new URL('../public/saker-favicon.svg', import.meta.url), 'utf8')
  assert.match(icon, /viewBox="0 0 64 64"/)
})
