import { useEffect, useState } from 'react'
import {
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Textarea,
} from '@cyber/ui'
import { ApiError, api } from '@/lib/api'

export interface StopTarget {
  id: string
  title: string
}

/**
 * Operator stop for one direction. This is not a run cancel: the server holds
 * the direction (and cancels every child direction with it) so the pump cannot
 * re-open it; only an explicit retry clears the hold. The server rejects an
 * empty reason (409), so the dialog refuses to send one at all and says why.
 */
export function StopIntentDialog({
  target,
  onOpenChange,
  onStopped,
}: {
  target: StopTarget | null
  onOpenChange: (open: boolean) => void
  onStopped: (result: { intent_id: string; run_id: string; held: boolean }, target: StopTarget) => void
}) {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState('')

  useEffect(() => {
    setReason('')
    setError('')
    setFieldError('')
    setBusy(false)
  }, [target?.id])

  async function submit() {
    if (!target || busy) return
    const text = reason.trim()
    if (!text) {
      setFieldError('理由必填：没有理由的停止等于丢工作。写清为什么停，后来的人才知道要不要重试。')
      return
    }
    setBusy(true)
    setError('')
    setFieldError('')
    try {
      const result = await api.cancelIntent(target.id, text)
      onOpenChange(false)
      onStopped(result, target)
    } catch (e) {
      const status = e instanceof ApiError ? e.status : 0
      const message = e instanceof Error ? e.message : String(e)
      setError(status ? `停止被拒绝（HTTP ${status}）：${message}` : `停止失败：${message}`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={!!target} onOpenChange={(next) => { if (!busy) onOpenChange(next) }}>
      <DialogContent className="max-w-lg" aria-busy={busy}>
        <DialogHeader>
          <DialogTitle>停止意图（人工）</DialogTitle>
          <DialogDescription>
            「{target?.title ?? ''}」会停止，并记成“已停止（人工）”，不是运行失败。
          </DialogDescription>
        </DialogHeader>

        <Callout tone="warning">
          <div className="space-y-1 text-[12px] leading-relaxed">
            <p>会连带取消这条意图的整棵下游意图树；正在跑的 run 也会立即取消。</p>
            <p>停止不会自动恢复。之后需要继续时，用同一处的「重试」显式恢复。</p>
          </div>
        </Callout>

        <div className="space-y-1.5">
          <label htmlFor="stop-intent-reason" className="block text-[12px] font-medium text-ink-2">
            停止理由（必填）
          </label>
          <Textarea
            id="stop-intent-reason"
            value={reason}
            rows={3}
            disabled={busy}
            aria-invalid={fieldError ? true : undefined}
            aria-describedby={fieldError ? 'stop-intent-reason-error' : undefined}
            placeholder="例如：意图与当前边界不符 / 前提已失效 / 重复派发"
            onChange={(e) => {
              setReason(e.target.value)
              if (fieldError) setFieldError('')
            }}
          />
          {fieldError && (
            <p id="stop-intent-reason-error" className="text-[11px] leading-relaxed text-destructive">
              {fieldError}
            </p>
          )}
          <p className="text-[11px] leading-relaxed text-ink-3">
            理由会写进账本；不预填自动原因，也不接受空理由。
          </p>
        </div>

        {error && <Callout tone="destructive">{error}</Callout>}

        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void submit()}>
            {busy ? '停止中…' : '确认停止'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
