import { test } from 'node:test'
import assert from 'node:assert/strict'
import { groupRunEvents, eventArguments, eventOutcome, runEventMetrics } from '../src/lib/run-events.ts'
import type { Event } from '../src/lib/types.ts'

test('tool outcomes are three-way: explicit false fails, a missing ok is unknown',()=>{
 const events:Event[]=[
  {kind:'tool_result',tool:'bash',ok:true,at:'2026-01-01T00:00:01Z'},
  {kind:'tool_result',tool:'bash',ok:false,at:'2026-01-01T00:00:02Z'},
  // 缺 bit 不是当前形态：既不算成功也不算失败（后端 NotOK() 同口径）。
  {kind:'tool_result',tool:'bash',at:'2026-01-01T00:00:03Z'},
 ]
 assert.deepEqual(events.map(eventOutcome),['ok','failed','unknown'])
 const metrics=runEventMetrics(events)
 assert.equal(metrics.completed,3)
 assert.equal(metrics.failures,1,'only the explicit false counts as a failure')
 assert.equal(metrics.unknown,1)
})

test('tool results pair by call id and cannot leak across turns',()=>{
 const events:Event[]=[
  {kind:'turn_started',at:'2026-01-01T00:00:00Z'},
  {kind:'tool_call',tool:'read',call_id:'a',at:'2026-01-01T00:00:01Z'},
  {kind:'tool_call',tool:'bash',call_id:'b',at:'2026-01-01T00:00:02Z'},
  {kind:'tool_result',tool:'bash',call_id:'b',ok:false,at:'2026-01-01T00:00:03Z'},
  {kind:'tool_result',tool:'read',call_id:'a',ok:true,at:'2026-01-01T00:00:04Z'},
  {kind:'turn_started',at:'2026-01-01T00:00:05Z'},
  {kind:'tool_result',tool:'read',call_id:'a',ok:true,at:'2026-01-01T00:00:06Z'},
 ]
 const groups=groupRunEvents(events)
 assert.deepEqual(groups[1].indices,[1,4]);assert.deepEqual(groups[2].indices,[2,3])
 assert.equal(groups[4].turn,2);assert.deepEqual(groups[4].indices,[6])
 assert.equal(runEventMetrics(events).failures,1)
})

test('a normal turn ending stays out of the anomaly filter', () => {
  // Legacy events used end_turn for normal completion. An explicit harness
  // error still wins over a completion reason, and cancellation stays visible.
  const end: Event = { kind: 'turn_ended', at: '' }
  assert.equal(eventOutcome({ ...end, stop_reason: 'end_turn' }), 'ok')
  assert.equal(eventOutcome({ ...end, stop_reason: 'completed', ok: true }), 'ok')
  assert.equal(eventOutcome({ ...end, stop_reason: 'completed', ok: false }), 'failed')
  assert.equal(eventOutcome({ ...end, stop_reason: 'canceled' }), 'failed')
})

test('plaintext JSON arguments parse into an object, Unicode included',()=>{
 const value={command:'echo 测试',timeout:30}
 const raw={tool_call:{arguments:JSON.stringify(value)}}
 assert.deepEqual(eventArguments({kind:'tool_call',at:'',raw}),value)
 assert.match(JSON.stringify(eventArguments({kind:'tool_call',at:'',raw}),null,2),/\n/, 'pretty-printed, not a single escaped line')
})

test('a row whose arguments field is missing still shows the raw envelope',()=>{
 const raw={tool_call:{id:'c1'}}
 assert.deepEqual(eventArguments({kind:'tool_call',at:'',raw}),raw)
})

test('non-JSON plaintext arguments survive untouched',()=>{
 const raw={tool_call:{arguments:'not json at all'}}
 assert.equal(eventArguments({kind:'tool_call',at:'',raw}),'not json at all')
})

test('results pair by call id only, and repeated timestamps still get distinct keys',()=>{
 const events:Event[]=[
  {kind:'tool_call',at:'2026-01-01T00:00:01Z',tool:'bash',raw:{tool_call:{id:'a'}}},
  {kind:'tool_call',at:'2026-01-01T00:00:01Z',tool:'bash',raw:{tool_call:{id:'b'}}},
  {kind:'tool_result',at:'2026-01-01T00:00:02Z',raw:{tool_result:{call_id:'b'}}},
  {kind:'tool_result',at:'2026-01-01T00:00:03Z',raw:{tool_result:{call_id:'a'}}},
  // 没有 call id 的结果身份不明：单独成行，不按出现顺序挂到 A 或 B 上。
  {kind:'tool_result',at:'2026-01-01T00:00:04Z',tool:'bash'},
  {kind:'status',at:'2026-01-01T00:00:04Z'},
  {kind:'status',at:'2026-01-01T00:00:04Z'},
 ]
 const groups=groupRunEvents(events)
 assert.deepEqual(groups[0].indices,[0,3]);assert.deepEqual(groups[1].indices,[1,2])
 assert.deepEqual(groups[2].indices,[4])
 assert.equal(new Set(groups.map(g=>g.key)).size,groups.length)
})
