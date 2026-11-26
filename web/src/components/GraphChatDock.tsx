import { MessageSquare, X } from 'lucide-react'
import { Button } from '@cyber/ui'
import { DockResizeHandle, type DockSize } from './graph/DockResize'

/**
 * The graph's 下发 entry point (the conversation itself lives on the 通信 page).
 * Its width comes from the shared dock size and is clamped by the map's own
 * width (`saker-graph-dock`), not by 100vw: the rail and the dock leave less room
 * than the window reports. The corner and safe-area insets match the other
 * graph overlays so the same edge is where the operator expects it.
 */
export function GraphChatDock({dock,onClose,onOpenConversation}:{dock:DockSize;onClose:()=>void;onOpenConversation:()=>void}) {
  return <div data-graph-chat className="saker-graph-dock saker-graph-dock-right saker-graph-under-toolbar absolute z-20 rounded-xl border border-hairline bg-card p-4 shadow-elevated" style={{width:dock.width}}>
    <DockResizeHandle dock={dock}/>
    <div className="flex items-center gap-2"><MessageSquare className="h-4 w-4"/><span className="saker-section-title">通信</span><Button className="ml-auto" size="icon-sm" variant="ghost" onClick={onClose} aria-label="关闭通信入口"><X className="h-4 w-4"/></Button></div>
    <p className="my-3 text-sm text-ink-3">在通信页查看对话、按角色与执行者筛选，并给指挥官或某个意图下发一条消息。</p>
    <Button onClick={onOpenConversation}>打开通信页</Button>
  </div>
}
