import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { BookOpen, FilePlus2, RefreshCw, Search, Trash2, TriangleAlert, Upload } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Field,
  Input,
  ListRow,
  SkeletonList,
  Spinner,
  Tabs,
  TabsList,
  TabsTrigger,
  Textarea,
  useConfirm,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { useDebounced, useLoad } from '@/lib/hooks'
import type { KBCandidate, KBEntry, Playbook } from '@/lib/types'

const blank = (): KBEntry => ({id: '', title: '', description: '', detail: '', source: '', tags: [], source_refs: []})
const lines = (s: string) => s.split('\n').map(v => v.trim()).filter(Boolean)

// Built-in entries ship in the binary and belong to the repository; instance
// entries live only in this instance's data directory. An instance entry may
// reuse a built-in id, which shadows the built-in text for this instance only.
function layerBadge(entry: KBEntry): { label: string; variant: 'muted' | 'primary' | 'warning' } {
  if (entry.origin === 'builtin') return {label: '内置', variant: 'muted'}
  if (entry.covers_builtin) return {label: '实例覆盖内置', variant: 'warning'}
  return {label: '实例', variant: 'primary'}
}

export function Knowledge() {
  const confirm = useConfirm()
  const params = new URLSearchParams(window.location.search)
  const sourceProject = params.get('source_project') ?? ''
  const sourceID = params.get('source_playbook') ?? ''
  const source = useLoad(() => sourceProject && sourceID ? api.playbookSource(sourceProject,sourceID) : Promise.resolve<Playbook | null>(null), [sourceProject,sourceID])
  const sourceOpened = useRef(false)
  const list = useLoad(api.kb, [])
  const drafts = useLoad(api.kbCandidates, [])
  const [tab, setTab] = useState<'published' | 'candidates'>('published')
  const [layer, setLayer] = useState<'all' | 'builtin' | 'instance'>('all')
  const [q, setQ] = useState('')
  const query = useDebounced(q, 150)
  const [tag, setTag] = useState<string | null>(null)
  const [selected, setSelected] = useState('')
  const [editing, setEditing] = useState<{entry: KBEntry; reason: string; base_revision: string; id_locked: boolean; source?: {id: string; project: string; updated_at: string}; id?: string; version?: number} | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const entries = list.data?.entries ?? []
  const candidates = drafts.data ?? []
  const builtinCount = entries.filter(entry => entry.origin === 'builtin').length
  const instanceCount = entries.length - builtinCount
  const published = layer === 'all' ? entries : entries.filter(entry => entry.origin === layer)
  const rows = tab === 'published' ? published.map(entry => ({key: entry.id, entry, candidate: undefined as KBCandidate | undefined})) : candidates.map(candidate => ({key: candidate.id, entry: candidate.entry, candidate}))
  const filtered = rows.filter(row => (!tag || row.entry.tags?.includes(tag)) && JSON.stringify(row.entry).toLowerCase().includes(query.trim().toLowerCase()))
  const current = filtered.find(row => row.key === selected) ?? filtered[0]
  const tags = useMemo(() => [...new Set(entries.flatMap(e => e.tags ?? []))].sort(), [entries])
  // 选中的标签可能因条目被删除或重新加载而从 tags 消失：它不再渲染成可点的 chip，
  // 列表却会一直空着、刷新也救不回来。标签集合变化时清掉这个悬空筛选。
  useEffect(() => {
    if (tag && !tags.includes(tag)) setTag(null)
  }, [tag, tags])
  const refresh = () => { list.reload(); drafts.reload() }
  const pendingCandidates = candidates.filter(c => !c.published).length
  async function act(action: () => Promise<unknown>, message: string) {
    setBusy(true); setError(''); setNotice('')
    try { await action(); refresh(); setNotice(message) }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  function edit(entry: KBEntry, candidate?: KBCandidate, playbook?: Playbook) {
    sourceOpened.current = true
    setEditing({entry: {...entry}, reason: candidate?.reason ?? '', base_revision: candidate?.base_revision ?? list.data?.revisions?.[entry.id] ?? '', id_locked:!!candidate?.id || !!entry.id, source:playbook ? {id:playbook.id,project:sourceProject,updated_at:playbook.updated_at} : undefined, id: candidate?.id, version: candidate?.version}); setError(''); setNotice('')
  }
  useEffect(() => { if (source.data && !sourceOpened.current) edit({...blank(),title:source.data.title},undefined,source.data) }, [source.data])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!editing) return
    const body = {id:editing.id, version:editing.version, base_revision:editing.base_revision, reason:editing.reason, entry: {...editing.entry, tags: editing.entry.tags?.map(t => t.trim()).filter(Boolean), source_refs: lines((editing.entry.source_refs ?? []).join('\n'))}}
    await act(async () => { const row = !body.id && editing.source ? await api.nominatePlaybookKB(editing.source.project,editing.source.id,{...body,source_updated_at:editing.source.updated_at}) : await api.saveKBCandidate(body); setSelected(row.id); setTab('candidates'); setEditing(null) }, '候选已保存；由你点击发布后才会进入世界知识。')
  }
  async function remove() {
    if (!current) return
    const c = current.candidate
    const builtin = current.entry.origin === 'builtin'
    if (!await confirm({title:c ? '删除这条候选？' : '删除这条世界知识？',description:c ? '删除候选记录，已发布的世界知识保留。' : builtin ? '内置条目在本实例屏蔽，仓库资产和其他实例不受影响。' : '只从本实例移除；内置条目和仓库资产不受影响。',confirmLabel:'删除',destructive:true})) return
    await act(() => c ? api.deleteKBCandidate(c.id,c.version) : api.deleteKB(current.entry.id,list.data?.revisions?.[current.entry.id] ?? ''),c ? '候选已删除。' : builtin ? '内置条目已在当前实例屏蔽，仓库内容不变。' : '知识已从当前实例移除。')
  }

  const errorCallout = (list.error || drafts.error) && (
    <div role="alert">
      <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span className="min-w-0 saker-break">{list.error || drafts.error}</span>
          <Button size="xs" variant="outline" disabled={busy} onClick={refresh}>重试</Button>
        </div>
      </Callout>
    </div>
  )

  return (
    <Page
      title="世界知识"
      subtitle={`${entries.length} 条已发布（内置 ${builtinCount} · 实例 ${instanceCount}） · ${pendingCandidates} 条待晋升 · 实例共享，按需检索`}
      className="@container"
      actions={
        <>
          <Button size="xs" variant="outline" disabled={busy} onClick={() => act(api.reloadKB, '已重新加载磁盘条目，查询立即使用新版本。')}>
            <RefreshCw className="h-3.5 w-3.5" />重新加载
          </Button>
          {!editing && (
            <Button size="xs" disabled={busy} onClick={() => edit(blank())}>
              <FilePlus2 className="h-3.5 w-3.5" />新建候选
            </Button>
          )}
        </>
      }
      scroll={false}
    >
      <div className="flex h-full min-h-0 flex-col">
        {/* One filter row: category tabs, search, tag filters and the match
            count all at h-8 so the list starts high on the page. */}
        <div className="shrink-0 border-b border-hairline px-4 py-3 sm:px-5">
          <div className="flex flex-wrap items-center gap-2">
            <Tabs value={tab} onValueChange={(value) => { setTab(value as 'published' | 'candidates'); setSelected(''); setEditing(null) }}>
              <TabsList className="h-8">
                <TabsTrigger value="published" className="h-6 gap-1.5 px-2.5 text-[12px]" disabled={busy}>
                  已发布知识 <span className="saker-nums font-mono text-[11px] text-ink-3">{entries.length}</span>
                </TabsTrigger>
                <TabsTrigger value="candidates" className="h-6 gap-1.5 px-2.5 text-[12px]" disabled={busy}>
                  晋升候选 <span className="saker-nums font-mono text-[11px] text-ink-3">{pendingCandidates}</span>
                </TabsTrigger>
              </TabsList>
            </Tabs>
            {tab === 'published' && (
              <div className="flex items-center gap-1" role="group" aria-label="按来源筛选">
                {([['all', '全部', entries.length], ['builtin', '内置', builtinCount], ['instance', '实例', instanceCount]] as const).map(([value, label, count]) => (
                  <Chip
                    key={value}
                    active={layer === value}
                    onClick={() => { setLayer(value); setSelected(''); setEditing(null) }}
                    className="h-7 px-2 text-[11px]"
                  >
                    {label} <span className="saker-nums font-mono text-[10px] text-ink-3">{count}</span>
                  </Chip>
                ))}
              </div>
            )}
            <div className="relative min-w-[180px] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" aria-hidden="true" />
              <Input
                aria-label="搜索知识"
                value={q}
                onChange={e => setQ(e.target.value)}
                placeholder="搜索知识、适用条件、来源"
                className="h-8 pl-8 text-[12px]"
              />
            </div>
            <span className="saker-nums shrink-0 font-mono text-[11px] text-ink-3">
              显示 {filtered.length} / {rows.length}
            </span>
            <Button size="sm" variant="ghost" disabled={busy} onClick={refresh}>刷新列表</Button>
          </div>
          {tags.length > 0 && (
            <div className="mt-2 flex max-h-16 flex-wrap gap-1 overflow-auto">
              {tag && (
                <Chip active onClick={() => setTag(null)} className="text-[11px]">
                  全部
                </Chip>
              )}
              {tags.map(t => (
                <Chip key={t} active={tag === t} onClick={() => setTag(tag === t ? null : t)} className="text-[11px]">
                  {t}
                </Chip>
              ))}
            </div>
          )}
        </div>

        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain @3xl:flex-row @3xl:overflow-hidden">
          <aside className="flex h-[min(42vh,22rem)] min-h-[280px] w-full shrink-0 flex-col border-b border-hairline @3xl:h-auto @3xl:min-h-0 @3xl:w-[320px] @3xl:border-b-0 @3xl:border-r @5xl:w-[360px]">
            <div className="min-h-0 flex-1 overflow-auto overscroll-contain p-2 [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin]">
              {(list.loading || drafts.loading) && <SkeletonList count={5} />}
              {errorCallout}
              {filtered.map((row, index) => {
                const active = current?.key === row.key
                return (
                  <ListRow
                    key={row.key}
                    active={active}
                    disabled={busy}
                    onClick={() => { setSelected(row.key); setEditing(null) }}
                    className={`mb-1 min-h-[56px] gap-2.5 px-2.5 py-2.5 ${index >= 8 ? '[contain-intrinsic-size:auto_4.5rem] [content-visibility:auto]' : ''}`}
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-[13px] font-medium text-ink">{row.entry.title}</span>
                      {row.candidate ? (
                        <Badge variant={row.candidate.published ? 'success' : 'warning'} size="sm" className="shrink-0">
                          {row.candidate.published ? '已发布' : '候选'}
                        </Badge>
                      ) : (
                        <Badge variant={layerBadge(row.entry).variant} size="sm" className="shrink-0">{layerBadge(row.entry).label}</Badge>
                      )}
                    </span>
                    <span className="mt-1 line-clamp-2 text-[12px] leading-5 text-ink-2">{row.entry.description}</span>
                    <span className="mt-1 block truncate font-mono text-[11px] text-ink-3">{row.entry.id}</span>
                  </ListRow>
                )
              })}
              {!list.loading && !drafts.loading && filtered.length === 0 && (
                <EmptyState
                  compact
                  icon={BookOpen}
                  title={rows.length === 0 ? (tab === 'published' ? (layer === 'all' ? '还没有已发布知识' : `没有${layer === 'builtin' ? '内置' : '实例'}条目`) : '还没有晋升候选') : '没有匹配的条目'}
                  description={rows.length === 0
                    ? (tab === 'published'
                      ? (layer === 'all' ? '内置条目随 saker 发布；实例条目在本页发布后只属于当前实例。' : '切回「全部」查看其他来源的条目。')
                      : '晋升候选由运行沉淀，也可以在右上角「新建候选」手动补充事实与来源后发布。')
                    : '换个关键词，或清掉标签筛选。'}
                />
              )}
            </div>
            <div className="flex shrink-0 items-center gap-2 border-t border-hairline px-3 py-2 text-[11px] text-ink-3">
              <span role="status" aria-live="polite" className="saker-nums font-mono">显示 {filtered.length} / {rows.length}</span>
              <Button size="xs" variant="ghost" disabled={busy} onClick={refresh} className="ml-auto">刷新列表</Button>
            </div>
          </aside>

          <section className="min-h-[280px] min-w-0 flex-1 overflow-auto overscroll-contain @3xl:min-h-0">
            <div className="mx-auto max-w-3xl space-y-4 p-4 sm:p-6">
              {error && <div role="alert"><Callout tone="destructive">{error}</Callout></div>}
              {notice && <Callout tone="success">{notice}</Callout>}
              {source.error && (
                <div role="alert">
                  <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                      <span className="min-w-0 saker-break">来源战场知识读取失败：{source.error}</span>
                      <Button size="xs" variant="outline" onClick={source.reload}>重试</Button>
                    </div>
                  </Callout>
                </div>
              )}
              {source.data && (
                <details className="rounded-lg border border-hairline bg-surface-2/40 p-3 text-[12px] leading-6">
                  <summary className="cursor-pointer font-medium text-ink">来源战场知识：{source.data.title}（仅供编辑参考）</summary>
                  <p className="mt-2 text-ink-2">{source.data.when}</p>
                  <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words text-[12px] text-ink-2">{source.data.doc}</pre>
                  <p className="mt-2 text-ink-3">请人工提取通用事实、补齐来源和适用条件。原文不会自动进入世界 KB。</p>
                </details>
              )}

              {editing ? (
                <Panel title={editing.id ? '编辑晋升候选' : '提名世界知识'} extra={<Badge variant="outline">保存后仍是候选</Badge>}>
                  <form onSubmit={save}>
                    <fieldset disabled={busy} className="min-w-0 space-y-4">
                      <p className="text-[12px] leading-5 text-ink-3">
                        保留通用事实、适用条件与来源。项目凭据、临时环境状态和工具教程留在原处。
                      </p>
                      {editing.entry.origin === 'builtin' && (
                        <Callout tone="info">这是内置条目：保存并发布只会生成当前实例的覆盖，内置文本与仓库资产都不变。</Callout>
                      )}
                      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
                        <Field label="稳定 ID" hint="发布后以此 ID 引用；不可与已发布条目重复。">
                          <Input
                            required
                            disabled={editing.id_locked}
                            aria-label="稳定 ID"
                            value={editing.entry.id}
                            onChange={e => setEditing({...editing, base_revision:list.data?.deleted_revisions?.[e.target.value] ?? '', entry: {...editing.entry, id:e.target.value}})}
                            placeholder="例如 protocol-example"
                            className="font-mono text-[12px]"
                          />
                        </Field>
                        <Field label="标题">
                          <Input required aria-label="标题" value={editing.entry.title} onChange={e => setEditing({...editing, entry: {...editing.entry, title:e.target.value}})} />
                        </Field>
                      </div>
                      {!editing.id_locked && editing.base_revision && (
                        <Callout tone="info">此 ID 在当前实例已删除；显式发布后将重新收录。</Callout>
                      )}
                      {([['description', '摘要', 2], ['detail', '事实正文', 7], ['applicability', '适用范围与前提条件', 3], ['source', '来源说明', 2]] as const).map(([key, label, height]) => (
                        <Field key={key} label={label}>
                          <Textarea
                            rows={height}
                            required={key === 'description' || key === 'detail'}
                            aria-label={label}
                            value={editing.entry[key] ?? ''}
                            onChange={e => setEditing({...editing, entry:{...editing.entry, [key]:e.target.value}})}
                          />
                        </Field>
                      ))}
                      <Field label="来源引用" hint="每行一条；可写文件路径、URL 或证据 ID。">
                        <Textarea
                          rows={3}
                          aria-label="来源引用"
                          className="font-mono text-[12px]"
                          value={(editing.entry.source_refs ?? []).join('\n')}
                          onChange={e => setEditing({...editing, entry: {...editing.entry, source_refs:e.target.value.split('\n')}})}
                        />
                      </Field>
                      <Field label="标签" hint="逗号分隔；会出现在左侧标签筛选里。">
                        <Input aria-label="标签" value={(editing.entry.tags ?? []).join(',')} onChange={e => setEditing({...editing, entry: {...editing.entry, tags:e.target.value.split(',')}})} />
                      </Field>
                      <Field label="为什么可以跨项目复用" hint="晋升理由随候选一起保存，发布前请写清楚适用边界。">
                        <Textarea required rows={3} aria-label="为什么可以跨项目复用" value={editing.reason} onChange={e => setEditing({...editing, reason:e.target.value})} />
                      </Field>
                      {editing.entry.commands?.length ? (
                        <Callout tone="info">原条目包含验证命令，保存时会一并保留。发布前请核对其适用条件。</Callout>
                      ) : null}
                      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-4">
                        <Button disabled={busy} type="submit" size="sm" aria-busy={busy}>
                          {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
                          {busy ? '保存中…' : '保存候选'}
                        </Button>
                        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => setEditing(null)}>取消</Button>
                      </div>
                    </fieldset>
                  </form>
                </Panel>
              ) : current ? (
                <Panel
                  title={<span className="truncate" title={current.entry.title}>{current.entry.title}</span>}
                  extra={
                    <div className="flex flex-wrap items-center justify-end gap-1.5">
                      <Badge variant="muted" className="hidden font-mono sm:inline-flex">{current.entry.id}</Badge>
                      {!current.candidate && (
                        <Badge variant={layerBadge(current.entry).variant} size="sm" className="shrink-0">{layerBadge(current.entry).label}</Badge>
                      )}
                      <Button size="xs" variant="outline" disabled={busy} onClick={() => edit(current.entry, current.candidate)}>编辑候选</Button>
                      <Button size="xs" variant="ghost" disabled={busy} className="text-ink-2 hover:text-destructive" onClick={remove}>
                        <Trash2 className="h-3.5 w-3.5" />{current.candidate ? '删除候选' : '删除知识'}
                      </Button>
                      {current.candidate && !current.candidate.published && (
                        <Button size="xs" variant="soft" disabled={busy} aria-busy={busy} onClick={() => act(() => api.publishKB(current.candidate!.id, current.candidate!.version), '已发布到本实例，所有项目的世界知识查询立即生效。')}>
                          <Upload className="h-3.5 w-3.5" />发布到本实例
                        </Button>
                      )}
                    </div>
                  }
                  bodyClassName="space-y-4"
                >
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Badge variant="muted" className="font-mono sm:hidden">{current.entry.id}</Badge>
                    {current.entry.tags?.map(t => (
                      <Chip key={t} active={tag === t} onClick={() => setTag(tag === t ? null : t)} className="h-6 px-2 text-[11px]">{t}</Chip>
                    ))}
                  </div>
                  {!current.candidate && (
                    <p className="flex flex-wrap items-center gap-2 text-[12px] leading-6 text-ink-3">
                      <Badge variant={layerBadge(current.entry).variant} size="sm">{layerBadge(current.entry).label}</Badge>
                      {current.entry.origin === 'builtin'
                        ? '随 saker 二进制发布，由仓库作者维护；本页的编辑与删除只作用于当前实例。'
                        : current.entry.covers_builtin
                          ? '当前实例对内置条目的覆盖，只在本实例生效；内置文本与仓库资产不变。'
                          : '只属于当前实例，不会进入仓库内置知识。'}
                    </p>
                  )}
                  <p className="text-[13px] leading-6 text-ink-2">{current.entry.description}</p>
                  {current.entry.applicability && (
                    <div className="rounded-lg border border-primary/20 bg-primary/[0.05] p-4">
                      <h3 className="saker-eyebrow mb-2 normal-case tracking-normal">适用范围与前提条件</h3>
                      <p className="whitespace-pre-wrap break-words text-[13px] leading-6 text-ink-2">{current.entry.applicability}</p>
                    </div>
                  )}
                  <p className="whitespace-pre-wrap break-words text-[13px] leading-7 text-ink-2">{current.entry.detail}</p>
                  {current.entry.commands?.map((cmd, i) => (
                    <pre key={i} className="saker-break overflow-auto rounded-lg border border-hairline bg-surface-2/50 p-3 font-mono text-[12px] text-ink-2">{cmd}</pre>
                  ))}
                  {current.candidate && (
                    <div className="rounded-lg border border-hairline bg-surface-2/40 p-4 text-[12px] leading-6">
                      <p className="font-medium text-ink">晋升理由</p>
                      <p className="mt-1 whitespace-pre-wrap break-words text-ink-2">{current.candidate.reason}</p>
                      <p className="mt-2 break-all font-mono text-[11px] text-ink-3">
                        {current.candidate.origin} · 候选 v{current.candidate.version}
                      </p>
                    </div>
                  )}
                  <footer className="space-y-2 border-t border-hairline pt-4 text-[12px] leading-6 text-ink-3">
                    <p className="whitespace-pre-wrap break-words">{current.entry.source || '来源见引用'}</p>
                    {current.entry.source_refs?.map((ref, i) => <p key={i} className="break-all font-mono">{ref}</p>)}
                  </footer>
                </Panel>
              ) : (
                <div className="saker-panel rounded-xl">
                  <EmptyState
                    icon={BookOpen}
                    title="尚无知识"
                    description="从左侧选择条目，或点右上角「新建候选」补充事实与来源后发布。内置条目随 saker 二进制发布，实例条目只属于当前实例。"
                    action={<Button size="sm" variant="outline" disabled={busy} onClick={() => edit(blank())}><FilePlus2 className="h-3.5 w-3.5" />新建候选</Button>}
                  />
                </div>
              )}

              <p className="rounded-lg border border-hairline bg-surface-2/40 p-3 text-[12px] leading-6 text-ink-3">
                内置条目随 saker 二进制发布，只有仓库作者维护 <code className="font-mono text-[11px] text-ink-2">internal/kb/assets/entries</code>；本页的发布和删除只写当前实例的 <code className="font-mono text-[11px] text-ink-2">&lt;data&gt;/kb</code>，不会进入仓库，也没有导入脚本。
              </p>
            </div>
          </section>
        </div>
      </div>
    </Page>
  )
}
