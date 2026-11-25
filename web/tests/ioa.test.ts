import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildRecordMaps,
  describeRecord,
  eventLabel,
  messageKindLabel,
  parsePeerMessage,
  parseRecordText,
  participantName,
  resolveEndpoint,
  shortAddress,
} from '../src/lib/ioa.ts'
import type { IOARecord, Participant } from '../src/lib/types.ts'

// The fixtures below mirror the shapes a portal run produces: the
// commander talks through the control plane (empty envelope source, `[collab]`
// prelude), while intents answer through their own harness session and the
// reply's IOA target is the control plane's node.
const people: Participant[] = [
  { id: 'commander', kind: 'commander', title: '指挥官', contactable: true },
  { id: 'pt_in_a484fc1d', kind: 'intent', intent_id: 'in_a484fc1d', title: '侦察与资产测绘：portal.example', status: 'done', contactable: false },
  { id: 'pt_in_fb5e6d72', kind: 'intent', intent_id: 'in_fb5e6d72', title: '应用层测试：portal.example 主站与接口', status: 'running', contactable: true },
]

const sessions: Record<string, string> = {
  '194adc7aca2b87015f6d4c5c472b242e': '指挥官',
  '9da4e9bb76501b6b364c7345664e6c5a': '侦察与资产测绘：portal.example',
  '9adf25f634fdc8d84221061002ba38e1': '应用层测试：portal.example 主站与接口',
  'b27ae7b3422dfd93c2e4dbe601af5293': '组件漏洞排查：portal.example',
}

const commanderSay: IOARecord = {
  seq: 1,
  ioa_message_id: '575284f6c09bcc91ce10a4d9c292fc26',
  sender: 'cd3ecb7f657f17e27aef96ff92cf9312',
  created_at: '2026-11-11T07:09:47.8056262Z',
  observed_at: '2026-11-11T07:09:49.812748Z',
  envelope: {
    event: 'message',
    id: 'm_20f911f4',
    source: {},
    target: { session_id: '9da4e9bb76501b6b364c7345664e6c5a' },
  },
  content: {
    text: '[collab] from=commander to=pt_in_a484fc1d kind=say message_id=m_20f911f4\n指挥官现场提示（已亲自验证）：目标可达。HTTP GET 返回 200。',
  },
}

const workerReport: IOARecord = {
  seq: 14,
  ioa_message_id: 'a1b2c3',
  sender: '28cd3cd7aa11bb22cc33dd44ee55ff66',
  created_at: '2026-11-11T07:20:00Z',
  observed_at: '2026-11-11T07:20:01Z',
  envelope: {
    event: 'message',
    source: { session_id: 'b27ae7b3422dfd93c2e4dbe601af5293' },
    target: { session_id: 'cd3ecb7f657f17e27aef96ff92cf9312' },
  },
  content: { text: '本轮已交付 11 条 facts。要点：示例站点 CMS + nginx。' },
}

const participantAddressed: IOARecord = {
  seq: 6,
  ioa_message_id: 'f1e2d3',
  sender: '28cd3cd7aa11bb22cc33dd44ee55ff66',
  observed_at: '2026-11-11T07:15:00Z',
  envelope: {
    event: 'message',
    source: { session_id: 'b27ae7b3422dfd93c2e4dbe601af5293' },
    target: { session_id: 'pt_in_fb5e6d72' },
  },
  content: { text: '交叉情报（组件/敏感信息意图，已实测）。' },
}

const records = [commanderSay, workerReport, participantAddressed]
const maps = buildRecordMaps(sessions, people, records)

test('a [collab] prelude is split off the body, never shown as prose', () => {
  const parsed = parsePeerMessage(commanderSay.content!.text as string)
  assert.deepEqual(parsed.peer, {
    from: 'commander',
    to: 'pt_in_a484fc1d',
    kind: 'say',
    messageId: 'm_20f911f4',
    inReplyTo: '',
    facts: [],
  })
  assert.equal(parsed.body, '指挥官现场提示（已亲自验证）：目标可达。HTTP GET 返回 200。')
})

test('reply id and fact refs are parsed out of the prelude', () => {
  const parsed = parsePeerMessage('[collab] from=commander to=pt_in_x kind=result message_id=m_9 in_reply_to=m_1 facts=f_a,f_b\n正文\n第二行')
  assert.equal(parsed.peer?.inReplyTo, 'm_1')
  assert.deepEqual(parsed.peer?.facts, ['f_a', 'f_b'])
  assert.equal(parsed.body, '正文\n第二行')
})

test('CRLF, missing kind and a prelude without a body stay parseable', () => {
  assert.equal(parsePeerMessage('[collab] from=a to=b\r\nbody').body, 'body')
  assert.equal(parsePeerMessage('[collab] from=a to=b\r\nbody').peer?.kind, '')
  const empty = parsePeerMessage('[collab] from=a to=b')
  assert.equal(empty.peer?.messageId, '')
  assert.equal(empty.body, '')
})

test('foreign or malformed markers keep the text intact', () => {
  const plain = parsePeerMessage('普通汇报，没有协作头。')
  assert.equal(plain.peer, null)
  assert.equal(plain.body, '普通汇报，没有协作头。')
  const foreign = parsePeerMessage('[collab]garbage\nstill a body')
  assert.equal(foreign.peer, null)
  assert.equal(foreign.body, '[collab]garbage\nstill a body')
  const otherKeys = parsePeerMessage('[collab] foo=bar\nstill a body')
  assert.equal(otherKeys.peer, null)
  assert.equal(otherKeys.body, '[collab] foo=bar\nstill a body')
})

test('the commander message resolves to 指挥官 and its session, not to 未提供地址', () => {
  const view = describeRecord(commanderSay, maps)
  assert.equal(view.from?.label, '指挥官')
  assert.equal(view.from?.kind, 'commander')
  assert.match(view.fromDetail, /控制台直投/)
  assert.match(view.fromDetail, /from=commander/)
  assert.equal(view.to?.label, '侦察与资产测绘：portal.example')
  assert.equal(view.toDetail, '会话 9da4e9bb76…')
  assert.equal(view.peer?.kind, 'say')
  assert.equal(view.peer?.messageId, 'm_20f911f4')
})

test('a worker report shows its source session and keeps the reply target as a transport node', () => {
  const view = describeRecord(workerReport, maps)
  assert.equal(view.from?.label, '组件漏洞排查：portal.example')
  assert.equal(view.fromDetail, '会话 b27ae7b342…')
  assert.equal(view.to?.label, 'IOA 转接节点')
  assert.equal(view.to?.kind, 'control')
  assert.equal(view.toDetail, 'IOA 节点 cd3ecb7f65… · 最终接收者未确认')
  assert.equal(view.peer, null)
  assert.equal(view.body, '本轮已交付 11 条 facts。要点：示例站点 CMS + nginx。')
})

test('a raw message is marked self-addressed only when the full session addresses match', () => {
  const sessionID = 'b27ae7b3422dfd93c2e4dbe601af5293'
  const otherSessionID = 'b27ae7b3422dfd93c2e4dbe601af5294'
  const labels = { ...maps, sessions: { ...sessions, [otherSessionID]: sessions[sessionID] } }
  const message = { ...workerReport, envelope: { event: 'message', source: { session_id: sessionID }, target: { session_id: sessionID } } }

  assert.equal(describeRecord(message, labels).sameSession, true)
  assert.equal(describeRecord({ ...message, envelope: { ...message.envelope, target: { session_id: otherSessionID } } }, labels).sameSession, false)
  assert.equal(describeRecord({ ...message, envelope: { ...message.envelope, target: { session_id: sessionID, node_id: 'other-node' } } }, labels).sameSession, false)
  assert.equal(describeRecord({ ...message, envelope: { ...message.envelope, event: 'inbox.route' } }, labels).sameSession, false)
  assert.equal(describeRecord(commanderSay, labels).sameSession, false)
})

test('a participant id used as the IOA target resolves through the participant list', () => {
  const view = describeRecord(participantAddressed, maps)
  assert.equal(view.to?.label, '应用层测试：portal.example 主站与接口')
  assert.equal(view.toDetail, '参与者 pt_in_fb5e6d72')
})

test('missing addresses say so instead of inventing a name', () => {
  const bare: IOARecord = {
    seq: 99,
    ioa_message_id: 'x',
    observed_at: '2026-11-11T07:00:00Z',
    envelope: { event: 'inbox.route' },
    content: { text: '生命周期记录' },
  }
  const view = describeRecord(bare, maps)
  assert.equal(view.from, null)
  assert.match(view.fromDetail, /^未标注/)
  assert.equal(view.to, null)
  assert.match(view.toDetail, /^未标注/)
})

test('an unknown 32-hex address stays visibly unregistered', () => {
  const endpoint = resolveEndpoint('ffffffffffffffffffffffffffffffff', maps)
  assert.equal(endpoint.kind, 'unknown')
  assert.equal(endpoint.label, '未命名会话')
  assert.match(endpoint.hint, /不在当前项目的会话标签/)
})

test('native direction addresses use the project participant title without a message header', () => {
  const target = { session_id: 'in_fb5e6d72' }
  const view = describeRecord({ ...participantAddressed, envelope: { ...participantAddressed.envelope, target } }, maps)
  assert.equal(view.header, null)
  assert.equal(view.to?.kind, 'intent')
  assert.equal(view.to?.id, target.session_id)
  assert.equal(view.to?.label, people[2].title)
  assert.equal(view.toDetail, `意图 ${target.session_id}`)

  const missing = resolveEndpoint('in_lw7icmai2k', maps)
  assert.equal(missing.kind, 'intent')
  assert.equal(missing.label, '未登记意图')
  assert.equal(missing.short, 'in_lw7icmai2k')
})

test('commander, operator and participant ids resolve without a session label', () => {
  assert.equal(resolveEndpoint('commander', maps).label, '指挥官')
  assert.equal(resolveEndpoint('operator', maps).label, '操作员')
  assert.equal(resolveEndpoint('pt_in_a484fc1d', maps).kind, 'participant')
  assert.equal(resolveEndpoint('9da4e9bb76501b6b364c7345664e6c5a', maps).kind, 'session')
  assert.equal(resolveEndpoint('194adc7aca2b87015f6d4c5c472b242e', maps).kind, 'commander')
})

test('the control-node set comes from peer messages, not from any sender', () => {
  const onlyReports = buildRecordMaps(sessions, people, [workerReport])
  assert.equal(onlyReports.controlNodes.size, 0)
  assert.equal(resolveEndpoint('cd3ecb7f657f17e27aef96ff92cf9312', onlyReports).label, '未命名会话')
  assert.equal(maps.controlNodes.has('cd3ecb7f657f17e27aef96ff92cf9312'), true)
})

test('labels and short ids stay honest', () => {
  assert.equal(shortAddress('9da4e9bb76501b6b364c7345664e6c5a'), '9da4e9bb76…')
  assert.equal(shortAddress('abc'), 'abc')
  assert.equal(participantName('pt_in_a484fc1d', people), '侦察与资产测绘：portal.example')
  assert.equal(participantName('pt_in_unknown', people), 'pt_in_unknown')
  assert.equal(participantName('commander', people), '指挥官')
  assert.equal(participantName('operator', people), '操作员')
  assert.equal(eventLabel('message'), '消息')
  assert.equal(eventLabel('subagent.delegate'), '子代理派发')
  assert.equal(eventLabel(''), '协议记录')
  assert.equal(eventLabel('custom.event'), 'custom.event')
  assert.equal(messageKindLabel('say'), '现场发言')
  assert.equal(messageKindLabel('question'), '提问')
  // Proposal-chain and operator-request messages are named instead of being
  // rendered as raw protocol tokens.
  assert.equal(messageKindLabel('proposal'), '新意图提议')
  assert.equal(messageKindLabel('proposal_approved'), '提议已批准')
  assert.equal(messageKindLabel('proposal_rejected'), '提议已驳回')
  assert.equal(messageKindLabel('proposal_dispatched'), '提议已派发')
  assert.equal(messageKindLabel('proposal_withdrawn'), '提议已撤回')
  assert.equal(messageKindLabel('operator_request_resolved'), '请求处理结果')
  assert.equal(messageKindLabel('weird'), 'weird')
})

// ---- console-delivered headers -------------------------------------------
// The raw view used to parse only `[collab]`. A console delivery is formatted by
// mailbox.go as `[消息] from=…（origin note） kind=… message_id=…`, and a notice as
// `[机械通知] fact=… vN message_id=…`; without parsing them the sender and the
// ledger message id stayed inside the prose.
const consoleDelivery: IOARecord = {
  seq: 20,
  ioa_message_id: 'c0ffee',
  sender: 'cd3ecb7f657f17e27aef96ff92cf9312',
  created_at: '2026-11-11T07:30:00Z',
  observed_at: '2026-11-11T07:30:01Z',
  envelope: {
    event: 'message',
    source: {},
    target: { session_id: '9da4e9bb76501b6b364c7345664e6c5a' },
  },
  content: {
    text: '[消息] from=operator（操作员/控制台令牌，不是指挥官） kind=say message_id=m_30 in_reply_to=m_29 facts=f_1,f_2\n控制台提示正文。',
  },
}

const noticeRecord: IOARecord = {
  seq: 21,
  ioa_message_id: 'beef01',
  sender: 'cd3ecb7f657f17e27aef96ff92cf9312',
  observed_at: '2026-11-11T07:31:00Z',
  envelope: { event: 'message', source: {}, target: { session_id: 'pt_in_fb5e6d72' } },
  content: { text: '[机械通知] fact=f_9 v3 message_id=m_32\n（按事实 ID 读取当前状态；不是对话，不授权执行）' },
}

test('a console [消息] header is split into fields, note and body', () => {
  const parsed = parseRecordText(consoleDelivery.content!.text as string)
  assert.equal(parsed.header?.form, 'inbox')
  // The origin note is bracketed after from=; it must not become part of the id.
  assert.equal(parsed.header?.from, 'operator')
  assert.equal(parsed.header?.kind, 'say')
  assert.equal(parsed.header?.messageId, 'm_30')
  assert.equal(parsed.header?.inReplyTo, 'm_29')
  assert.deepEqual(parsed.header?.facts, ['f_1', 'f_2'])
  assert.equal(parsed.body, '控制台提示正文。')
})

test('a [机械通知] header keeps its fact pointer and version', () => {
  const parsed = parseRecordText(noticeRecord.content!.text as string)
  assert.equal(parsed.header?.form, 'notice')
  assert.equal(parsed.header?.noticeFactId, 'f_9')
  assert.equal(parsed.header?.noticeVersion, 3)
  assert.equal(parsed.header?.messageId, 'm_32')
  assert.equal(parsed.header?.from, '')
  assert.equal(parsed.body, '（按事实 ID 读取当前状态；不是对话，不授权执行）')
})

test('the [重投] marker is read before the header, not shown as body', () => {
  const parsed = parseRecordText('[重投] [消息] from=commander（本项目指挥官模型） kind=question message_id=m_31\n重投正文')
  assert.equal(parsed.header?.form, 'inbox')
  assert.equal(parsed.header?.from, 'commander')
  assert.equal(parsed.header?.redelivered, true)
  assert.equal(parsed.body, '重投正文')
})

test('unrecognised headers and plain text stay verbatim', () => {
  const foreign = parseRecordText('[消息] foo=bar\nstill a body')
  assert.equal(foreign.header, null)
  assert.equal(foreign.body, '[消息] foo=bar\nstill a body')
  const plain = parseRecordText('普通汇报，没有寻址头。')
  assert.equal(plain.header, null)
  assert.equal(plain.body, '普通汇报，没有寻址头。')
})

test('a console delivery resolves its sender from the header instead of 未标注', () => {
  const view = describeRecord(consoleDelivery, buildRecordMaps(sessions, people, [consoleDelivery]))
  assert.equal(view.peer, null, 'a console delivery is not a [collab] peer message')
  assert.equal(view.header?.messageId, 'm_30')
  assert.equal(view.from?.label, '操作员')
  assert.match(view.fromDetail, /控制台直投/)
  assert.match(view.fromDetail, /from=operator/)
  assert.equal(view.body, '控制台提示正文。')
})

test('a mechanical notice does not invent a sender', () => {
  const view = describeRecord(noticeRecord, buildRecordMaps(sessions, people, [noticeRecord]))
  assert.equal(view.from, null)
  assert.match(view.fromDetail, /账本机械通知/)
  assert.equal(view.to?.label, '应用层测试：portal.example 主站与接口')
  assert.equal(view.header?.noticeFactId, 'f_9')
})

test('the control-node set includes the console-delivered and notice forms', () => {
  const onlyConsole = buildRecordMaps(sessions, people, [consoleDelivery])
  assert.equal(onlyConsole.controlNodes.has(consoleDelivery.sender as string), true)
  const onlyNotice = buildRecordMaps(sessions, people, [noticeRecord])
  assert.equal(onlyNotice.controlNodes.has(noticeRecord.sender as string), true)
})
