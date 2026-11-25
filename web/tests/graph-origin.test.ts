// P0-9c 阶段2/3：图谱「发起者样式」与「原生助手层」。
//
// The graph has two channels: status colour (unchanged) and provenance shape.
// This file pins the derived semantics — the pure model and the render shapes —
// without a DOM:
//   - created_by classification (commander / operator / executor / unknown)
//   - the derived 发起边 (executor peer launches) exists, is not a degree input
//     and changes neither structural depth nor either layout
//   - native-subagent aggregates come from the backend Run fields, never from a
//     journal decode, and never invent a helper when no call was recorded
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildGraph,
  edgeKindLabel,
  ghostID,
  intentOrigin,
  originLabel,
  stepID,
} from '../src/graph/model.ts'
import type { GraphNode } from '../src/graph/model.ts'
import { originMarker } from '../src/graph/render.ts'
import { ForceSim } from '../src/graph/sim.ts'
import type { Intent, Run } from '../src/lib/types.ts'

function intent(over: Partial<Intent> = {}): Intent {
  return {
    id: 'in_task',
    title: 'task',
    description: '',
    status: 'running',
    attempts: 1,
    created_at: '2026-11-11T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function run(over: Partial<Run> = {}): Run {
  return {
    id: 'r_task',
    intent_id: 'in_task',
    node: 'local',
    status: 'done',
    started_at: '2026-11-11T00:00:00Z',
    events: 0,
    declared_facts: 0,
    ...over,
  }
}

/** The parts a layout comparison must not move. */
function laidOut(model: ReturnType<typeof buildGraph>): string {
  return JSON.stringify(model.nodes.map((n) => [n.id, n.depth, n.degree]))
}

// ---- 发起者分类 -----------------------------------------------------------

test('intentOrigin reads only created_by and never guesses a fourth role', () => {
  assert.equal(intentOrigin(intent({ created_by_participant_id: 'commander' })), 'commander')
  assert.equal(intentOrigin(intent({ created_by_participant_id: 'operator' })), 'operator')
  assert.equal(intentOrigin(intent({ created_by_participant_id: 'pt_in_abc123' })), 'executor')
  // Legacy/absent rows and the bare prefix are unknown, not an invented role.
  assert.equal(intentOrigin(intent({})), 'unknown')
  assert.equal(intentOrigin(intent({ created_by_participant_id: '' })), 'unknown')
  assert.equal(intentOrigin(intent({ created_by_participant_id: '   ' })), 'unknown')
  assert.equal(intentOrigin(intent({ created_by_participant_id: 'pt_' })), 'unknown')
  assert.equal(intentOrigin(intent({ created_by_participant_id: 'someone' })), 'unknown')
  // Copy is part of the same contract as edgeKindLabel.
  assert.equal(originLabel('commander'), '指挥官')
  assert.equal(originLabel('operator'), '操作员')
  assert.equal(originLabel('executor'), '执行者')
  assert.equal(originLabel('unknown'), '未记录')
  assert.equal(originLabel(undefined), '未记录')
})

test('the second channel is shape, and unknown/legacy draws nothing', () => {
  assert.equal(originMarker('commander'), 'diamond')
  assert.equal(originMarker('executor'), 'circle')
  assert.equal(originMarker('operator'), 'double-ring')
  assert.equal(originMarker('unknown'), null)
  assert.equal(originMarker(undefined), null)
})


// ---- 发起边（派生，非账本） -------------------------------------------------

function originEdges(model: ReturnType<typeof buildGraph>) {
  return model.edges.filter((e) => e.kind === 'origin')
}





// ---- 原生助手聚合（后端字段，无 journal 解码） -----------------------------

test('helper aggregates come from the latest executor run that actually called one', () => {
  const runs = [
    run({
      id: 'r_old',
      intent_id: 'in_task',
      started_at: '2026-11-11T00:00:00Z',
      native_subagent_calls: 2,
      native_subagent_active: 0,
      native_subagent_duration_ms: 1500,
    }),
    // A newer executor run with no helper does not erase the last recorded turn.
    run({ id: 'r_new_empty', intent_id: 'in_task', started_at: '2026-11-12T00:00:00Z' }),
    // A support run's helper count is not this direction's turn.
    run({
      id: 'r_verifier',
      intent_id: 'in_task',
      purpose: 'verifier',
      started_at: '2026-11-13T00:00:00Z',
      native_subagent_calls: 9,
      native_subagent_active: 9,
      native_subagent_duration_ms: 9000,
    }),
  ]
  const graph = buildGraph([], [intent()], [], undefined, runs)
  const step = graph.byId.get(stepID('in_task'))!
  assert.deepEqual(step.helpers, { calls: 2, active: 0, durationMs: 1500 })
  assert.deepEqual(graph.byId.get(ghostID('in_task'))!.helpers, step.helpers)

  // A newer executor run with calls wins (active/duration come from that run).
  const live = buildGraph(
    [],
    [intent()],
    [],
    undefined,
    [
      ...runs,
      run({
        id: 'r_live',
        intent_id: 'in_task',
        started_at: '2026-11-14T00:00:00Z',
        native_subagent_calls: 3,
        native_subagent_active: 1,
        native_subagent_duration_ms: 400,
      }),
    ],
  )
  assert.deepEqual(live.byId.get(stepID('in_task'))!.helpers, { calls: 3, active: 1, durationMs: 400 })
})

test('no helper record means no field at all — nothing is invented', () => {
  const graph = buildGraph(
    [],
    [intent()],
    [],
    undefined,
    [
      run({ id: 'r_plain', intent_id: 'in_task' }),
      run({ id: 'r_verifier', intent_id: 'in_task', purpose: 'verifier', native_subagent_calls: 4 }),
    ],
  )
  for (const node of graph.nodes as GraphNode[]) {
    assert.equal(node.helpers, undefined, `${node.id} must not carry an empty helper block`)
  }
  assert.equal(JSON.stringify(graph.nodes).includes('helpers'), false)
})
