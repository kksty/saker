import type { Event } from './types.ts'

export interface EventGroup { key: string; turn: number; indices: number[]; call?: Event; result?: Event }

/**
 * 结果只按 call id 与调用配对（协议必带 `ToolResult.call_id`，见
 * `internal/executor/executor.go:105-108`）。没有 id 的结果单独成行，不按出
 * 现顺序去猜它属于哪一次调用——猜错会把一次失败挂到别人头上。
 */
export function groupRunEvents(events: Event[]): EventGroup[] {
  const groups: EventGroup[]=[]
  let turn=0
  const byCall=new Map<string,EventGroup>()
  const keys=new Map<string,number>()
  events.forEach((event,index)=>{
    if(event.kind==='turn_started'){turn++;byCall.clear()}
    const raw=event.raw as {tool_call?:{id?:string};tool_result?:{call_id?:string}}|undefined
    const callID=event.call_id || raw?.tool_call?.id || raw?.tool_result?.call_id
    if(event.kind==='tool_result'&&callID){
      const group=byCall.get(callID)
      if(group&&!group.result){group.indices.push(index);group.result=event;return}
    }
    const base=event.event_id || `${event.kind}:${event.turn_id??''}:${callID??''}:${event.at||index}`
    const occurrence=keys.get(base)??0;keys.set(base,occurrence+1)
    const group: EventGroup={key:occurrence?`${base}:${occurrence}`:base,turn,indices:[index]}
    if(event.kind==='tool_call'){group.call=event;if(callID)byCall.set(callID,group)}
    groups.push(group)
  })
  return groups
}

export function eventArguments(event: Event): unknown {
  const raw=event.raw as {tool_call?:{arguments?:unknown};arguments?:unknown}|undefined
  const argumentsValue=raw?.tool_call?.arguments ?? raw?.arguments
  // journal 里的工具参数是原文，类型是字符串（写入侧 internal/executor/process.go:298 与
  // internal/aopexec/executor.go:742 都过 executor.PlainToolArguments）。JSON 原样存成
  // 字符串，这里解析回对象，列表里才按缩进显示、不变成一整行转义文本；非 JSON 的媒体
  // 类型解析不了，就原样返回，不丢内容。
  if(typeof argumentsValue==='string'){
    try {return JSON.parse(argumentsValue)}catch{return argumentsValue}
  }
  return argumentsValue ?? event.raw
}

export type EventOutcome = 'ok' | 'failed' | 'unknown'

/**
 * 结果三态。缺 `ok` 既不算成功也不算失败：后端 `executor.NotOK()` 只认显式 `false`，
 * 而新写入的每条 tool_result 都写 `ok`——缺它只意味着这行不是当前形态（旧格式或不
 * 适用），按「未知」显示，不猜成失败。
 */
export function eventOutcome(event: Event): EventOutcome {
  if(event.kind==='error'||event.kind==='warning')return 'failed'
  if(event.kind==='tool_result'){
    if(event.ok===true)return 'ok'
    if(event.ok===false)return 'failed'
    return 'unknown'
  }
  if (event.kind === 'turn_ended') {
    if (event.ok === false) return 'failed'
    const reason = event.stop_reason
    if (reason && !['completed', 'done', 'end_turn'].includes(reason)) return 'failed'
  }
  return 'ok'
}

export function eventFailed(event: Event): boolean {
  return eventOutcome(event)==='failed'
}

export function runEventMetrics(events: Event[]) {
  const completed=events.filter(e=>e.kind==='tool_result')
  const timestamps=events.map(e=>Date.parse(e.at)).filter(Number.isFinite)
  const seconds=timestamps.length>1?(Math.max(...timestamps)-Math.min(...timestamps))/1000:0
  return {events:events.length,perMinute:seconds>0?Math.round(events.length*60/seconds):undefined,completed:completed.length,failures:completed.filter(eventFailed).length,unknown:completed.filter(e=>eventOutcome(e)==='unknown').length}
}
