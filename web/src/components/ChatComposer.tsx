// The operator's one 下发 box: who it goes to and what it says.
//
// There is no lifetime switch any more. A directive is a durable message on the
// ledger — to the commander (its next round reads it) or to one direction (that
// direction reads it, and the server records an escalation the commander has to
// answer). Boundaries live in the project documents, so nothing here can change
// them, and the receipt does not lecture about it either.
import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import {
  Button,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  Textarea,
  ToggleGroup,
  ToggleGroupItem,
} from '@cyber/ui'
import { readViewMemory, writeViewMemory } from '@/lib/view-memory'
import { useSessionState } from '@/lib/session-state'
import { api } from '@/lib/api'
import {
  directiveErrorMessage,
  directiveFingerprint,
  directiveKeyFor,
  directiveReceipt,
  directiveRequest,
  oneShotDirections,
  randomRequestKey,
  runningDirectionsHint,
  type DirectiveDraft,
} from '@/lib/directive'
import { statusLabel } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { DirectiveResponse, DirectiveTarget } from '@/lib/types'

type ComposerProps = {
  onSent: (result: DirectiveResponse) => void
  paused: boolean
  runningExecutors: number
  autoFocus?: boolean
  /** Chat-page density: choices on one row without the explanatory paragraph. */
  compact?: boolean
}

export function ChatComposer(props: ComposerProps) {
  // No location key here: the composer is mounted per page right column, and the
  // project switch already remounts the pages that host it.
  return <Composer {...props} />
}

function Composer({ onSent, paused, runningExecutors, autoFocus = false, compact = false }: ComposerProps) {
  const [target, setTarget] = useSessionState<DirectiveTarget>('composer.target', 'commander')
  const [intentID, setIntentID] = useSessionState('composer.intentID', '')
  const [text, setText] = useSessionState('composer.text', '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState('')

  const intents = usePoll(api.intents, 8000)
  // A settled direction cannot receive a message; drop stale ids as the poll moves.
  const directions = useMemo(() => oneShotDirections(intents.data ?? []), [intents.data])
  const selected = target === 'intent' ? directions.find((i) => i.id === intentID) : undefined

  useEffect(() => {
    if (!intents.data || intents.error || target !== 'intent' || !intentID) return
    if (!directions.some((i) => i.id === intentID)) setIntentID('')
  }, [target, intentID, directions, intents.data, intents.error])

  // One key per directive: a transport retry of the same content/recipient must
  // not create a second row; changing anything regenerates the key.
  const [savedKey, setSavedKey] = useSessionState('composer.request', { fingerprint: '', key: '' })
  const keyRef = useRef(savedKey)
  const draftScope = `draft:${window.location.pathname}`
  const active = useRef(true)
  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
    }
  }, [])

  function chooseTarget(next: DirectiveTarget) {
    if (busy) return
    setTarget(next)
    setError('')
    setReceipt('')
  }

  function hint(): string {
    if (target === 'commander') {
      return paused
        ? '项目已暂停：消息会排队，点「继续项目」后由指挥官的下一轮读取。'
        : `只影响指挥官下一轮决策；要加意图、加资产或放宽范围，就发在这里。${runningDirectionsHint(runningExecutors)}。`
    }
    if (selected) {
      return `只送达这个意图（${statusLabel(selected.status)}）：正在跑就插话，否则排到它下一轮；同时上报指挥官裁决。`
    }
    return '只送达选中的意图；同时上报指挥官裁决。'
  }

  async function send() {
    const content = text.trim()
    if (!content || busy) return
    if (target === 'intent' && !intentID) {
      setError('先选一个意图再下发')
      return
    }
    const draft: DirectiveDraft = {
      text: content,
      target,
      intentID: target === 'intent' ? intentID : '',
    }
    keyRef.current = directiveKeyFor(keyRef.current, directiveFingerprint(draft), randomRequestKey)
    setSavedKey(keyRef.current)
    setBusy(true)
    setError('')
    setReceipt('')
    try {
      const result = await api.directive(directiveRequest(draft, keyRef.current.key))
      if (!active.current) {
        // A successful response may arrive after navigating away. Only clear
        // this submission, never a newer draft written in the other view.
        const stored = readViewMemory<{ key: string }>(draftScope, 'composer.request')
        if (stored?.key === keyRef.current.key &&
            readViewMemory<string>(draftScope, 'composer.text')?.trim() === content &&
            (readViewMemory<string>(draftScope, 'composer.target') ?? 'commander') === target &&
            (target !== 'intent' || readViewMemory<string>(draftScope, 'composer.intentID') === intentID)) {
          writeViewMemory(draftScope, 'composer.text', '')
        }
        return
      }
      setReceipt(directiveReceipt(result))
      // Only a successful server answer clears the draft: never optimistic.
      setText('')
      keyRef.current = { fingerprint: '', key: '' }
      setSavedKey(keyRef.current)
      onSent(result)
    } catch (e) {
      if (active.current) setError(directiveErrorMessage(e))
    } finally {
      if (active.current) setBusy(false)
    }
  }

  const choiceClass = compact ? 'h-7 px-1.5 text-[10.5px]' : 'h-8 px-2 text-[11px]'
  const sendFromKeyboard = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // Safari dispatches the confirming Enter before `compositionend`, so
    // `isComposing` alone is not enough: keyCode 229 is the IME marker.
    const native = e.nativeEvent
    if (e.key === 'Enter' && !e.shiftKey && !native.isComposing && native.keyCode !== 229) {
      e.preventDefault()
      void send()
    }
  }
  const placeholder = target === 'commander'
    ? '例如：这台机器的日志审计也在范围内；订阅可以测；先补 /api 的授权模型。'
    : '例如：这个意图先停在只读验证；portal.example 主站也在范围内。'
  const label = target === 'commander' ? '发给指挥官' : '发给意图'

  const targetPicker = (
    <ToggleGroup value={target} onValueChange={(value) => chooseTarget(value as DirectiveTarget)} disabled={busy} ariaLabel="下发对象">
      <ToggleGroupItem value="commander" className={choiceClass}>指挥官</ToggleGroupItem>
      <ToggleGroupItem value="intent" className={choiceClass}>某个意图</ToggleGroupItem>
    </ToggleGroup>
  )

  const directionPicker = target === 'intent' && (
    <div className="space-y-1.5">
      <Select
        value={intentID || undefined}
        onValueChange={(value) => setIntentID(value)}
        disabled={busy || directions.length === 0}
      >
        <SelectTrigger className={compact ? 'h-7 text-[11px]' : 'h-8 text-[12px]'} aria-label="选择下发意图">
          <SelectValue placeholder={directions.length > 0 ? '选择意图' : '没有可选意图'} />
        </SelectTrigger>
        <SelectContent className="max-w-[calc(100vw-2rem)]">
          {directions.map((intent) => (
            <SelectItem key={intent.id} value={intent.id} className="text-[12px]">
              <span className="truncate">{intent.title}</span>
              <span className="ml-1.5 font-mono text-[11px] text-ink-3">{intent.id.slice(0, 12)}</span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {selected && (
        <div className="flex flex-wrap items-center gap-x-2 text-[11px] text-ink-3">
          <span className="font-mono">{selected.id}</span>
          <span>{statusLabel(selected.status)}</span>
        </div>
      )}
    </div>
  )

  const hintLine = (
    <p id="composer-hint" className={compact ? 'text-[10.5px] leading-4 text-ink-3' : 'text-[11px] leading-4 text-ink-3'}>
      {hint()}
    </p>
  )

  const tail = (
    <>
      {error && (
        <div role="alert" className="text-[11px] leading-4 text-destructive">
          {error}
        </div>
      )}
      {receipt && (
        <p data-directive-receipt className={compact ? 'text-[11px] leading-5 text-ink-2' : 'text-[12px] leading-5 text-ink-2'}>
          {receipt}
        </p>
      )}
    </>
  )

  if (compact) {
    return (
      <div className="shrink-0 space-y-1.5 border-t border-hairline p-2.5" data-directive-composer data-compact="true">
        <div className="flex items-end gap-2">
          <Textarea
            rows={2}
            value={text}
            disabled={busy}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={sendFromKeyboard}
            maxLength={4000}
            autoFocus={autoFocus}
            enterKeyHint="send"
            aria-describedby="composer-hint"
            placeholder={placeholder}
            className="min-h-[44px] flex-1 text-[12px] leading-relaxed placeholder:text-ink-2"
            aria-label="下发内容"
          />
          <Button
            size="sm"
            className="mb-0.5 shrink-0"
            disabled={busy || !text.trim() || (target === 'intent' && !intentID)}
            onClick={() => void send()}
          >
            {busy && <Spinner size="sm" className="text-primary-foreground" />}
            {busy ? '处理中…' : label}
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <div className="flex items-center gap-1.5">
            <span className="text-[10px] text-ink-3">对象</span>
            {targetPicker}
          </div>
          <span className="saker-nums ml-auto font-mono text-[10px] text-ink-3">{text.trim().length}/4000</span>
        </div>
        {directionPicker}
        {hintLine}
        {tail}
      </div>
    )
  }

  return (
    <div className="shrink-0 space-y-2 p-3" data-directive-composer>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[10.5px] text-ink-3">对象</span>
          {targetPicker}
        </div>
        <span className="text-[11px] text-ink-3">给指挥官的会进入它下一轮；给意图的只到那个意图</span>
      </div>
      {directionPicker}
      <Textarea
        rows={2}
        value={text}
        disabled={busy}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={sendFromKeyboard}
        maxLength={4000}
        autoFocus={autoFocus}
        enterKeyHint="send"
        aria-describedby="composer-hint"
        placeholder={placeholder}
        className="min-h-[56px] text-[12px] leading-relaxed placeholder:text-ink-2"
        aria-label="下发内容"
      />
      <div className="flex items-center gap-2">
        <span className="saker-nums font-mono text-[11px] text-ink-3">
          {text.trim().length}/4000 · Enter 发送，Shift+Enter 换行
        </span>
        <Button
          size="sm"
          className="ml-auto shrink-0"
          disabled={busy || !text.trim() || (target === 'intent' && !intentID)}
          onClick={() => void send()}
        >
          {busy && <Spinner size="sm" className="text-primary-foreground" />}
          {busy ? '处理中…' : label}
        </Button>
      </div>
      {hintLine}
      {tail}
    </div>
  )
}
