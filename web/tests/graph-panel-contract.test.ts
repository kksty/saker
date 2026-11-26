// The graph node panel's state vocabulary: readiness is only asked while a
// direction can still run; the header and child rows speak the operator's words
// instead of graph enums; the latest run line and the collapsible warnings keep
// the panel from burying its own answer. These are source contracts because the
// panel is a React tree loaded lazily with API hooks; the pure rules behind the
// readiness gate are unit-tested in intent-detail.test.ts.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const panel = await readFile(
  fileURLToPath(new URL('../src/components/graph/GraphNodePanel.tsx', import.meta.url)),
  'utf8',
)

test('readiness is not polled for a direction that cannot run', () => {
  assert.match(panel, /readinessApplies\(intent\?\.status, intent\?\.hold\)/)
  assert.match(panel, /showReadiness \? 10000 : 0/)
  assert.match(panel, /readinessNotice\(intent\.status, intent\.hold\)/)
})

test('the header uses operator words', () => {
  assert.match(panel, /nodeTypeLabel\(node\.type, fact\?\.kind\)/)
})

test('the latest run line and the collapsible warnings are wired', () => {
  assert.match(panel, /<RunSummaryLine run=\{latestRun\} onOpenRun=\{onOpenRun\} \/>/)
  assert.match(panel, /<details className="group rounded-lg border border-warning\/25/)
  assert.match(panel, /group-open:hidden/)
})

// A stale handoff version is not a generic failure: refresh the intent and the
// handoff record, keep the operator's note, and submit again only after review.
test('a handoff version conflict refreshes the record and keeps the input', () => {
  assert.match(panel, /code === 'handoff_conflict'/)
  assert.match(panel, /const fresh = await api\.intent\(intent\.id\)/)
  assert.match(panel, /setHandoffVersion\(latest\)/)
  assert.match(panel, /说明已保留/)
  assert.match(panel, /setHandoffOpen\(true\)/)
})
