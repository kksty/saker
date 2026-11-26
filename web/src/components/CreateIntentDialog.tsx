import { useRef, useState } from 'react'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@cyber/ui'
import { useSessionState } from '@/lib/session-state'
import { navigate } from '@/lib/navigation'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'

/** The one way to add work: an intent is the unit the executor receives. */
export function CreateIntentDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (id: string) => void
}) {
  const [title, setTitle] = useSessionState('intent.title', '')
  const [description, setDescription] = useSessionState('intent.description', '')
  const [evalText, setEvalText] = useSessionState('intent.eval', '')
  const [busy, setBusy] = useState(false)
  const [nodeID,setNodeID]=useSessionState('intent.node','default')
  const [error, setError] = useState('')
  const nodes = useLoad(api.nodes, [open])
  const bound = (nodes.data?.nodes ?? []).filter((n) => n.bound)

  const input = useRef<HTMLTextAreaElement>(null)
  const submitting = useRef(false)
  const [invalid, setInvalid] = useState(false)
  const checking = nodes.loading || nodes.refreshing
  const dependencyError = nodes.error
  const missing = invalid && !title.trim() && !description.trim()
  // The choice is persisted per tab: the node may have been unbound since, and the
  // server rejects a dispatch to an unbound node (nodes.go checkNode).
  const nodeReady = nodeID === 'default' || bound.some((n) => n.id === nodeID)
  const ready = !checking && !dependencyError && bound.length > 0 && nodeReady

  async function submit() {
    if (submitting.current || !ready) return
    if (!title.trim() && !description.trim()) {
      setInvalid(true)
      input.current?.focus()
      return
    }
    submitting.current = true
    setBusy(true)
    setError('')
    try {
      const created = await api.createIntent({
        title: title.trim(),
        description: description.trim(),
        eval: evalText.trim() || undefined,
        // Authorization and prohibitions live in the project brief + built-in
        // constraints; an intent does not carry its own scope document.
        node: nodeID==='default'?undefined:nodeID,

      })
      setTitle('')
      setDescription('')
      setEvalText('')
      setNodeID('default')
      onOpenChange(false)
      onCreated(created.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!busy) onOpenChange(next) }}>
      <DialogContent className="max-h-[90dvh] max-w-lg overflow-y-auto overscroll-contain" aria-busy={busy} onKeyDown={(e) => {
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.nativeEvent.isComposing) { e.preventDefault(); void submit() }
      }}>
        <DialogHeader>
          <DialogTitle>新建意图</DialogTitle>
          <DialogDescription>
            把要验证的问题写清楚即可。授权范围与禁止事项由项目简报和内置约束负责；新意图由指挥官/执行者自行划分，不必在这里设置测试范围。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field label="标题（可选）" hint="留空则自动取问题的第一句">
            <Input
              disabled={busy}
              enterKeyHint="next"
              value={title}
              onChange={(e) => { setTitle(e.target.value); if (invalid) setInvalid(false) }}
              placeholder="例如：验证 /admin 越权读取订单"
            />
          </Field>
          <Field
            label="要验证什么"
            hint={missing ? undefined : '一句话说清要验证的问题；只说清目标，不写授权边界'}
          >
            <Textarea
              ref={input}
              autoFocus
              disabled={busy}
              aria-invalid={missing ? true : undefined} data-user-invalid={(missing ? true : undefined) ? 'true' : undefined}
              aria-describedby={missing ? 'create-intent-description-error' : undefined}
              enterKeyHint="done"
              rows={4}
              value={description}
              onChange={(e) => { setDescription(e.target.value); if (invalid) setInvalid(false) }}
              placeholder="普通用户能否直接读取他人订单？"
            />
            {missing && (
              <span id="create-intent-description-error" className="block text-[11px] leading-4 text-destructive">
                标题或问题至少填一个
              </span>
            )}
          </Field>
          <Field label="验收标准（可选）" hint="怎样判断这一步完成；留空表示不设">
            <Textarea
              disabled={busy}
              rows={3}
              value={evalText}
              onChange={(e) => setEvalText(e.target.value)}
              placeholder="例如：拿到 /admin 的 200 响应原文"
              className="text-[12px]"
            />
          </Field>
          {checking ? <p role="status" className="text-xs text-ink-3">正在检查执行节点…</p> : dependencyError ? (
            <div role="alert" className="space-y-2 text-xs text-destructive">
              <p>暂时无法检查执行环境，输入已保留。请重试。</p>
              <Button size="xs" variant="outline" onClick={() => { nodes.reload() }}>重新检查</Button>
              <details><summary>错误详情</summary>{dependencyError}</details>
            </div>
          ) : bound.length === 0 ? (
            <div className="space-y-2 text-xs"><p>尚未绑定执行节点，请检查本机宿主。</p><Button size="xs" variant="outline" onClick={() => { onOpenChange(false); navigate(`${window.location.pathname}?view=runtime&tab=nodes`) }}>打开执行环境</Button></div>
          ) : (
            <div className="space-y-1.5 text-[12px] text-ink-3">
              <Select value={nodeID} onValueChange={setNodeID}><SelectTrigger><SelectValue placeholder="执行节点" /></SelectTrigger><SelectContent><SelectItem value="default">项目默认节点</SelectItem>{bound.map(n=><SelectItem key={n.id} value={n.id}>{n.name||n.note||n.id}{n.online===false?'（离线）':''}</SelectItem>)}</SelectContent></Select>
              {!nodeReady && <p role="alert" className="text-destructive">原执行节点已不可用，请重新选择节点。</p>}
            </div>
          )}
          {error && <div role="alert" className="text-[12px] text-destructive">创建失败，输入已保留，可重试。<details><summary>错误详情</summary>{error}</details></div>}
        </div>
        <p className="text-xs text-ink-3">Ctrl / ⌘ + Enter 创建 · 草稿会在当前标签页保留</p>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !ready}>
            {busy ? '创建中…' : '创建'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
