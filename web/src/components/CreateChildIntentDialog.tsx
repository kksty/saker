import { useEffect, useRef, useState } from 'react'
import {
  Button,
  Callout,
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
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import type { CreateChildIntentResult } from '@/lib/types'

/** What the child hangs under: the direction currently open in the inspector. */
export interface ChildIntentParent {
  id: string
  title: string
}

/** Cryptographically random when available; readable and unique enough otherwise. */
function newChildKey(): string {
  const rand =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  return `ui-child-${rand}`
}

/**
 * Hang one piece of work under the direction in the inspector. This is an
 * optional split, not a required step: the parent keeps running, the child gets
 * its own run (and its own profile), and both are ordinary intent rows.
 *
 * The form is just the task input. How the work gets done stays the executor's
 * business; the console does not prescribe an attack order here.
 */
export function CreateChildIntentDialog({
  open,
  onOpenChange,
  parent,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  parent: ChildIntentParent
  /** Called after the server accepted the child; the inspector reloads its list. */
  onCreated: (r: CreateChildIntentResult) => void
}) {
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [evalText, setEvalText] = useState('')
  const [key, setKey] = useState(() => newChildKey())
  const [busy, setBusy] = useState(false)
  const [nodeID,setNodeID]=useState('inherit')
  const nodes=useLoad(api.nodes,[open])
  const bound=(nodes.data?.nodes??[]).filter(n=>n.bound)
  const [error, setError] = useState('')
  const [titleInvalid, setTitleInvalid] = useState(false)
  const titleRef = useRef<HTMLInputElement>(null)
  const [result, setResult] = useState<CreateChildIntentResult | null>(null)

  // A fresh open is a fresh task: clear the fields and mint the key this dialog
  // keeps for its whole life. A retry after a failure reuses it on purpose.
  useEffect(() => {
    if (!open) return
    setTitle('')
    setNodeID('inherit')
    setDescription('')
    setEvalText('')

    setKey(newChildKey())
    setBusy(false)
    setError('')
    setTitleInvalid(false)
    setResult(null)
  }, [open, parent.id])

  async function submit() {
    const cleanTitle = title.trim()
    if (!cleanTitle) {
      setTitleInvalid(true)
      setError('先写标题：意图是账本里的一行')
      titleRef.current?.focus()
      return
    }
    setTitleInvalid(false)
    setBusy(true)
    setError('')
    try {
      const r = await api.createChildIntent(parent.id, {
        title: cleanTitle,
        description: description.trim() || undefined,
        eval: evalText.trim() || undefined,
        idempotency_key: key,
        node:nodeID==='inherit'?undefined:nodeID,
      })
      setResult(r)
      onCreated(r)
      // A newly created child consumed this key; another child is another task.
      // On an idempotent hit the key stays, because that is the same child.
      if (r.created) {
        setTitle('')
        setDescription('')
        setEvalText('')

        setKey(newChildKey())
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!busy) onOpenChange(next) }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto overscroll-contain" aria-busy={busy}>
        <DialogHeader>
          <DialogTitle>派发意图</DialogTitle>
          <DialogDescription>
            把一块工作挂到「{parent.title}」下面（可选）：新意图有自己的 run，会自动开跑；
            原意图继续跑，新意图出现在意图列表和图谱里。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <Field label="执行节点"><Select value={nodeID} onValueChange={setNodeID}><SelectTrigger><SelectValue/></SelectTrigger><SelectContent><SelectItem value="inherit">沿用原意图节点</SelectItem>{bound.map(n=><SelectItem key={n.id} value={n.id}>{n.name||n.note||n.id}</SelectItem>)}</SelectContent></Select></Field>
          <Field label="标题" hint={titleInvalid ? undefined : '一句话说清这块工作要产出什么'}>
            <Input
              ref={titleRef}
              value={title}
              maxLength={200}
              required
              autoFocus
              aria-invalid={titleInvalid ? true : undefined} data-user-invalid={(titleInvalid ? true : undefined) ? 'true' : undefined}
              aria-describedby={titleInvalid ? 'child-intent-title-error' : undefined}
              enterKeyHint="next"
              onChange={(e) => { setTitle(e.target.value); if (titleInvalid) setTitleInvalid(false) }}
              placeholder="例如：验证 /admin 越权读取订单"
            />
            {titleInvalid && <span id="child-intent-title-error" className="block text-[11px] leading-4 text-destructive">先写标题：意图是账本里的一行</span>}
          </Field>
          <Field label="要回答的问题" hint="执行者只看这段文字：目标、输入、边界">
            <Textarea
              rows={3}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="普通用户能否直接读取他人订单？只做只读验证，不改数据。"
            />
          </Field>
          <Field label="验收标准（可选）" hint="什么样的结果算答完；交给 harness 的目标评估器">
            <Textarea
              rows={2}
              value={evalText}
              onChange={(e) => setEvalText(e.target.value)}
              enterKeyHint="done"
              placeholder="例如：给出可复现的请求/响应，并说明是否越权"
            />
          </Field>
          <Field
            label="幂等键（自动生成）"
            hint="同一对话框内保持不变：重试用同一个键会找回这个意图，不会重复创建"
          >
            <div className="tw-break font-mono text-[11px] text-ink-3">{key}</div>
          </Field>

          {error && <Callout tone="destructive">{error}</Callout>}

          {result && (
            <Callout tone={result.created ? 'success' : 'warning'}>
              <div className="space-y-0.5">
                {result.created ? (
                  <div>
                    意图已创建{result.started ? '并开跑' : '，等并发空闲后自动开始'}：
                    <span className="ml-1 font-mono">{result.child.id}</span>
                  </div>
                ) : (
                  <div>
                    幂等命中：意图 <span className="font-mono">{result.child.id}</span>{' '}
                    已存在，没有重复创建。
                  </div>
                )}
                {result.child.title && (
                  <div className="text-[11px] opacity-80">{result.child.title}</div>
                )}
                {result.queued && <div className="text-[11px] opacity-80">{result.queued}</div>}
              </div>
            </Callout>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {result ? '关闭' : '取消'}
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? '创建中…' : '创建意图'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
