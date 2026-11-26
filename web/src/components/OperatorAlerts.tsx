import { useEffect, useRef, useState } from 'react'
import { Button } from '@cyber/ui'
import { api } from '@/lib/api'
import { navigate } from '@/lib/navigation'
import { readViewMemory, writeViewMemory } from '@/lib/view-memory'
import type { Message } from '@/lib/types'

export function OperatorAlerts({projectID}:{projectID:string}) {
  const scope=`operator-inbox:${projectID}`
  const [unread,setUnread]=useState<Message[]>([])
  const [dismissed,setDismissed]=useState(false)
  const [permission,setPermission]=useState(()=>typeof Notification==='undefined'?'unavailable':Notification.permission)
  const seen=useRef(readViewMemory<number>(scope,'seen')??0)
  const through=useRef(seen.current)
  const notified=useRef(seen.current)
  const pendingRead=useRef(readViewMemory<number>(scope,'pending-read')??0)
  const markRead=()=>{pendingRead.current=Math.max(pendingRead.current,through.current);writeViewMemory(scope,'pending-read',pendingRead.current);seen.current=through.current;writeViewMemory(scope,'seen',seen.current);setUnread([]);setDismissed(false)}
  useEffect(()=>{
    let active=true,busy=false
    const poll=async()=>{
      if(busy)return;busy=true
      try{
        if(pendingRead.current>0){const cursor=pendingRead.current;await api.operatorMessagesSeen(cursor);if(pendingRead.current===cursor){pendingRead.current=0;writeViewMemory(scope,'pending-read',0)}}
        const incoming:Message[]=[]
        let cursor=through.current
        for(let page=0;page<20;page++){
          const result=await api.operatorMessages(cursor)
          if(!active)return
          incoming.push(...result.messages.filter(m=>m.status!=='confirmed'));cursor=result.next
          if(!result.more)break
        }
        through.current=cursor
        if(!incoming.length)return
        const visible=new URLSearchParams(window.location.search).get('view')==='messages'&&!document.hidden
        if(visible){markRead();return}
        setUnread(current=>[...current,...incoming.filter(m=>m.seq>seen.current&&!current.some(old=>old.id===m.id))])
        const latest=incoming[incoming.length-1]!
        if(latest.seq>notified.current){
          notified.current=latest.seq;setDismissed(false)
          if(typeof Notification!=='undefined'&&Notification.permission==='granted'&&document.hidden){const n=new Notification('Saker：收到操作员消息',{body:latest.text.slice(0,140),tag:`saker:${projectID}`});n.onclick=()=>{window.focus();navigate(`${window.location.pathname}?view=messages`);n.close()}}
        }
      }catch{/* Keep unread state; the next poll retries the same cursor. */}finally{busy=false}
    }
    void poll();const timer=window.setInterval(()=>{if(document.visibilityState!=='hidden')void poll()},4000)
    // The inbox is one fetch plus up to 20 pages; a background tab does not need
    // it. Coming back to the tab refreshes immediately so nothing is missed.
    const onVisible=()=>{if(document.visibilityState==='visible')void poll()}
    window.addEventListener('visibilitychange',onVisible)
    window.addEventListener('saker:operator-messages-read',markRead)
    return()=>{active=false;window.clearInterval(timer);window.removeEventListener('visibilitychange',onVisible);window.removeEventListener('saker:operator-messages-read',markRead)}
  },[projectID])
  useEffect(()=>{
    const decorate=()=>{const base=document.title.replace(/^\(\d+\) /,'');const next=unread.length?`(${unread.length}) ${base}`:base;if(document.title!==next)document.title=next}
    decorate();const observer=new MutationObserver(decorate);observer.observe(document.querySelector('title')??document.head,{childList:true,subtree:true,characterData:true})
    return()=>{observer.disconnect();document.title=document.title.replace(/^\(\d+\) /,'')}
  },[unread.length])
  if(!unread.length||dismissed)return null
  return <aside role="status" aria-live="polite" aria-atomic="true" style={{ bottom: 'max(1rem, env(safe-area-inset-bottom, 0px))', right: 'max(1rem, env(safe-area-inset-right, 0px))' }} className="fixed z-50 max-w-sm rounded-xl border border-primary/30 bg-card p-4 shadow-elevated">
    <p className="text-sm font-medium">收到 {unread.length} 条发给操作员的消息</p><p className="my-2 line-clamp-3 whitespace-pre-wrap text-xs text-ink-3">{unread[unread.length-1]?.text}</p>
    <div className="flex flex-wrap gap-2"><Button size="xs" onClick={()=>{navigate(`${window.location.pathname}?view=messages`);markRead()}}>打开通信页</Button><Button size="xs" variant="ghost" aria-label="收起提醒，稍后仍可在通信页查看" onClick={()=>setDismissed(true)}>收起提醒</Button>
      {permission==='default'&&<Button size="xs" variant="ghost" onClick={()=>void Notification.requestPermission().then(setPermission)}>开启桌面提醒</Button>}
    </div>
  </aside>
}
