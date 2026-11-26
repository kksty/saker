import { useMemo, useState } from 'react'
import { BookOpen, Command, FolderSearch, Package, RefreshCw, Search, Terminal, Trophy, Wrench } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Chip,
  EmptyState,
  Input,
  Meter,
  SkeletonList,
  StatTile,
  StatusIndicator,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtBytes, fmtInt, fmtTime } from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import {
  TOOL_ROWS_VISIBLE,
  TOOL_SORTS,
  averageMs,
  failureRate,
  formatMs,
  formatRate,
  sortToolRows,
  type ToolSort,
} from '@/lib/tool-stats'
import { commandKind, filterCommands, type CommandKind } from '@/lib/tools'

const categories = [
  { id: 'native', label: '原生命令', icon: Terminal },
  { id: 'skill', label: '技能', icon: BookOpen },
  { id: 'runtime', label: '会话控制', icon: Command },
] as const

/**
 * Toolbox is a read-only capability directory: three metrics, then the host's
 * registered commands (filterable), then two side-by-side lists (call
 * leaderboard and local tool files). Every block has an explicit empty state —
 * "no host directory" is a state, not a blank panel.
 */
export function Toolbox() {
  const data = usePoll(api.tools, 10000)
  const runtime = useLoad(api.runtimeTools, [])
  const [kind, setKind] = useState<CommandKind>('native')
  const [query, setQuery] = useState('')
  // The order is the operator's: the API sorts by calls, but "which tool keeps
  // failing" and "which tool is slow" are different questions asked on the same
  // page. Expanding the list is a separate choice from its order.
  const [sort, setSort] = useState<ToolSort>('calls')
  const [expanded, setExpanded] = useState(false)
  const rows = data.data?.leaderboard ?? []
  const arsenal = data.data?.arsenal ?? []
  const commands = runtime.data?.commands ?? []
  const filtered = filterCommands(commands, kind, query)
  const sorted = useMemo(() => sortToolRows(rows, sort), [rows, sort])
  const visible = expanded ? sorted : sorted.slice(0, TOOL_ROWS_VISIBLE)
  const total = rows.reduce((sum, row) => sum + row.calls, 0)
  const roots = data.data?.tool_roots ?? []
  const max = rows.reduce((value, row) => Math.max(value, row.calls), 1)
  const connected = runtime.data?.state === 'connected'
  const refreshing = runtime.refreshing || data.refreshing
  const refresh = () => { data.reload(); runtime.reload() }

  const metrics = [
    { label: '宿主命令与技能', value: connected ? fmtInt(commands.length) : '—', hint: connected ? '来自运行中的默认宿主' : '等待宿主目录', icon: Wrench },
    { label: '项目工具调用', value: data.data ? fmtInt(total) : '—', hint: `${fmtInt(data.data?.runs_scanned ?? 0)} 条运行日志`, icon: Trophy },
    { label: '本机工具文件', value: data.data ? fmtInt(arsenal.length) : '—', hint: '实例与用户 arsenal 目录', icon: Package },
  ]

  return (
    <Page
      title="工具与能力"
      subtitle="查看宿主实际注册的能力、调用活跃度与本机工具文件"
      actions={
        <Button size="sm" variant="outline" disabled={refreshing} aria-label="刷新工具与能力" onClick={refresh}>
          <RefreshCw className={refreshing ? 'h-3.5 w-3.5 motion-safe:animate-spin' : 'h-3.5 w-3.5'} />
          {refreshing ? '刷新中…' : '刷新'}
        </Button>
      }
    >
      <div className="space-y-4">
        {data.error && (
          <Callout tone="destructive">
            {data.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={refresh}>
              重试
            </Button>
          </Callout>
        )}
        {runtime.error && (
          <Callout tone="destructive">
            读取宿主目录失败：{runtime.error}
            <Button size="xs" variant="outline" className="ml-2" onClick={refresh}>
              重试
            </Button>
          </Callout>
        )}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          {metrics.map(({ label, value, hint, icon: Icon }) => (
            <div key={label} className="saker-panel rounded-xl p-4">
              <StatTile
                label={label}
                value={value}
                hint={hint}
                size="lg"
                icon={<Icon className="h-3.5 w-3.5" />}
              />
            </div>
          ))}
        </div>

        <Panel
          title="宿主能力目录"
          extra={
            <StatusIndicator
              compact
              status={connected ? 'success' : 'idle'}
              label={runtime.loading ? '读取中' : connected ? '已连接' : '未连接'}
            />
          }
        >
          <div className="mb-3 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 flex-1 text-[12px] leading-relaxed text-ink-2">
              <p>目录反映当前默认宿主的注册结果。模板、指纹、浏览器依赖与外部服务是否可用，仍以实际执行结果为准。</p>
              {connected && (
                <p className="saker-break mt-1 font-mono text-[11px] leading-4 text-ink-3">
                  {runtime.data?.agent_id} · {runtime.data?.provider || '未报告模型提供方'} / {runtime.data?.model || '未报告模型'} · {fmtTime(runtime.data?.checked_at)}
                </p>
              )}
            </div>
            <div className="relative w-full sm:w-64">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
              <Input
                className="h-8 pl-7 text-[12px]"
                aria-label="搜索宿主命令与技能"
                placeholder="搜索名称、说明或用法"
                value={query}
                onChange={event => setQuery(event.target.value)}
                type="search"
                inputMode="search"
                enterKeyHint="search"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
          </div>

          {runtime.data?.error && <div className="mb-3"><Callout tone="warning">{runtime.data.error}</Callout></div>}
          {runtime.data?.model_error && <div className="mb-3"><Callout tone="warning">模型配置异常：{runtime.data.model_error}</Callout></div>}

          <div className="mb-3 flex flex-wrap gap-1.5" role="group" aria-label="能力类别">
            {categories.map(({ id, label, icon: Icon }) => (
              <Chip key={id} active={kind === id} onClick={() => setKind(id)}>
                <Icon className="h-3.5 w-3.5" />
                {label}
                <span className="saker-nums font-mono text-[11px] opacity-70">
                  {commands.filter(command => commandKind(command) === id).length}
                </span>
              </Chip>
            ))}
          </div>

          {runtime.loading ? (
            <SkeletonList count={3} />
          ) : filtered.length > 0 ? (
            <div className="grid gap-2 md:grid-cols-2 2xl:grid-cols-3">
              {filtered.map(command => (
                <article key={command.name} className="saker-inset min-w-0 rounded-lg p-3 transition-colors hover:border-ink-3/30">
                  <h3 className="saker-break font-mono text-[13px] font-semibold leading-5 text-ink">{command.name}</h3>
                  {command.description && (
                    <p className="saker-break mt-1.5 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{command.description}</p>
                  )}
                  {command.usage && (
                    <p className="saker-break mt-2 rounded-md bg-surface-2/70 px-2.5 py-2 font-mono text-[11px] leading-relaxed text-ink-3">{command.usage}</p>
                  )}
                </article>
              ))}
            </div>
          ) : (
            <EmptyState
              icon={Wrench}
              title={query ? '没有匹配的能力' : connected ? '宿主未报告此类能力' : '暂时没有宿主目录'}
              description={
                query
                  ? '尝试其他名称，或切换能力类别。'
                  : connected
                    ? '这里仅展示宿主已注册的项目，不从历史调用推断。'
                    : '宿主连接恢复后刷新，项目调用记录仍可在下方查看。'
              }
              action={query ? <Button size="sm" variant="outline" onClick={() => setQuery('')}>清除搜索</Button> : undefined}
            />
          )}
        </Panel>

        <div className="grid items-start gap-4 xl:grid-cols-3">
          <Panel title={`项目调用排行 · ${rows.length} 种`} className="xl:col-span-2">
            {data.loading ? (
              <SkeletonList count={4} />
            ) : rows.length === 0 ? (
              <EmptyState
                icon={Trophy}
                title={(data.data?.runs_scanned ?? 0) === 0 ? '还没有运行日志' : '这些日志里没有工具调用'}
                description={
                  (data.data?.runs_scanned ?? 0) === 0
                    ? '运行产生工具事件后，这里会显示项目调用记录。'
                    : `已扫描 ${fmtInt(data.data?.runs_scanned ?? 0)} 份运行日志，其中没有工具调用事件。`
                }
              />
            ) : (
              <div>
                {/* The API's own order is calls; the operator may ask a different
                    question of the same rows. Rate and duration come from the
                    totals the server already sends. */}
                <div className="mb-3 flex flex-wrap items-center gap-1.5" role="group" aria-label="排行排序">
                  <span className="text-[11px] text-ink-3">排序</span>
                  {TOOL_SORTS.map(option => (
                    <Chip key={option.id} active={sort === option.id} onClick={() => setSort(option.id)}>
                      {option.label}
                    </Chip>
                  ))}
                </div>
                <div className="space-y-3">
                  {visible.map((row, index) => {
                    const rate = failureRate(row)
                    const average = averageMs(row)
                    return (
                      <div key={`${row.local ? 'local' : 'native'}:${row.tool}`} className="flex items-start gap-3">
                        <span className="saker-nums mt-0.5 w-5 shrink-0 text-right font-mono text-[11px] text-ink-3">
                          {String(index + 1).padStart(2, '0')}
                        </span>
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                            <span className="saker-break font-mono text-[12px] text-ink">
                              {row.tool}
                              {row.local && <Badge size="sm" variant="muted" className="ml-2">本机</Badge>}
                            </span>
                            <span className="saker-nums shrink-0 font-mono text-[11px] text-ink-3">
                              {fmtInt(row.calls)} 次 · {fmtInt(row.runs)} 份日志
                            </span>
                          </div>
                          <Meter
                            className="my-1.5"
                            value={Math.max(2, (row.calls / max) * 100)}
                            aria-label={`${row.tool} 调用占比`}
                          />
                          <p className="saker-nums flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] leading-4 text-ink-3">
                            <span title="失败数 / 有结果的调用数">
                              失败 {formatRate(rate)}
                              {rate !== null && <span className="opacity-70">（{fmtInt(row.failures ?? 0)}/{fmtInt(row.completed ?? 0)}）</span>}
                            </span>
                            <span title="已完成调用的平均耗时；没有耗时记录时显示 —">均耗时 {formatMs(average)}</span>
                            <span>最近 {fmtTime(row.last_at)}</span>
                          </p>
                          {row.error_kinds?.length ? (
                            // Top three labels only: enough to see "it keeps dying
                            // on --tlsv1.2", not a log viewer. Native details keeps
                            // it keyboard- and screen-reader-usable with no state.
                            <details className="mt-1 text-[11px] leading-4 text-ink-3">
                              <summary className="cursor-pointer select-none">
                                失败原因 · {row.error_kinds.length} 类
                              </summary>
                              <ul className="mt-1 space-y-0.5 pl-3">
                                {row.error_kinds.map(kind => (
                                  <li key={kind.label} className="saker-break flex items-baseline gap-2">
                                    <span className="saker-nums shrink-0 font-mono text-ink-2">×{kind.count}</span>
                                    <span className="min-w-0 font-mono">{kind.label}</span>
                                  </li>
                                ))}
                              </ul>
                            </details>
                          ) : null}
                        </div>
                      </div>
                    )
                  })}
                </div>
                {sorted.length > TOOL_ROWS_VISIBLE && (
                  // The list is capped so a long arsenal does not push the page
                  // down; the total stays in the title and on this button.
                  <div className="mt-3 border-t border-hairline pt-2.5">
                    <Button size="xs" variant="ghost" onClick={() => setExpanded(value => !value)}>
                      {expanded ? `收起（只显示前 ${TOOL_ROWS_VISIBLE}）` : `展开全部 ${sorted.length} 种`}
                    </Button>
                  </div>
                )}
              </div>
            )}
          </Panel>

          <Panel title="本机工具文件" extra={<Badge size="sm" variant="muted">{arsenal.length}</Badge>}>
            <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
              工具由 harness 的 arsenal 管理，跨项目共享，删除项目时保留。文件与版本来自安装目录及清单；文件存在不代表可执行或已进入当前宿主环境。
            </p>
            {data.loading ? (
              <SkeletonList count={3} />
            ) : arsenal.length === 0 ? (
              <EmptyState icon={Package} title="没有检测到工具文件" description="安装到实例或用户 arsenal 目录后刷新查看。" />
            ) : (
              <div className="divide-y divide-hairline">
                {arsenal.map(tool => (
                  <div key={tool.name} className="py-2.5 first:pt-0 last:pb-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="saker-break font-mono text-[12px] font-medium text-ink">{tool.name}</span>
                      {tool.version && <Badge size="sm" variant="secondary">{tool.version}</Badge>}
                      {tool.source && <Badge size="sm" variant="muted">{tool.source}</Badge>}
                      <span className="saker-nums ml-auto font-mono text-[11px] text-ink-3">{fmtBytes(tool.size)}</span>
                    </div>
                    {tool.dirs?.map(dir => (
                      <p key={dir} className="saker-break mt-1 font-mono text-[11px] leading-4 text-ink-3">{dir}</p>
                    ))}
                  </div>
                ))}
              </div>
            )}

            {/* Where the instance looked, and whether PATH carries it. Without
                this, "no tool files" is indistinguishable from "wrong directory
                or PATH not set" — the two failures have different fixes. */}
            {roots.length > 0 && (
              <div className="mt-3 border-t border-hairline pt-2.5">
                <h3 className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-ink-3">
                  <FolderSearch className="h-3.5 w-3.5" />
                  工具根目录
                </h3>
                <div className="space-y-2">
                  {roots.map(root => (
                    <div key={`${root.source}:${root.path}`} className="min-w-0 text-[11px] leading-4">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="saker-break font-mono text-ink-2">{root.bin}</span>
                        <Badge size="sm" variant="muted">{root.source}</Badge>
                        <Badge size="sm" variant="secondary">{root.exists ? '已装工具' : '未装工具'}</Badge>
                        {root.path_injected && <Badge size="sm" variant="muted">在 PATH</Badge>}
                      </div>
                      {root.note && <p className="saker-break mt-0.5 text-ink-3">{root.note}</p>}
                    </div>
                  ))}
                </div>
                {data.data?.tool_roots_note && (
                  <p className="saker-break mt-1.5 text-[11px] leading-4 text-ink-3">{data.data.tool_roots_note}</p>
                )}
              </div>
            )}
          </Panel>
        </div>
      </div>
    </Page>
  )
}
