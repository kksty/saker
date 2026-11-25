import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildGraph,
  edgeFocus,
  edgeKindLabel,
  focusTarget,
  ghostID,
  mainPath,
  nodeTypeLabel,
  stepID,
} from '../src/graph/model.ts'
import type { GraphEdge, GraphModel, GraphNode } from '../src/graph/model.ts'
import type { Fact, Goal, Intent, Run } from '../src/lib/types.ts'

function fact(id: string, over: Partial<Fact> = {}): Fact {
  return {
    id,
    intent_id: 'in_task',
    kind: 'note',
    title: id,
    summary: '',
    confidence: 'observed',
    status: 'unverified',
    claim_version: 1,
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

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

function goal(over: Partial<Goal> = {}): Goal {
  return {
    id: 'g_1',
    statement: '达成目标',
    met: true,
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function sortedAdjacency(m: Map<string, string[]>): Map<string, string[]> {
  return new Map([...m].map(([k, v]) => [k, [...v].sort()]))
}

test('a premise is marked blocked unless admission could accept it', () => {  const graph = buildGraph(
    [
      fact('f_unverified', { kind: 'host', status: 'unverified' }),
      fact('f_verified', { kind: 'host', status: 'verified' }),
      // A note can never be a necessary premise, whatever its status says
      // (model.ReviewableKind): this is the "存在一条边≠已被证明" case.
      fact('f_note', { kind: 'note', status: 'verified' }),
    ],
    [
      intent({ id: 'in_gated', status: 'pending', from_fact_ids: ['f_unverified'] }),
      intent({ id: 'in_open', status: 'pending', from_fact_ids: ['f_verified'] }),
      intent({ id: 'in_note', status: 'pending', from_fact_ids: ['f_note'] }),
    ],
    [],
    undefined,
    [],
    '',
    [],
    [],
  )
  const premise = (target: string) =>
    graph.edges.find((e) => e.target === stepID(target) && e.kind === 'premise')
  assert.equal(premise('in_gated')?.blocked, true, 'unverified host premise must be blocked')
  assert.equal(premise('in_open')?.blocked, false, 'verified reviewable premise is a usable gate')
  assert.equal(premise('in_note')?.blocked, true, 'an unreviewable kind can never satisfy the gate')
})

// A policy stop is not a verdict: the ledger's hold flag has to reach the step,
// its ghost and the produced edge, or the map can only say "failed" for both.
test('hold marks the step, its ghost and the produced edge as stopped', () => {
  const held = buildGraph([], [intent({ id: 'in_held', status: 'failed', hold: true })], [])
  assert.equal(held.byId.get(stepID('in_held'))?.stopped, true)
  assert.equal(held.byId.get(ghostID('in_held'))?.stopped, true)
  const produced = held.edges.find((e) => e.kind === 'intent' && e.intent?.id === 'in_held')
  assert.equal(produced?.stopped, true)

  // A direction stopped before it ever dispatched is a pending+hold row.
  const queued = buildGraph([], [intent({ id: 'in_held_pending', status: 'pending', hold: true })], [])
  assert.equal(queued.byId.get(stepID('in_held_pending'))?.stopped, true)

  const failed = buildGraph([], [intent({ id: 'in_failed', status: 'failed' })], [])
  assert.equal(failed.byId.get(stepID('in_failed'))?.stopped, false)
})

test('a premise the snapshot cannot open is marked blocked, not a satisfied gate', () => {
  const graph = buildGraph(
    [],
    [intent({ id: 'in_missing', status: 'pending', from_fact_ids: ['f_missing'] })],
    [],
    undefined,
    [],
    '',
    [{ id: 'fact:f_missing', state: 'missing' }],
    [],
  )
  const edge = graph.edges.find((e) => e.target === stepID('in_missing') && e.kind === 'premise')
  assert.equal(edge?.blocked, true, 'a missing premise reference must not look like a satisfied gate')
  const source = graph.byId.get(edge?.source ?? '')
  assert.equal(source?.type, 'boundary', 'the missing source is carried as a boundary leaf')
})

test('pin and reference are distinct relations; the dedupe key carries the kind', () => {
  const graph = buildGraph(
    [fact('f_ref'), fact('f_pin'), fact('f_both')],
    [intent({ from_fact_ids: [] })],
    [],
    undefined,
    [],
    '',
    [],
    [
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
      // The ledger may repeat an edge; same source/target/kind must still dedupe.
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
      { source: 'fact:f_pin', target: 'intent:in_task', kind: 'pin' },
      // One fact can be both read as context and pinned: both relations survive.
      { source: 'fact:f_both', target: 'intent:in_task', kind: 'reference' },
      { source: 'fact:f_both', target: 'intent:in_task', kind: 'pin' },
    ],
  )
  const at = (source: string, kind: string) =>
    graph.edges.filter((e) => e.source === source && e.target === 's:in_task' && e.kind === kind)
  assert.equal(at('f:f_ref', 'reference').length, 1, 'a repeated reference edge is deduped')
  assert.equal(at('f:f_ref', 'pin').length, 0, 'a reference never turns into a pin')
  assert.equal(at('f:f_pin', 'pin').length, 1)
  assert.equal(at('f:f_pin', 'reference').length, 0, 'a pin never turns into a reference')
  assert.equal(at('f:f_both', 'reference').length, 1)
  assert.equal(at('f:f_both', 'pin').length, 1, 'pair+kind dedupe keeps both relations')
  // A pin still names the direction whose panel it opens.
  assert.equal(at('f:f_pin', 'pin')[0].intent?.id, 'in_task')
  assert.equal(edgeKindLabel('reference'), '引用')
  assert.equal(edgeKindLabel('pin'), '固定')
})

test('the model indexes every edge and derives step/fact/incoming/outgoing lookups', () => {
  const produced = fact('f_out')
  const graph = buildGraph(
    [fact('f_pre', { intent_id: 'in_other' }), produced],
    [intent({ from_fact_ids: ['f_pre'] })],
    [],
  )
  // edge.id → edge.
  assert.equal(graph.edgesById.size, new Set(graph.edges.map((e) => e.id)).size)
  for (const e of graph.edges) assert.equal(graph.edgesById.get(e.id), e)
  // intent.id → its step node (the one the canvas selects).
  assert.equal(graph.stepByIntent.get('in_task'), graph.byId.get(stepID('in_task')))
  assert.equal(graph.stepByIntent.get('in_task')?.type, 'step')
  // fact.id (unprefixed) → the fact body, for detail re-fetch.
  assert.equal(graph.factsById.get('f_out'), produced)
  assert.equal(graph.factsById.get('f_pre')?.id, 'f_pre')
  // Adjacency indexes match the edges exactly.
  assert.deepEqual(graph.incoming.get('s:in_task'), ['f:f_pre'])
  assert.deepEqual(graph.outgoing.get('s:in_task'), ['f:f_out'])
  assert.deepEqual(graph.incoming.get('f:f_out'), ['s:in_task'])
  const naiveIn = new Map<string, string[]>()
  const naiveOut = new Map<string, string[]>()
  for (const e of graph.edges) {
    naiveIn.set(e.target, [...(naiveIn.get(e.target) ?? []), e.source])
    naiveOut.set(e.source, [...(naiveOut.get(e.source) ?? []), e.target])
  }
  assert.deepEqual(sortedAdjacency(graph.incoming), sortedAdjacency(naiveIn))
  assert.deepEqual(sortedAdjacency(graph.outgoing), sortedAdjacency(naiveOut))
})

test('host/service pairing still takes the first host with a matching title', () => {
  const graph = buildGraph(
    [
      fact('h_first', { kind: 'host', title: '10.0.0.5' }),
      fact('h_second', { kind: 'host', title: '10.0.0.5' }),
      fact('svc', { kind: 'service', title: '10.0.0.5:8080' }),
      fact('svc_orphan', { kind: 'service', title: '10.9.9.9:443' }),
    ],
    [],
    [],
  )
  const hostEdges = graph.edges.filter((e) => e.kind === 'host')
  assert.equal(hostEdges.length, 1, 'only the service with a known host is paired')
  assert.equal(hostEdges[0].source, 'f:h_first', 'the first host fact wins, as before')
  assert.equal(hostEdges[0].target, 'f:svc')
})

test('goal evidence ownership takes the first fact carrying that evidence', () => {
  const first = fact('f_first', { evidence_ids: ['e_shared'] })
  const second = fact('f_second', { evidence_ids: ['e_shared'] })
  const graph = buildGraph(
    [first, second],
    [],
    [goal({ evidence_id: 'e_shared' })],
  )
  const goalEdges = graph.edges.filter((e) => e.kind === 'goal')
  assert.equal(goalEdges.length, 1)
  assert.equal(goalEdges[0].source, 'f:f_first')
  assert.equal(goalEdges[0].target, 'g:g_1')
  // No owner → the brief fallback edge, not a guessed goal line.
  const fallback = buildGraph([], [], [goal({ evidence_id: 'e_missing' })])
  assert.equal(fallback.edges.filter((e) => e.kind === 'goal').length, 0)
  assert.equal(fallback.edges.filter((e) => e.kind === 'brief').length, 1)
})

test('an unmet goal stays off the map, unconnected, until it is achieved', () => {
  const graph = buildGraph([fact('f_out')], [intent()], [goal({ met: false })])
  assert.equal(graph.byId.has('g:g_1'), false, 'an unmet goal must not be drawn as a node')
  assert.equal(graph.edges.some((e) => e.target === 'g:g_1'), false, 'an unmet goal must not be connected')

  // Once achieved it is a target node anchored to the fact carrying its evidence.
  const done = buildGraph(
    [fact('f_done', { evidence_ids: ['e_done'] })],
    [],
    [goal({ evidence_id: 'e_done' })],
  )
  assert.equal(done.byId.get('g:g_1')?.status, 'met')
  assert.equal(done.edges.filter((e) => e.kind === 'goal').length, 1)
})

test('fact_meta merges onto the matching fact nodes only', () => {
  const graph = buildGraph(
    [fact('f_meta'), fact('f_plain')],
    [],
    [],
    undefined,
    [],
    '',
    [],
    [],
    [],
    [
      {
        id: 'f_meta',
        needs_review: true,
        reviewed_claim_version: 2,
        evidence_count: 3,
        ref_count: 1,
      },
      // A meta row for a fact that is not on this page is ignored.
      {
        id: 'f_elsewhere',
        needs_review: false,
        reviewed_claim_version: 0,
        evidence_count: 0,
        ref_count: 0,
      },
    ],
  )
  const meta = graph.byId.get('f:f_meta')
  assert.equal(meta?.needsReview, true)
  assert.equal(meta?.reviewedClaimVersion, 2)
  assert.equal(meta?.evidenceCount, 3)
  assert.equal(meta?.refCount, 1)
  assert.equal(graph.byId.get('f:f_plain')?.needsReview, undefined)
  assert.equal(graph.byId.has('f:f_elsewhere'), false)
})

test('a selection wins over hover, and a line is a subject like a node', () => {
  // While an edge selection let hover win, moving the pointer off the line
  // released the dim and every other line came back to full brightness: a click
  // looked like it had done nothing. The selection is the subject now.
  assert.equal(focusTarget('e:premise', null), 'e:premise')
  assert.equal(focusTarget('e:premise', 'f:f_out'), 'e:premise')
  assert.equal(focusTarget('s:in_1', null), 's:in_1')
  assert.equal(focusTarget('s:in_1', 'e:intent'), 's:in_1')
  // Hover leads only while nothing is selected.
  assert.equal(focusTarget(null, 'e:intent'), 'e:intent')
  assert.equal(focusTarget(null, null), null)
})

test('edgeFocus is exactly the relation, so sibling lines dim', () => {
  // Two premises into the same step: a *node* focus on the step would light both
  // (each is an ancestor hop), but a selected line lights only its own two ends,
  // so the sibling premise and the direction's produced-fact line fade.
  const premise = { id: 'e:premise', source: 'f:inject', target: 's:in_reuse' }
  const sibling = { id: 'e:sibling', source: 'f:other', target: 's:in_reuse' }
  const focus = edgeFocus(premise)
  assert.deepEqual([...focus].sort(), ['f:inject', 's:in_reuse'])
  assert.equal(focus.has(sibling.source), false, 'a sibling premise fact is not part of this line')
  // A produced-fact line is a peer of a premise line: same rule, its own ends.
  const produced = edgeFocus({ source: 's:in_reuse', target: 'f:out' })
  assert.deepEqual([...produced].sort(), ['f:out', 's:in_reuse'])
})

function run(over: Partial<Run> = {}): Run {
  return {
    id: 'r_1',
    intent_id: 'in_task',
    node: 'local',
    status: 'done',
    started_at: '2026-11-11T00:00:00Z',
    events: 1,
    declared_facts: 2,
    ...over,
  }
}

test('a fact keeps only the handoff warning that names it', () => {
  // One bad evidence path used to put its "事实「A」的证据…" row on every sibling
  // fact of the direction, because warnings were aggregated per intent and then
  // copied to every node. A fact now carries only what names it; the direction
  // still carries the run's whole record.
  const citation = '事实「A」的证据「raw/x.hdr」无法登记：文件不存在（已作为引用保留）'
  const direction = '交接未全部持久化：游标保留'
  const runs = [run({ warnings: [citation, direction] })]
  const graph = buildGraph(
    [fact('f_a', { title: 'A' }), fact('f_b', { title: 'B' })],
    [intent()],
    [],
    undefined,
    runs,
  )
  assert.deepEqual(graph.byId.get('f:f_a')?.warnings, [citation], 'the named fact keeps its warning')
  assert.equal(graph.byId.get('f:f_b')?.warnings, undefined, "a sibling fact must not carry another fact's citation warning")
  assert.deepEqual(graph.byId.get(stepID('in_task'))?.warnings, [citation, direction], 'the direction keeps the whole run record')
})

test('a warned title longer than the ledger clip still matches its fact', () => {
  // internal/ledger/ingest.go clips a warned title at 80 runes and appends "…";
  // the graph must reproduce the clip, or a long title would never match.
  const title = '长'.repeat(100)
  const clipped = '长'.repeat(80) + '…'
  const warning = `事实「${clipped}」的证据「raw/x.hdr」无法登记：文件不存在`
  const graph = buildGraph([fact('f_long', { title })], [intent()], [], undefined, [run({ warnings: [warning] })])
  assert.deepEqual(graph.byId.get('f:f_long')?.warnings, [warning])
})

test('a warning with no fact prefix stays on the direction, not on facts', () => {
  const warning = '模型无响应：意图已置 failed+Hold'
  const graph = buildGraph(
    [fact('f_a', { title: 'A' })],
    [intent()],
    [],
    undefined,
    [run({ warnings: [warning] })],
  )
  assert.equal(graph.byId.get('f:f_a')?.warnings, undefined)
  assert.deepEqual(graph.byId.get(stepID('in_task'))?.warnings, [warning])
})

function bareNode(id: string, type: GraphNode['type'], orderTime?: string): GraphNode {
  return { id, type, label: id, x: 0, y: 0, vx: 0, vy: 0, degree: 0, w: 1, h: 1, depth: 1, orderTime }
}

test('mainPath takes the deepest premise and never a sibling branch', () => {
  // Two premise facts of the same kind but different depth: the path follows the
  // one that continues further back, because "how did we get here" is the
  // question a path exists to answer. A sibling sharing only the root stays out:
  // a branch is a different chain, not a wider one.
  const graph = buildGraph(
    [
      fact('f_root', { intent_id: 'in_root' }),
      fact('f_deep', { intent_id: 'in_deep' }),
      fact('f_shallow', { intent_id: 'in_shallow' }),
      fact('f_out', { intent_id: 'in_target' }),
      fact('f_far', { intent_id: 'in_next' }),
    ],
    [
      intent({ id: 'in_root' }),
      intent({ id: 'in_deep', from_fact_ids: ['f_root'] }),
      intent({ id: 'in_shallow' }),
      intent({ id: 'in_target', from_fact_ids: ['f_deep', 'f_shallow'] }),
      intent({ id: 'in_next', from_fact_ids: ['f_out'] }),
    ],
    [],
  )
  const path = mainPath(graph, 's:in_target')
  assert.ok(path, 'a step always has a path')
  const order = (id: string) => path.indexOf(id)
  assert.ok(order('s:in_target') >= 0, 'the subject is on its own path')
  assert.ok(order('f:f_deep') >= 0, 'the deeper premise is on the path')
  assert.equal(path.includes('f:f_shallow'), false, 'the shallow sibling is a different chain')
  assert.ok(order('f:f_root') < order('f:f_deep'), 'provenance is ordered far to near')
  assert.ok(order('f:f_deep') < order('s:in_target'))
  assert.ok(order('s:in_target') < order('f:f_out'), 'the output follows the subject')
  assert.ok(order('f:f_out') < order('s:in_next'), 'and the consumer follows the output')
  assert.ok(order('s:in_next') < order('f:f_far'), 'the path reaches the far output')
  assert.equal(path.filter((n) => n === 's:in_target').length, 1, 'the subject appears once')
})

test('mainPath breaks a tie by newer created_at, then by smaller id', () => {
  const build = (times: [string, string]) =>
    buildGraph(
      [
        fact('f_a', { intent_id: 'in_a', created_at: times[0] }),
        fact('f_b', { intent_id: 'in_b', created_at: times[1] }),
        fact('f_out', { intent_id: 'in_target' }),
      ],
      [
        intent({ id: 'in_a', created_at: times[0] }),
        intent({ id: 'in_b', created_at: times[1] }),
        intent({ id: 'in_target', from_fact_ids: ['f_a', 'f_b'] }),
      ],
      [],
    )
  // Equal depth on both sides: the later recorded row wins, so the path prefers
  // what the engagement learned most recently.
  const newer = mainPath(build(['2026-11-11T00:00:00Z', '2026-11-11T02:00:00Z']), 's:in_target')
  assert.ok(newer, 'the subject has a path')
  assert.equal(newer.includes('f:f_b'), true, 'the newer premise wins the tie')
  assert.equal(newer.includes('f:f_a'), false)
  // Same timestamp: the id decides, so a replay draws the same path.
  const sameTime = mainPath(build(['2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z']), 's:in_target')
  assert.ok(sameTime)
  assert.equal(sameTime.includes('f:f_a'), true, 'equal time falls back to the smaller id')
  assert.equal(sameTime.includes('f:f_b'), false)
})

test('mainPath ignores delegation, reference and brief edges', () => {
  // Only premise and produced edges advance work. Counting spawn would make
  // "one long chain" the whole delegation tree, which is not one penetration.
  // The child cites a *different* premise, so its only tie to the parent is the
  // delegation edge.
  const graph = buildGraph(
    [
      fact('f_root', { intent_id: 'in_root' }),
      fact('f_other', { intent_id: 'in_other' }),
      fact('f_child', { intent_id: 'in_child' }),
    ],
    [
      intent({ id: 'in_root' }),
      intent({ id: 'in_other' }),
      intent({ id: 'in_child', parent_intent_id: 'in_root', from_fact_ids: ['f_other'] }),
    ],
    [],
  )
  const path = mainPath(graph, 's:in_root')
  assert.ok(path)
  assert.equal(path.includes('s:in_child'), false, 'a spawned child is not a chain successor')
  assert.equal(path.includes('f:f_child'), false)
})

test('a ghost ring is its direction, not a step of the chain', () => {
  const graph = buildGraph(
    [fact('f_premise')],
    [
      intent({ id: 'in_prev', from_fact_ids: ['f_premise'] }),
      intent({ id: 'in_bare', from_fact_ids: ['f_premise'] }),
    ],
    [],
  )
  // in_bare declared nothing, so the model draws a ghost ring instead of a fact.
  const fromGhost = mainPath(graph, 'w:in_bare')
  const fromStep = mainPath(graph, 's:in_bare')
  assert.deepEqual(fromGhost, fromStep, 'clicking the ring selects the direction')
  assert.ok(fromGhost)
  assert.equal(fromGhost.includes('w:in_bare'), false, 'an empty ring never ends a chain')
  assert.equal(fromGhost.includes('s:in_prev'), false, 'a sibling direction is a different chain')
})

test('mainPath refuses an unknown id and keeps a lone node', () => {
  assert.equal(mainPath(buildGraph([], [], []), 'f:missing'), null)
  assert.equal(mainPath(buildGraph([], [], []), null), null)
  // A node with no chain edges is a chain of one, not an error: the caller shows
  // "1 节点" rather than an empty panel.
  const nodes = [bareNode('g:g_1', 'goal')]
  const edges: GraphEdge[] = [{ id: 'e:brief:g:g_1', source: 'p:project', target: 'g:g_1', kind: 'brief' }]
  const model = { nodes, edges } as Pick<GraphModel, 'nodes' | 'edges'>
  assert.deepEqual(mainPath(model, 'g:g_1'), ['g:g_1'])
})

test('node type labels are operator language, not graph enums', () => {
  assert.equal(nodeTypeLabel('step'), '意图')
  assert.equal(nodeTypeLabel('intent'), '意图')
  assert.equal(nodeTypeLabel('ghost'), '意图')
  assert.equal(nodeTypeLabel('fact', 'host'), 'host')
  // The ledger kind stands alone: a 事实 prefix was a second taxonomy on a badge
  // that already sits inside the fact panel.
  assert.equal(nodeTypeLabel('fact', 'artifact'), 'artifact')
  assert.equal(nodeTypeLabel('fact'), '事实')
  assert.equal(nodeTypeLabel('goal'), '目标')
  assert.equal(nodeTypeLabel('project'), '项目')
  assert.equal(nodeTypeLabel('boundary'), '引用')
})
