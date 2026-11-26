import { useRef, useState } from 'react'
import {
  Button, Callout, Dialog, DialogContent, DialogDescription, DialogFooter,
  DialogHeader, DialogTitle, Spinner,
} from '@cyber/ui'
import { api } from '@/lib/api'

export interface SealGoalRef { id: string; statement: string }

/** One explicit commander round; evidence selection belongs to the commander. */
export function SealGoalDialog({ goal, open, onOpenChange, onSealed, paused = true }: {
  paused?: boolean
  goal: SealGoalRef | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSealed?: () => void
}) {
  const [busy, setBusy] = useState(false)
  const submitting = useRef(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState('')
  function changeOpen(next: boolean) {
    if (busy) return
    setError('')
    setResult('')
    onOpenChange(next)
  }
  async function submit() {
    if (submitting.current || !paused) return
    submitting.current = true
    setBusy(true)
    setError('')
    setResult('')
    try {
      const out = await api.closeoutProject()
      if (out.completed) { onSealed?.(); onOpenChange(false) }
      else setResult('指挥官已完成本轮核对，目标仍未全部达成。请查看指挥官答复与目标缺口；项目保持暂停。')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { submitting.current = false; setBusy(false) }
  }
  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        className="max-h-[90dvh] max-w-md overflow-y-auto overscroll-contain"
        aria-busy={busy}
        onOpenAutoFocus={(event) => {
          // A costly, model-consuming round is not the default action: focus
          // starts on Close so a stray Enter cannot start it.
          event.preventDefault()
          if (typeof document !== 'undefined') document.getElementById('seal-goal-close')?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>让指挥官收口</DialogTitle>
          <DialogDescription>
            指挥官将运行一轮，核对目标与已有事实、证据，整理完成依据，无需手动选择事实。
            证明充分才登记完成；证明不足会说明缺口。此轮消耗模型 token，项目保持暂停。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="saker-inset saker-break rounded-lg px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">{goal?.statement}</div>
          {!paused && <Callout tone="warning">请先暂停项目，再让指挥官核对并收口。</Callout>}
          {result && <div role="status"><Callout tone="warning">{result}</Callout></div>}
          {error && <div role="alert"><Callout tone="destructive">{error}</Callout></div>}
        </div>
        <DialogFooter>
          <Button id="seal-goal-close" variant="outline" onClick={() => changeOpen(false)} disabled={busy}>关闭</Button>
          <Button onClick={submit} disabled={busy || !!result || !paused}>
            {busy ? <><Spinner size="sm" className="mr-1.5" />指挥官正在整理…</> : '让指挥官核对并收口'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
