// The bottom-left stack layout: three cards with an explicit size budget.
//
// The old column was 12.5–17rem wide and let the legend and commander grow at
// each other's expense: opening the legend squeezed the commander to a strip,
// and the 52% height budget could not hold all three cards. The contract now is:
// the target and the commander stay whole, the legend between them takes only
// the leftover height with a self-scrolling body, and the commander is pinned
// last at the bottom of the column.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const read = (path: string) => readFile(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
const panel = await read('../src/components/graph/GraphPanel.tsx')
const legend = await read('../src/components/graph/GraphLegend.tsx')
const commander = await read('../src/components/CommanderCard.tsx')
const css = await read('../src/app.css')

test('the stack is one bounded column with room for all three cards', () => {
  assert.match(panel, /saker-graph-corner-bl saker-graph-dock saker-graph-stack absolute flex flex-col/)
  assert.match(panel, /saker-scroll flex min-h-0 flex-1 flex-col gap-1\.5 overflow-y-auto/)
  assert.match(css, /\.saker-graph-stack \{[\s\S]*?inline-size: clamp\(16rem, 18cqi, 20rem\)/)
  assert.match(css, /\.saker-graph-stack \{[\s\S]*?max-block-size: max\(3\.5rem, calc\(var\(--graph-overlay-height\) \* 0\.72\)\)/)
})

test('the commander card sits at the bottom, below the target and legend', () => {
  const goal = panel.indexOf('终点</span>')
  const legendAt = panel.indexOf('<GraphLegend />')
  const commanderAt = panel.indexOf('<CommanderCard board={board}')
  assert.ok(goal >= 0 && legendAt > goal && commanderAt > legendAt, 'goal → legend → commander')
  assert.match(commander, /w-full shrink-0 overflow-hidden rounded-xl/)
  // Two compact one-line goals with a 管理 action each; a third and beyond is
  // summed into a link to the state page instead of growing the card.
  assert.match(panel, /openGoals\.slice\(0, 2\)\.map\(\(g\) =>/)
  assert.match(panel, /onClick=\{\(\) => setEditingGoal\(g\)\}/)
  assert.match(panel, /另有 \{openGoals\.length - 2\} 个未达成目标/)
})

test('a narrow map hides the stack under an open node panel, like the filter card', () => {
  assert.match(
    css,
    /\.saker-graph-stage:has\(\.saker-graph-panel, \[data-graph-chat\]\) \.saker-graph-filter,\s*\.saker-graph-stage:has\(\.saker-graph-panel, \[data-graph-chat\]\) \.saker-graph-stack \{[\s\S]*?display: none;/,
  )
})

test('an expanded legend takes the column remainder and scrolls its own body', () => {
  assert.match(legend, /saker-graph-dock flex min-h-10 w-full flex-col/)
  assert.match(legend, /open \? 'min-h-40 flex-1' : 'shrink-0'/)
  assert.match(legend, /saker-scroll saker-graph-legend-body min-h-0 flex-1 overflow-y-auto/)
  assert.match(css, /\.saker-graph-legend-body \{[\s\S]*?overscroll-behavior: contain/)
})
