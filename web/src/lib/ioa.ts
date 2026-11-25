// Reading the console's native IOA conversation.
//
// A native observation is raw harness correspondence: the envelope says which
// address delivered to which address, and a peer message carries a `[collab]`
// prelude in front of its text so the receiving model knows who is speaking,
// how to reply and which ledger row it belongs to. The console must not show
// that prelude as prose: it is addressing metadata, the sentence after the
// newline is the body.
//
// Addresses are resolved against three maps, in this order: harness sessions
// (labelled with the intent title), project participants (commander /
// pt_in_… ids) and the control plane's own IOA nodes. Anything left over is an
// unregistered address and stays visibly unregistered instead of being dressed
// up as a name.

import type { IOARecord, Participant } from './types'

export interface PeerPrelude {
  from: string
  to: string
  kind: string
  messageId: string
  inReplyTo: string
  facts: string[]
}

// Every addressing line the control plane puts in front of injected bytes, in
// the form the receiver's model sees it. They are all one line followed by the
// body:
//
//   [collab] from=… to=… kind=… message_id=…     (harness peer messages)
//   [消息] from=… kind=… message_id=…            (console delivery; mailbox.go
//                                                formatInboxMessage, which puts
//                                                the sender's origin note in
//                                                full-width parens after from=)
//   [机械通知] fact=… vN message_id=…            (ledger notice, no sender)
//
// A row the control plane put back on the queue is prefixed with `[重投] `.
const COLLAB_PREFIX = '[collab]'
const INBOX_PREFIX = '[消息]'
const NOTICE_PREFIX = '[机械通知]'
const REDELIVERY_PREFIX = '[重投] '

/** The addressing form a record carries; `notice` has no sender. */
export type RecordHeaderForm = 'collab' | 'inbox' | 'notice'

/**
 * One parsed addressing header. Unlike `PeerPrelude` (the `[collab]` shape the
 * peer protocol defines), this covers every form the console has to read: the
 * raw view used to parse only `[collab]`, so a console delivery (`[消息]`) or a
 * notice kept its header as prose and lost its sender and message id.
 */
export interface RecordHeader {
  form: RecordHeaderForm
  from: string
  to: string
  kind: string
  messageId: string
  inReplyTo: string
  facts: string[]
  /** `[机械通知]` points at a fact record instead of a sender. */
  noticeFactId: string
  noticeVersion: number
  /** The control plane re-queued the row after a failed run. */
  redelivered: boolean
}

/** `from=`/`to=` may carry a Chinese origin note in full-width parens. */
function stripOriginNote(value: string): string {
  return value.replace(/（[^）]*）$/, '')
}

function scanHeaderFields(line: string): Map<string, string> {
  const fields = new Map<string, string>()
  for (const token of line.split(/[ \t]+/)) {
    const eq = token.indexOf('=')
    if (eq <= 0) continue
    fields.set(token.slice(0, eq), token.slice(eq + 1))
  }
  return fields
}

function buildHeader(form: RecordHeaderForm, line: string, redelivered: boolean): RecordHeader | null {
  const fields = scanHeaderFields(line)
  const header: RecordHeader = {
    form,
    from: '',
    to: '',
    kind: '',
    messageId: fields.get('message_id') ?? '',
    inReplyTo: fields.get('in_reply_to') ?? '',
    facts: (fields.get('facts') ?? '').split(',').map((f) => f.trim()).filter(Boolean),
    noticeFactId: '',
    noticeVersion: 0,
    redelivered,
  }
  if (form === 'notice') {
    header.noticeFactId = fields.get('fact') ?? ''
    const version = /(?:^|[ \t])v(\d+)(?:[ \t]|$)/.exec(line)
    if (version) header.noticeVersion = Number(version[1])
    // A notice without its fact pointer is not ours; keep the text intact.
    if (!header.noticeFactId && !header.messageId) return null
    return header
  }
  header.from = stripOriginNote(fields.get('from') ?? '')
  if (form === 'collab') header.to = stripOriginNote(fields.get('to') ?? '')
  header.kind = fields.get('kind') ?? ''
  // A header with no recognised field is somebody else's marker, not ours;
  // keep the text intact rather than silently eating its first line.
  if (!header.from && !header.to && !header.kind && !header.messageId) return null
  return header
}

/**
 * Split one raw record's text into its addressing header (any of the forms the
 * console writes) and the body the model actually received. Text without a
 * recognised header is returned verbatim.
 */
export function parseRecordText(text: string): { header: RecordHeader | null; body: string } {
  let rest = text
  let redelivered = false
  if (rest.startsWith(REDELIVERY_PREFIX)) {
    redelivered = true
    rest = rest.slice(REDELIVERY_PREFIX.length)
  }
  const form: RecordHeaderForm | '' = rest.startsWith(COLLAB_PREFIX)
    ? 'collab'
    : rest.startsWith(INBOX_PREFIX)
      ? 'inbox'
      : rest.startsWith(NOTICE_PREFIX)
        ? 'notice'
        : ''
  if (!form) return { header: null, body: text }
  const prefix = form === 'collab' ? COLLAB_PREFIX : form === 'inbox' ? INBOX_PREFIX : NOTICE_PREFIX
  const newline = rest.indexOf('\n')
  const line = (newline >= 0 ? rest.slice(0, newline) : rest).slice(prefix.length).replace(/\r$/, '')
  const body = newline >= 0 ? rest.slice(newline + 1) : ''
  const header = buildHeader(form, line.trim(), redelivered)
  if (!header) return { header: null, body: text }
  return { header, body }
}

/** Split a peer message into its `[collab]` prelude and the operator body. */
export function parsePeerMessage(text: string): { peer: PeerPrelude | null; body: string } {
  if (!text.startsWith(COLLAB_PREFIX)) return { peer: null, body: text }
  const { header, body } = parseRecordText(text)
  if (!header || header.form !== 'collab') return { peer: null, body: text }
  const peer: PeerPrelude = {
    from: header.from,
    to: header.to,
    kind: header.kind,
    messageId: header.messageId,
    inReplyTo: header.inReplyTo,
    facts: header.facts,
  }
  return { peer, body }
}

export type EndpointKind = 'session' | 'participant' | 'intent' | 'commander' | 'control' | 'unknown'

/** One side of a record's direction, already resolved to something readable. */
export interface Endpoint {
  id: string
  label: string
  kind: EndpointKind
  /** Short form of the opaque id; never a substitute for the message id. */
  short: string
  /** Tooltip: what this address is and how it was resolved. */
  hint: string
}

export interface RecordMaps {
  sessions: Record<string, string>
  participants: Participant[]
  /** IOA node ids the control plane itself uses to deliver peer messages. */
  controlNodes: ReadonlySet<string>
}

/** Keep every opaque id on screen short but still comparable. */
export function shortAddress(id: string, n = 10): string {
  if (!id) return '—'
	if (/^(?:pt_)?[a-z]+_[a-z2-7]{10}$/.test(id)) return id
  return id.length > n ? `${id.slice(0, n)}…` : id
}

/** Stable identity of one raw IOA record: the application event id, else its
 *  transport id, else the observer sequence. */
export function rawRecordKey(rec: IOARecord): string {
  return rec.envelope?.id || rec.ioa_message_id || String(rec.seq)
}

function sessionEndpoint(id: string, label: string): Endpoint {
  return {
    id,
    label,
    kind: label === '指挥官' ? 'commander' : 'session',
    short: shortAddress(id),
    hint: `harness 会话 ${id}（标签：${label}）`,
  }
}

function participantEndpoint(p: Pick<Participant, 'id' | 'title' | 'kind'>): Endpoint {
  return {
    id: p.id,
    label: p.title,
    kind: p.kind === 'commander' ? 'commander' : 'participant',
    short: shortAddress(p.id),
    hint: p.kind === 'commander' ? '参与者 commander（指挥官）' : `参与者 ${p.id}（意图：${p.title}）`,
  }
}

/**
 * Resolve one address. The order matters: a session label is the most precise
 * (it is the run the bytes landed in), a participant is the human-facing
 * identity. A control node identifies a transport address, not a participant.
 */
export function resolveEndpoint(id: string, maps: RecordMaps): Endpoint {
  const session = maps.sessions[id]
  if (session) return sessionEndpoint(id, session)
  const person = maps.participants.find((p) => p.id === id)
  if (person) return participantEndpoint(person)
  // Native delivery can address the direction directly, before it has a session.
  const direction = maps.participants.find((p) => p.kind === 'intent' && p.intent_id === id)
  if (direction || /^in_(?:[a-z2-7]{10}|[0-9a-f]{8})$/.test(id)) {
    return {
      id,
      label: direction?.title || '未登记意图',
      kind: 'intent',
      short: id,
      hint: direction ? `意图 ${id}（${direction.title}）` : `意图 ${id} 不在当前项目的参与者列表里`,
    }
  }
  if (maps.controlNodes.has(id)) {
    return {
      id,
      label: 'IOA 转接节点',
      kind: 'control',
      short: shortAddress(id),
      hint: `IOA 节点 ${id} 曾出现在系统投递记录中；仅凭节点地址无法确定最终参与者`,
    }
  }
  if (id === 'commander') {
    return { id, label: '指挥官', kind: 'commander', short: 'commander', hint: '参与者 commander；当前会话标签里没有它' }
  }
  if (id === 'operator') {
    return { id, label: '操作员', kind: 'participant', short: 'operator', hint: '操作员（控制台人工输入）' }
  }
  if (id.startsWith('pt_')) {
    return {
      id,
      label: '未登记参与者',
      kind: 'unknown',
      short: shortAddress(id, 12),
      hint: `参与者 ${id} 不在当前项目的参与者列表里（可能已结束或被清理）`,
    }
  }
  return {
    id,
    label: '未命名会话',
    kind: 'unknown',
    short: shortAddress(id),
    hint: `IOA 地址 ${id} 不在当前项目的会话标签或参与者列表里（会话可能已结束或换轮）`,
  }
}

/**
 * Which IOA nodes belong to the control plane? Every injected message is
 * formatted by tianwu before it is sent — peer messages get `[collab]`, console
 * deliveries `[消息]`, ledger notices `[机械通知]` — so a record carrying one of
 * those headers identifies a candidate transport node. The address alone does
 * not say which participant ultimately received a reply.
 */
export function buildRecordMaps(
  sessions: Record<string, string>,
  participants: Participant[],
  records: readonly IOARecord[],
): RecordMaps {
  const controlNodes = new Set<string>()
  for (const rec of records) {
    if (!rec.sender) continue
    const text = typeof rec.content?.text === 'string' ? rec.content.text : ''
    // The prefix check keeps the old behaviour for a malformed `[collab]` (it is
    // still tianwu's marker); the parser adds the two other forms.
    if (parseRecordText(text).header !== null || text.startsWith(COLLAB_PREFIX)) controlNodes.add(rec.sender)
  }
  return { sessions, participants, controlNodes }
}

export interface RecordView {
  /** The `[collab]` prelude, when the record is a peer message. */
  peer: PeerPrelude | null
  /** Any addressing header (`[collab]` / `[消息]` / `[机械通知]`), when present. */
  header: RecordHeader | null
  body: string
  from: Endpoint | null
  to: Endpoint | null
  sameSession: boolean
  /** Readable explanation of the source address, shown as `来源：…`. */
  fromDetail: string
  /** Readable explanation of the target address, shown as `目标：…`. */
  toDetail: string
}

/** One-line operator explanation of a resolved address (`来源：…` / `目标：…`). */
export function endpointDetail(endpoint: Endpoint): string {
  switch (endpoint.kind) {
    case 'session':
      return `会话 ${endpoint.short}`
    case 'commander':
      return endpoint.id === 'commander' ? '参与者 commander' : '指挥官会话'
    case 'participant':
      return `参与者 ${endpoint.id}`
    case 'intent':
      return `意图 ${endpoint.id}`
    case 'control':
      return `IOA 节点 ${endpoint.short}`
    default:
      return `未登记地址 ${endpoint.short}`
  }
}

/**
 * Turn one raw record into the console's display model: readable direction,
 * separated body, and a plain-language note wherever the envelope alone does
 * not answer "who sent this to whom".
 */
export function describeRecord(rec: IOARecord, maps: RecordMaps): RecordView {
  const text = typeof rec.content?.text === 'string' ? rec.content.text : JSON.stringify(rec.content ?? {})
  const { header, body } = parseRecordText(text)
  const peer: PeerPrelude | null =
    header && header.form === 'collab'
      ? {
          from: header.from,
          to: header.to,
          kind: header.kind,
          messageId: header.messageId,
          inReplyTo: header.inReplyTo,
          facts: header.facts,
        }
      : null
  const envelope = rec.envelope
  const sourceID = envelope?.source?.session_id ?? ''
  const targetID = envelope?.target?.session_id ?? ''
  const sourceNode = envelope?.source?.node_id || rec.sender
  const targetNode = envelope?.target?.node_id
  const sameSession = envelope?.event === 'message' && sourceID !== '' && sourceID === targetID
    && (!targetNode || targetNode === sourceNode)

  let from = sourceID ? resolveEndpoint(sourceID, maps) : null
  let fromDetail = from ? endpointDetail(from) : ''
  if (!from && header?.from) {
    from = resolveEndpoint(header.from, maps)
    fromDetail = header.form === 'collab' && header.from !== 'commander'
      ? `控制台直投（from=${header.from}）`
      : `控制台直投（消息头 from=${header.from}）`
    from.hint = `${from.hint}；IOA 记录没有来源会话，发送方由消息头 from= 声明`
  } else if (!from && header?.form === 'notice') {
    // A ledger notice has no sender: it is a record change, not speech.
    fromDetail = '账本机械通知（ledger，无来源会话）'
  } else if (!from) {
    fromDetail = rec.sender ? `未标注（IOA 发送节点 ${shortAddress(rec.sender, 8)}）` : '未标注（IOA 记录没有来源会话）'
  }

  let to = targetID ? resolveEndpoint(targetID, maps) : null
  let toDetail = to ? endpointDetail(to) : ''
  if (to?.kind === 'control') {
    toDetail += ' · 最终接收者未确认'
  }
  if (!to && header?.to) {
    to = resolveEndpoint(header.to, maps)
    toDetail = `控制台直投（消息头 to=${header.to}）`
  } else if (!to) {
    toDetail = '未标注（IOA 记录没有目标会话）'
  }

  return { peer, header, body, from, to, sameSession, fromDetail, toDetail }
}

/** Event kinds are protocol names; the console says what they mean in Chinese. */
export function eventLabel(event: string): string {
  switch (event) {
    case 'message':
      return '消息'
    case 'inbox.route':
      return '收件路由'
    case 'subagent.delegate':
      return '子代理派发'
    case 'subagent.return':
      return '子代理返回'
    case 'task.result':
      return '任务结果'
    case '':
      return '协议记录'
    default:
      return event
  }
}

/** `kind` is sender-declared message semantics, not a role or an enum. */
export function messageKindLabel(kind: string): string {
  switch (kind) {
    case 'instruction':
      return '指令变更'
    case 'say':
      return '现场发言'
    case 'question':
      return '提问'
    case 'update':
      return '进展更新'
    case 'result':
      return '结果回传'
    case 'note':
      return '备注'
    // Proposal chain and the operator-request round-trip are control-plane
    // messages: the console shows what happened instead of the raw kind token.
    case 'proposal':
      return '新意图提议'
    case 'proposal_approved':
      return '提议已批准'
    case 'proposal_rejected':
      return '提议已驳回'
    case 'proposal_dispatched':
      return '提议已派发'
    case 'proposal_withdrawn':
      return '提议已撤回'
    case 'operator_request_resolved':
      return '请求处理结果'
    default:
      return kind
  }
}

/** Human-facing name for a project participant id, with the id as fallback. */
export function participantName(id: string, people: Participant[]): string {
  if (!id) return '—'
  const person = people.find((p) => p.id === id)
  if (person) return person.title
  if (id === 'commander') return '指挥官'
  if (id === 'operator') return '操作员'
  return id
}

/** Who is speaking, for the chat row's identity mark. */
export type ParticipantRole = 'operator' | 'commander' | 'executor'

/**
 * Role of a participant id. The chat avatar must use this instead of the first
 * character of `participantName`: abbreviating 「操作员」 to one glyph prints
 * 「操」, which is a word nobody typed, and a direction title's first glyph says
 * nothing about who is speaking. The id and `kind` are the identity; the title
 * is only a display label.
 */
export function participantRole(id: string, people: Participant[]): ParticipantRole {
  if (id === 'operator') return 'operator'
  if (id === 'commander') return 'commander'
  if (people.find((p) => p.id === id)?.kind === 'commander') return 'commander'
  return 'executor'
}
