// The operator's versioned messages to the commander.
import { useMemo, useState } from 'react'
import { History, Pencil, Plus, RotateCcw, ScrollText } from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Field,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  Spinner,
  Textarea,
  ToggleGroup,
  ToggleGroupItem,
} from '@cyber/ui'
import { Panel } from '@/components/Page'
import { api } from '@/lib/api'
import { fmtTime, statusLabel } from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import {
  activeInstructionCount,
  INSTRUCTION_EMPTY,
  INSTRUCTION_GUIDANCE,
  MAX_INSTRUCTION_RUNES,
  instructionCreateBody,
  instructionCreateError,
  instructionCreatedReceipt,
  instructionDraftError,
  instructionFingerprint,
  instructionIntentOptions,
  instructionKeyFor,
  instructionRevokedReceipt,
  instructionRevokeBody,
  instructionRevokeError,
  instructionRevisions,
  instructionScopeLabel,
  instructionStateLabel,
  instructionUpdateBody,
  instructionUpdateDraftError,
  instructionUpdateError,
  instructionUpdatedReceipt,
  orderInstructions,
  type InstructionDraft,
  type InstructionScope,
} from '@/lib/operator-instructions'
import type { DirectiveKeyState } from '@/lib/directive'
import type { Instruction, Intent } from '@/lib/types'

/**
 * The 通信 sidebar card: how many standing instructions exist, the newest few,
 * and the one button that opens the management dialog. Management (create,
 * revoke, history) stays in the dialog because a 248px column cannot show a
 * form, a versioned list and revision text at once.
 */
export function OperatorInstructionsCard() {
  const [open, setOpen] = useState(false)
  const active = usePoll(() => api.instructions('active'), 11_000)
  const rows = orderInstructions(active.data?.instructions ?? [])
  const count = activeInstructionCount(rows)

  return (
    <>
      <Panel
        data-testid="operator-instructions"
        title={`持续指令（${count}）`}
        extra={
          <Button size="xs" variant="outline" onClick={() => setOpen(true)}>
            <Plus className="h-3.5 w-3.5" /> 管理
          </Button>
        }
        bodyClassName="p-3"
      >
        <p className="text-[10.5px] leading-4 text-ink-3">{INSTRUCTION_GUIDANCE}</p>
        {active.error && (
          <p role="alert" className="mt-2 text-[11px] leading-4 text-destructive">
            读取消息失败（{active.error}）
          </p>
        )}
        {active.loading && !active.data && <SkeletonList count={2} />}
        {!active.loading && !active.error && rows.length === 0 && (
          <p className="mt-2 text-[11px] leading-4 text-ink-3">{INSTRUCTION_EMPTY}</p>
        )}
        {rows.length > 0 && (
          <ul role="list" aria-label="当前持续指令" className="mt-2 space-y-1.5">
            {rows.slice(0, 3).map((row) => (
              <li key={row.id} className="rounded-md border border-hairline bg-surface-2/40 px-2 py-1.5">
                <div className="flex items-center gap-1.5 text-[10.5px] text-ink-3">
                  <Badge size="sm" variant={row.state === 'revoked' ? 'muted' : 'success'}>
                    {instructionStateLabel(row.state)}
                  </Badge>
                  <span className="min-w-0 truncate">{instructionScopeLabel(row)}</span>
                  <span className="saker-nums ml-auto shrink-0 font-mono">v{row.version}</span>
                </div>
                <p className="saker-break mt-1 line-clamp-2 whitespace-pre-wrap text-[11px] leading-4 text-ink-2">
                  {row.text}
                </p>
              </li>
            ))}
            {rows.length > 3 && (
              <li className="text-[10.5px] text-ink-3">另有 {rows.length - 3} 条，打开「管理」查看。</li>
            )}
          </ul>
        )}
      </Panel>
      {open && (
        <OperatorInstructionsDialog
          onClose={() => setOpen(false)}
          onChanged={() => active.reload()}
        />
      )}
    </>
  )
}

/** Create + standing list + inline body edit + per-instruction revision history.
 *  One dialog so the four operations read against the same current set. */
export function OperatorInstructionsDialog({ onClose, onChanged }: { onClose: () => void; onChanged: () => void }) {
  const rows = useLoad(() => api.instructions('all'), [])
  const intents = useLoad(api.intents, [])
  const titles = useMemo(
    () => new Map((intents.data ?? []).map((intent) => [intent.id, intent.title] as const)),
    [intents.data],
  )
  const directions = useMemo(() => instructionIntentOptions(intents.data ?? []), [intents.data])
  const sorted = orderInstructions(rows.data?.instructions ?? [])

  const [scope, setScope] = useState<InstructionScope>('project')
  const [intentID, setIntentID] = useState('')
  const [text, setText] = useState('')
  const [showRevoked, setShowRevoked] = useState(false)
  const [busy, setBusy] = useState(false)
  const [revoking, setRevoking] = useState('')
  // The row being edited: the snapshot is taken when 编辑 is clicked, so saving
  // always carries the version the operator actually read (a background reload
  // that lands mid-edit must not borrow a newer version).
  const [editingID, setEditingID] = useState('')
  const [editDraft, setEditDraft] = useState('')
  const [editSnapshot, setEditSnapshot] = useState<Instruction | null>(null)
  const [updating, setUpdating] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState('')
  const [keyState, setKeyState] = useState<DirectiveKeyState>({ fingerprint: '', key: '' })

  const visible = sorted.filter((row) => showRevoked || row.state !== 'revoked')
  const draft: InstructionDraft = { text, scope, intentID }
  const tooLong = [...text.trim()].length > MAX_INSTRUCTION_RUNES
  const canSend = !busy && !instructionDraftError(draft)
  const editError = editingID ? instructionUpdateDraftError(editDraft) : undefined
  const editUnchanged = editDraft.trim() === (editSnapshot?.text ?? '')
  const canSaveEdit = Boolean(editingID) && !updating && !editError && !editUnchanged
  const locked = busy || updating

  function startEdit(instruction: Instruction) {
    if (updating) return
    setEditingID(instruction.id)
    setEditDraft(instruction.text)
    setEditSnapshot(instruction)
    setError('')
    setReceipt('')
  }

  function cancelEdit() {
    if (updating) return
    setEditingID('')
    setEditDraft('')
    setEditSnapshot(null)
  }

  async function saveEdit() {
    const snapshot = editSnapshot
    if (!snapshot || updating || !canSaveEdit) return
    setUpdating(true)
    setError('')
    setReceipt('')
    try {
      const updated = await api.updateInstruction(snapshot.id, instructionUpdateBody(snapshot, editDraft))
      setReceipt(instructionUpdatedReceipt(updated))
      setEditingID('')
      setEditDraft('')
      setEditSnapshot(null)
      rows.reload()
      onChanged()
    } catch (e) {
      // Keep the editor and the typed draft: the operator fixes or retries
      // without losing what the server just refused.
      setError(instructionUpdateError(e))
    } finally {
      setUpdating(false)
    }
  }

  async function submit() {
    if (busy) return
    const invalid = instructionDraftError(draft)
    if (invalid) {
      setError(invalid)
      return
    }
    setBusy(true)
    setError('')
    setReceipt('')
    try {
      const fingerprint = instructionFingerprint(draft)
      const key = instructionKeyFor(keyState, fingerprint)
      const result = await api.directive(instructionCreateBody(draft, key.key))
      setReceipt(instructionCreatedReceipt(result))
      setText('')
      // A new sentence must not reuse the old key: clear it only after the row
      // really exists, so a failed submit still retries with the same key.
      setKeyState({ fingerprint: '', key: '' })
      rows.reload()
      onChanged()
    } catch (e) {
      setError(instructionCreateError(e))
    } finally {
      setBusy(false)
    }
  }

  async function revoke(instruction: Instruction) {
    if (revoking) return
    setRevoking(instruction.id)
    setError('')
    setReceipt('')
    try {
      const updated = await api.revokeInstruction(instruction.id, instructionRevokeBody(instruction))
      setReceipt(instructionRevokedReceipt(updated))
      rows.reload()
      onChanged()
    } catch (e) {
      setError(instructionRevokeError(e))
    } finally {
      setRevoking('')
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next && !locked) onClose() }}>
      <DialogContent
        className="max-h-[90dvh] max-w-2xl overflow-y-auto overscroll-contain"
        aria-busy={locked}
        data-testid="operator-instructions-dialog"
      >
        <DialogHeader>
          <DialogTitle>持续指令</DialogTitle>
          <DialogDescription>{INSTRUCTION_GUIDANCE}</DialogDescription>
        </DialogHeader>

        <section aria-label="新建持续指令" className="space-y-2.5 rounded-lg border border-hairline p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[11px] text-ink-3">关联范围</span>
            <ToggleGroup
              value={scope}
              onValueChange={(value) => {
                setScope(value as InstructionScope)
                setError('')
              }}
              disabled={busy}
              ariaLabel="消息关联范围"
            >
              <ToggleGroupItem value="project">整个项目</ToggleGroupItem>
              <ToggleGroupItem value="intent">某个意图</ToggleGroupItem>
            </ToggleGroup>
          </div>
          {scope === 'intent' && (
            <Field label="意图">
              <Select value={intentID || undefined} onValueChange={setIntentID} disabled={busy || directions.length === 0}>
                <SelectTrigger aria-label="选择消息关联的意图">
                  <SelectValue placeholder={directions.length > 0 ? '选择意图' : '没有可选意图'} />
                </SelectTrigger>
                <SelectContent className="max-w-[calc(100vw-2rem)]">
                  {directions.map((direction) => (
                    <SelectItem key={direction.id} value={direction.id} className="text-[12px]">
                      <span className="truncate">{direction.title}</span>
                      <span className="ml-1.5 font-mono text-[11px] text-ink-3">
                        {direction.id.slice(0, 12)} · {statusLabel(direction.status)}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field
            label="消息正文"
            error={tooLong ? `消息最多 ${MAX_INSTRUCTION_RUNES} 字。` : undefined}
          >
            <Textarea
              rows={4}
              value={text}
              disabled={busy}
              onChange={(event) => setText(event.target.value)}
              placeholder="例如：意图标题优先使用简体中文。"
              aria-label="持续指令正文"
              className="min-h-[88px] text-[12.5px] leading-relaxed"
            />
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={() => void submit()} disabled={!canSend}>
              {busy && <Spinner size="sm" className="text-primary-foreground" />}
              {busy ? '保存中…' : '保存并持续生效'}
            </Button>
            <span className="saker-nums ml-auto font-mono text-[10.5px] text-ink-3">
              {text.trim().length}/{MAX_INSTRUCTION_RUNES}
            </span>
          </div>
        </section>

        {/* Shared feedback: a create refusal and an inline-edit refusal must be
            visible from the same place instead of only beside the create form. */}
        {error && (
          <div role="alert">
            <Callout tone="destructive">{error}</Callout>
          </div>
        )}
        {receipt && (
          <p data-instruction-receipt className="text-[11.5px] leading-5 text-ink-2">
            {receipt}
          </p>
        )}

        <section aria-label="已保存的持续指令" className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <div className="saker-section-title">已保存的消息</div>
            <span className="saker-nums font-mono text-[11px] text-ink-3">
              {activeInstructionCount(sorted)} 条生效中
            </span>
            <Button
              size="xs"
              variant="ghost"
              className="ml-auto"
              aria-pressed={showRevoked}
              onClick={() => setShowRevoked((shown) => !shown)}
            >
              <ScrollText className="h-3.5 w-3.5" />
              {showRevoked ? '隐藏已撤销' : '显示已撤销'}
            </Button>
            <Button size="xs" variant="ghost" onClick={() => rows.reload()} disabled={rows.loading}>
              刷新
            </Button>
          </div>
          {rows.loading && !rows.data && <SkeletonList count={2} />}
          {rows.error && (
            <p role="alert" className="text-[11px] leading-4 text-destructive">
              读取消息失败（{rows.error}）
            </p>
          )}
          {!rows.loading && !rows.error && visible.length === 0 && (
            <EmptyState compact icon={ScrollText} title="还没有持续指令" description={INSTRUCTION_EMPTY} />
          )}
          <ul role="list" aria-label="持续指令列表" className="space-y-2">
            {visible.map((instruction) => (
              <InstructionRow
                key={instruction.id}
                instruction={instruction}
                titles={titles}
                busy={Boolean(revoking) || updating}
                revoking={revoking === instruction.id}
                editing={editingID === instruction.id}
                draft={editingID === instruction.id ? editDraft : ''}
                draftError={editingID === instruction.id ? editError : undefined}
                canSave={canSaveEdit}
                updating={updating}
                onEdit={() => startEdit(instruction)}
                onDraftChange={setEditDraft}
                onSave={() => void saveEdit()}
                onCancel={cancelEdit}
                onRevoke={() => void revoke(instruction)}
              />
            ))}
          </ul>
        </section>
      </DialogContent>
    </Dialog>
  )
}

function InstructionRow({
  instruction,
  titles,
  busy,
  revoking,
  editing,
  draft,
  draftError,
  canSave,
  updating,
  onEdit,
  onDraftChange,
  onSave,
  onCancel,
  onRevoke,
}: {
  instruction: Instruction
  titles: ReadonlyMap<string, string>
  busy: boolean
  revoking: boolean
  editing: boolean
  draft: string
  draftError?: string
  canSave: boolean
  updating: boolean
  onEdit: () => void
  onDraftChange: (value: string) => void
  onSave: () => void
  onCancel: () => void
  onRevoke: () => void
}) {
  const [showHistory, setShowHistory] = useState(false)
  const history = useLoad(
    () => (showHistory ? api.instructionHistory(instruction.id) : Promise.resolve(null)),
    [showHistory, instruction.id, instruction.version],
  )

  return (
    <li data-instruction={instruction.id} className="rounded-lg border border-hairline p-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px] text-ink-3">
        <Badge size="sm" variant={instruction.state === 'revoked' ? 'muted' : 'success'}>
          {instructionStateLabel(instruction.state)}
        </Badge>
        <span>{instructionScopeLabel(instruction, titles)}</span>
        <span className="saker-nums font-mono">v{instruction.version}</span>
        <span className="ml-auto">{fmtTime(instruction.updated_at || instruction.created_at)}</span>
      </div>
      {editing ? (
        // Inline edit, not a second form: the text being replaced stays in place
        // so the operator can see the row version the save will carry.
        <div className="mt-1.5 space-y-1.5">
          <Textarea
            rows={4}
            value={draft}
            disabled={updating}
            onChange={(event) => onDraftChange(event.target.value)}
            aria-label="修改持续指令正文"
            className="min-h-[88px] text-[12.5px] leading-relaxed"
          />
          <div className="flex flex-wrap items-center gap-2">
            <span className="saker-nums font-mono text-[10.5px] text-ink-3">
              {[...draft.trim()].length}/{MAX_INSTRUCTION_RUNES}
            </span>
            {draftError && <span className="text-[10.5px] text-destructive">{draftError}</span>}
            <Button size="xs" className="ml-auto" onClick={onSave} disabled={!canSave}>
              {updating ? '保存中…' : '保存修改'}
            </Button>
            <Button size="xs" variant="ghost" onClick={onCancel} disabled={updating}>
              取消
            </Button>
          </div>
        </div>
      ) : (
        <>
          <p className="saker-break mt-1.5 whitespace-pre-wrap text-[12px] leading-relaxed text-ink-2">{instruction.text}</p>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            <Button
              size="xs"
              variant="ghost"
              aria-pressed={showHistory}
              onClick={() => setShowHistory((shown) => !shown)}
            >
              <History className="h-3.5 w-3.5" /> 历史
            </Button>
            {/* A revoked row offers no edit: reviving it is a create/scope
                decision, not a body change, and must not be one click away. */}
            {instruction.state !== 'revoked' && (
              <>
                <Button size="xs" variant="ghost" onClick={onEdit} disabled={busy}>
                  <Pencil className="h-3.5 w-3.5" /> 编辑
                </Button>
                <Button size="xs" variant="ghost" onClick={onRevoke} disabled={busy}>
                  <RotateCcw className="h-3.5 w-3.5" /> {revoking ? '撤销中…' : '撤销'}
                </Button>
              </>
            )}
          </div>
        </>
      )}
      {!editing && showHistory && (
        <div className="mt-1.5 space-y-1.5 border-t border-hairline pt-1.5">
          {history.loading && <p className="text-[11px] text-ink-3">读历史中…</p>}
          {history.error && (
            <p className="text-[11px] text-destructive">历史没读到：{history.error}</p>
          )}
          {!history.loading && !history.error &&
            instructionRevisions(history.data).map((revision) => (
              <details key={`${instruction.id}-${revision.version}`} className="rounded-md border border-hairline px-2 py-1.5">
                <summary className="cursor-pointer text-[11px] text-ink-2">
                  v{revision.version} · {revision.operation} · {instructionStateLabel(revision.state)}
                  {revision.at ? <span className="ml-1.5 text-ink-3">{fmtTime(revision.at)}</span> : null}
                </summary>
                <pre className="saker-break mt-1.5 max-h-48 overflow-auto whitespace-pre-wrap font-mono text-[11px] leading-relaxed text-ink-3">
                  {revision.text || '（这一版没有正文）'}
                </pre>
              </details>
            ))}
          {!history.loading && !history.error && instructionRevisions(history.data).length === 0 && (
            <p className="text-[11px] text-ink-3">还没有历史版本。</p>
          )}
        </div>
      )}
    </li>
  )
}
