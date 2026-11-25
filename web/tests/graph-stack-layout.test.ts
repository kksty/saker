// The bottom-left stack is one scroll container with three complete cards. A
// layout regression here is what made an expanded 指挥官 card squeeze the legend
// into a ~50px strip: the legend was the only flexible child, so flex shrank it
// instead of the stack scrolling. The cards are read as source contracts because
// the map is a canvas + lazy React tree that node tests cannot lay out.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const read = (path: string) => readFile(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
const panel = await read('../src/components/graph/GraphPanel.tsx')
const legend = await read('../src/components/graph/GraphLegend.tsx')
const commander = await read('../src/components/CommanderCard.tsx')
const css = await read('../src/app.css')

test('the stack scrolls, so each card inside it keeps its height', () => {
  // The wrapper is the bounded scroll container; its cards must not shrink.
  assert.match(panel, /tw-graph-corner-bl tw-graph-dock tw-graph-stack absolute flex flex-col/)
  assert.match(panel, /tw-scroll flex min-h-0 flex-1 flex-col gap-1\.5 overflow-y-auto/)
  assert.match(css, /\.tw-graph-stack \{[\s\S]*?max-block-size:/)
  assert.match(css, /\.tw-graph-stack > \.tw-scroll/)
})

test('the legend cannot be the child that gives up its height', () => {
  // 指挥官 and the goal card were already shrink-0; the legend was not, which is
  // exactly why it collapsed while the others stayed whole.
  assert.match(legend, /tw-graph-dock flex min-h-10 w-full shrink-0 flex-col/)
  assert.match(commander, /w-full shrink-0 overflow-hidden rounded-xl/)
  assert.match(panel, /w-full shrink-0 rounded-xl border border-hairline bg-card\/95/)
})

test('an expanded legend is bounded and scrolls its own body', () => {
  // Complete card, bounded content: the body scrolls instead of growing the
  // stack by its full ~2000px list or being clipped by a neighbour.
  assert.match(legend, /tw-scroll max-h-64 min-h-0 flex-1 overflow-y-auto/)
  assert.match(legend, /aria-expanded=\{open\}/)
})
