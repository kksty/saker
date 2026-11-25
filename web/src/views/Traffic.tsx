import { useEffect, useState } from 'react'
import { Download, Network, RefreshCw, Search } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, SkeletonList } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtTime } from '@/lib/format'
import { useDebounced, useLoad, usePoll } from '@/lib/hooks'
import { bodyPreview, capturePreviewBadge, capturePreviewFlags, capturePreviewNote } from '@/lib/traffic'
import type { HTTPFlow } from '@/lib/types'

/**
 * The captured-traffic reader: a scannable request list on the left, the
 * selected exchange on the right. The list row shows method / status / time /
 * URL; the detail separates request and response into their own sections with
 * mono packet text and an explicit body preview, so a 64 KiB body cannot push
 * the layout sideways.
 */
export function Traffic() {
  const [q, setQ] = useState('')
  const query = useDebounced(q, 200)
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState('')
  const list = usePoll(async () => ({...await api.httpFlows(query, offset), query, offset}), 5000, [query, offset])
  useEffect(() => { setOffset(0); setSelected('') }, [query])
  const page = list.data?.query === query && list.data.offset === offset ? list.data : undefined
  const rows = page?.flows ?? []
  const first = rows[0]?.ref ?? ''
  useEffect(() => { if (!selected && first) setSelected(first) }, [selected, first])
  const ref = selected || first
  const detail = useLoad(() => ref ? api.httpFlow(ref) : Promise.resolve<HTTPFlow | null>(null), [ref])
  // While a different detail is loading, never label the previous packet with
  // the new selection's identity.
  const current = detail.data?.record.ref === ref ? detail.data : null
  const currentFlags = current ? capturePreviewFlags(current.record) : null
  const pageIndex = Math.floor(offset / 100) + 1
  return (
    <Page
      title="HTTP 流量"
      subtitle="本项目执行轮观察到的请求与响应 · 自动刷新，不触发模型调用"
      scroll={false}
      className="@container"
      actions={
        <Button size="sm" variant="outline" onClick={() => { list.reload(); detail.reload() }} disabled={list.loading || detail.loading}>
          <RefreshCw className={cn('h-3.5 w-3.5', (list.refreshing || detail.refreshing) && 'motion-safe:animate-spin')} /> 刷新
        </Button>
      }
    >
      <div className="flex h-full min-h-0 flex-col">
        <div className="shrink-0 space-y-2 border-b border-hairline p-3 sm:px-5">
          <p className="text-[12px] leading-relaxed text-ink-3">
            记录经过 harness 出口且已开启捕获的 HTTP／HTTPS。HTTPS 明文需要 MITM 与证书信任；绕过代理的程序、隧道内部和其他 TCP 流量不一定可见。正文默认只保留最多 4 KiB 的预览（标记「仅预览」）；宿主配置 traffic.body_storage=disk 后才会保存完整正文（单条上限 8 MiB、磁盘预算 2 GiB），超过限制的正文标记「已截断」。
          </p>
          <div className="relative max-w-xl">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
            <Input
              aria-label="筛选 HTTP 流量"
              type="search"
              value={q}
              onChange={e => setQ(e.target.value)}
              placeholder="筛选 URL、方法、状态码或意图 ID"
              className="h-8 pl-8 text-[12px]"
              inputMode="search"
              enterKeyHint="search"
              autoComplete="off"
              spellCheck={false}
            />
          </div>
        </div>

        <div className="flex min-h-0 flex-1 flex-col @4xl:flex-row">
          <aside className="flex max-h-[45%] min-h-0 flex-col border-b border-hairline @4xl:max-h-none @4xl:w-[44%] @4xl:border-b-0 @4xl:border-r">
            <div className="min-h-0 flex-1 overflow-auto overscroll-contain [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin]" aria-busy={list.loading || (!page && list.refreshing)}>
              {(list.loading || (!page && list.refreshing)) && (
                <div className="p-3">
                  {/* aria-busy on the list region carries the busy state; a live
                      region here would announce an interstitial "loading…". */}
                  <SkeletonList count={6} />
                </div>
              )}
              {list.error && (
                <div className="p-3">
                  <Callout tone="destructive">
                    {list.error}
                    <Button size="xs" variant="outline" className="ml-2" onClick={list.reload}>
                      重试
                    </Button>
                  </Callout>
                </div>
              )}
              {rows.length > 0 && (
                <ul role="list" className="divide-y divide-hairline" aria-label="流量记录">
                  {rows.map((row, index) => {
                    const active = row.ref === ref
                    const bytes = row.response_bytes.toLocaleString()
                    const flags = capturePreviewFlags(row)
                    // Guide `defer-rendering-heavy-content`: only rows that are
                    // certainly below the first 100vh-ish window are deferred;
                    // the first rows stay uncontained so the initial paint is
                    // not slowed down by the containment engine itself.
                    return (
                      <li
                        key={row.ref}
                        className={index < 8 ? undefined : '[contain-intrinsic-size:auto_5.5rem] [content-visibility:auto]'}
                      >
                      <button
                        type="button"
                        onClick={() => setSelected(row.ref)}
                        aria-current={active ? 'true' : undefined}
                        className={cn(
                          'block w-full px-2.5 py-2.5 text-left transition-colors',
                          active
                            ? 'bg-primary/[0.09] shadow-[inset_2px_0_0_var(--primary)]'
                            : 'hover:bg-surface-2',
                        )}
                      >
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono text-[11px] font-semibold leading-4 text-ink">{row.method}</span>
                          <Badge variant={row.status >= 500 || row.error ? 'warning' : 'muted'} size="sm">
                            <span className="tw-nums font-mono">{row.status || '无响应'}</span>
                          </Badge>
                          {/* Body completeness is a content fact, not a status:
                              a small dot keeps the row scannable and the title
                              spells out which bytes are missing. */}
                          {flags.partial && (
                            <span
                              className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-warning"
                              aria-hidden="true"
                              title={capturePreviewNote(flags)}
                            />
                          )}
                          {flags.partial && (
                            <span className="text-[10px] font-medium text-warning" title={capturePreviewNote(flags)}>
                              {capturePreviewBadge(flags)}
                            </span>
                          )}
                          <time className="tw-nums ml-auto whitespace-nowrap font-mono text-[11px] leading-4 text-ink-3">{fmtTime(row.at)}</time>
                        </div>
                        <p className="tw-break mt-1 font-mono text-[12px] leading-5 text-ink">{row.url}</p>
                        <p className="tw-break mt-0.5 font-mono text-[11px] leading-4 text-ink-3">
                          {row.run_id} · 响应 <span className="tw-nums">{bytes}</span> B{!row.complete ? ' · 记录不完整' : ''}
                        </p>
                      </button>
                      </li>
                    )
                  })}
                </ul>
              )}
              {!list.loading && !list.error && rows.length === 0 && (
                <EmptyState
                  icon={Network}
                  title="暂无已捕获流量"
                  description="执行者产生可观测 HTTP 请求后，会出现在这里。已有旧运行不会自动补采。"
                />
              )}
            </div>
            <div className="flex items-center gap-2 border-t border-hairline p-2.5 text-[11px] text-ink-3" role="group" aria-label="流量分页">
              <span role="status" aria-live="polite" className="tw-nums font-mono">共 {page?.total ?? 0} 条 · 第 {pageIndex} 页</span>
              <Button size="xs" variant="ghost" className="ml-auto" disabled={offset === 0} onClick={() => { setSelected(''); setOffset(Math.max(0, offset - 100)) }}>
                上一页
              </Button>
              <Button size="xs" variant="ghost" disabled={offset + 100 >= (page?.total ?? 0)} onClick={() => { setSelected(''); setOffset(offset + 100) }}>
                下一页
              </Button>
            </div>
          </aside>

          <section className="min-h-0 min-w-0 flex-1 overflow-auto overscroll-contain p-4 [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin] sm:p-5" aria-busy={detail.loading}>
            {detail.loading && <SkeletonList count={4} />}
            {detail.error && (
              <Callout tone="destructive">
                {detail.error}
                <Button size="xs" variant="outline" className="ml-2" onClick={detail.reload}>
                  重试
                </Button>
              </Callout>
            )}
            {!detail.loading && !current && !detail.error && rows.length > 0 && (
              <EmptyState compact icon={Network} title="选择一条流量" description="左侧选择一条请求，查看请求头、响应头与正文预览。" />
            )}
            {current && (
              <div className="space-y-4">
                {page && selected && !rows.some(row => row.ref === selected) && (
                  <Callout>当前选中记录已移出本页列表，详情保持不变。</Callout>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="tw-section-title">请求与响应</h2>
                  {currentFlags?.partial && (
                    <Badge size="sm" variant="warning">{capturePreviewBadge(currentFlags)}</Badge>
                  )}
                  <a
                    href={api.httpFlowURL(current.record.ref)}
                    className="ml-auto inline-flex items-center gap-1 rounded text-[12px] text-ink-2 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  >
                    <Download className="h-3.5 w-3.5" /> 下载原始记录
                  </a>
                </div>
                <dl className="tw-inset grid grid-cols-1 gap-x-4 gap-y-1.5 rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-3 sm:grid-cols-2">
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>运行</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.run_id}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>意图</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.intent_id || '—'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>会话</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.session_id || '未提供'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>流量</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.flow_id}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>操作</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.operation?.operation_id || '未提供'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>调用</dt><dd className="tw-break min-w-0 text-ink-2">{current.record.operation?.call_id || '未提供'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>字节</dt><dd className="tw-break min-w-0 text-ink-2">请求 {typeof current.record.request_bytes === 'number' ? `${current.record.request_bytes.toLocaleString()} B` : '未记录'} · 响应 {typeof current.record.response_bytes === 'number' ? `${current.record.response_bytes.toLocaleString()} B` : '未记录'}</dd></div>
                  <div className="flex min-w-0 items-baseline gap-1.5"><dt>正文</dt><dd className={`tw-break min-w-0 ${currentFlags?.partial ? 'text-warning' : 'text-ink-2'}`}>{currentFlags?.partial ? capturePreviewBadge(currentFlags) : '已保存完整正文'}</dd></div>
                </dl>
                {current.record.error && <Callout tone="warning">{current.record.error}</Callout>}
                {!current.record.complete && <Callout tone="warning">原生记录标记为不完整，正文可能缺失。</Callout>}
                {currentFlags?.partial && <Callout tone="warning">{capturePreviewNote(currentFlags)}</Callout>}
                <Exchange
                  title="请求"
                  start={`${current.flow.request?.method ?? ''} ${current.flow.request?.url ?? ''} ${current.flow.request?.protocol ?? ''}`}
                  headers={current.flow.request?.headers}
                  body={current.flow.request?.body}
                />
                {current.flow.response ? (
                  <Exchange
                    title="响应"
                    start={`${current.flow.response.status_code ?? ''} ${current.flow.response.reason_phrase ?? ''}`}
                    headers={current.flow.response.headers}
                    body={current.flow.response.body}
                  />
                ) : (
                  <Callout>未收到响应；不推断为成功或漏洞。</Callout>
                )}
              </div>
            )}
          </section>
        </div>
      </div>
    </Page>
  )
}

function Exchange({title, start, headers = [], body = ''}: {title: string; start: string; headers?: {name: string; value: string}[]; body?: string}) {
  const preview = bodyPreview(body)
  return (
    <Panel title={title} bodyClassName="space-y-2">
      {/* 正文块自己滚（max-h + overflow-auto），但不拦截滚动链：指针停在报文上时，
          块内到底后滚轮必须继续带动外层详情面板，否则整页看起来「滑不动」。 */}
      <pre className="tw-inset tw-break overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-2 [scrollbar-width:thin]">
        {start}{'\n'}{headers.map(h => `${h.name}: ${h.value}`).join('\n')}
      </pre>
      <pre className="tw-inset tw-break max-h-[420px] overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-2 [scrollbar-width:thin]">
        {preview.text || '（空正文）'}
      </pre>
      <p className="tw-nums text-[11px] leading-4 text-ink-3">
        {preview.size.toLocaleString()} B
        {preview.binary ? ' · 二进制，十六进制预览' : ''}
        {preview.truncated ? ' · 界面只解码前 64 KiB；这不是服务端的截断标记，下载原始记录查看全部已保存字节' : ''}
      </p>
    </Panel>
  )
}
