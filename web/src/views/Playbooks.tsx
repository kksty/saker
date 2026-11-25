import { useEffect, useMemo, useRef, useState } from 'react'
import { Archive, Ban, BookMarked, Check, Copy, RefreshCw, Search, ShieldCheck, Sparkles, Trash2, Globe } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  useConfirm,
} from '@cyber/ui'
import { Page } from '@/components/Page'
import { api, projectID } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { useDebounced, useLoad } from '@/lib/hooks'
import type { Playbook, PlaybookStatus } from '@/lib/types'

// The API derives the displayed status from the entry and its source facts.
// Origin records who wrote the entry and remains visible after a downgrade.
const STATUS: Record<
  string,
  { label: string; variant: 'success' | 'warning' | 'muted'; hint: string }
> = {
  confirmed: { label: '已确认', variant: 'success', hint: '执行者声明，来源事实均已核验并登记了原始证据' },
  candidate: { label: '候选', variant: 'warning', hint: '指挥官提名，或执行者声明的来源事实未满足确认条件' },
  retired: { label: '已退役', variant: 'muted', hint: '不再进入派发简报；文本保留' },
}

const STATUS_ORDER: PlaybookStatus[] = ['confirmed', 'candidate', 'retired']

type SortMode = 'updated' | 'title'

function statusMeta(status: string) {
  return STATUS[status] ?? { label: status || '未知', variant: 'muted' as const, hint: '' }
}

function outcomeMeta(outcome?: string) {
  if (outcome === 'dead_end') return { label: '死路', variant: 'caution' as const, icon: Ban }
  return { label: '可用', variant: 'success' as const, icon: ShieldCheck }
}

function originLabel(origin: string) {
  return origin === 'executor' ? '执行者声明' : '指挥官提名'
}

export function Playbooks() {
  const confirm = useConfirm()

  const [q, setQ] = useState('')
  const query = useDebounced(q, 150)
  const [status, setStatus] = useState<PlaybookStatus | 'all'>('all')
  const [tag, setTag] = useState<string | null>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const [sort, setSort] = useState<SortMode>('updated')
  const [selected, setSelected] = useState<string | null>(null)
  const [copied, setCopied] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [archived, setArchived] = useState(false)
  const params = { q: query, status: status === 'all' ? '' : status, tag: tag ?? '', archived: archived ? 'only' : 'exclude' }
  const list = useLoad(() => api.playbooksPage(params), [query, status, tag, archived])
  const [extra, setExtra] = useState<Playbook[]>([])
  const [nextCursor, setNextCursor] = useState('')
  const generation = useRef(0)
  useEffect(() => {
    generation.current++
    setBusy('')
    setExtra([])
    setNextCursor(list.data?.next_cursor ?? '')
    return () => { generation.current++ }
  }, [list.data, query, status, tag, archived])
  const entries = useMemo(() => [...(list.data?.playbooks ?? []), ...extra], [list.data, extra])
  const [history, setHistory] = useState<{
    id: string
    rows: { version: number; operation: string; at: string; value?: Playbook }[]
    next?: string
  }>()

  const counts = useMemo(() => {
    if (list.data?.counts) return list.data.counts
    const out: Record<string, number> = {}
    for (const p of entries) out[p.status] = (out[p.status] ?? 0) + 1
    return out
  }, [entries, list.data])

  // 标签计数只覆盖已载入的条目（分页还在往下加载），不是全库统计：
  // 界面上必须写明范围，否则 21 会被读成"全库有 21 条"。
  const tags = useMemo(() => {
    const out = new Map<string, number>()
    for (const p of entries) for (const t of p.tags ?? []) out.set(t, (out.get(t) ?? 0) + 1)
    return [...out.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  }, [entries])

  const filtered = useMemo(() => {
    const rows = [...entries]
    // The API orders confirmed knowledge first, then most recently updated
    // (internal/retrieval/playbooks.go). The default client sort must keep that
    // confirmed-first guarantee: a freshly touched candidate must not outrank
    // confirmed knowledge, and pagination has to stay a stable continuation of
    // the server order (id breaks ties).
    const confirmed = (p: Playbook) => (p.status === 'confirmed' ? 0 : 1)
    if (sort === 'title') rows.sort((a, b) => a.title.localeCompare(b.title, 'zh'))
    else {
      rows.sort((a, b) =>
        confirmed(a) - confirmed(b)
        || (b.updated_at > a.updated_at ? 1 : b.updated_at < a.updated_at ? -1 : 0)
        || a.id.localeCompare(b.id))
    }
    return rows
  }, [entries, sort])

  const current = filtered.find((p) => p.id === selected) ?? filtered[0]
  const hasFilter = Boolean(query || tag || status !== 'all')
  const firstLoad = list.loading && entries.length === 0

  async function more() {
    if (!nextCursor || busy || list.loading || list.refreshing) return
    const request = generation.current
    setBusy('page')
    setError('')
    try {
      const page = await api.playbooksPage({ ...params, cursor: nextCursor })
      if (request !== generation.current) return
      setExtra(old => [...old, ...page.playbooks])
      setNextCursor(page.next_cursor ?? '')
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function archive(p: Playbook) {
    const request = generation.current
    setBusy(p.id)
    setError('')
    try {
      await api.archivePlaybook(p, !p.archived)
      if (request === generation.current) list.reload()
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function readHistory(p: Playbook, cursor = '') {
    const request = generation.current
    setBusy(p.id)
    setError('')
    try {
      const page = await api.playbookHistory(p.id, cursor)
      if (request !== generation.current) return
      setHistory(old => ({
        id: p.id,
        rows: cursor && old?.id === p.id ? [...old.rows, ...page.history] : page.history,
        next: page.next_cursor,
      }))
    } catch (e) {
      if (request === generation.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (request === generation.current) setBusy('')
    }
  }

  async function copy(text: string, key: string) {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(key)
      window.setTimeout(() => setCopied((v) => (v === key ? '' : v)), 1200)
    } catch {
      /* clipboard is best-effort; the text is visible anyway */
    }
  }

  async function retire(p: Playbook) {
    const ok = await confirm({
      title: '退役这条战场知识？',
      description: `「${p.title}」不再进入执行者的派发简报。文本会保留，之后执行者同名声明可以让它复活。`,
      confirmLabel: '退役',
      destructive: true,
    })
    if (!ok) return
    setBusy(p.id)
    setError('')
    try {
      await api.retirePlaybook(p.id)
      list.reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function remove(p: Playbook) {
    if (!await confirm({title:'删除这条战场知识？',description:`删除「${p.title}」的记录；来源事实和已发布的世界知识保留。`,confirmLabel:'删除',destructive:true})) return
    setBusy(p.id); setError('')
    try { await api.deletePlaybook(p.id,p.updated_at); list.reload() }
    catch(e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy('') }
  }

  return (
    <Page
      title="战场知识"
      subtitle={`${archived ? '归档' : '当前'}知识 · 已载 ${entries.length}/${list.data?.total ?? 0} 条 · 已确认 ${counts.confirmed ?? 0} · 候选 ${counts.candidate ?? 0}`}
      scroll={false}
      className="@container"
    >
      <div className="flex h-full min-h-0 flex-col overflow-y-auto overscroll-contain @3xl:flex-row @3xl:overflow-hidden">
        <aside className="flex h-[min(42vh,22rem)] min-h-[280px] w-full shrink-0 flex-col border-b border-hairline @3xl:h-auto @3xl:min-h-0 @3xl:w-[360px] @3xl:border-b-0 @3xl:border-r">
          {/* One toolbar: search + facet toggle + refresh, all h-8. */}
          <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline p-3">
            <div className="relative min-w-0 flex-1 basis-[180px]">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
              <Input
                ref={searchInput}
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="搜索名称、何时用、命令、标签"
                className="h-8 pl-7 text-[12px]"
                aria-label="搜索战场知识"
              />
            </div>
            <Button
              size="sm"
              variant={archived ? 'secondary' : 'outline'}
              active={archived}
              className="h-8 shrink-0"
              title={archived ? '回到当前知识' : '只看已归档知识'}
              onClick={() => setArchived(v => !v)}
            >
              <Archive className="h-3.5 w-3.5" />
              {archived ? '查看当前' : '查看归档'}
            </Button>
            <Button
              size="icon-sm"
              variant="outline"
              className="shrink-0"
              aria-label="刷新战场知识"
              title="刷新列表"
              disabled={list.loading || list.refreshing}
              onClick={list.reload}
            >
              <RefreshCw className={`h-3.5 w-3.5 ${list.loading || list.refreshing ? 'motion-safe:animate-spin' : ''}`} />
            </Button>
          </div>

          <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-hairline px-3 py-2">
            <Chip
              active={status === 'all'}
              onClick={() => setStatus('all')}
              className="h-8 rounded-md px-2.5 text-[12px]"
            >
              全部
              <span className="tw-nums font-mono opacity-60">{Object.values(counts).reduce((sum, count) => sum + count, 0)}</span>
            </Chip>
            {STATUS_ORDER.map((s) => (
              <Chip
                key={s}
                active={status === s}
                onClick={() => setStatus(status === s ? 'all' : s)}
                className="h-8 rounded-md px-2.5 text-[12px]"
                title={STATUS[s]?.hint}
              >
                {STATUS[s]?.label ?? s}
                <span className="tw-nums font-mono opacity-60">{counts[s] ?? 0}</span>
              </Chip>
            ))}
            <Select value={sort} onValueChange={(v) => setSort(v as SortMode)}>
              <SelectTrigger className="ml-auto h-8 w-[104px] shrink-0 border-hairline bg-transparent px-2 text-[12px] text-ink-3" aria-label="排序">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="updated" className="text-[12px]">最近更新</SelectItem>
                <SelectItem value="title" className="text-[12px]">按名称</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {(tags.length > 0 || tag !== null) && (
            <div
              role="group"
              aria-label="标签筛选"
              className="flex max-h-24 shrink-0 flex-wrap items-center gap-1.5 overflow-y-auto border-b border-hairline px-3 py-1.5"
            >
              <span
                className="shrink-0 text-[11px] leading-7 text-ink-3"
                title="标签和后面的数字只统计已经载入到列表里的这些条目，不是全库计数；往下多载入一些会跟着变"
              >
                标签 · 已载入 {entries.length} 条内
              </span>
              {tag !== null && (
                <Chip active onClick={() => setTag(null)} aria-label="清除标签筛选" title="清除标签筛选" className="h-7 rounded-md px-2 text-[11px]">
                  全部
                </Chip>
              )}
              {tags.map(([name, count]) => (
                <Chip
                  key={name}
                  active={tag === name}
                  onClick={() => setTag(tag === name ? null : name)}
                  title={`已载入的 ${entries.length} 条里有 ${count} 条带这个标签；点一下按它筛选，会重新查服务端`}
                  className="h-7 max-w-full rounded-md px-2 text-[11px]"
                >
                  <span className="min-w-0 tw-break">{name}</span>
                  <span className="tw-nums shrink-0 rounded bg-muted px-1 font-mono text-ink-3">{count}</span>
                </Chip>
              ))}
            </div>
          )}

          <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
            {firstLoad && <SkeletonList count={6} />}
            {list.error && (
              <div className="p-1">
                <Callout tone="destructive">
                  <div className="flex flex-wrap items-center gap-2">
                    <span>读取战场知识失败：{list.error}</span>
                    <Button size="xs" variant="outline" onClick={list.reload}>重试</Button>
                  </div>
                </Callout>
              </div>
            )}
            {!firstLoad && !list.error && entries.length === 0 && hasFilter && (
              <EmptyState
                compact
                title="没有匹配的战场知识"
                description="换个关键词，或点掉状态 / 标签过滤。"
                action={
                  <Button size="sm" variant="outline" onClick={() => { setQ(''); setTag(null); setStatus('all') }}>
                    清除筛选
                  </Button>
                }
              />
            )}
            {!firstLoad && !list.error && entries.length === 0 && !hasFilter && (
              <EmptyState
                compact
                icon={BookMarked}
                title={archived ? '没有归档的战场知识' : '还没有战场知识'}
                description={
                  archived
                    ? '归档的知识会出现在这里；归档不改文本，仍可恢复。'
                    : '执行者在跑过的那一轮里声明一条，指挥官也可以提名候选；确认后会出现在这里。'
                }
                action={archived ? <Button size="sm" variant="outline" onClick={() => setArchived(false)}>查看当前</Button> : undefined}
              />
            )}
            <ul role="list" className="divide-y divide-hairline" aria-label="战场知识列表">
              {filtered.map((p, index) => {
                const st = statusMeta(p.status)
                const active = current?.id === p.id
                const rowTags = p.tags ?? []
                const orderedTags = tag && rowTags.includes(tag)
                  ? [tag, ...rowTags.filter((name) => name !== tag)]
                  : rowTags
                const previewTags = orderedTags.slice(0, 2)
                const remainingTags = orderedTags.slice(previewTags.length)
                return (
                  <li
                    key={p.id}
                    className={index >= 8 ? '[contain-intrinsic-size:auto_6.5rem] [content-visibility:auto]' : undefined}
                  >
                  <button
                    type="button"
                    onClick={() => setSelected(p.id)}
                    aria-current={active ? 'true' : undefined}
                    className={`block w-full rounded-lg px-2.5 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${
                      active ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]' : 'hover:bg-surface-2'
                    }`}
                  >
                    <div className="flex items-center gap-2">
                      <span className="min-w-0 truncate text-[13px] font-medium text-ink">{p.title}</span>
                      <Badge size="sm" variant={st.variant} className="ml-auto shrink-0">
                        {st.label}
                      </Badge>
                    </div>
                    <div className="mt-0.5 line-clamp-2 text-[12px] leading-relaxed text-ink-2">
                      {p.when}
                    </div>
                    <div className="mt-1 flex items-center gap-2">
                      <div className="flex min-w-0 flex-1 items-center gap-1">
                        {previewTags.map((t) => (
                          <Badge key={t} size="sm" variant={tag === t ? 'primary' : 'muted'} className="min-w-0" title={t}>
                            <span className="truncate">{t}</span>
                          </Badge>
                        ))}
                        {remainingTags.length > 0 && (
                          <Badge size="sm" variant="muted" className="shrink-0 tabular-nums" title={remainingTags.join('、')}>
                            +{remainingTags.length}
                          </Badge>
                        )}
                      </div>
                      <span className="shrink-0 font-mono text-[11px] text-ink-3">{fmtTime(p.updated_at).slice(5)}</span>
                    </div>
                  </button>
                  </li>
                )
              })}
            </ul>
            <p role="status" aria-live="polite" className="sr-only">
              已载入 {entries.length} 条战场知识
            </p>
          </div>

          <div className="flex shrink-0 flex-wrap items-center gap-2 border-t border-hairline px-3 py-2 text-[11px] text-ink-3">
            <span className="tw-nums font-mono">已载入 {entries.length} / {list.data?.total ?? 0} 条</span>
            {nextCursor && (
              <Button size="xs" variant="ghost" className="ml-auto" disabled={!!busy || list.loading} onClick={() => void more()}>
                {busy === 'page' ? '加载中…' : '加载更多'}
              </Button>
            )}
          </div>
        </aside>

        <section className="min-h-[280px] min-w-0 flex-1 overflow-y-auto overscroll-contain @3xl:min-h-0">
          {!current ? (
            <div className="flex h-full items-center justify-center">
              {list.error && entries.length === 0 ? (
                <EmptyState
                  icon={BookMarked}
                  title="战场知识读取失败"
                  description={list.error}
                  action={<Button size="sm" variant="outline" onClick={list.reload}>重试</Button>}
                />
              ) : entries.length === 0 ? (
                /* The list column already explains the empty library; a second
                   full empty state here would read as two errors. */
                <span className="text-[12px] text-ink-3">选中左侧条目查看详情</span>
              ) : (
                <EmptyState
                  icon={BookMarked}
                  title="没有匹配项"
                  description="换个关键词或状态试试。"
                />
              )}
            </div>
          ) : (
            <article className="mx-auto max-w-3xl px-5 py-5 sm:px-6">
              <header className="flex flex-wrap items-center gap-2">
                <h2 className="text-h4 min-w-0 break-words font-semibold text-ink">{current.title}</h2>
                <Badge variant={statusMeta(current.status).variant}>
                  {statusMeta(current.status).label}
                </Badge>
                {(() => {
                  const o = outcomeMeta(current.outcome)
                  const Icon = o.icon
                  return (
                    <Badge variant={o.variant} className="gap-1">
                      <Icon className="h-3.5 w-3.5" />
                      {o.label}
                    </Badge>
                  )
                })()}
                <span className="ml-auto flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
                  <span>{originLabel(current.origin)}</span>
                  {current.intent_id && <span className="font-mono">· {current.intent_id}</span>}
                  <span>· 更新于 {fmtTime(current.updated_at)}</span>
                  <span className="font-mono">· {current.id}</span>
                </span>
              </header>

              <p className="mt-1.5 text-[13px] leading-relaxed text-ink-2">{current.when}</p>

              {current.status === 'candidate' && (
                <Callout tone="warning" className="mt-3">
                  {current.origin === 'executor'
                    ? '执行者已声明这条知识。来源事实缺失、未核验、已失效，或缺少原始证据登记时，条目显示为候选。'
                    : '这条知识由指挥官提名，目前显示为候选。'}
                </Callout>
              )}
              {current.outcome === 'dead_end' && (
                <Callout tone="info" className="mt-3">
                  这条路走不通。除非前提（版本、入口、权限）变了，不要原样重试。
                </Callout>
              )}

              {current.doc && (
                <div className="mt-5">
                  <div className="tw-eyebrow mb-1.5">命令 / 判据 / 坑</div>
                  <div className="relative">
                    <pre className="tw-inset tw-break overflow-x-auto whitespace-pre-wrap rounded-md px-2.5 py-2 pr-20 font-mono text-[12px] leading-relaxed text-ink-2">
                      {current.doc}
                    </pre>
                    <Button
                      size="xs"
                      variant="ghost"
                      className="absolute right-1 top-1 h-7 gap-1 text-[11px]"
                      onClick={() => copy(current.doc ?? '', current.id)}
                      title="复制战场知识"
                    >
                      {copied === current.id ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                      {copied === current.id ? '已复制' : '复制'}
                    </Button>
                  </div>
                </div>
              )}

              {(current.tags ?? []).length > 0 && (
                <div role="group" aria-label="条目标签" className="mt-4 flex flex-wrap gap-1.5">
                  {(current.tags ?? []).map((t) => (
                    <Chip
                      key={t}
                      active={tag === t}
                      onClick={() => setTag(tag === t ? null : t)}
                      className="min-h-7 min-w-0 max-w-full rounded-md px-2 text-left text-[11px]"
                    >
                      <span className="tw-break min-w-0">{t}</span>
                    </Chip>
                  ))}
                </div>
              )}

              {(current.from_fact_ids ?? []).length > 0 && (
                <div className="mt-4">
                  <div className="tw-eyebrow mb-1.5">来源事实</div>
                  <div className="flex flex-wrap gap-1.5">
                    {(current.from_fact_ids ?? []).map((id) => (
                      <Badge key={id} size="sm" variant="muted" className="tw-break font-mono">
                        {id}{current.from_fact_versions?.[id] ? ` · v${current.from_fact_versions[id]}` : ''}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}

              {current.source_run_id && (
                <div className="mt-3 text-[11px] text-ink-3">
                  最近声明运行：<a className="font-mono underline decoration-hairline underline-offset-2 hover:text-ink" href={`?view=runs&run=${encodeURIComponent(current.source_run_id)}`}>{current.source_run_id}</a>
                </div>
              )}

              {error && (
                <Callout tone="destructive" className="mt-4">
                  {error}
                </Callout>
              )}

              <footer className="mt-6 flex flex-wrap items-center gap-2 border-t border-hairline pt-3 text-[11px] text-ink-3">
                <span>项目私有知识：不是事实、不进报告、不算覆盖。</span>
                <span className="ml-auto flex items-center gap-1.5">
                  {current.status === 'confirmed' && <Sparkles className="h-3.5 w-3.5" />}
                  {current.status === 'retired' && <Archive className="h-3.5 w-3.5" />}
                </span>
                <Button size="sm" variant="outline" disabled={!!busy} onClick={() => window.location.assign(`/g/?view=kb&source_project=${encodeURIComponent(projectID())}&source_playbook=${encodeURIComponent(current.id)}`)}>
                  <Globe className="h-3.5 w-3.5" />提名世界知识
                </Button>
                <Button size="sm" variant="outline" disabled={!!busy} onClick={() => void archive(current)}>
                  {current.archived ? '恢复' : '归档'}
                </Button>
                <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => void readHistory(current)}>修订历史</Button>
                {current.status !== 'retired' && (
                  <Button
                    size="sm"
                    variant="outline"
                    className="border-destructive/35 text-destructive hover:border-destructive/50 hover:bg-destructive/10"
                    disabled={busy === current.id}
                    onClick={() => retire(current)}
                  >
                    {busy === current.id ? '退役中…' : '退役'}
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={!!busy}
                  onClick={() => remove(current)}
                >
                  <Trash2 className="h-3.5 w-3.5" />删除
                </Button>
              </footer>
              {history?.id === current.id && (
                <div className="mt-3 space-y-2 border-t border-hairline pt-2 text-[11px]">
                  {history.rows.map((h, i) => (
                    <details key={`${h.version}:${i}`} className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-1.5">
                      <summary className="cursor-pointer select-none text-ink-3 hover:text-ink">
                        <span className="tw-nums font-mono">v{h.version}</span> · {h.operation} · {fmtTime(h.at)}
                      </summary>
                      <pre className="tw-break mt-1 whitespace-pre-wrap font-mono text-[11px] text-ink-2">{JSON.stringify(h.value, null, 2)}</pre>
                    </details>
                  ))}
                  {history.next && <Button size="xs" variant="ghost" disabled={!!busy} onClick={() => void readHistory(current, history.next)}>继续读取历史</Button>}
                </div>
              )}
            </article>
          )}
        </section>
      </div>
    </Page>
  )
}
