import { useEffect, useRef, useState } from 'react'
import { Activity, ArrowUpRight, ChevronDown, Download, FolderKanban, Pencil, Play, Plus, RefreshCw, Search, SearchX, Trash2, TriangleAlert, Upload, X } from 'lucide-react'
import { cn } from '@cyber/theme'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Input,
  SkeletonList,
  Spinner,
  StatTile,
  StatusIndicator,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { NewProjectDialog } from '@/components/NewProjectDialog'
import { DeleteProjectDialog } from '@/components/DeleteProjectDialog'
import { api, gotoProject } from '@/lib/api'
import { DOCUMENT_KINDS, DOCUMENT_META } from '@/lib/documents'
import type { DocumentsResponse } from '@/lib/types'
import { fmtInt, fmtTime } from '@/lib/format'
import type { ProjectInfo, SkippedProject, VulnLibraryQuarantine } from '@/lib/types'

/**
 * Data-root directories that did not load. This is a standing notice, not a
 * toast: the project stays missing until someone fixes or moves the directory,
 * so every visit must say so — no dismiss button, nothing that auto-hides. The
 * rest of the instance is unaffected, and the loader never deletes or rewrites
 * the directory, so the notice also says what was *not* done to it.
 */
export function SkippedProjectsNotice({ skipped }: { skipped: SkippedProject[] }) {
  if (skipped.length === 0) return null
  return (
    <Callout tone="warning" icon={<TriangleAlert aria-hidden="true" className="h-4 w-4" />}>
      <div className="min-w-0 space-y-2">
        <p className="text-[13px] leading-relaxed text-ink">
          <span className="font-medium">{fmtInt(skipped.length)} 个项目目录没加载</span>
          ：其它项目照常启动与读写。这些目录原样保留——不删、不改、也不新建空账本；修好或移走该目录后重启即可重新加载。
        </p>
        <ul role="list" aria-label="没有加载的项目目录" className="space-y-1.5">
          {skipped.map((item) => (
            <li
              key={`${item.id}:${item.dir}`}
              className="min-w-0 rounded-lg border border-hairline bg-surface-2/40 px-2.5 py-2 text-[12px] leading-relaxed"
            >
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-mono text-[11px] text-ink">{item.id || '（无 id）'}</span>
                <Badge size="sm" variant="warning">没加载</Badge>
              </div>
              <div className="saker-break mt-1 font-mono text-[11px] text-ink-3" title={item.dir}>{item.dir}</div>
              <div className="saker-break mt-1 text-ink-2">{item.error}</div>
            </li>
          ))}
        </ul>
      </div>
    </Callout>
  )
}

/**
 * The vuln-library index was set aside at boot. This hides no project, but it
 * does make every 漏洞 search return nothing — an operator who is not told reads
 * that as "the library has no entries", which is a different fact. The file was
 * renamed rather than truncated or deleted, so the notice also says where it
 * went and that the ledger's facts are untouched.
 */
export function VulnLibraryQuarantineNotice({ quarantine }: { quarantine?: VulnLibraryQuarantine | null }) {
  if (!quarantine) return null
  return (
    <Callout tone="warning" icon={<TriangleAlert aria-hidden="true" className="h-4 w-4" />}>
      <div className="min-w-0 space-y-2">
        <p className="text-[13px] leading-relaxed text-ink">
          <span className="font-medium">漏洞库索引损坏，已隔离并以空库启动</span>
          ：这份索引读不成有效格式，启动时已把它改名另存（不删、不截断），实例与所有项目照常运行。注意按漏洞库检索现在返回空——账本里的事实不受影响，这不代表「库里没有漏洞」。
        </p>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px] leading-relaxed">
          <dt className="text-ink-3">另存为</dt>
          <dd className="saker-break font-mono text-[11px] text-ink-2">{quarantine.path}</dd>
          <dt className="text-ink-3">原因</dt>
          <dd className="saker-break text-ink-2">{quarantine.reason}</dd>
          <dt className="text-ink-3">隔离时间</dt>
          <dd className="text-ink-2">{fmtTime(quarantine.quarantined_at)}</dd>
        </dl>
      </div>
    </Callout>
  )
}

function ProjectMetric({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="min-w-0 px-2 py-2.5 text-center" title={title}>
      <dt className="text-[12px] leading-4 text-ink-2">{label}</dt>
      <dd className="saker-nums mt-1 truncate font-mono text-[15px] font-semibold leading-5 text-ink">{value}</dd>
    </div>
  )
}

export function Projects({
  projects,
  skipped = [],
  quarantine = null,
  current,
  onChanged,
  onDeleted,
  refreshing,
  loading,
}: {
  projects: ProjectInfo[]
  /** Data-root directories that failed to load; empty on a healthy instance. */
  skipped?: SkippedProject[]
  /** A vuln-library index set aside at boot; null on a healthy boot. */
  quarantine?: VulnLibraryQuarantine | null
  current: string
  onChanged: () => void
  onDeleted: (id: string) => void
  refreshing: boolean
  loading: boolean
}) {
  const [editing, setEditing] = useState<ProjectInfo | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<ProjectInfo | null>(null)
  // Whole-project transfer: export downloads one .sakerproj; import uploads one.
  // Large files are streamed by the browser, never parsed in the page.
  const [exporting, setExporting] = useState('')
  const [importing, setImporting] = useState(false)
  const [transferError, setTransferError] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const [search, setSearch] = useState('')
  const query = search.trim().toLowerCase()
  const visibleProjects = query ? projects.filter(project =>
    [project.name, project.id, project.scope].some(value => value?.toLowerCase().includes(query)),
  ) : projects
  const activeProjects = projects.filter(project => project.running > 0).length
  const running = projects.reduce((total, project) => total + project.running, 0)

  function clearSearch() {
    setSearch('')
    searchInput.current?.focus()
  }

  // The brief and the constraint document are read here, not written: their one
  // write entry is the project's 项目文档 page (PUT /api/documents/{kind} with a
  // revision), so this dialog only shows the three documents and links there.
  const [docs, setDocs] = useState<DocumentsResponse | null>(null)
  const [docsError, setDocsError] = useState('')
  function openScope(p: ProjectInfo) {
    setEditing(p)
  }
  useEffect(() => {
    if (!editing) return
    let alive = true
    setDocs(null)
    setDocsError('')
    api.projectDocuments(editing.id)
      .then((response) => { if (alive) setDocs(response) })
      .catch((e) => { if (alive) setDocsError(e instanceof Error ? e.message : String(e)) })
    return () => { alive = false }
  }, [editing])

  function deleted(id: string) {
    onDeleted(id)
    if (id === current) {
      const next = projects.find(p => p.id !== id)
      if (next) gotoProject(next.id)
      else window.location.assign('/g/?view=projects')
    }
  }

  async function exportProject(p: ProjectInfo) {
    if (exporting) return
    setExporting(p.id)
    setTransferError('')
    try {
      const res = await api.exportProject(p.id)
      // The endpoint answers with a download path; the browser saves the
      // attachment and stays on this page.
      window.location.assign(api.projectExportURL(res.name))
    } catch (e) {
      setTransferError(e instanceof Error ? e.message : String(e))
    } finally {
      setExporting('')
    }
  }

  async function importFile(file: File | undefined) {
    if (!file || importing) return
    setImporting(true)
    setTransferError('')
    try {
      const res = await api.importProject(file)
      onChanged()
      gotoProject(res.project.id)
    } catch (e) {
      setTransferError(e instanceof Error ? e.message : String(e))
    } finally {
      setImporting(false)
      if (fileInput.current) fileInput.current.value = ''
    }
  }

  return (
    <Page
      title="项目"
      subtitle="管理项目及其账本，支持 .sakerproj 文件导入与导出。"
      className="@container overscroll-contain"
      actions={
        <>
          <Button
            size="icon-sm"
            variant="ghost"
            onClick={onChanged}
            disabled={loading || refreshing}
            aria-label="刷新项目列表"
            aria-busy={refreshing}
            title="重新拉取项目列表"
          >
            <RefreshCw className={cn('h-3.5 w-3.5', refreshing && 'motion-safe:animate-spin')} />
          </Button>
          <input
            ref={fileInput}
            type="file"
            accept=".sakerproj,application/zip"
            className="hidden"
            aria-hidden="true"
            onChange={(e) => void importFile(e.target.files?.[0])}
          />
          <Button size="sm" variant="outline" className="gap-1.5" onClick={() => fileInput.current?.click()} disabled={importing} aria-busy={importing}>
            {importing ? <Spinner size="sm" className="h-3.5 w-3.5" /> : <Upload className="h-3.5 w-3.5" />}
            {importing ? '导入中…' : '导入项目'}
          </Button>
          <Button size="sm" className="gap-1.5" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" /> 新建项目
          </Button>
        </>
      }
    >
      <div className="mx-auto max-w-[1440px] space-y-5 sm:space-y-7">
        <span role="status" aria-live="polite" className="sr-only">
          {loading ? '读取项目中' : refreshing ? '刷新项目中' : query ? `找到 ${fmtInt(visibleProjects.length)} 个项目` : `共 ${fmtInt(projects.length)} 个项目`}
        </span>
        {projects.length > 0 && (
          <section aria-label="项目统计" className="saker-metric-grid saker-metric-cards grid-cols-3">
            <StatTile size="lg" label="项目总数" value={fmtInt(projects.length)} icon={<FolderKanban aria-hidden="true" className="size-3.5" />} />
            <StatTile size="lg" label="活跃项目" value={fmtInt(activeProjects)} icon={<Activity aria-hidden="true" className="size-3.5" />} />
            <StatTile size="lg" label="正在运行" value={fmtInt(running)} tone={running > 0 ? 'success' : 'default'} icon={<Play aria-hidden="true" className="size-3.5" />} />
          </section>
        )}

        {/* Above the list on purpose: a missing project must be read before the
            operator starts wondering why the count is short. */}
        <SkippedProjectsNotice skipped={skipped} />
        {/* Same reason: empty vuln searches are the other thing the operator
            would misread as data loss. */}
        <VulnLibraryQuarantineNotice quarantine={quarantine} />

        <section aria-labelledby="project-list-heading" aria-busy={loading || refreshing}>
          <div className="mb-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <div className="flex min-h-6 items-baseline gap-2.5">
              <h2 id="project-list-heading" className="text-h3 font-semibold text-ink">项目列表</h2>
              {(loading || refreshing) ? <span className="text-caption text-ink-2">{refreshing ? '刷新中…' : '读取中…'}</span> : query && projects.length > 0 && (
                <span className="saker-nums text-caption text-ink-2">{fmtInt(visibleProjects.length)} / {fmtInt(projects.length)}</span>
              )}
            </div>
            {projects.length > 0 && (
              <div className="relative w-full sm:w-64">
                <Search aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-ink-3" />
                <Input ref={searchInput} type="search" aria-label="搜索项目" placeholder="搜索名称、ID 或授权边界" value={search}
                  onChange={event => setSearch(event.target.value)}
                  className="bg-card pl-9 pr-9 [&::-webkit-search-cancel-button]:hidden"
                />
                {search && (
                  <Button type="button" variant="ghost" size="icon-xs" className="absolute right-1 top-1/2 -translate-y-1/2" aria-label="清空项目搜索" onClick={clearSearch}>
                    <X aria-hidden="true" className="size-3.5" />
                  </Button>
                )}
              </div>
            )}
          </div>
          {loading && projects.length === 0 ? (
            <Panel>
              <SkeletonList count={4} />
            </Panel>
          ) : projects.length === 0 ? (
            <Panel>
              <EmptyState icon={FolderKanban} title="还没有项目" description="可新建项目，也可以导入已有的 .sakerproj 文件。" />
            </Panel>
          ) : visibleProjects.length === 0 ? (
            <Panel>
              <EmptyState icon={SearchX} title="没有匹配的项目" description="可尝试其他项目名称、ID 或授权边界。" />
            </Panel>
          ) : (
            <ul role="list" className="grid grid-cols-1 gap-4 @3xl:grid-cols-2 @7xl:grid-cols-3" aria-label="项目列表">
              {visibleProjects.map((p, index) => {
                const open = p.id === current
                return (
                  <li
                    key={p.id}
                    className={cn(
                      'saker-panel saker-project-card flex min-w-0 flex-col gap-4 rounded-xl p-4 transition-colors sm:p-5',
                      index >= 8 && '[contain-intrinsic-size:auto_13rem] [content-visibility:auto]',
                    )}
                  >
                    <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2.5 gap-y-2 sm:gap-x-3">
                      <span className="saker-project-emblem grid size-9 place-items-center rounded-lg text-primary sm:size-10" aria-hidden="true">
                        <FolderKanban className="size-[18px]" />
                      </span>
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                        <h3 className="saker-break line-clamp-2 text-[16px] font-semibold leading-6 text-ink" title={p.name}>{p.name}</h3>
                        {open && <Badge variant="primary" size="sm" className="shrink-0">当前</Badge>}
                      </div>
                      <StatusIndicator
                        status={p.running > 0 ? 'running' : 'idle'}
                        label={p.running > 0 ? `${fmtInt(p.running)} 在跑` : '无运行'}
                        compact
                        className="justify-self-end rounded-full px-2 py-0.5"
                      />
                      <p className="saker-break col-span-full line-clamp-2 text-caption text-ink-2" title={p.scope}>{p.scope || '未指定授权边界'}</p>
                    </div>

                    <dl className="saker-inset mt-auto grid grid-cols-4 divide-x divide-hairline overflow-hidden rounded-lg">
                      <ProjectMetric label="意图" value={fmtInt(p.intents)} />
                      <ProjectMetric label="事实" value={fmtInt(p.facts)} />
                      <ProjectMetric label="证据" value={fmtInt(p.evidence)} />
                      <ProjectMetric
                        label="并发"
                        value={p.max_runs_effective === 0 ? '不限' : fmtInt(p.max_runs_effective)}
                        title={p.max_runs == null ? `继承实例默认（${p.max_runs_effective}）` : '项目覆盖'}
                      />
                    </dl>

                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="min-w-0 text-[11px] leading-4 text-ink-3">
                        <div className="saker-break font-mono">{p.id}</div>
                        <div className="saker-nums" title="创建时间">{fmtTime(p.created)}</div>
                      </div>
                      <div className="ml-auto flex shrink-0 items-center gap-1">
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          onClick={() => void exportProject(p)}
                          disabled={exporting === p.id}
                          aria-label={`导出项目「${p.name}」`}
                          title={p.running > 0 ? '导出项目（先暂停并等待运行结束）' : '导出项目为 .sakerproj 包'}
                        >
                          {exporting === p.id ? <Spinner size="sm" className="h-3.5 w-3.5" /> : <Download className="h-3.5 w-3.5" />}
                        </Button>
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          onClick={() => openScope(p)}
                          aria-label={`查看项目「${p.name}」的起点、终点与内置约束`}
                          title="查看起点、终点与内置约束（在项目的「项目文档」页改）"
                        >
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          className="text-ink-2 hover:bg-destructive/10 hover:text-destructive"
                          onClick={() => setDeleting(p)}
                          aria-label={`删除项目「${p.name}」`}
                          title="删除项目"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                        {!open && (
                          <Button size="sm" variant="secondary" className="ml-1 gap-1.5" aria-label={`打开项目「${p.name}」`} onClick={() => gotoProject(p.id)}>
                            打开 <ArrowUpRight aria-hidden="true" className="h-3.5 w-3.5" />
                          </Button>
                        )}
                      </div>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </section>

        {transferError && (
          <div role="alert">
            <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
              {transferError}
            </Callout>
          </div>
        )}

        <details className="group rounded-lg border border-hairline bg-surface-1 px-4 py-3 open:space-y-2">
          <summary className="flex cursor-pointer list-none items-center gap-2 text-caption font-medium text-ink-2 [&::-webkit-details-marker]:hidden">
            <ChevronDown aria-hidden="true" className="size-3.5 -rotate-90 text-ink-3 group-open:rotate-0" />
            导入、导出与删除说明
          </summary>
          <p className="text-caption leading-relaxed text-ink-2">
            导出前请暂停项目并等运行结束。导出的 <span className="font-mono">.sakerproj</span> 包含项目清单、账本、状态笔记、运行日志、证据与登录材料。导入会创建一个暂停的项目，允许同名。确认删除后会停止运行并清理项目图谱、工作区和运行日志。已镜像的漏洞条目与全局工具会保留。
          </p>
        </details>
      </div>

      <NewProjectDialog open={creating} onOpenChange={setCreating} onCreated={onChanged} />
      {deleting && <DeleteProjectDialog project={deleting} onClose={() => setDeleting(null)} onDeleted={deleted} />}

      <Dialog open={!!editing} onOpenChange={(o) => { if (!o) setEditing(null) }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>起点、终点与内置约束 · {editing?.name}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            {docsError && (
              <div role="alert">
                <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
                  文档没读到：{docsError}
                </Callout>
              </div>
            )}
            {DOCUMENT_KINDS.map((kind) => (
              <div key={kind}>
                <div className="text-[12px] font-medium text-ink-2">{DOCUMENT_META[kind].title}</div>
                <p className="saker-break mt-1 max-h-40 overflow-y-auto whitespace-pre-wrap rounded-lg border border-hairline bg-surface-2/40 px-3 py-2 text-[12px] leading-relaxed text-ink-2">
                  {docs
                    ? docs.documents?.[kind]?.text?.trim() || (kind === 'goal' ? '（还没有未收口的目标）' : '（空）')
                    : docsError ? '—' : '读取中…'}
                </p>
              </div>
            ))}
            <p className="text-[11px] leading-4 text-ink-3">
              这三份文档只在项目的「项目文档」页改：整篇编辑、每次保存留一版历史，并带修订号，不会被别处覆盖。
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditing(null)}>
              关闭
            </Button>
            <Button
              onClick={() => {
                if (editing) window.location.assign(`/p/${encodeURIComponent(editing.id)}/?view=docs`)
              }}
            >
              去「项目文档」页改
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}
