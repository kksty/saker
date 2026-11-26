import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArrowDown, FileText, Link2, MessageSquare, RefreshCw, Send, Terminal, Users } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Popover,
  PopoverContent,
  PopoverTrigger,
  ScrollArea,
  SkeletonList,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel, pageGutter } from '@/components/Page'
import { WorkspaceSidebar } from '@/components/WorkspaceSidebar'
import { ChatComposer } from '@/components/ChatComposer'
import { DeliveryEvidence, type EvidenceContext } from '@/components/DeliveryEvidence'
import { OperatorInstructionsCard } from '@/components/OperatorInstructions'
import { OperatorProposalsCard } from '@/components/OperatorProposals'
import { SenderAvatar } from '@/components/SenderAvatar'
import { alignRecords, type Alignment } from '@/lib/alignment'
import { buildTimeline, instructionNoticeState, type TimelineEntry } from '@/lib/conversation'
import {
  api,
  backlogSummary,
  isMechanicalSender,
  queueLoad,
  queueUnderPressure,
  readFactNotice,
} from '@/lib/api'
import { fmtBytes, fmtTime } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { AsyncState } from '@/lib/hooks'
import { useLatestScroll } from '@/lib/latest-scroll'
import { useSessionState } from '@/lib/session-state'
import {
  buildRecordMaps,
  describeRecord,
  eventLabel,
  messageKindLabel,
  participantName,
  participantRole,
  rawRecordKey,
  type RecordMaps,
} from '@/lib/ioa'
import type { BoardResponse, CommunicationQueue, CommunicationStatus, IOARecord, Message, Participant } from '@/lib/types'

/** Delivery state as the operator sees it. "accepted" is IOA taking the bytes,
 *  not the peer's model reading them; only an ack becomes "confirmed". */
function deliveryVariant(status: string): 'success' | 'info' | 'warning' | 'danger' | 'muted' {
  switch (status) {
    case 'confirmed':
      return 'success'
    case 'accepted':
      return 'info'
    case 'queued':
    case 'sending':
      return 'warning'
    case 'unknown':
      return 'danger'
    default:
      return 'muted'
  }
}

function deliveryShortLabel(status: string): string {
  switch (status) {
    case 'queued':
      return '排队'
    case 'sending':
      return '投递中'
    case 'accepted':
      return '已接收'
    case 'confirmed':
      return '已确认'
    case 'unknown':
      return '结果未知'
    default:
      return status || '—'
  }
}

function deliveryLabel(status: string): string {
  switch (status) {
    case 'queued':
      return '排队（对方下次运行可见）'
    case 'sending':
      return '投递中（不能撤回）'
    case 'accepted':
      return '宿主已接收（未确认已读）'
    case 'confirmed':
      return '对方已确认'
    case 'unknown':
      return '结果未知（不要自动重发）'
    default:
      return status
  }
}

/** Empty result for "no new rows": shared so a poll allocates nothing. */
const NO_NEW_KEYS: ReadonlySet<string> = new Set()

/** No raw observations: shared so the alignment memo does not allocate. */
const NO_RECORDS: readonly IOARecord[] = []
const NO_OBSERVATIONS = { records: NO_RECORDS as IOARecord[], sessions: {} as Record<string, string> }

const messageKey = (m: Message): string => m.id

/** The React key of a raw IOA record; the fallback is the monotonic seq. */
const nativeRecordKey = rawRecordKey

/**
 * Keys that appeared since the previous poll of this list.
 *
 * The first non-empty snapshot only seeds the baseline: a page load, a filter
 * change or an instance/project toggle must not mark old history as "just
 * delivered". `seen` is then replaced by the current key set, so old rows
 * dropping out of the 200/500 window leave nothing behind, while a row hidden by
 * the sender/recipient filter and shown again later is still old — callers pass
 * the *unfiltered* list for that reason.
 */
function useFreshKeys<T>(items: readonly T[], keyOf: (item: T) => string): ReadonlySet<string> {
  const keyRef = useRef(keyOf)
  keyRef.current = keyOf
  const seen = useRef<Set<string> | null>(null)
  const fresh = useMemo(() => {
    const known = seen.current
    if (!known) return NO_NEW_KEYS
    const next = new Set<string>()
    for (const item of items) {
      const key = keyRef.current(item)
      if (!known.has(key)) next.add(key)
    }
    return next.size > 0 ? next : NO_NEW_KEYS
  }, [items])
  useEffect(() => {
    // An empty snapshot (not loaded yet, or filtered to nothing) is not a
    // baseline: the first rows that arrive are the start of the stream.
    if (items.length === 0) return
    const next = new Set<string>()
    for (const item of items) next.add(keyRef.current(item))
    seen.current = next
  }, [items])
  return fresh
}

/** Project conversation. It is a record of participants talking, not a fact
 *  feed: messages never enter the graph or the report. The operator types here
 *  like a chat; every send is one delivery per recipient, and the receipt says
 *  who got it now, who reads it next round, and which settled direction was
 *  deliberately skipped. */
export function Messages({ boardState }: {
  /** The shell's board poll: the composer's paused/running hints and the refresh
   *  after a send use it. The page used to poll the same endpoint itself. */
  boardState: AsyncState<BoardResponse>
}) {
  const board = boardState
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [sender, setSender] = useState('all')
  const [recipient, setRecipient] = useState('all')
  useEffect(() => {
    window.dispatchEvent(new Event('saker:operator-messages-read'))
  }, [])
  const messages = usePoll(api.messages, 4000)
  const rows = messages.data ?? []
  // Tracked over the *unfiltered* list on purpose: the sender/recipient filter
  // may hide a row and show it again, and a historical row must not be marked as
  // just-delivered just because it became visible again.
  const freshMessageIDs = useFreshKeys(rows, messageKey)
  const participants = usePoll(api.participants, 8000)
  const people = participants.data ?? []
  // The aligned transport projection (lib/alignment). The ledger stays the
  // primary chain; raw IOA observations are an audit layer, so they are polled
  // only while the operator has the alignment open.
  const [evidence, setEvidence] = useSessionState('messages:evidence', true)
  const observed = usePoll(
    () => (evidence ? api.ioaObservations() : Promise.resolve(NO_OBSERVATIONS)),
    8000,
    [evidence],
  )
  const nativeRecords: readonly IOARecord[] = observed.data?.records ?? NO_RECORDS
  const nativeSessions = observed.data?.sessions ?? {}
  const nativeMaps = useMemo(
    () => buildRecordMaps(nativeSessions, people, nativeRecords),
    [nativeSessions, people, nativeRecords],
  )
  const freshRecordKeys = useFreshKeys(nativeRecords, nativeRecordKey)
  // Align against the whole ledger window, not the filtered rows: a row hidden by
  // the sender/recipient filter must not mark its raw record as unmatched.
  const alignment = useMemo(() => alignRecords(rows, nativeRecords), [rows, nativeRecords])
  // Queue pressure is observation, not delivery: a deferred notice means the
  // router kept the change in the journal instead of dropping it.
  const communication = usePoll(api.communication, 8000)

  const [nativeOpen, setNativeOpen] = useState(false)
  const composerRef = useRef<HTMLDivElement | null>(null)
  const roleMatch = (id: string, filter: string) =>
    filter === 'all' ||
    (filter === 'executors' && id.startsWith('pt_')) ||
    id === filter
  const visibleRows = rows.filter(
    (m) => roleMatch(m.from_participant_id, sender) && roleMatch(m.to_participant_id, recipient),
  )
  const timeline = useMemo(() => buildTimeline(visibleRows), [visibleRows])
  const latest = useLatestScroll<HTMLDivElement>(timeline.length, `${sender}:${recipient}`)
  const paused = Boolean(board.data?.project_paused || board.data?.decider.paused)
  const runningExecutors = board.data?.intents.running ?? 0

  function focusComposer() {
    composerRef.current?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
  }

  return (
    <Page
      title="通信"
      subtitle="查看项目对话和投递状态。侧栏管理发给指挥官的持续指令。"
      actions={
        <Button size="sm" variant="outline" className="gap-1.5" onClick={focusComposer}>
          <Send className="h-3.5 w-3.5" />
          下发
        </Button>
      }
      scroll={false}
      className="@container"
    >
      <div className={cn('flex h-full min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain py-4 sm:py-5 @3xl:grid @3xl:grid-cols-[248px_minmax(0,1fr)] @3xl:gap-4 @3xl:overflow-hidden', pageGutter)}>
        <WorkspaceSidebar
          label="参与者与积压"
          summary={participants.error || communication.error ? '读取失败' : `${people.length} 位参与者`}
          expanded={sidebarOpen}
          onToggle={() => setSidebarOpen(open => !open)}
        >
          <div className="flex max-h-[min(55dvh,24rem)] min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain @3xl:max-h-none @3xl:overflow-hidden">
            <Panel
              title={`参与者（${people.length}）`}
              className="@3xl:flex @3xl:min-h-0 @3xl:flex-col"
              bodyClassName="p-1.5 @3xl:min-h-0 @3xl:flex-1 @3xl:overflow-y-auto"
              extra={
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="刷新参与者"
                  title="刷新参与者"
                  onClick={() => participants.reload()}
                >
                  <RefreshCw className={`h-3.5 w-3.5 ${participants.loading ? 'motion-safe:animate-spin' : ''}`} />
                </Button>
              }
            >
              {participants.loading && !participants.data && <SkeletonList count={4} />}
              {participants.error && (
                <div className="p-1">
                  <Callout tone="destructive">
                    <div className="flex flex-wrap items-center gap-2">
                      <span>读参与者失败：{participants.error}</span>
                      <Button size="xs" variant="outline" onClick={() => participants.reload()}>重试</Button>
                    </div>
                  </Callout>
                </div>
              )}
              {!participants.loading && !participants.error && people.length === 0 && (
                <EmptyState
                  compact
                  icon={Users}
                  title="还没有参与者"
                  description="意图开始执行后，参与者会出现在这里。"
                />
              )}
              <ul role="list" aria-label="参与者名单">
                {people.map((p) => (
                  <ParticipantRow key={p.id} p={p} />
                ))}
              </ul>
            </Panel>
            <div className="flex shrink-0 flex-col gap-3">
              <CommunicationBacklog status={communication.data} error={communication.error} people={people} onRetry={() => communication.reload()} />
              <OperatorInstructionsCard />
            <OperatorProposalsCard />
            </div>
          </div>
        </WorkspaceSidebar>

        <section className="saker-panel flex min-h-min min-w-0 flex-1 flex-col overflow-hidden rounded-xl @3xl:min-h-0">
          <header className="saker-panel-header flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
            <div className="saker-section-title">对话</div>
            <span className="saker-nums font-mono text-[11px] text-ink-3">{timeline.length} 条</span>
            <Button
              size="icon-xs"
              variant="ghost"
              className="ml-auto"
              aria-label="刷新通信记录"
              title="刷新通信记录"
              onClick={() => { messages.reload(); communication.reload(); observed.reload() }}
            >
              <RefreshCw className={`h-3.5 w-3.5 ${messages.loading ? 'motion-safe:animate-spin' : ''}`} />
            </Button>
            <div className="flex w-full flex-wrap items-center gap-1.5 @3xl:w-auto">

              <Button
                size="xs"
                variant={evidence ? 'soft' : 'outline'}
                aria-pressed={evidence}
                title="把每条账本消息和本项目的 IOA 原始观察按消息编号对齐；关闭后只显示账本对话"
                onClick={() => setEvidence((current) => !current)}
              >
                <Link2 className="h-3.5 w-3.5" />
                对齐原始投递
              </Button>
              <Popover>
                <PopoverTrigger asChild>
                  <Button size="xs" variant="ghost" aria-label="更多通信工具" title="更多通信工具">
                    更多
                  </Button>
                </PopoverTrigger>
                <PopoverContent align="end" className="w-60 p-1.5">
                  <Button
                    size="xs"
                    variant="ghost"
                    className="w-full justify-start"
                    title="整个实例的 IOA 原始观察（含其他项目与未关联会话）"
                    onClick={() => setNativeOpen(true)}
                  >
                    <Terminal className="h-3.5 w-3.5" />
                    实例原始观察
                  </Button>
                  <p className="px-2 pt-1 text-[10.5px] leading-4 text-ink-3">
                    查看系统记录的投递情况。已读状态以回执为准。
                  </p>
                </PopoverContent>
              </Popover>
            </div>
          </header>

          <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-hairline px-3 py-1.5">
            <div className="flex min-w-0 flex-1 gap-2 @3xl:flex-none">
              <FilterSelect label="发送者" value={sender} onChange={setSender} people={people} />
              <FilterSelect label="收件人" value={recipient} onChange={setRecipient} people={people} />
            </div>
            <DeliveryHelp />
            <span className="ml-auto hidden text-[11px] text-ink-3 @3xl:inline">最新在下面</span>
          </div>

          {messages.error && (
            <div className="shrink-0 border-b border-hairline p-2">
              <Callout tone="destructive">
                <div className="flex flex-wrap items-center gap-2">
                  <span>读对话失败：{messages.error}</span>
                  <Button size="xs" variant="outline" onClick={() => messages.reload()}>重试</Button>
                </div>
              </Callout>
            </div>
          )}

          <div className="relative min-h-36 flex-1 @3xl:min-h-0">
            <div
              ref={latest.ref}
              role="region"
              aria-label="对话记录"
              tabIndex={0}
              className="h-full overflow-y-auto overscroll-contain px-3 py-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40 [scrollbar-color:var(--ink-3)_transparent] [scrollbar-width:thin]"
            >
              {messages.loading && rows.length === 0 && <SkeletonList count={5} />}
              {!messages.loading && timeline.length === 0 && !messages.error && (
                <EmptyState
                  compact
                  icon={MessageSquare}
                  title={rows.length === 0 ? '还没有消息' : '这个筛选下没有记录'}
                  description={
                    rows.length === 0
                      ? '在下方发第一条：只交给指挥官、全部执行者或某个意图。'
                      : '换一个发送者或收件人，或在下方下发一条消息。'
                  }
                />
              )}
              {timeline.length > 0 && (
                <div role="list" aria-label="项目对话">
                  {timeline.map((entry, index) => {
                    const day = fmtTime(entry.kind === 'message' ? entry.message.created_at : entry.notice.created_at).slice(0, 5)
                    const previous = index > 0 ? timeline[index - 1] : undefined
                    const previousDay = previous
                      ? fmtTime(previous.kind === 'message' ? previous.message.created_at : previous.notice.created_at).slice(0, 5)
                      : ''
                    return (
                      <div role="listitem" key={entry.kind === 'message' ? entry.message.id : entry.key}>
                        {day !== previousDay && <DayDivider day={day} />}
                        {entry.kind === 'instruction' ? (
                          <InstructionNoticeLine entry={entry} />
                        ) : (
                          <ChatMessage
                            message={entry.message}
                            people={people}
                            fresh={freshMessageIDs.has(entry.message.id)}
                            evidence={
                              evidence
                                ? {
                                    records: alignment.byMessage.get(entry.message.id) ?? NO_RECORDS,
                                    maps: nativeMaps,
                                    fresh: freshRecordKeys,
                                  }
                                : undefined
                            }
                          />
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
              {evidence && (
                <AlignedFooter
                  alignment={alignment}
                  maps={nativeMaps}
                  fresh={freshRecordKeys}
                  error={observed.error}
                  loading={observed.loading}
                  total={nativeRecords.length}
                />
              )}
              <p role="status" aria-live="polite" className="sr-only">
                对话共 {timeline.length} 条，最新消息在底部
              </p>
            </div>
            {!latest.pinned && (
              <button
                type="button"
                onClick={latest.jump}
                className="absolute bottom-3 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-primary/30 bg-popover px-3 py-1.5 text-[11px] font-medium text-ink shadow-elevated transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                <ArrowDown className="h-3.5 w-3.5" />
                {latest.unread > 0 ? `${latest.unread} 条新消息` : '回到最新'}
              </button>
            )}
          </div>

          {/* The single 下发 entry: the same component as the graph's right dock,
              condensed to a chat bar so it does not cover the conversation. */}
          <div
            ref={composerRef}
            className="max-h-[min(46dvh,16rem)] shrink-0 overflow-y-auto overscroll-contain @3xl:max-h-[46dvh]"
          >
            <ChatComposer
              compact
              paused={paused}
              runningExecutors={runningExecutors}

              onSent={() => {
                messages.reload()
                board.reload()
              }}
            />
          </div>
        </section>
      </div>


      <Dialog open={nativeOpen} onOpenChange={setNativeOpen}>
        <DialogContent className="flex h-[min(88vh,860px)] w-[min(96vw,1180px)] max-w-none flex-col gap-0 overflow-hidden p-0">
          <DialogHeader className="shrink-0 border-b border-hairline px-5 py-4 pr-12 text-left">
            <DialogTitle>实例原始观察</DialogTitle>
            <DialogDescription className="text-[12px]">
              整个实例（含其他项目与未关联会话）的 IOA 原始观察，用于和「运行」页核对；本项目消息的对应投递已在对话里按消息编号对齐显示。观察到消息不代表对方已读。
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1">
            <NativeRecords people={people} />
          </div>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

function FilterSelect({
  label,
  value,
  onChange,
  people,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  people: Participant[]
}) {
  return (
    <label className="flex min-w-0 flex-1 items-center gap-1 text-[11px] text-ink-3">
      <span className="shrink-0">{label}</span>
      <select
        className="h-7 min-w-0 flex-1 rounded-md border border-hairline bg-card px-1.5 text-[11px] text-ink-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 @3xl:w-[156px]"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-label={`按${label}筛选`}
      >
        <option value="all">全部</option>
        <option value="commander">指挥官</option>
        <option value="operator">操作员</option>
        <option value="executors">全部执行者</option>
        {people.filter((p) => p.id.startsWith('pt_')).map((p) => (
          <option key={p.id} value={p.id}>{p.title}</option>
        ))}
      </select>
    </label>
  )
}

function DeliveryHelp() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="xs" variant="ghost" aria-label="投递状态怎么读">状态说明</Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 text-[11px] leading-relaxed">
        <p className="text-[12px] font-medium text-ink">投递状态</p>
        <p className="mt-1 text-ink-2">这些状态记录消息投递进度。动作结果请查看对应运行。</p>
        <ul className="mt-1 space-y-0.5 text-ink-2">
          <li><span className="text-ink">排队</span>：对方下次运行开始时可见</li>
          <li><span className="text-ink">投递中</span>：已经发出，不能撤回</li>
          <li><span className="text-ink">已接收</span>：宿主收下了，但未确认对方模型已读</li>
          <li><span className="text-ink">已确认</span>：对方给了确认回执</li>
          <li><span className="text-ink">结果未知</span>：不确定是否送达，不要自动重发</li>
        </ul>
        <p className="mt-2 text-ink-3">一条消息 = 一次投递，不代表对方已读，也不代表结论；事实与证据在「图谱」里。</p>
      </PopoverContent>
    </Popover>
  )
}

function DayDivider({ day }: { day: string }) {
  return (
    <div className="my-3 flex items-center gap-2 text-[10px] text-ink-3">
      <span className="h-px flex-1 bg-hairline" />
      <span className="rounded-full bg-surface-2/60 px-2 py-0.5 font-mono saker-nums">{day}</span>
      <span className="h-px flex-1 bg-hairline" />
    </div>
  )
}

// One ledger row per instruction revision, not per recipient: the notice is a
// current record (creating, editing and revoking each queue one), so the line
// says the revision changed and leaves management to the sidebar card.
function InstructionNoticeLine({ entry }: { entry: Extract<TimelineEntry, { kind: 'instruction' }> }) {
  const state = instructionNoticeState(entry.notice.text)
  return (
    <div className="my-2 flex justify-center">
      <div
        data-instruction-notice
        title="账本里的持续指令变更记录；编辑与撤销在通信侧栏的持续指令卡片"
        className="flex max-w-full flex-wrap items-center justify-center gap-x-1.5 gap-y-0.5 rounded-full border border-hairline bg-surface-2/40 px-3 py-1 text-[10.5px] leading-4 text-ink-3"
      >
        <FileText className="h-3 w-3 shrink-0" />
        <span className="text-ink-2">
          持续指令{entry.notice.instruction_version ? ` v${entry.notice.instruction_version}` : ''} 已更新
        </span>
        {state && (
          <span className={state === 'revoked' ? 'text-warning' : 'text-success'}>
            {state === 'revoked' ? '已撤销' : '生效'}
          </span>
        )}
        <span>· 通知 {entry.recipients} 人</span>
        <time className="font-mono saker-nums">{fmtTime(entry.notice.created_at).slice(6)}</time>
      </div>
    </div>
  )
}

/** The aligned transport projection attached to one ledger row (the component
 *  lives in components/DeliveryEvidence so it can be rendered in tests). */
function ChatMessage({
  message,
  people,
  fresh,
  evidence,
}: {
  message: Message
  people: Participant[]
  fresh: boolean
  evidence?: EvidenceContext
}) {
  const mechanical = isMechanicalSender(message.from_participant_id)
  const notice = readFactNotice(message)
  if (mechanical) return <MechanicalNotice message={message} people={people} notice={notice} fresh={fresh} evidence={evidence} />
  const sender = participantName(message.from_participant_id, people)
  const role = participantRole(message.from_participant_id, people)
  const outgoing = role === 'operator'
  const recipient = participantName(message.to_participant_id, people)
  // "sending" is IOA's in-flight transport state. A row that just arrived through
  // the live-run bridge (delivery_path=ioa) was injected into a running session,
  // so it is marked while it is the new row and then goes quiet.
  const live = message.status === 'sending' || (fresh && message.delivery_path === 'ioa')
  const hasDetails = Boolean(
    notice ||
    message.error ||
    message.in_reply_to ||
    (message.fact_refs?.length ?? 0) > 0 ||
    message.notice_intent_id ||
    (message.redeliveries ?? 0) > 0,
  )
  return (
    <article
      data-message-id={message.id}
      title={`消息 ${message.id} · ${deliveryLabel(message.status)}`}
      className={cn('group flex items-start gap-2 py-1.5', outgoing ? 'flex-row-reverse' : 'flex-row')}
    >
      <SenderAvatar role={role} className="mt-5" />
      <div className={cn('flex min-w-0 max-w-[min(78%,44rem)] flex-col', outgoing ? 'items-end' : 'items-start')}>
        <div className={cn('mb-1 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[10.5px] leading-4 text-ink-3')}>
          {live && (
            <span
              aria-hidden="true"
              title={message.status === 'sending' ? '投递中（已经发出，不能撤回）' : '刚刚投递进正在运行的会话'}
              className="h-1.5 w-1.5 shrink-0 rounded-full bg-success"
            />
          )}
          <span className="min-w-0 max-w-[14rem] truncate font-medium text-ink-2" title={sender}>{sender}</span>
          <span aria-hidden="true" className="shrink-0 text-ink-3/60">→</span>
          <span className={cn('min-w-0 max-w-[10rem] truncate', outgoing ? 'text-ink-3' : 'text-ink-2')} title={recipient}>{recipient}</span>
          {message.kind && (
            <span className="shrink-0 rounded border border-hairline px-1 text-[10px] leading-4">
              {message.kind === 'fact_notice' ? '共享发现' : messageKindLabel(message.kind)}
            </span>
          )}
          {message.delivery_path && (
            <span className="shrink-0" title="投递路径：运行中=说进对方正在跑的会话；下一轮=随对方下次运行的提示词注入">
              {message.delivery_path === 'ioa' ? '运行中' : '下一轮'}
            </span>
          )}
          <span className="ml-auto flex shrink-0 items-center gap-1.5">
            <time className="font-mono saker-nums" title={message.created_at}>{fmtTime(message.created_at).slice(6)}</time>
            <Badge size="sm" variant={deliveryVariant(message.status)} title={deliveryLabel(message.status)}>
              {deliveryShortLabel(message.status)}
            </Badge>
          </span>
        </div>
        <div
          className={cn(
            'saker-break min-w-0 max-w-full whitespace-pre-wrap break-words rounded-2xl border px-3 py-2 text-[12.5px] leading-relaxed',
            outgoing
              ? 'rounded-br-md border-primary/25 bg-primary/[0.13] text-ink'
              : 'rounded-bl-md border-hairline bg-surface-2/70 text-ink-2',
          )}
        >
          {message.text || '（无正文）'}
        </div>
        {hasDetails && (
          <div className={cn('mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10.5px] leading-4 text-ink-3', outgoing && 'justify-end')}>
            <span className="font-mono" title="项目消息编号">{message.id}</span>
            {(message.redeliveries ?? 0) > 0 && (
              <Badge size="sm" variant="warning" title={`重试已重新注入这条未确认消息 ${message.redeliveries} 次`}>
                重投 {message.redeliveries}
              </Badge>
            )}
            {notice && (
              <>
                <span>事实 <span className="font-mono text-ink-2">{notice.factId}</span> v{notice.version}</span>
                {notice.coalesced > 0 && <span>合并 {notice.coalesced} 次</span>}
                <span title="通知只携带指针；正文、版本与证据在事实详情里按 ID 读取">按 ID 读取</span>
              </>
            )}
            {message.notice_intent_id && (
              <span title="下游意图结果通知：交给承接者判断，不改写原意图的必要前提">
                来源意图 <span className="font-mono">{message.notice_intent_id}</span>
              </span>
            )}
            {message.in_reply_to && <span>回复 <span className="font-mono">{message.in_reply_to}</span></span>}
            {(message.fact_refs?.length ?? 0) > 0 && (
              <span className="flex flex-wrap items-center gap-1" title={(message.fact_refs ?? []).join('\n')}>
                引用事实
                {message.fact_refs?.map((fact) => (
                  <Badge key={fact} size="sm" variant="outline" className="font-mono">{fact}</Badge>
                ))}
              </span>
            )}
            {message.error && <span className="text-destructive">{message.error}</span>}
          </div>
        )}
        {evidence && <DeliveryEvidence message={message} {...evidence} />}
      </div>
    </article>
  )
}

function MechanicalNotice({
  message,
  people,
  notice,
  fresh,
  evidence,
}: {
  message: Message
  people: Participant[]
  notice: ReturnType<typeof readFactNotice>
  fresh: boolean
  evidence?: EvidenceContext
}) {
  const live = message.status === 'sending' || (fresh && message.delivery_path === 'ioa')
  return (
    <article
      data-message-id={message.id}
      data-mechanical-notice="true"
      title={`消息 ${message.id} · ${deliveryLabel(message.status)}`}
      className="my-1.5 rounded-lg border border-hairline bg-surface-2/25 px-3 py-2"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10.5px] leading-4 text-ink-3">
        {live && <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-success" />}
        <span className="font-medium text-ink-2">机械通知</span>
        <Badge size="sm" variant="outline" className="font-mono" title="机械发送者身份：没有模型会话，也不代表授权">ledger</Badge>
        <span aria-hidden="true" className="text-ink-3/60">→</span>
        <span className="text-ink-2">{participantName(message.to_participant_id, people)}</span>
        {message.kind && (
          <Badge size="sm" variant="muted">
            {message.kind === 'fact_notice' ? '共享发现通知' : messageKindLabel(message.kind)}
          </Badge>
        )}
        <time className="ml-auto font-mono saker-nums" title={message.created_at}>{fmtTime(message.created_at).slice(6)}</time>
        <Badge size="sm" variant={deliveryVariant(message.status)} title={deliveryLabel(message.status)}>
          {deliveryShortLabel(message.status)}
        </Badge>
      </div>
      <p className="saker-break mt-1 whitespace-pre-wrap text-[11.5px] leading-relaxed text-ink-3">{message.text}</p>
      {notice && (
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10.5px] leading-4 text-ink-3">
          <span>事实 <span className="font-mono text-ink-2">{notice.factId}</span></span>
          <span aria-hidden="true" className="text-ink-3/50">·</span>
          <span>记录版本 <span className="font-mono">v{notice.version}</span></span>
          {notice.coalesced > 0 && <span>合并 {notice.coalesced} 次</span>}
          <span title="通知只携带指针；正文、版本与证据在事实详情里按 ID 读取">按 ID 读取记录（通知不是正文，也不授权动作）</span>
        </div>
      )}
      {message.error && <div className="mt-1 text-[11px] text-destructive">{message.error}</div>}
      {evidence && <DeliveryEvidence message={message} {...evidence} />}
    </article>
  )
}

const UNALIGNED_REASON: Record<string, string> = {
  'no-header': '没有寻址头：协议事件或无法识别的投递',
  'header-without-message-id': '寻址头里没有消息编号',
  'message-not-in-window': '消息编号不在当前账本窗口（可能早于最近 200 条，或被撤回）',
}

/**
 * The other half of the projection: project observations that no ledger row in
 * the window claims. They stay visible and separate — silently dropping them
 * would hide harness-internal traffic and rows older than the message window,
 * while folding them into the chat would invent conversation the ledger never
 * recorded.
 */
function AlignedFooter({
  alignment,
  maps,
  fresh,
  error,
  loading,
  total,
}: {
  alignment: Alignment
  maps: RecordMaps
  fresh: ReadonlySet<string>
  error?: string
  loading: boolean
  total: number
}) {
  const [open, setOpen] = useState(false)
  const { unaligned, byMessage } = alignment
  const matched = [...byMessage.values()].reduce((sum, rows) => sum + rows.length, 0)
  if (error) return <div className="mt-2"><Callout tone="destructive">读原始观察失败：{error}</Callout></div>
  if (loading && total === 0) return <div className="mt-2"><SkeletonList count={1} /></div>
  if (unaligned.length === 0) {
    return (
      <p className="mt-2 text-center text-[10px] text-ink-3" role="status">
        原始观察 {total} 条 · 与当前账本匹配 {matched} 条
      </p>
    )
  }
  const shown = unaligned.slice(-50)
  return (
    <section className="mt-3 rounded-lg border border-hairline bg-surface-2/20">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex w-full items-center gap-1.5 px-2.5 py-2 text-left text-[11px] text-ink-3 transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <Terminal className="h-3 w-3 shrink-0" />
        <span className="font-medium text-ink-2">未匹配当前账本（{unaligned.length}）</span>
        <span className="saker-nums ml-auto shrink-0 font-mono">
          {matched} / {total} 已对上
        </span>
        <span className="shrink-0">{open ? '收起' : '展开'}</span>
      </button>
      {open && (
        <div className="px-2.5 pb-2.5">
          <p className="mb-1.5 text-[10.5px] leading-4 text-ink-3">
            这些 IOA 观察未能与当前列表中的账本消息匹配。它们可能是宿主内部事件，也可能对应列表范围之外的消息。
            {unaligned.length > shown.length && ` 只列最近 ${shown.length} 条。`}
          </p>
          <ul role="list" aria-label="未匹配当前账本的原始观察" className="space-y-1.5">
            {shown.map(({ record, reason }) => (
              <li key={nativeRecordKey(record)}>
                <p className="mb-0.5 text-[10px] leading-4 text-ink-3/80">{UNALIGNED_REASON[reason] ?? reason}</p>
                <NativeMessage rec={record} maps={maps} defer={false} fresh={fresh.has(nativeRecordKey(record))} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

/** Per-recipient queue pressure and routing checkpoints. "Deferred" means the
 *  router left a change in the journal because that queue was at its bound: the
 *  change is retried, not lost. The panel is deliberately small — it reports
 *  pressure, it is not a second inbox. */
function CommunicationBacklog({ status, error, people, onRetry }: {
  status?: CommunicationStatus
  error?: string
  people: Participant[]
  onRetry: () => void
}) {
  const summary = backlogSummary(status)
  const limits = { max_messages: status?.max_messages ?? 0, max_bytes: status?.max_bytes ?? 0 }
  const visible = (status?.queues ?? []).filter((q) => q.pending > 0 || q.deferred)
  return (
    <Panel title="通信积压" className="shrink-0" bodyClassName="space-y-2 p-3">
      {error && (
        <Callout tone="destructive">
          <div className="flex flex-wrap items-center gap-2">
            <span>读通信积压失败：{error}</span>
            <Button size="xs" variant="outline" onClick={onRetry}>重试</Button>
          </div>
        </Callout>
      )}
      <p className="text-[12px] leading-5 text-ink-2">
        排队 <span className="saker-nums font-mono">{summary.queued}</span> 条 · <span className="saker-nums font-mono">{fmtBytes(summary.bytes)}</span> · <span className="saker-nums font-mono">{summary.recipients}</span> 个收件人
        {summary.deferred > 0 && (
          <> · <span className="text-warning">{summary.deferred} 个队列延后待发</span></>
        )}
      </p>
      {status?.paused && (
        <p className="text-[11px] leading-4 text-warning">
          项目暂停：关联通知只入队不投递，恢复后由活动会话或下一次提示词读取。
        </p>
      )}
      {!status && <p className="text-[11px] text-ink-3">读取中…</p>}
      {status && visible.length === 0 && <p className="text-[11px] text-ink-3">没有积压</p>}
      {visible.slice(0, 8).map((q) => <QueueRow key={q.recipient} q={q} limits={limits} people={people} />)}
      {status && (
        <p className="text-[11px] leading-4 text-ink-3">
          上限 {status.max_messages} 条 / {fmtBytes(status.max_bytes)}；满队列时发送返回 429 message_backpressure，改动不会被静默丢弃。
        </p>
      )}
    </Panel>
  )
}

function QueueRow({ q, limits, people }: {
  q: CommunicationQueue
  limits: { max_messages: number; max_bytes: number }
  people: Participant[]
}) {
  const load = queueLoad(q, limits)
  const pressure = queueUnderPressure(q, limits)
  return (
    <div data-queue-recipient={q.recipient} className="space-y-1 rounded-md border border-hairline px-2.5 py-1.5 text-[11px]">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 truncate text-[12px] text-ink-2" title={q.recipient}>{participantName(q.recipient, people)}</span>
        {q.deferred && (
          <Badge size="sm" variant="warning" title="队列已满：变化留在日志，检查点未推进，稍后重试；不会丢弃">
            延后
          </Badge>
        )}
        {pressure && !q.deferred && (
          <Badge size="sm" variant="primary" title="接近容量上限">接近上限</Badge>
        )}
        <span className="saker-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">
          {q.pending} 条 · {fmtBytes(q.bytes)}
        </span>
      </div>
      <div className="h-1 overflow-hidden rounded-full bg-surface-2" title={`容量占用 ${Math.round(load * 100)}%`}>
        <div
          className={pressure ? 'h-full bg-warning' : 'h-full bg-info/60'}
          style={{ width: `${Math.max(2, Math.round(load * 100))}%` }}
        />
      </div>
      <div className="flex items-center gap-2 font-mono text-[11px] text-ink-3">
        <span title="路由检查点（日志序号）不是已读回执">cursor {q.cursor}</span>
        {q.oldest && <span title={q.oldest}>最早 {fmtTime(q.oldest).slice(5)}</span>}
      </div>
    </div>
  )
}

function NativeRecords({ people }: { people: Participant[] }) {
  // This dialog is the whole-instance audit; the project-scoped projection of
  // the same records lives in the conversation timeline above.
  const observed = usePoll(api.instanceIOAObservations, 4000)
  const records = observed.data?.records ?? []
  const sessions = observed.data?.sessions ?? {}
  const maps = useMemo(() => buildRecordMaps(sessions, people, records), [sessions, people, records])
  const freshRecordKeys = useFreshKeys(records, nativeRecordKey)
  return <div className="flex min-h-0 flex-1 flex-col">
    <div className="shrink-0 space-y-1.5 border-b border-hairline px-3 py-2 text-[11px] leading-5 text-ink-3">
      <p>
        实例最近 500 条观察，包含其他项目和未关联会话 · {records.length} 条 · 观察到消息不代表对方已读。
        本项目的消息已在对话页按 <code>message_id</code> 对齐；这里是跨项目的原始审计。
      </p>
      <details className="rounded-md border border-hairline bg-surface-2/40 px-2.5 py-1.5">
        <summary className="cursor-pointer select-none text-ink-2 hover:text-ink">这些记录怎么读？</summary>
        <ul className="mt-1 space-y-0.5 pl-0.5">
          <li><span className="text-ink-2">一条卡片 = IOA 里的一次投递原始记录</span>；「记录详情」里是 IOA 消息 id、发送节点和原始会话 id。</li>
          <li><span className="text-ink-2">意图</span>：左边是来源、右边是接收方，标题已还原成会话/参与者名称；名字下面一行写明地址来源。显示「控制台直投」表示控制台经 IOA 直接送进对方会话，IOA 记录里没有来源会话，发送方以消息头的 <code>from=</code> 为准，不是丢数据。</li>
          <li><span className="text-ink-2">寻址头</span>：Saker注入的 <code>[collab]</code>（同伴协作）、<code>[消息]</code>（控制台下投）、<code>[机械通知]</code>（账本通知）都已拆成字段，正文从换行之后开始；前面的 <code>[重投]</code> 表示这条是失败运行后重新入队。</li>
          <li><span className="text-ink-2">会话短串</span>（如 <code>9da4e9bb76…</code>）：接收方 harness 会话 id 前 10 位，只用于和「运行」页核对，<em>不是</em>消息编号；消息编号是 <code>m_xxxxxxxx</code>。</li>
        </ul>
      </details>
    </div>
    {observed.error && <div className="px-3 pt-2"><Callout tone="destructive">{observed.error}</Callout></div>}
    {observed.loading && records.length === 0 && <div className="p-2"><SkeletonList count={4} /></div>}
    {records.length === 0 && !observed.loading && !observed.error && (
      <EmptyState
        compact
        title="暂无 IOA 观察"
        description="运行中的原生会话消息会出现在这里；观察到消息不代表对方已读。"
      />
    )}
    <ScrollArea className="min-h-0 flex-1">
      <div role="list" aria-label="IOA 原生记录" className="space-y-2 p-3">
        {records.map((rec, index) => (
          <NativeMessage key={nativeRecordKey(rec)} rec={rec} maps={maps} defer={index >= 8} fresh={freshRecordKeys.has(nativeRecordKey(rec))} />
        ))}
      </div>
    </ScrollArea>
  </div>
}

/** One raw IOA observation, rendered as direction + addressing facts + body. */
function NativeMessage({ rec, maps, defer, fresh }: { rec: IOARecord; maps: RecordMaps; defer: boolean; fresh: boolean }) {
  const env = rec.envelope
  const view = describeRecord(rec, maps)
  const event = env?.event ?? ''
  // "sending" is IOA's in-flight transport state. 中断式投递 was aimed at a turn
  // that was running *at the moment the record was observed*; it is an event,
  // not a state, so the dot marks it only while it is the fresh row instead of
  // turning old history into a permanent marker.
  const live = env?.status === 'sending' || (fresh && Boolean(env?.interrupt))
  const chips: { key: string; node: ReactNode }[] = [
    { key: 'source', node: <span data-ioa-source-note>来源：{view.fromDetail}</span> },
    { key: 'target', node: <span data-ioa-target-note>目标：{view.toDetail}</span> },
  ]
  if (view.header?.redelivered) {
    chips.push({
      key: 'redelivered',
      node: <span title="平台在失败的运行后把这条消息重新入队再投递">重投后投递</span>,
    })
  }
  if (view.header?.noticeFactId) {
    const version = view.header.noticeVersion > 0 ? ` v${view.header.noticeVersion}` : ''
    chips.push({
      key: 'notice',
      node: <span title="账本机械通知：只携带事实指针，不是对话，也不授权执行">事实 <span className="font-mono">{view.header.noticeFactId}</span>{version}（机械通知）</span>,
    })
  }
  if (view.header?.kind) {
    chips.push({
      key: 'kind',
      node: <span title={`消息语义由发送方声明：kind=${view.header.kind}`}>类型 {messageKindLabel(view.header.kind)}</span>,
    })
  }
  if (view.header?.messageId) {
    chips.push({
      key: 'message',
      node: <span className="font-mono" title="项目消息编号；对话页里的账本行是同一个号">{view.header.messageId}</span>,
    })
  }
  if (view.header?.inReplyTo) {
    chips.push({
      key: 'reply',
      node: <span>回复 <span className="font-mono">{view.header.inReplyTo}</span></span>,
    })
  }
  if ((view.header?.facts.length ?? 0) > 0) {
    chips.push({
      key: 'facts',
      node: <span title={(view.header?.facts ?? []).join('\n')}>引用事实 {view.header?.facts.length} 条</span>,
    })
  }
  if (env?.agent?.name) {
    chips.push({ key: 'agent', node: <span title={`子代理 ${env.agent.name}${env.agent.type ? ` · ${env.agent.type}` : ''}`}>子代理 {env.agent.name}</span> })
  }
  if (env?.call_id) {
    chips.push({ key: 'call', node: <span className="font-mono" title="派发调用 id">调用 {env.call_id}</span> })
  }
  return <article role="listitem" data-ioa-seq={rec.seq} data-ioa-event={event} className={`overflow-hidden rounded-lg border border-hairline bg-card/40 ${defer ? '[contain-intrinsic-size:auto_8rem] [content-visibility:auto]' : ''}`}>
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 pt-2.5">
      {/* Only the header dot marks the record: the card is a whole record, not a
          status, and nothing animates a row into place (see app.css). */}
      {live && (
        <span
          aria-hidden="true"
          title={env?.interrupt ? '中断式投递：刚刚打断对方的运行' : '投递中'}
          className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-success"
        />
      )}
      <Badge size="sm" variant={event === 'message' ? 'info' : 'muted'} title="IOA 协议事件类型">{eventLabel(event)}</Badge>
      <span
        data-ioa-endpoint="source"
        title={view.from?.hint}
        className={`min-w-0 text-[12px] font-medium ${view.from ? 'text-info' : 'italic text-ink-3'}`}
      >{view.from?.label ?? '来源未标注'}</span>
      <span className="text-ink-3" aria-hidden>→</span>
      <span
        data-ioa-endpoint="target"
        title={view.to?.hint}
        className={`min-w-0 text-[12px] font-medium ${view.to ? 'text-info' : 'italic text-ink-3'}`}
      >{view.to?.label ?? '目标未标注'}</span>
      {view.sameSession && <Badge size="sm" variant="warning">来源与目标为同一会话</Badge>}
      {env?.interrupt && <Badge size="sm" variant="warning" title="要求中断对方当前模型请求后立即投递">中断式投递</Badge>}
      {env?.status && <Badge size="sm" variant="muted" title="harness 报告的投递状态">{env.status}</Badge>}
      <time className="saker-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3" title={rec.created_at || rec.observed_at}>{fmtTime(rec.created_at || rec.observed_at)}</time>
    </div>
    <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 px-3 pt-1 text-[11px] text-ink-3">
      {chips.map((chip, i) => (
        <span key={chip.key} className="flex items-center gap-1.5">{i > 0 && <span aria-hidden className="text-ink-3/50">·</span>}{chip.node}</span>
      ))}
    </div>
    <div data-ioa-body className="saker-break mx-3 my-2 whitespace-pre-wrap rounded-r-md border-l-2 border-info/50 bg-surface-2/40 px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">{view.body || '（无正文）'}</div>
    <details data-ioa-details className="border-t border-hairline px-3 py-1.5 text-[11px] text-ink-3">
      <summary className="cursor-pointer select-none hover:text-ink">记录详情（原始 id 与时间）</summary>
      <div className="mt-1 space-y-0.5">
        <DetailLine label="IOA 消息" value={rec.ioa_message_id || '—'} hint="IOA 传输层的消息 id，与项目消息编号不同" />
        {env?.id && <DetailLine label="事件 id" value={env.id} hint="应用层事件 id；同一消息的重试复用它，便于去重" />}
        {rec.sender && <DetailLine label="发送节点" value={rec.sender} hint="IOA 节点指纹（控制台/宿主各有一个），不是参与者 id" />}
        {env?.source?.session_id && <DetailLine label="来源地址" value={env.source.session_id} />}
        {env?.target?.session_id && <DetailLine label="目标地址" value={env.target.session_id} />}
        {env?.call_id && <DetailLine label="调用" value={env.call_id} />}
        {rec.created_at && <DetailLine label="创建时间" value={fmtTime(rec.created_at)} />}
        <DetailLine label="观察时间" value={fmtTime(rec.observed_at)} />
      </div>
    </details>
    {rec.error && <div className="px-3 pb-2"><Callout tone="destructive">{rec.error}</Callout></div>}
  </article>
}

function DetailLine({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex gap-2">
      <span className="w-16 shrink-0 text-ink-3/80">{label}</span>
      <span className="saker-break min-w-0 flex-1 font-mono" title={hint ?? value}>{value}</span>
    </div>
  )
}

function ParticipantRow({ p }: { p: Participant }) {
  return (
    <li
      className="mb-0.5 rounded-lg px-2.5 py-2 transition-colors hover:bg-surface-2 focus-within:bg-surface-2/60"
      data-participant-id={p.id}
    >
      <div className="flex items-center gap-2">
        <span
          className={`inline-block h-1.5 w-1.5 shrink-0 rounded-full ${p.contactable ? 'bg-success' : 'bg-ink-3/50'}`}
          title={p.contactable ? '可联系' : '已结束，需显式重开'}
        />
        <span className="min-w-0 truncate text-[12px] font-medium text-ink">{p.title}</span>
        {p.kind === 'commander' && <Badge size="sm" variant="secondary">指挥官</Badge>}
        <span className="saker-nums ml-auto shrink-0 font-mono text-[11px] text-ink-3">#{p.generation ?? 0}</span>
      </div>
      <div className="mt-0.5 truncate font-mono text-[11px] text-ink-3">{p.id}</div>
      <div className="flex flex-wrap gap-1.5 font-mono text-[11px] text-ink-3">
        {p.status ? <span>{p.status}</span> : null}
        {p.profile ? <span className="truncate">· {p.profile}</span> : null}
        {p.model ? <span className="truncate">· {p.model}</span> : null}
      </div>
      {p.note && <div className="mt-0.5 text-[11px] leading-4 text-ink-3">{p.note}</div>}
    </li>
  )
}
