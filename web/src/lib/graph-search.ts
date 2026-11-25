// Local node search for the graph card.
//
// The search box answers "where is the node I mean" over the model that is
// currently drawn. It is not the whole-ledger retrieval (that is the separate
// `factsPage` call): a node hidden by a drawing filter is not on this map, so
// pretending it was found would point the camera at nothing.
//
// Everything here is pure so the ranking, the cycle and the highlighted
// fragment can be tested without a canvas. The canvas still only paints the
// `matches` set; the ordered `hits` drive the result list and the keyboard.

import type { GraphEdge, GraphModel, GraphNode } from '@/graph/model'
import { graphNodeOrder } from './graph-filter.ts'

/** Which field the query matched, strongest first. */
export type GraphSearchField = 'label' | 'sub' | 'id'

export interface GraphSearchHit {
  node: GraphNode
  /**
   * Lower is stronger: exact label, label prefix, label substring, then a
   * description/id hit. A stable sort keeps equal ranks in map order, so the
   * operator can walk the list in the same direction the map reads.
   */
  rank: 0 | 1 | 2 | 3 | 4
  field: GraphSearchField
}

export interface GraphSearchResult {
  /** Trimmed query the result was built from ('' when the box is empty). */
  query: string
  /** Ranked nodes the list shows. Ghost rings repeat a direction and are not listed. */
  hits: GraphSearchHit[]
  /**
   * Ids the canvas highlights: every matched node (ghosts included, so the
   * un-produced end of a direction lights up with its step) and every matched
   * edge label. Null when the query is empty, which is the canvas's "no search".
   */
  matches: Set<string> | null
}

type SearchableModel = Pick<GraphModel, 'nodes' | 'edges'>

/** One label match; `null` when this node does not answer the query. */
function matchNode(node: GraphNode, needle: string): Pick<GraphSearchHit, 'rank' | 'field'> | null {
  const label = node.label.toLocaleLowerCase()
  if (label === needle) return { rank: 0, field: 'label' }
  if (label.startsWith(needle)) return { rank: 1, field: 'label' }
  if (label.includes(needle)) return { rank: 2, field: 'label' }
  if ((node.sub ?? '').toLocaleLowerCase().includes(needle)) return { rank: 3, field: 'sub' }
  if (node.id.toLocaleLowerCase().includes(needle)) return { rank: 4, field: 'id' }
  return null
}

function edgeMatches(edge: GraphEdge, needle: string): boolean {
  return Boolean(edge.label && edge.label.toLocaleLowerCase().includes(needle))
}

/**
 * Rank the drawn model against a free-text query.
 *
 * Matching is case-insensitive substring search over the label, the description
 * and the id. It deliberately does not match status/kind words: "done" would
 * otherwise surface every finished direction and bury the one node the operator
 * named.
 */
export function searchGraph(model: SearchableModel, rawQuery: string): GraphSearchResult {
  const query = rawQuery.trim()
  if (!query) return { query: '', hits: [], matches: null }
  const needle = query.toLocaleLowerCase()
  const matches = new Set<string>()
  // Ghosts repeat their direction's label; they light up with the match but are
  // not a second result row (the same rule as the list view's keyboard order).
  const ordered = graphNodeOrder(model.nodes.filter((node) => node.type !== 'ghost'))
  const hits: GraphSearchHit[] = []
  for (const node of ordered) {
    const hit = matchNode(node, needle)
    if (!hit) continue
    matches.add(node.id)
    hits.push({ node, ...hit })
  }
  for (const node of model.nodes) {
    if (node.type === 'ghost' && matchNode(node, needle)) matches.add(node.id)
  }
  for (const edge of model.edges) {
    if (edgeMatches(edge, needle)) matches.add(edge.id)
  }
  // Stable sort: the base order is the map's own keyboard order, so equal ranks
  // walk left-to-right / top-to-bottom instead of jumping around.
  hits.sort((a, b) => a.rank - b.rank)
  return { query, hits, matches }
}

/**
 * Move the active row without wrapping. The list has a visible end; falling off
 * it would silently jump back to the first hit while the camera stayed put.
 */
export function moveSearchIndex(current: number, count: number, delta: 1 | -1): number {
  if (count <= 0) return -1
  if (current < 0 || current >= count) return delta > 0 ? 0 : count - 1
  return Math.min(count - 1, Math.max(0, current + delta))
}

/** Enter cycles: from the last hit the next one is the first. */
export function cycleSearchIndex(current: number, count: number, delta: 1 | -1): number {
  if (count <= 0) return -1
  if (current < 0 || current >= count) return delta > 0 ? 0 : count - 1
  return (current + delta + count) % count
}

/** The short kind tag on a result row (operator words, not graph enums). */
export function searchHitKind(node: GraphNode): string {
  switch (node.type) {
    case 'fact':
      return node.kind || '事实'
    case 'step':
    case 'intent':
      return '意图'
    case 'goal':
      return '目标'
    case 'project':
      return '项目'
    case 'boundary':
      return '边界'
    default:
      return node.type
  }
}

export interface MatchSegment {
  text: string
  hit: boolean
}

/**
 * Split a label into matched/unmatched runs for the result row. Only the first
 * occurrence is marked: a query repeated inside one title should not turn the
 * whole row into a highlighter.
 */
export function matchSegments(text: string, rawQuery: string): MatchSegment[] {
  const query = rawQuery.trim()
  if (!query) return [{ text, hit: false }]
  const at = text.toLocaleLowerCase().indexOf(query.toLocaleLowerCase())
  if (at < 0) return [{ text, hit: false }]
  const segments: MatchSegment[] = []
  if (at > 0) segments.push({ text: text.slice(0, at), hit: false })
  segments.push({ text: text.slice(at, at + query.length), hit: true })
  if (at + query.length < text.length) segments.push({ text: text.slice(at + query.length), hit: false })
  return segments
}

/**
 * A one-line result title that always shows *why* it matched.
 *
 * The card is ~200px wide and a node title can be a full sentence. Plain
 * truncation cuts the tail, so a query that matched the end of the title
 * (`…可从办公网访问`) produced a row with no visible highlight at all. When the
 * match sits past the visible head, keep a short identity prefix and jump to
 * the match with an ellipsis. `matchSegments` then marks it normally.
 */
export function matchSnippet(
  text: string,
  rawQuery: string,
  options: { head?: number; before?: number; after?: number } = {},
): MatchSegment[] {
  const query = rawQuery.trim()
  if (!query) return [{ text, hit: false }]
  const at = text.toLocaleLowerCase().indexOf(query.toLocaleLowerCase())
  if (at < 0) return [{ text, hit: false }]
  const head = Math.max(0, options.head ?? 3)
  const before = Math.max(0, options.before ?? 2)
  const after = Math.max(0, options.after ?? 2)
  // The match is already in the visible head: leave the label intact for CSS
  // truncation, exactly like a short title.
  if (at <= head + before) return matchSegments(text, query)
  const from = Math.max(0, at - before)
  const to = Math.min(text.length, at + query.length + after)
  return [
    { text: text.slice(0, head), hit: false },
    { text: '…', hit: false },
    ...matchSegments(text.slice(from, to), query),
  ]
}
