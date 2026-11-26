import { useMemo, useState } from 'react'
import { Command, History, RefreshCw, Search } from 'lucide-react'
import { Badge, Button, Callout, Input, Spinner } from '@cyber/ui'
import { api } from '@/lib/api'
import {
  appendRunHistory,
  commandDetailText,
  commandMatches,
  historyEventSummary,
  nativeSessionErrorText,
  nextRunHistoryCursor,
} from '@/lib/run-native'
import type { NativeSessionEvent, RunCommandsResponse, RunHistoryPage } from '@/lib/types'

/**
 * The native command menu of the run's harness session. Loading is explicit
 * (one host dial), never a poll: the menu changes rarely and a read on every
 * render would journal a failed read on hosts without the commands namespace.
 */
export function RunCommandsPanel({ runID, sessionID }: { runID: string; sessionID?: string }) {
  const [data, setData] = useState<RunCommandsResponse>()
  const [error, setError] = useState<unknown>()
  const [loading, setLoading] = useState(false)
  const [query, setQuery] = useState('')

  async function load() {
    if (loading) return
    setLoading(true)
    setError(undefined)
    try {
      setData(await api.runCommands(runID))
    } catch (e) {
      setError(e)
    } finally {
      setLoading(false)
    }
  }

  const commands = useMemo(
    () => (data?.commands ?? []).filter((command) => commandMatches(command, query)),
    [data, query],
  )
  // The catalogue itself, not the filtered rows: a response without it is a
  // shape change, and "0 条" would be a claim the host never made.
  const catalog = Array.isArray(data?.commands) ? data!.commands : undefined
  const failure = error ? nativeSessionErrorText(error) : null

  return (
    <section aria-label="原生命令" data-testid="run-native-commands" className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Command className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" />
        <span className="text-[12px] font-medium text-ink">原生命令</span>
        {sessionID && <span className="font-mono text-[11px] text-ink-3">会话 {sessionID.slice(0, 12)}…</span>}
        <Button size="xs" variant="outline" className="ml-auto" onClick={() => void load()} disabled={loading} aria-label="加载原生命令">
          {loading ? <Spinner size="sm" className="h-3 w-3" /> : <RefreshCw className={`h-3 w-3 ${loading ? 'motion-safe:animate-spin' : ''}`} />}
          {data ? '重新加载' : '加载命令'}
        </Button>
      </div>
      {!data && !error && !loading && (
        <p className="text-[11px] leading-relaxed text-ink-3">
          按需读取宿主会话的命令目录（ListCommands）。这里只列命令，不执行；执行仍走会话自己的 shell/REPL。
        </p>
      )}
      {failure && (
        <Callout tone={failure.tone}>
          <span className="saker-break">{failure.text}</span>
          <Button size="xs" variant="outline" className="ml-2" onClick={() => void load()}>重试</Button>
        </Callout>
      )}
      {data && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative min-w-0 flex-[1_1_180px]">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" aria-hidden="true" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                aria-label="筛选原生命令"
                placeholder="筛选命令名、别名或说明"
                className="h-8 pl-7 text-[12px]"
                type="search"
                inputMode="search"
                enterKeyHint="search"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <span className="saker-nums font-mono text-[11px] text-ink-3">
              {catalog ? `${commands.length}/${catalog.length} 条` : '命令数未读全'} · {data.source}
            </span>
          </div>
          {!catalog ? (
            <p className="text-[11px] leading-relaxed text-ink-3">
              宿主返回的命令目录没读全：缺少清单，不能当成「0 条命令」。
            </p>
          ) : catalog.length === 0 ? (
            <p className="text-[11px] leading-relaxed text-ink-3">
              宿主返回了空命令目录：这个会话没有可枚举的原生命令（或宿主版本不支持 ListCommands）。
            </p>
          ) : commands.length === 0 ? (
            <p className="text-[11px] leading-relaxed text-ink-3">没有匹配「{query.trim()}」的命令。</p>
          ) : (
            <ul role="list" aria-label="原生命令列表" className="space-y-1">
              {commands.map((command) => {
                const detail = commandDetailText(command)
                return (
                  <li key={command.name} className="rounded border border-hairline bg-card/50 px-2.5 py-1.5">
                    <div className="flex flex-wrap items-baseline gap-x-2">
                      <code className="font-mono text-[12px] text-ink">/{command.name}</code>
                      {command.description && <span className="saker-break text-[11px] text-ink-2">{command.description}</span>}
                    </div>
                    {detail && <div className="saker-break font-mono text-[11px] text-ink-3">{detail}</div>}
                  </li>
                )
              })}
            </ul>
          )}
        </>
      )}
    </section>
  )
}

/**
 * The host's canonical event history (ListEvents), paged by `next_cursor`.
 * The host payload is proto JSON the console does not own: rows show a stable
 * summary where recognisable and the raw document otherwise. A page failure
 * keeps the already-loaded rows; "已到末尾" is only said when the host's cursor
 * is exhausted, never inferred from an empty response.
 */
export function RunHistoryPanel({ runID, sessionID }: { runID: string; sessionID?: string }) {
  const [events, setEvents] = useState<NativeSessionEvent[]>([])
  const [page, setPage] = useState<RunHistoryPage>()
  const [error, setError] = useState<unknown>()
  const [loading, setLoading] = useState(false)

  const cursor = nextRunHistoryCursor(page)
  const canLoadMore = Boolean(page && cursor)

  async function load(after: string) {
    if (loading) return
    setLoading(true)
    setError(undefined)
    try {
      const next = await api.runHistory(runID, after)
      setEvents((previous) => appendRunHistory(previous, next))
      setPage(next)
    } catch (e) {
      setError(e)
    } finally {
      setLoading(false)
    }
  }

  const failure = error ? nativeSessionErrorText(error) : null

  return (
    <section aria-label="宿主历史" data-testid="run-native-history" className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <History className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" />
        <span className="text-[12px] font-medium text-ink">宿主历史</span>
        {sessionID && <span className="font-mono text-[11px] text-ink-3">会话 {sessionID.slice(0, 12)}…</span>}
        <Badge size="sm" variant="outline">canonical ListEvents</Badge>
        <Button
          size="xs"
          variant="outline"
          className="ml-auto"
          onClick={() => void load('')}
          disabled={loading}
          aria-label={page ? '从第一页重新加载宿主历史' : '加载宿主历史第一页'}
        >
          {loading && !page ? <Spinner size="sm" className="h-3 w-3" /> : <RefreshCw className={`h-3 w-3 ${loading ? 'motion-safe:animate-spin' : ''}`} />}
          {page ? '从第一页重新加载' : '加载第一页'}
        </Button>
      </div>
      {!page && !error && !loading && (
        <p className="text-[11px] leading-relaxed text-ink-3">
          按需读取宿主侧的 canonical 事件历史（ListEvents）。它与账本事件流是两份记录：账本记录Saker的判断，这里记录会话实际发生的事。
        </p>
      )}
      {failure && (
        <Callout tone={failure.tone}>
          <span className="saker-break">{failure.text}</span>
          <Button
            size="xs"
            variant="outline"
            className="ml-2"
            onClick={() => void load(page ? cursor : '')}
          >
            重试
          </Button>
        </Callout>
      )}
      {page && page.events.length === 0 && events.length === 0 && !error && (
        <p className="text-[11px] leading-relaxed text-ink-3">
          宿主返回了空页：这个会话在宿主侧没有事件（可能刚建立或已被宿主清理）。
        </p>
      )}
      {events.length > 0 && (
        <>
          <ul role="list" aria-label="宿主历史事件" className="space-y-1">
            {events.map((item) => {
              const summary = historyEventSummary(item.event)
              return (
                <li key={item.cursor} className="rounded border border-hairline bg-card/50 px-2.5 py-1.5">
                  <div className="flex flex-wrap items-baseline gap-x-2">
                    <Badge size="sm" variant="muted">{summary.kind}</Badge>
                    {summary.state && <span className="font-mono text-[11px] text-info">{summary.state}</span>}
                    <span className="saker-nums ml-auto font-mono text-[10px] text-ink-3" title="宿主游标">{item.cursor}</span>
                  </div>
                  <p className="saker-break mt-0.5 font-mono text-[11px] leading-relaxed text-ink-2">{summary.text}</p>
                  {summary.at && <p className="font-mono text-[10px] text-ink-3">{summary.at}</p>}
                </li>
              )
            })}
          </ul>
          <div className="flex flex-wrap items-center gap-2">
            <span className="saker-nums font-mono text-[11px] text-ink-3" role="status" aria-live="polite">
              已加载 {events.length} 条{canLoadMore ? '，还有更多' : '，已到末尾'}
            </span>
            {canLoadMore ? (
              <Button size="xs" variant="outline" className="ml-auto" onClick={() => void load(cursor)} disabled={loading} aria-label="加载更多宿主历史">
                {loading ? <Spinner size="sm" className="h-3 w-3" /> : null}
                {loading ? '加载中…' : '加载更多'}
              </Button>
            ) : (
              <span className="ml-auto text-[11px] text-ink-3">宿主历史已全部载入。</span>
            )}
          </div>
        </>
      )}
    </section>
  )
}
