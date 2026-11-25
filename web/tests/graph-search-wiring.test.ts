// Wiring between the search card, the panel's hit state and the canvas.
//
// The pure ranking (graph-search.test.ts), the card's keyboard/IME behaviour
// (graph-search-ui.test.ts) and the camera arithmetic (graph-camera.test.ts)
// are covered by rendering or unit tests. What remains is the glue that cannot
// be rendered cheaply: GraphPanel is a page-sized component. These source
// contracts pin the three hand-offs that make the feature work — the card gets
// the ranked hits, the canvas gets the active hit id, and Enter reaches
// focusNode with the dock inset.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const read = (path: string) => readFile(fileURLToPath(new URL(path, import.meta.url)), 'utf8')

const panel = await read('../src/components/graph/GraphPanel.tsx')
const filters = await read('../src/components/graph/GraphFilters.tsx')
const canvas = await read('../src/components/GraphCanvas.tsx')
const render = await read('../src/graph/render.ts')

test('the ranked hits feed the card and the panel keeps one active hit', () => {
  assert.match(panel, /searchGraph\(model, query\)/)
  assert.match(panel, /hits=\{search\.hits\}/)
  assert.match(panel, /activeIndex=\{activeHit\}/)
  assert.match(panel, /const activeMatchId = activeHit >= 0/)
  assert.match(panel, /onActivate=\{activateHit\}/)
})

test('locating a hit frames the node and respects the narrow-map rule', () => {
  assert.match(panel, /canvas\.current\?\.focusNode\(hit\.node\.id, \{ rightInset:/)
  assert.match(panel, /const narrow = stageWidth < NARROW_MAP_PX/)
  assert.match(panel, /if \(!narrow\) onSelect\(hit\.node\.id\)/)
  // The listed result and the painted map must be the same subject.
  assert.match(panel, /activeMatch=\{activeMatchId\}/)
})

test('the canvas paints the active hit as a doubled ring', () => {
  assert.match(canvas, /activeMatch\?: string \| null/)
  assert.match(canvas, /activeMatch: state\.activeMatch/)
  assert.match(canvas, /focusNode: \(id: string, options/)
  assert.match(render, /const isActiveMatch = o\.activeMatch === n\.id/)
  assert.match(render, /ACTIVE_MATCH_RING/)
})

test('the card is a combobox over a listbox and never searches a composing word', () => {
  assert.match(filters, /role="combobox"/)
  assert.match(filters, /role="listbox"/)
  assert.match(filters, /onCompositionStart=/)
  assert.match(filters, /if \(!composing\.current\) onQueryChange/)
})
