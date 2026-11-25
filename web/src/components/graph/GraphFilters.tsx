// The graph page's search/filter card (T6, docs/33 §4.6). Two groups:
//  1) node search (ranked local hits, whole-ledger search, match list, fact-kind
//     chips, match count);
//  2) the drawing-only filters added by T7's lib/graph-filter.ts: recency
//     window over facts, hide finished branches, keep only the selected chain.
// None of these change what the ledger says; they change what is drawn.
//
// Search is always visible. The adjacent filter button opens the full controls.
//
// The search box is a combobox over a ranked list (lib/graph-search.ts): the
// operator can walk the hits with ↑/↓ and locate one with Enter, without
// hunting for the node on a 5k-dot canvas. Enter on the already-located hit
// advances, so repeated Enter is the familiar find-next gesture. Matching is
// IME-safe: a composing Chinese word is never searched half-typed.

import { useEffect, useId, useRef, useState } from 'react'
import type { ChangeEvent, CompositionEvent, KeyboardEvent } from 'react'
import { ListFilter, Search, X } from 'lucide-react'
import { Button, Chip, Input } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import { GRAPH_TIME_RANGES } from '@/lib/graph-filter'
import {
  matchSnippet,
  moveSearchIndex,
  searchHitKind,
  type GraphSearchHit,
} from '@/lib/graph-search'
import { statusLabel } from '@/lib/format'
import { useSessionState } from '@/lib/session-state'
import type { Fact } from '@/lib/types'
import { focusGraphCanvas, isTypingTarget } from './focus'
import type { GraphFiltersState } from './use-graph-model'

const FACT_KINDS = ['vuln', 'cred', 'host', 'service', 'loot', 'artifact', 'note']
/** Rows the card shows at once; the window follows the active hit. */
const VISIBLE_HITS = 6

export interface GraphFiltersProps {
  /** Project id: the index build and the search are project-scoped. */
  current: string
  /** The text in the box (raw; the canvas matches the debounced copy). */
  q: string
  onQueryChange: (q: string) => void
  /** Ranked node hits for the debounced query. */
  hits: readonly GraphSearchHit[]
  /** Index of the hit the keyboard/pointer is on, or -1. */
  activeIndex: number
  onActiveIndex: (index: number) => void
  /** Click on a row: centre the map on that hit and open it. */
  onOpenHit: (index: number) => void
  /** Enter: locate the active hit, or the next one when it is already located. */
  onActivate: (backward: boolean) => void
  /** True while the debounced query still trails the text in the box. */
  pending?: boolean
  /** Board fact counts per kind, for the chips. */
  counts: Record<string, number>
  filters: GraphFiltersState
  onFiltersChange: (next: GraphFiltersState) => void
  /** Jump the map to a fact found by the whole-ledger search. */
  onFocusFact: (factID: string) => void
  /** 冷热压暗开关：选中后把链路外的节点压暗（默认关，链路高亮与它各自独立）。 */
  coldDim: boolean
  onToggleColdDim: (next: boolean) => void
}

export function GraphFilters({
  current,
  q,
  onQueryChange,
  hits,
  activeIndex,
  onActiveIndex,
  onOpenHit,
  onActivate,
  pending = false,
  counts,
  filters,
  onFiltersChange,
  onFocusFact,
  coldDim,
  onToggleColdDim,
}: GraphFiltersProps) {
  const [showOptions, setShowOptions] = useSessionState('graph.filterOptionsOpen', false)
  const [showDrawingOptions, setShowDrawingOptions] = useSessionState('graph.drawingOptionsOpen', false)
  const optionsID = useId()
  const listID = useId()
  const hintID = useId()
  const inputRef = useRef<HTMLInputElement | null>(null)
  // Controlled draft: composition events update the visible text without
  // committing a half-typed word to the search.
  const composing = useRef(false)
  const [draft, setDraft] = useState(q)
  const [searchMode, setSearchMode] = useState('lexical')
  const [searchCursor, setSearchCursor] = useState('')
  const [searchTerm, setSearchTerm] = useState('')
  const [globalMatches, setGlobalMatches] = useState<Fact[]>([])
  const [searchingGraph, setSearchingGraph] = useState(false)
  const embedding = useLoad(api.retrievalStatus, [])
  const [indexing, setIndexing] = useState(false)
  const [indexProgress, setIndexProgress] = useState('')
  // Search/index failures are local to this card (the page-level action error
  // belongs to pause/decide); never swallow a rejected promise.
  const [searchError, setSearchError] = useState('')
  const indexLive = useRef(true)
  const indexGeneration = useRef(0)
  useEffect(() => {
    indexGeneration.current++
    indexLive.current = true
    setIndexing(false)
    setIndexProgress('')
    setSearchError('')
    setGlobalMatches([])
    setSearchCursor('')
    return () => {
      indexLive.current = false
      indexGeneration.current++
    }
  }, [current])

  // The parent owns the query; sync the draft unless the operator is mid-IME.
  useEffect(() => {
    if (!composing.current) setDraft(q)
  }, [q])

  // `/` focuses the search box from the map, the same gesture as in most
  // consoles. It never steals a slash typed into a field.
  useEffect(() => {
    // Partial window mocks (SSR tests) may not provide the event target.
    if (typeof window === 'undefined' || typeof window.addEventListener !== 'function') return
    function onKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey || event.defaultPrevented) return
      if (isTypingTarget(event.target)) return
      event.preventDefault()
      inputRef.current?.focus()
      inputRef.current?.select()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  async function buildIndex() {
    setIndexing(true)
    setSearchError('')
    let cursor = ''
    const generation = indexGeneration.current
    try {
      do {
        const page = await api.indexFacts(cursor)
        if (!indexLive.current || generation !== indexGeneration.current) return
        setIndexProgress(`尚待读取 ${page.remaining_to_visit}/${page.total}`)
        cursor = page.next_cursor ?? ''
        if (!page.has_more) break
      } while (cursor && indexLive.current && generation === indexGeneration.current)
      setIndexProgress('索引已就绪')
    } catch (e) {
      if (indexLive.current && generation === indexGeneration.current) {
        setSearchError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      if (indexLive.current && generation === indexGeneration.current) setIndexing(false)
    }
  }

  async function searchGraph(cursor = '') {
    setSearchingGraph(true)
    setSearchError('')
    try {
      const term = cursor ? searchTerm : q
      const page = await api.factsPage({ q: term, mode: searchMode, limit: 20, cursor })
      setSearchTerm(term)
      setSearchCursor(page.next_cursor ?? '')
      setGlobalMatches((previous) => (cursor ? [...previous, ...page.facts] : page.facts))
    } catch (e) {
      setSearchError(e instanceof Error ? e.message : String(e))
    } finally {
      setSearchingGraph(false)
    }
  }

  function clearSearch() {
    onQueryChange('')
    onActiveIndex(-1)
    setDraft('')
    setSearchTerm('')
    setSearchCursor('')
    setGlobalMatches([])
    setSearchError('')
  }

  function handleChange(event: ChangeEvent<HTMLInputElement>) {
    setDraft(event.target.value)
    // During composition the browser is still assembling the word: committing
    // it here would run a search for a prefix the operator never typed.
    if (!composing.current) onQueryChange(event.target.value)
  }

  function handleCompositionStart() {
    composing.current = true
  }

  function handleCompositionEnd(event: CompositionEvent<HTMLInputElement>) {
    composing.current = false
    onQueryChange(event.currentTarget.value)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    // Escape cancels an IME candidate; it must not also clear the box.
    if (composing.current) return
    const count = hits.length
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      if (count === 0) return
      event.preventDefault()
      onActiveIndex(moveSearchIndex(activeIndex, count, event.key === 'ArrowDown' ? 1 : -1))
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      if (count === 0) {
        // A local miss is the one point where the whole-ledger index is the
        // natural next step; the hint under the box says so.
        if (!pending && draft.trim()) void searchGraph()
        return
      }
      onActivate(event.shiftKey)
      return
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      if (draft.trim()) {
        clearSearch()
        inputRef.current?.focus()
      } else {
        // Nothing to clear: hand the map back to the keyboard.
        inputRef.current?.blur()
        focusGraphCanvas()
      }
    }
  }

  function toggleKind(kind: string) {
    const next = new Set(filters.hiddenKinds)
    if (next.has(kind)) next.delete(kind)
    else next.add(kind)
    onFiltersChange({ ...filters, hiddenKinds: next })
  }

  const patch = (part: Partial<GraphFiltersState>) => onFiltersChange({ ...filters, ...part })

  // Collapsed controls must never hide that they are doing something: the
  // summary carries how many drawing filters are in effect.
  const activeFilters =
    filters.hiddenKinds.size +
    (filters.timeRange !== 'all' ? 1 : 0) +
    (filters.hideDone ? 1 : 0) +
    (filters.showHelpers === false ? 1 : 0)

  const searchText = q.trim()
  const active = activeIndex >= 0 && activeIndex < hits.length ? activeIndex : -1
  // The visible window follows the active hit, so Enter never points at a row
  // that is outside the card.
  const start = active < 0 ? 0 : Math.max(0, Math.min(active - Math.floor(VISIBLE_HITS / 2), Math.max(0, hits.length - VISIBLE_HITS)))
  const visible = hits.slice(start, start + VISIBLE_HITS)
  const showList = searchText.length > 0
  const rowID = (index: number) => `${listID}-${index}`

  return (
    <div className="tw-graph-overlay-scroll min-h-0 flex-1 overflow-y-auto p-2">
      <div className="flex items-center gap-1">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-ink-3" />
          <Input
            ref={inputRef}
            value={draft}
            onChange={handleChange}
            onCompositionStart={handleCompositionStart}
            onCompositionEnd={handleCompositionEnd}
            onKeyDown={handleKeyDown}
            role="combobox"
            aria-label="搜索节点"
            aria-autocomplete="list"
            aria-expanded={showList && hits.length > 0}
            aria-controls={showList && hits.length > 0 ? listID : undefined}
            aria-activedescendant={showList && active >= 0 ? rowID(active) : undefined}
            aria-describedby={hintID}
            aria-keyshortcuts="/"
            enterKeyHint="search"
            placeholder="搜索节点（/）"
            className="h-8 border-hairline bg-surface-2/50 pl-8 pr-7 text-caption"
          />
          {draft && (
            <button
              type="button"
              aria-label="清空搜索"
              title="清空搜索（Esc）"
              className="absolute right-0.5 top-1/2 flex size-6 -translate-y-1/2 items-center justify-center rounded text-ink-3 transition-colors hover:bg-surface-2 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => {
                clearSearch()
                inputRef.current?.focus()
              }}
            >
              <X aria-hidden="true" className="size-3.5" />
            </button>
          )}
        </div>
        <Button
          size="icon-sm"
          variant={showOptions || activeFilters > 0 ? 'soft' : 'ghost'}
          className="relative h-8 w-8 shrink-0"
          aria-label="筛选与检索"
          aria-expanded={showOptions}
          aria-controls={optionsID}
          title={activeFilters > 0 ? `筛选与检索（${activeFilters} 项生效）` : '筛选与检索'}
          onClick={() => setShowOptions(open => !open)}
        >
          <ListFilter aria-hidden="true" className="size-4" />
          {activeFilters > 0 && <span className="absolute right-0.5 top-0.5 text-[9px] leading-none" aria-hidden="true">{activeFilters}</span>}
        </Button>
      </div>

      {/* The shortcut contract is visual-only off; the combobox reads it once. */}
      <p id={hintID} className="sr-only">
        上下键选择命中节点，Enter 定位，Shift+Enter 定位上一个，Escape 清空搜索；
        先输入关键词，从斜杠键可随时回到这里。
      </p>

      {/* The combobox announces the active option itself; this polite line is
          the count and the current subject for readers that do not follow
          aria-activedescendant (the old sr-only name list said the same thing
          with up to twenty names). */}
      <div className="sr-only" role="status" aria-live="polite" aria-label="搜索结果">
        {showList
          ? hits.length > 0
            ? `命中 ${hits.length} 个节点${active >= 0 ? `，当前第 ${active + 1} 个：${hits[active].node.label}` : ''}`
            : pending
              ? '正在匹配节点'
              : '没有匹配的节点'
          : ''}
      </div>

      {showList && (
        <div className="mt-1.5 flex min-w-0 items-center gap-1 text-[11px] text-ink-3">
          {hits.length > 0 ? (
            <span className="truncate">
              命中 <span className="tw-nums font-mono font-semibold text-ink-2">{hits.length}</span> 个节点
              {active >= 0 && (
                <>
                  {' · '}
                  <span className="tw-nums font-mono text-ink-2">
                    {active + 1}/{hits.length}
                  </span>
                </>
              )}
            </span>
          ) : pending ? (
            <span>匹配中…</span>
          ) : (
            <span>无匹配节点</span>
          )}
        </div>
      )}

      {showList && hits.length > 0 && (
        <>
          <div id={listID} role="listbox" aria-label="搜索命中节点" className="mt-1 space-y-px">
            {visible.map((hit, offset) => {
              const index = start + offset
              return (
                <div
                  key={hit.node.id}
                  id={rowID(index)}
                  role="option"
                  aria-selected={index === active}
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseEnter={() => onActiveIndex(index)}
                  onClick={() => onOpenHit(index)}
                  title={[hit.node.label, hit.node.sub].filter(Boolean).join(' · ')}
                  className={cn(
                    'flex h-7 cursor-pointer items-center gap-1.5 rounded px-1.5 text-[12px] transition-colors',
                    index === active
                      ? 'bg-surface-2 text-ink shadow-[inset_2px_0_0_var(--primary)]'
                      : 'text-ink-2 hover:bg-surface-2/70 hover:text-ink',
                  )}
                >
                  <span className="shrink-0 rounded bg-surface-2/80 px-1 text-[10px] leading-4 text-ink-3">
                    {searchHitKind(hit.node)}
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    {matchSnippet(hit.node.label || '（无标题）', searchText).map((part, partIndex) =>
                      part.hit ? (
                        <mark key={partIndex} className="bg-transparent p-0 font-semibold text-info">
                          {part.text}
                        </mark>
                      ) : (
                        <span key={partIndex}>{part.text}</span>
                      ),
                    )}
                  </span>
                  {hit.field !== 'label' ? (
                    // Say why a row with no visible query text is here: a
                    // description or id hit would otherwise look unrelated.
                    <span className="shrink-0 text-[10px] text-ink-3">
                      {hit.field === 'sub' ? '描述' : 'ID'}
                    </span>
                  ) : hit.node.status ? (
                    <span className="shrink-0 text-[10px] text-ink-3">{statusLabel(hit.node.status)}</span>
                  ) : null}
                </div>
              )
            })}
          </div>
          <div className="mt-1 flex items-center justify-between gap-2 text-[10px] text-ink-3">
            <span className="truncate">↑↓ 选择 · Enter 定位 · ⇧Enter 上一个</span>
            {hits.length > VISIBLE_HITS && (
              <span className="tw-nums shrink-0 font-mono">
                {start + 1}–{start + visible.length}/{hits.length}
              </span>
            )}
          </div>
        </>
      )}

      {showList && !pending && hits.length === 0 && (
        <p className="mt-1 text-[11px] leading-4 text-ink-3">
          地图上没有匹配的节点。类型或时间筛选可能隐藏了它，Enter 在整库中检索。
        </p>
      )}

      {/* 整库检索 stays next to the local result instead of hiding behind the
          filter button: it is the fallback when the node is not on this map. */}
      {showList && (
        <div className="mt-1 flex items-center gap-1.5">
          <Button
            size="xs"
            variant="ghost"
            className="h-6 px-1.5 text-[11px]"
            onClick={() => void searchGraph()}
            disabled={searchingGraph}
            title="在整库中检索，包括尚未载入这张图的节点"
          >
            整库检索
          </Button>
          {searchingGraph && <span className="text-[11px] text-ink-3">检索中…</span>}
        </div>
      )}
      {searchError && <div className="mt-1 text-[11px] text-destructive">{searchError}</div>}
      {globalMatches.map((f) => (
        <button
          key={f.id}
          type="button"
          className="mt-1 block w-full truncate rounded px-1.5 py-1 text-left text-[12px] text-info transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => {
            clearSearch()
            onFocusFact(f.id)
          }}
        >
          {f.title}
        </button>
      ))}
      {searchCursor && (
        <Button size="xs" variant="ghost" disabled={searchingGraph} onClick={() => void searchGraph(searchCursor)}>
          继续检索结果
        </Button>
      )}

      <div id={optionsID} className="mt-2.5 border-t border-hairline pb-1 pt-2.5" hidden={!showOptions}>
        <div className="mb-2.5 flex flex-wrap gap-1.5">
          {FACT_KINDS.map((k) => (
            <Chip
              key={k}
              active={!filters.hiddenKinds.has(k)}
              onClick={() => toggleKind(k)}
              className="min-h-6 gap-1 px-2 py-0.5 text-caption"
              title={filters.hiddenKinds.has(k) ? `显示 ${k}` : `隐藏 ${k}`}
            >
              {k}
              <span className="tw-nums font-mono text-[11px] opacity-60">{counts[k] ?? 0}</span>
            </Chip>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <select
            className="h-8 min-w-0 flex-1 rounded-md border border-hairline bg-surface-2/50 px-2 text-[12px] text-ink-2 outline-none transition-colors hover:border-ink-3/40 focus-visible:border-ring/60 focus-visible:ring-2 focus-visible:ring-ring/35"
            value={searchMode}
            aria-label="检索模式"
            onChange={(e) => {
              setSearchMode(e.target.value)
              setSearchCursor('')
              setGlobalMatches([])
            }}
          >
            <option value="lexical">全文检索</option>
            <option value="semantic" disabled={!embedding.data?.configured}>
              语义检索
            </option>
            <option value="hybrid" disabled={!embedding.data?.configured}>
              混合检索
            </option>
          </select>
          {embedding.data?.configured && (
            <Button size="xs" variant="ghost" disabled={indexing} onClick={() => void buildIndex()}>
              构建索引
            </Button>
          )}
        </div>
        {indexProgress && <div className="mt-1 text-[11px] text-ink-3">{indexProgress}</div>}

        {/* Drawing-only filters. The ledger is never cut: these decide what the
            canvas draws (and therefore what the layout/hit-test sees). They are
            collapsed by default: finding something and choosing what to draw are
            two different questions, and the card answers the first one on open. */}
        <details
          className="mt-2.5 border-t border-hairline pt-2.5"
          open={showDrawingOptions}
          onToggle={(event) => {
            const expanded = event.currentTarget.open
            if (expanded !== showDrawingOptions) setShowDrawingOptions(expanded)
          }}
        >
          <summary className="cursor-pointer select-none text-[11px] text-ink-3 transition-colors hover:text-ink">
            画什么 · 类型 / 时间 / 显示
            {activeFilters > 0 && <span className="ml-1 text-ink-2">· {activeFilters} 项生效</span>}
          </summary>
          <div className="space-y-2 pt-2">
            <div className="flex items-center gap-2">
              <span className="shrink-0 text-[11px] text-ink-3">时间</span>
              <select
                className="h-7 min-w-0 flex-1 rounded-md border border-hairline bg-surface-2/50 px-1.5 text-[11px] text-ink-2 outline-none transition-colors hover:border-ink-3/40 focus-visible:border-ring/60 focus-visible:ring-2 focus-visible:ring-ring/35"
                value={filters.timeRange}
                aria-label="时间范围（按事实创建时间）"
                title="只按事实创建时间过滤；没有时间戳的事实视为「时间未知」而保留"
                onChange={(e) => patch({ timeRange: e.target.value as GraphFiltersState['timeRange'] })}
              >
                {GRAPH_TIME_RANGES.map((range) => (
                  <option key={range.id} value={range.id}>
                    {range.label}
                  </option>
                ))}
              </select>
            </div>
            <label className="flex cursor-pointer items-center gap-2 text-[12px] text-ink-2 transition-colors hover:text-ink">
              <input
                type="checkbox"
                className="h-3.5 w-3.5 accent-primary"
                checked={filters.hideDone}
                onChange={(e) => patch({ hideDone: e.target.checked })}
              />
              隐藏已完成分支
            </label>
            {/* Two drawing-only switches for the second channel: the 发起者 marks
                stay (they are the second channel), only the derived 发起边 and the
                temporary-helper satellites can be hidden. */}
            <label
              className="flex cursor-pointer items-center gap-2 text-[12px] text-ink-2 transition-colors hover:text-ink"
              title="开启后压暗链路以外的节点。关闭时，链路仍高亮，其余节点保持正常亮度"
            >
              <input
                type="checkbox"
                className="h-3.5 w-3.5 accent-primary"
                checked={coldDim}
                onChange={(e) => onToggleColdDim(e.target.checked)}
              />
              冷热压暗
            </label>

            <label
              className="flex cursor-pointer items-center gap-2 text-[12px] text-ink-2 transition-colors hover:text-ink"
              title="步骤 pill 右上角的原生 subagent 卫星点与“助×N”（父回合内临时助手，不登记 run/usage、不产生事实）"
            >
              <input
                type="checkbox"
                className="h-3.5 w-3.5 accent-primary"
                checked={filters.showHelpers !== false}
                onChange={(e) => patch({ showHelpers: e.target.checked })}
              />
              显示助手
            </label>
          </div>
        </details>
      </div>
    </div>
  )
}
