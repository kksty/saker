import { Suspense, lazy, useState } from 'react'
import { FileSearch, Network, RefreshCw, SquareTerminal } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  SkeletonList,
  StatusIndicator,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { hostPresentation } from '@/lib/nodes'

// Opening this page must not download xterm: the terminal pane is the only
// consumer of that ~330 KB chunk, and most visits never open one.
const NodeTerminal = lazy(() =>
  import('@/components/NodeTerminal').then((m) => ({ default: m.NodeTerminal })),
)

/** Native Hub scan discovery, project placement and per-node terminals. */
export function Nodes() {
  const list = usePoll(api.nodes, 4000)
  const nodes = list.data?.nodes ?? []
  const [terminal, setTerminal] = useState<string | null>(null)
  const [saving,setSaving]=useState(false)
  const [error,setError]=useState('')
  const [join,setJoin]=useState<{args:string[];note:string;workspace:string}|null>(null)
  async function bind(id:string, enable:boolean, makeDefault=false){
    setSaving(true);setError('')
    const selected=nodes.filter(n=>n.bound).map(n=>n.id)
    const next=enable?Array.from(new Set([...selected,id])):selected.filter(n=>n!==id)
    const current=list.data?.project_default??''
    const def=makeDefault?id:next.includes(current)?current:next[0]??''
    try{await api.setNodes(next,def);list.reload()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setSaving(false)}
  }
  async function setup(){try{setJoin(await api.nodeJoin())}catch(e){setError(e instanceof Error?e.message:String(e))}}


  return (
    <Page
      title="节点"
      subtitle="原生 Hub + 多个 scan 节点；启用项目节点并选择默认执行位置"
      scroll={false}
      className="@container"
      actions={
        <Button variant="outline" size="sm" onClick={list.reload} disabled={list.loading || list.refreshing}>
          <RefreshCw className={cn('h-3.5 w-3.5', list.refreshing && 'motion-safe:animate-spin')} /> 刷新状态
        </Button>
      }
    >
      <div className="flex h-full min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain @4xl:flex-row @4xl:overflow-hidden">
        <div className="min-w-0 flex-none space-y-4 py-1 pr-1 @4xl:flex-1 @4xl:overflow-y-auto">
          {error&&<Callout tone="destructive">{error}</Callout>}
          {list.data?.discovery_error&&<Callout tone="warning">节点发现暂不可用，已保留上次列表：{list.data.discovery_error}</Callout>}
          <Panel title="接入 scan 节点">
            <p className="mb-2 text-xs text-ink-3">连接同一个 Hub 后自动发现。执行者通过共享项目目录交接文件；跨机器时共享路径和平台 API 都需要可达。</p>
            <Button size="sm" variant="outline" onClick={()=>void setup()}>生成原生接入命令</Button>
            {join&&<div className="mt-3 space-y-2 text-xs"><p>{join.note}</p><p className="font-mono saker-break">项目目录：{join.workspace}</p><pre className="saker-break whitespace-pre-wrap rounded bg-surface-2 p-2">cyber-scan {join.args.map(a=>`'${a.replace(/'/g,"'\"'\"'")}'`).join(' ')}</pre><Button size="xs" variant="ghost" onClick={()=>setJoin(null)}>隐藏接入凭据</Button></div>}
          </Panel>
          {list.error && (
            <Callout tone="destructive">
              {list.error}
              <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
                重试
              </Button>
            </Callout>
          )}

          {list.loading && (
            <Panel bodyClassName="p-4">
              <SkeletonList count={2} />
            </Panel>
          )}

          {!list.loading && nodes.length === 0 && (
            <Panel bodyClassName="p-0">
              <EmptyState
                icon={Network}
                title="没有执行节点"
                description="请检查 Hub 与 scan 连接，恢复后刷新节点列表。"
              />
            </Panel>
          )}

          {nodes.length > 0 && (
            <Panel
              title="执行节点"
              extra={
                <span className="saker-nums font-mono text-[11px] text-ink-3">{nodes.length} 个</span>
              }
              bodyClassName="p-0"
            >
              <ul role="list" className="divide-y divide-hairline" aria-label="执行节点">
                {nodes.map((n, index) => {
                  const aop = n.transport === 'aop'
                  const where = aop ? n.endpoint : n.bin
                  const runtime = hostPresentation(n.host ?? (n.online!==undefined ? {online:n.online,ready:!!n.ready,last_error:n.config_error,pid:0,restarts:0,endpoint:n.endpoint??'',failed_ready:0,unhealthy:false} : undefined))
                  const active = terminal === n.id
                  return (
                    <li
                      key={n.id}
                      className={cn(
                        'px-2.5 py-2.5 transition-colors',
                        index >= 6 && '[contain-intrinsic-size:auto_6rem] [content-visibility:auto]',
                        active
                          ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                          : 'hover:bg-surface-2',
                      )}
                    >
                      <div className="flex items-start gap-2.5">
                        {aop ? (
                          <Network className="mt-1 h-4 w-4 shrink-0 text-ink-3" />
                        ) : (
                          <FileSearch className="mt-1 h-4 w-4 shrink-0 text-ink-3" />
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            {n.terminal ? (
                              <button
                                type="button"
                                onClick={() => setTerminal(n.id)}
                                disabled={aop && !runtime.terminal}
                                aria-expanded={active}
                                title="在下方面板打开终端"
                                className="saker-break rounded text-left font-mono text-[13px] font-medium leading-5 text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-40"
                              >
                                {n.id}
                              </button>
                            ) : (
                              <span className="saker-break font-mono text-[13px] font-medium leading-5 text-ink">{n.id}</span>
                            )}
                            {n.name && <span className="text-xs text-ink-2">{n.name}</span>}
                            {n.agent && <Badge size="sm" variant="info">scan</Badge>}
                            {n.default && <Badge size="sm" variant="secondary">实例默认</Badge>}
                            {n.project_default && <Badge size="sm" variant="primary">项目默认</Badge>}
                            <StatusIndicator
                              compact
                              status={aop ? runtime.status : 'idle'}
                              label={aop ? runtime.label : '按需启动审计进程'}
                            />
                          </div>
                          <div className="mt-2 flex flex-wrap gap-2">
                            <Button size="xs" variant={n.bound?'soft':'outline'} disabled={saving} onClick={()=>void bind(n.id,!n.bound)}>{n.bound?'移出项目':'启用到项目'}</Button>
                            {n.bound&&!n.project_default&&<Button size="xs" variant="ghost" disabled={saving} onClick={()=>void bind(n.id,true,true)}>设为默认</Button>}
                            {n.model&&<span className="text-xs text-ink-3">{n.model}</span>}
                          </div>
                          {n.working_dir&&<p className="mt-1 saker-break font-mono text-xs text-ink-3">{n.working_dir}</p>}
                          {n.note && (
                            <div className="saker-break mt-1 text-[12px] leading-relaxed text-ink-2">{n.note}</div>
                          )}
                          {where && (
                            <div className="saker-break mt-1 font-mono text-[11px] leading-4 text-ink-3">{where}</div>
                          )}
                          {n.host && (
                            <div className="mt-0.5 flex flex-wrap gap-x-3 text-[11px] leading-4 text-ink-3">
                              {n.host.pid > 0 && (
                                <span className="saker-nums font-mono">PID {n.host.pid}</span>
                              )}
                              <span>重启 <span className="saker-nums font-mono">{n.host.restarts}</span> 次</span>
                            </div>
                          )}
                          {n.host && !n.host.ready && (
                            <Callout tone="destructive" className="mt-2">
                              <div className="saker-break whitespace-pre-wrap text-[12px]">{n.host.last_error || '宿主正在启动，等待模型就绪。'}</div>
                              <div className="mt-1 text-[12px]">
                                默认模型暂不能派发。检查模型账户、连接与配置后，<a href="/g/?view=settings" className="underline underline-offset-2">到设置 → 模型保存重试</a>；无需绑定节点。
                              </div>
                            </Callout>
                          )}
                        </div>
                        {n.terminal && (
                          <Button
                            variant={active ? 'soft' : 'outline'}
                            size="xs"
                            className="shrink-0"
                            onClick={() => setTerminal(n.id)}
                            disabled={aop && !runtime.terminal}
                            active={active}
                            aria-label={`打开 ${n.id} 的终端`}
                          >
                            <SquareTerminal className="h-3 w-3" />
                            终端
                          </Button>
                        )}
                      </div>
                    </li>
                  )
                })}
              </ul>
              <p role="status" aria-live="polite" className="sr-only">
                执行节点共 {nodes.length} 个
              </p>
            </Panel>
          )}

          <Callout tone="info">
            <div className="space-y-1.5 text-xs">
              <p>控制台管理本机 Hub 与配对 scan；连接同一个 Hub 的其他 scan 自动出现在列表中，按项目启用并设置默认节点。</p>
              <p>节点间通过共享项目目录交接文件，绝对路径必须一致。远端节点还需能访问平台 API；断线时会话保留在原节点，不自动改派。</p>
              <p>终端连接所选 scan；关闭面板不会停止节点。其他 scan 的进程由其启动方管理。</p>
            </div>
          </Callout>
        </div>

        {terminal && (
          <div className="flex h-[420px] min-w-0 flex-none flex-col gap-2 pb-1 @4xl:h-auto @4xl:flex-[1.15]">
            <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl">
              <Suspense
                fallback={
                  <div className="saker-panel flex h-full min-h-[200px] flex-col gap-3 rounded-xl p-4">
                    <span className="text-[12px] text-ink-2">终端内核加载中…</span>
                    <SkeletonList count={4} />
                  </div>
                }
              >
                <NodeTerminal node={terminal} onClose={() => setTerminal(null)} />
              </Suspense>
            </div>
            <p className="saker-break px-1 font-mono text-[11px] leading-4 text-ink-3">
              终端运行在所选 scan 节点；关闭面板不会停止节点。
            </p>
          </div>
        )}
      </div>
    </Page>
  )
}
