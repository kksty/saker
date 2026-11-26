import { useState } from 'react'
import { Link2 } from 'lucide-react'
import { Badge } from '@cyber/ui'
import { evidenceGapReason } from '@/lib/alignment'
import { fmtTime } from '@/lib/format'
import { describeRecord, eventLabel, messageKindLabel, rawRecordKey, shortAddress, type RecordMaps } from '@/lib/ioa'
import type { IOARecord, Message } from '@/lib/types'

/** The aligned transport projection attached to one ledger row. */
export interface EvidenceContext {
  records: readonly IOARecord[]
  maps: RecordMaps
  fresh: ReadonlySet<string>
}

/**
 * The aligned transport projection under one ledger row: it links the message
 * the ledger recorded to the IOA delivery the observer saw, by message id and
 * IOA delivery id (lib/alignment). A row without an observation says why
 * instead of looking broken — queued and prompt-carried messages never produce
 * one, and a delivered one whose record rotated out of the observer window is
 * still visible as a ledger row.
 */
export function DeliveryEvidence({ message, records, maps, fresh }: { message: Message } & EvidenceContext) {
  const [open, setOpen] = useState(false)
  const observed = records.length > 0
  const first = records[0]
  const label = observed
    ? `IOA ${records.length > 1 ? `×${records.length}` : ''} ${shortAddress(first.ioa_message_id)} · 观察 ${fmtTime(first.observed_at).slice(6)}`
    : evidenceGapReason(message)
  return (
    <div className="mt-1 w-full">
      {observed ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((current) => !current)}
          title="账本消息与 IOA 原始观察按消息编号对齐；展开看线上实际收到的字节"
          className="flex w-full items-center gap-1.5 rounded-md border border-hairline bg-surface-2/25 px-2 py-1 text-left text-[10.5px] leading-4 text-ink-3 transition-colors hover:bg-surface-2/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <Link2 className="h-3 w-3 shrink-0" />
          <span className="shrink-0 font-medium text-ink-2">原始投递</span>
          <span className="saker-nums min-w-0 truncate font-mono text-ink-2">{label}</span>
          <span className="ml-auto shrink-0 text-primary">{open ? '收起' : '展开'}</span>
        </button>
      ) : (
        <p className="flex items-center gap-1.5 rounded-md border border-dashed border-hairline px-2 py-1 text-[10.5px] leading-4 text-ink-3">
          <Link2 className="h-3 w-3 shrink-0" aria-hidden="true" />
          <span className="shrink-0 font-medium text-ink-2">原始投递</span>
          <span className="min-w-0 truncate">{label}</span>
        </p>
      )}
      {open && observed && (
        <div className="mt-1 space-y-1.5 border-l-2 border-hairline pl-2">
          {records.map((record) => (
            <ObservedDelivery key={rawRecordKey(record)} record={record} maps={maps} fresh={fresh} />
          ))}
          <p className="text-[10px] leading-4 text-ink-3/80">
            账本消息 <span className="font-mono">{message.id}</span> · 投递路径{' '}
            {message.delivery_path === 'ioa' ? '运行中注入' : (message.delivery_path ?? '未记录')} · 观察到投递不代表对方已读
          </p>
        </div>
      )}
    </div>
  )
}

/** One raw observation as inline evidence under its ledger row. */
export function ObservedDelivery({ record, maps, fresh }: { record: IOARecord; maps: RecordMaps; fresh: ReadonlySet<string> }) {
  const view = describeRecord(record, maps)
  const event = record.envelope?.event ?? ''
  const header = view.header
  const live = record.envelope?.status === 'sending' || (fresh.has(rawRecordKey(record)) && Boolean(record.envelope?.interrupt))
  return (
    <div className="rounded-md border border-hairline bg-card/40 px-2 py-1.5 text-[10.5px] leading-4">
      <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-ink-3">
        {live && <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-success" />}
        {record.envelope?.interrupt && <Badge size="sm" variant="warning" title="要求中断对方当前模型请求后立即投递">中断式投递</Badge>}
        {header?.redelivered && <Badge size="sm" variant="warning" title="平台在失败运行后重新入队">重投</Badge>}
        <Badge size="sm" variant={event === 'message' ? 'info' : 'muted'} title="IOA 协议事件类型">{eventLabel(event)}</Badge>
        {view.sameSession && <Badge size="sm" variant="warning">来源与目标为同一会话</Badge>}
        {header?.kind && <span title="消息语义由发送方声明">{messageKindLabel(header.kind)}</span>}
        <span className="ml-auto shrink-0 font-mono" title="IOA 传输层的消息 id，与项目消息编号不同">IOA {shortAddress(record.ioa_message_id)}</span>
        {record.sender && (
          <span className="shrink-0 font-mono" title="IOA 发送节点指纹，不是参与者 id">节点 {shortAddress(record.sender, 8)}</span>
        )}
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-1 text-ink-3">
        <span title={view.from?.hint} className={view.from ? 'font-medium text-ink-2' : 'italic'}>
          {view.from?.label ?? '来源未标注'}
        </span>
        <span aria-hidden="true">→</span>
        <span title={view.to?.hint} className={view.to ? 'font-medium text-ink-2' : 'italic'}>
          {view.to?.label ?? '目标未标注'}
        </span>
        <span className="text-ink-3/70">（{view.fromDetail} → {view.toDetail}）</span>
      </div>
      {record.error && <p className="mt-0.5 text-destructive">{record.error}</p>}
      <p className="saker-break mt-1 whitespace-pre-wrap rounded bg-surface-2/40 px-1.5 py-1 text-[11px] leading-relaxed text-ink-2">
        {view.body || '（无正文）'}
      </p>
    </div>
  )
}
