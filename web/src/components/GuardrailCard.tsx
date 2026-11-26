import { useState } from 'react'
import { RefreshCw, ShieldCheck, ShieldOff } from 'lucide-react'
import {
  Badge, Button, Callout, Dialog, DialogContent, DialogDescription, DialogFooter,
  DialogHeader, DialogTitle, Input, SkeletonList,
} from '@cyber/ui'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import {
  guardrailArgumentsText,
  guardrailExpiry,
  guardrailItemKey,
  guardrailItemTitle,
  guardrailResolveNotice,
  guardrailSessionShort,
  guardrailStateLabel,
  guardrailStateReason,
  guardrailStateVariant,
} from '@/lib/guardrail'
import type {
  GuardrailPending,
  GuardrailPendingItem,
  GuardrailResolveResult,
  OperatorInboxGuardrail,
} from '@/lib/types'

/**
 * Tool-approval (guardrail/JEV) card.
 *
 * The host holds an intercepted tool call until the operator answers; the
 * console only relays that question. "Unavailable" and "never checked" are
 * configuration states, so they render muted with the server's reason — never
 * in the error colour, and never as "there is nothing to approve".
 *
 * Reads are explicit: the inbox card mounts with `autoLoad`, while a run page
 * exposes this as an entry ("读取待审批项") so merely selecting a settled run
 * does not dial its host. Every resolve asks for a second confirmation and
 * then refreshes both the pending list and the inbox projection.
 */
export function GuardrailCard({
  session,
  summary,
  autoLoad = true,
  onChanged,
  onLocateCall,
}: {
  /** Restrict the read to one session (run page). Omitted = every live session. */
  session?: string
  /** The inbox's cached projection, shown before/without a host read. */
  summary?: OperatorInboxGuardrail
  autoLoad?: boolean
  /** Reload the owner's inbox after a resolve or a read. */
  onChanged?: () => void
  /**
   * Point the owner at the raw call in a run's event stream. Undefined = the
   * owner has no stream to point into; a pending item without a call id cannot
   * be located and gets no button (never a guessed neighbour).
   */
  onLocateCall?: (target: { callID: string; sessionID: string }) => void
}) {
  const [requested, setRequested] = useState(autoLoad)
  const [target, setTarget] = useState<{ item: GuardrailPendingItem; approve: boolean } | null>(null)
  const [notice, setNotice] = useState<{ tone: 'success' | 'warning' | 'destructive'; text: string } | null>(null)
  const pending = useLoad<GuardrailPending | null>(
    () => (requested ? api.guardrailPending(session ?? '') : Promise.resolve(null)),
    [requested, session ?? ''],
  )
  const data = pending.data ?? null
  // A read that came back without a state (an older host, or a response the
  // console cannot classify) must not erase what the inbox summary already
  // knows: the badge would then say 不可用 while the line under it says
  // 「还没有读取过」 — two answers to one question. Prefer whichever source
  // actually answered, field by field.
  const state = data?.state || summary?.state
  const count = data?.count ?? summary?.count ?? 0
  const partial = data?.state === 'available' && data.sessions?.some((entry) => entry.state === 'unavailable')
  const reason = guardrailStateReason({
    state,
    reason: (data?.reason ?? '').trim() || summary?.reason,
  })

  function refresh() {
    setNotice(null)
    if (!requested) {
      setRequested(true)
      return
    }
    pending.reload()
    onChanged?.()
  }

  function finished(result: GuardrailResolveResult) {
    setNotice(guardrailResolveNotice(result))
    pending.reload()
    onChanged?.()
  }

  return (
    <section aria-label="工具审批" data-testid="guardrail-card" className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        {state === 'available' ? (
          <ShieldCheck className="h-3.5 w-3.5 text-success" aria-hidden="true" />
        ) : (
          <ShieldOff className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" />
        )}
        <span className="text-[12px] font-medium text-ink">工具审批</span>
        <Badge size="sm" variant={partial ? 'warning' : guardrailStateVariant(state, count)}>
          {partial ? (count > 0 ? `已查到 ${count} 项待审批` : '部分会话未读到') : guardrailStateLabel(state, count)}
        </Badge>
        {session && (
          <span className="font-mono text-[11px] text-ink-3" title={session}>
            会话 {guardrailSessionShort(session)}
          </span>
        )}
        <Button
          type="button"
          size="icon-xs"
          variant="ghost"
          className="ml-auto"
          aria-label="刷新工具审批"
          title={requested ? '重新向宿主读取待审批项' : '读取待审批项'}
          onClick={refresh}
          disabled={requested && (pending.loading || pending.refreshing)}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${requested && (pending.loading || pending.refreshing) ? 'motion-safe:animate-spin' : ''}`} aria-hidden="true" />
        </Button>
      </div>

      {!requested ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[11px] leading-relaxed text-ink-3">{reason}</span>
          <Button type="button" size="xs" variant="outline" onClick={() => setRequested(true)}>
            读取待审批项
          </Button>
        </div>
      ) : pending.loading ? (
        <SkeletonList count={2} />
      ) : pending.error ? (
        <Callout tone="warning">
          <span className="saker-break">读取工具审批失败：{pending.error}</span>
          <Button type="button" size="xs" variant="outline" className="ml-2" onClick={() => pending.reload()}>
            重试
          </Button>
        </Callout>
      ) : data && data.state === 'available' ? (
        <>
          {data.reason && <p className="saker-break text-[11px] leading-relaxed text-warning">{data.reason}</p>}
          {!Array.isArray(data.pending) ? (
            <p className="text-[11px] leading-relaxed text-ink-3">
              待审批清单没读全：宿主回了 available，但没有条目清单，不能当成「没有待审批」。
            </p>
          ) : data.pending.length === 0 ? (
            <p className="text-[11px] leading-relaxed text-ink-3">
              {partial ? '已查到的会话暂无待审批，其余会话状态未知。' : '这次查询未返回待审批项。'}
            </p>
          ) : (
            <ul role="list" aria-label="待审批工具调用" className="space-y-2">
              {data.pending.map((item) => {
                const expiry = guardrailExpiry(item.expires_at)
                const callID = item.call_id?.trim() ?? ''
                return (
                  <li key={guardrailItemKey(item)} className="rounded-lg border border-hairline bg-card/60 px-2.5 py-2">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge size="sm" variant={expiry.expired ? 'danger' : 'warning'}>{guardrailItemTitle(item)}</Badge>
                      {item.kind && <span className="font-mono text-[11px] text-ink-3">{item.kind}</span>}
                      {item.state && <span className="text-[11px] text-ink-3">状态 {item.state}</span>}
                      <span className={`ml-auto text-[11px] ${expiry.expired ? 'font-medium text-destructive' : 'text-ink-3'}`}>{expiry.label}</span>
                    </div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-2 font-mono text-[11px] text-ink-3">
                      <span title={item.session_id}>会话 {guardrailSessionShort(item.session_id)}</span>
                      <span className="saker-break">操作 {item.operation_id}</span>
                      {item.call_id && <span className="saker-break">调用 {item.call_id}</span>}
                    </div>
                    <details className="mt-1">
                      <summary className="cursor-pointer select-none text-[11px] text-ink-3">参数（宿主转发的原始 JSON）</summary>
                      <pre className="saker-inset saker-break mt-1 max-h-40 overflow-auto overscroll-contain whitespace-pre-wrap rounded p-2 font-mono text-[11px] leading-5 text-ink-2 [scrollbar-width:thin]">
                        {guardrailArgumentsText(item)}
                      </pre>
                    </details>
                    <div className="mt-1.5 flex flex-wrap gap-1.5">
                      {onLocateCall && callID && (
                        <Button
                          type="button"
                          size="xs"
                          variant="ghost"
                          onClick={() => onLocateCall({ callID, sessionID: item.session_id })}
                        >
                          在事件流里定位
                        </Button>
                      )}
                      <Button type="button" size="xs" variant="outline" onClick={() => setTarget({ item, approve: true })}>
                        批准…
                      </Button>
                      <Button
                        type="button"
                        size="xs"
                        variant="ghost"
                        className="text-warning hover:bg-warning/10 hover:text-warning"
                        onClick={() => setTarget({ item, approve: false })}
                      >
                        拒绝…
                      </Button>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </>
      ) : (
        <p className="saker-break text-[11px] leading-relaxed text-ink-3">{reason}</p>
      )}

      <div aria-live="polite">
        {notice && <Callout tone={notice.tone}>{notice.text}</Callout>}
      </div>

      {target && (
        <GuardrailResolveDialog
          item={target.item}
          approve={target.approve}
          onClose={() => setTarget(null)}
          onFinished={finished}
        />
      )}
    </section>
  )
}

/**
 * Inline approve/reject for one reviewed tool call surfaced by the event stream
 * (`guardrail_review` with state=pending). The pending read is explicit: the
 * event alone cannot promise the host still waits for an answer, so the
 * operator asks once and only then gets the same confirmation dialog the
 * approval card uses. Without a session/operation id the caller renders the
 * row read-only instead of mounting this.
 */
export function GuardrailApprovalButtons({
  sessionID,
  operationID,
  tool,
  onChanged,
}: {
  sessionID: string
  operationID: string
  tool?: string
  onChanged?: () => void
}) {
  // undefined = not checked yet, null = checked but no longer pending.
  const [item, setItem] = useState<GuardrailPendingItem | null | undefined>(undefined)
  const [loading, setLoading] = useState(false)
  const [message, setMessage] = useState('')
  const [target, setTarget] = useState<boolean | null>(null)

  async function check() {
    setLoading(true)
    setMessage('')
    try {
      const pending = await api.guardrailPending(sessionID)
      if (pending.state !== 'available') {
        setItem(null)
        setMessage(`工具审批不可用：${pending.reason?.trim() || '宿主未启用 guardrail/JEV'}`)
        return
      }
      const match = (pending.pending ?? []).find((entry) => entry.operation_id === operationID) ?? null
      setItem(match)
      if (!match) setMessage('宿主已不在待审批列表：可能已过期或已由别处处理。')
    } catch (e) {
      setItem(null)
      setMessage(`查询待审批状态失败：${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setLoading(false)
    }
  }

  if (item === undefined) {
    return (
      <Button
        type="button"
        size="xs"
        variant="outline"
        disabled={loading}
        onClick={() => void check()}
        aria-label="查看工具审批状态"
      >
        {loading ? '查询中…' : '查看审批状态…'}
      </Button>
    )
  }
  if (item === null) {
    return <span className="saker-break text-[11px] leading-relaxed text-ink-3">{message}</span>
  }
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Button type="button" size="xs" variant="outline" onClick={() => setTarget(true)} aria-label={`批准工具调用 ${tool || operationID}`}>
        批准…
      </Button>
      <Button
        type="button"
        size="xs"
        variant="ghost"
        className="text-warning hover:bg-warning/10 hover:text-warning"
        onClick={() => setTarget(false)}
        aria-label={`拒绝工具调用 ${tool || operationID}`}
      >
        拒绝…
      </Button>
      {target !== null && (
        <GuardrailResolveDialog
          item={item}
          approve={target}
          onClose={() => setTarget(null)}
          onFinished={(result) => {
            setItem(null)
            setMessage(guardrailResolveNotice(result).text)
            onChanged?.()
          }}
        />
      )}
    </div>
  )
}

/**
 * Second confirmation for one approval. The stakes are asymmetric: the dialog
 * opens on Cancel, shows the exact tool/arguments being allowed, and the
 * destructive (reject) action is styled as such. A 409 is rendered as the
 * host's answer with its reason, not as a generic network failure.
 */
function GuardrailResolveDialog({
  item,
  approve,
  onClose,
  onFinished,
}: {
  item: GuardrailPendingItem
  approve: boolean
  onClose: () => void
  onFinished: (result: GuardrailResolveResult) => void
}) {
  const [state, setState] = useState<'confirm' | 'pending' | 'success' | 'error'>('confirm')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const expiry = guardrailExpiry(item.expires_at)
  const busy = state === 'pending'
  const success = state === 'success'

  async function submit() {
    if (busy || success) return
    setState('pending')
    setError('')
    try {
      const result = await api.guardrailResolve({
        session_id: item.session_id,
        operation_id: item.operation_id,
        approve,
        reason: reason.trim() || undefined,
      })
      onFinished(result)
      if (result.state === 'resolved') setState('success')
      else {
        setError(guardrailResolveNotice(result).text)
        setState('error')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setState('error')
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
      <DialogContent
        className="max-h-[90dvh] max-w-[calc(100%_-_2rem)] overflow-y-auto overscroll-contain rounded-xl sm:max-w-md"
        closeLabel="关闭"
        aria-busy={busy}
        onEscapeKeyDown={(event) => { if (busy) event.preventDefault() }}
        onPointerDownOutside={(event) => { if (busy) event.preventDefault() }}
      >
        <DialogHeader>
          <DialogTitle>
            {success ? (approve ? '已提交批准' : '已提交拒绝') : approve ? '批准这次工具调用？' : '拒绝这次工具调用？'}
          </DialogTitle>
          <DialogDescription className="break-words leading-relaxed">
            {success
              ? '宿主已收到决定。待审批列表与收件箱已刷新。'
              : approve
                ? '批准后宿主会执行该工具调用，结果会进入本次运行的原始事件流。请核对工具与参数后再确认。'
                : '拒绝后宿主不会执行该工具调用，本次运行会带着这次拒绝继续或结束。'}
          </DialogDescription>
        </DialogHeader>

        <dl className="saker-inset grid grid-cols-1 gap-x-4 gap-y-1 rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-3">
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt>工具</dt>
            <dd className="saker-break min-w-0 text-ink-2">{guardrailItemTitle(item)}</dd>
          </div>
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt>会话</dt>
            <dd className="saker-break min-w-0 text-ink-2" title={item.session_id}>{item.session_id}</dd>
          </div>
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt>操作</dt>
            <dd className="saker-break min-w-0 text-ink-2">{item.operation_id}</dd>
          </div>
          {item.call_id && (
            <div className="flex min-w-0 items-baseline gap-1.5">
              <dt>调用</dt>
              <dd className="saker-break min-w-0 text-ink-2">{item.call_id}</dd>
            </div>
          )}
          <div className="flex min-w-0 items-baseline gap-1.5">
            <dt>期限</dt>
            <dd className={`min-w-0 ${expiry.expired ? 'text-destructive' : 'text-ink-2'}`}>{expiry.label}</dd>
          </div>
        </dl>

        <details>
          <summary className="cursor-pointer select-none text-[12px] text-ink-3">参数</summary>
          <pre className="saker-inset saker-break mt-1 max-h-48 overflow-auto overscroll-contain whitespace-pre-wrap rounded-lg p-3 font-mono text-[11px] leading-5 text-ink-2 [scrollbar-width:thin]">
            {guardrailArgumentsText(item, 2000)}
          </pre>
        </details>

        <label className="block space-y-1.5">
          <span className="block text-[12px] font-medium text-ink-2">审批备注（可选）</span>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            aria-label="审批备注（可选）"
            placeholder={approve ? '例如：授权范围内的只读探测' : '例如：超出项目授权口径'}
            disabled={busy || success}
            maxLength={1000}
          />
        </label>

        {error && <Callout tone="warning">{error}</Callout>}

        <DialogFooter className="gap-2">
          {success ? (
            <Button onClick={onClose}>完成</Button>
          ) : (
            <>
              <Button variant="outline" onClick={onClose} disabled={busy}>取消</Button>
              <Button
                variant={approve ? 'default' : 'destructive'}
                onClick={() => void submit()}
                disabled={busy}
              >
                {busy ? '提交中…' : state === 'error' ? '重试' : approve ? '确认批准' : '确认拒绝'}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
