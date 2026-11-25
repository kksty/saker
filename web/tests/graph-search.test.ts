// Ranking and keyboard-cycle rules for the graph's local node search.
//
// The search box used to be a raw substring scan with no order and no way to
// walk the result: this suite pins the contract the canvas/list now share —
// strongest label match first, map order for ties, ghosts not listed twice,
// edges highlighted but not counted as nodes.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  cycleSearchIndex,
  matchSegments,
  matchSnippet,
  moveSearchIndex,
  searchGraph,
  searchHitKind,
} from '../src/lib/graph-search.ts'
import type { GraphEdge, GraphNode } from '../src/graph/model.ts'

function node(id: string, over: Partial<GraphNode> = {}): GraphNode {
  return {
    id,
    type: 'fact',
    label: id,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 6,
    h: 6,
    depth: 2,
    ...over,
  }
}

function edge(id: string, over: Partial<GraphEdge> = {}): GraphEdge {
  return { id, source: 'a', target: 'b', kind: 'produced', ...over }
}

test('an exact label beats a prefix, which beats a contained label', () => {
  const fact = node('f:login', { label: '登录口', y: 0 })
  const prefix = node('f:login-2', { label: '登录口越权读取', y: 1 })
  const contained = node('f:other', { label: '验证登录口越权', y: 2 })
  const result = searchGraph({ nodes: [contained, prefix, fact], edges: [] }, '登录口')
  assert.deepEqual(result.hits.map((hit) => hit.node.id), ['f:login', 'f:login-2', 'f:other'])
  assert.deepEqual(result.hits.map((hit) => hit.rank), [0, 1, 2])
})

test('description and id hits rank after every label hit', () => {
  const byId = node('f:host-7', { label: '边缘节点', y: 0 })
  const bySub = node('f:a', { label: '资产清单', sub: '含 host-7 记录', y: 1 })
  const byLabel = node('f:b', { label: 'host-7 复核', y: 2 })
  const result = searchGraph({ nodes: [byId, bySub, byLabel], edges: [] }, 'HOST-7')
  assert.deepEqual(result.hits.map((hit) => hit.node.id), ['f:b', 'f:a', 'f:host-7'])
  assert.deepEqual(result.hits.map((hit) => hit.field), ['label', 'sub', 'id'])
})

test('equal ranks keep the map keyboard order (depth, then y, then x, then id)', () => {
  const first = node('f:b', { label: '端口 b', depth: 2, y: 10 })
  const second = node('f:a', { label: '端口 a', depth: 2, y: 20 })
  const shallow = node('s:in', { type: 'step', label: '端口复核', depth: 1 })
  const result = searchGraph({ nodes: [second, first, shallow], edges: [] }, '端口')
  assert.deepEqual(result.hits.map((hit) => hit.node.id), ['s:in', 'f:b', 'f:a'])
})

test('ghost rings light up with their direction but are not listed twice', () => {
  const step = node('s:in_1', { type: 'step', label: '验证登录口', depth: 1 })
  const ghost = node('g:in_1', { type: 'ghost', label: '验证登录口', depth: 1 })
  const result = searchGraph({ nodes: [step, ghost], edges: [] }, '登录口')
  assert.deepEqual(result.hits.map((hit) => hit.node.id), ['s:in_1'])
  assert.ok(result.matches?.has('s:in_1'))
  assert.ok(result.matches?.has('g:in_1'), 'the un-produced end of the same direction is highlighted')
})

test('a matched edge label is highlighted but never counted as a node', () => {
  const from = node('f:a', { label: '起点' })
  const to = node('f:b', { label: '终点' })
  const rel = edge('e:rel', { label: '访问凭据' })
  const result = searchGraph({ nodes: [from, to], edges: [rel] }, '凭据')
  assert.equal(result.hits.length, 0)
  assert.ok(result.matches?.has('e:rel'))
})

test('an empty or whitespace-only query means "no search"', () => {
  for (const query of ['', '   ', '\t']) {
    const result = searchGraph({ nodes: [node('f:a', { label: 'a' })], edges: [] }, query)
    assert.equal(result.matches, null)
    assert.deepEqual(result.hits, [])
  }
})

test('the query is trimmed for matching and echoed trimmed', () => {
  const result = searchGraph({ nodes: [node('f:a', { label: '登录口' })], edges: [] }, '  登录口  ')
  assert.equal(result.query, '登录口')
  assert.equal(result.hits.length, 1)
})

test('moving the active row clamps at the ends; Enter cycles', () => {
  assert.equal(moveSearchIndex(-1, 3, 1), 0)
  assert.equal(moveSearchIndex(-1, 3, -1), 2)
  assert.equal(moveSearchIndex(0, 3, -1), 0)
  assert.equal(moveSearchIndex(2, 3, 1), 2)
  assert.equal(moveSearchIndex(1, 3, 1), 2)
  assert.equal(moveSearchIndex(0, 0, 1), -1)

  assert.equal(cycleSearchIndex(2, 3, 1), 0)
  assert.equal(cycleSearchIndex(0, 3, -1), 2)
  assert.equal(cycleSearchIndex(-1, 3, 1), 0)
  assert.equal(cycleSearchIndex(9, 3, 1), 0)
  assert.equal(cycleSearchIndex(0, 0, 1), -1)
})

test('only the first query occurrence is marked, preserving the label text', () => {
  assert.deepEqual(matchSegments('Login 登录 login', '登录'), [
    { text: 'Login ', hit: false },
    { text: '登录', hit: true },
    { text: ' login', hit: false },
  ])
  assert.deepEqual(matchSegments('没有命中', '登录'), [{ text: '没有命中', hit: false }])
  assert.deepEqual(matchSegments('登录', '  '), [{ text: '登录', hit: false }])
})

test('a match at the end of a long title keeps a prefix and jumps to the match', () => {
  const label = '示例：预发环境调试端口可从办公网访问'
  const segments = matchSnippet(label, '访问')
  // The 200px row would truncate the tail, leaving the match invisible; the
  // snippet keeps a short identity prefix and puts the marked word in view.
  assert.deepEqual(segments, [
    { text: '示例：', hit: false },
    { text: '…', hit: false },
    { text: '公网', hit: false },
    { text: '访问', hit: true },
  ])
  // The marked text is the query, never an invented fragment.
  assert.equal(segments.filter((part) => part.hit).map((part) => part.text).join(''), '访问')
})

test('a match already in the visible head is left to ordinary truncation', () => {
  assert.deepEqual(matchSnippet('登录口越权读取', '登录口'), matchSegments('登录口越权读取', '登录口'))
  assert.deepEqual(matchSnippet('没有命中', '登录'), [{ text: '没有命中', hit: false }])
  assert.deepEqual(matchSnippet('登录', '  '), [{ text: '登录', hit: false }])
})

test('the snippet window is tunable and never reads outside the label', () => {
  const label = 'abcdefghij'
  assert.deepEqual(matchSnippet(label, 'j', { head: 1, before: 1, after: 3 }), [
    { text: 'a', hit: false },
    { text: '…', hit: false },
    { text: 'i', hit: false },
    { text: 'j', hit: true },
  ])
  assert.deepEqual(matchSnippet(label, 'abcd', { head: 1, before: 1 }), matchSegments(label, 'abcd'))
})

test('result rows use operator words for the node kind', () => {
  assert.equal(searchHitKind(node('f:a', { kind: 'vuln' })), 'vuln')
  assert.equal(searchHitKind(node('f:a')), '事实')
  assert.equal(searchHitKind(node('s:a', { type: 'step' })), '意图')
  assert.equal(searchHitKind(node('g:a', { type: 'goal' })), '目标')
  assert.equal(searchHitKind(node('b:a', { type: 'boundary' })), '边界')
})
