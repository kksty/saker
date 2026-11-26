// Pre-dispatch plan text for one intent: title, description and acceptance
// criterion. The editor is offered only for a pending intent that has never
// been dispatched; once a run exists, that run's archived brief is the text the
// executor actually received, and live changes belong to say/steer.
//
// Concurrency: the save carries the edit_version the editor opened with. A stale
// save answers 409 `intent_edit_conflict`; "载入最新" advances the base and
// preserves locally changed fields. An intent that acquired a run in the
// meantime answers 409 `intent_not_editable` and the editor explains the lock.
import { useState } from 'react'
import { Badge, Button, Callout, Input, Textarea } from '@cyber/ui'
import { ApiError, api } from '@/lib/api'
import type { Intent, IntentPlanEdit } from '@/lib/types'

interface PlanDraft {
  version: number
  title: string
  description: string
  eval: string
}

function fromIntent(intent: Intent): PlanDraft {
  return {
    version: intent.edit_version ?? 0,
    title: intent.title,
    description: intent.description,
    eval: intent.eval ?? '',
  }
}

function changedFields(base: PlanDraft, draft: PlanDraft): Omit<IntentPlanEdit, 'version'> {
  const changes: Omit<IntentPlanEdit, 'version'> = {}
  for (const field of ['title', 'description', 'eval'] as const) {
    if (draft[field] !== base[field]) changes[field] = draft[field]
  }
  return changes
}

/** Mirrors the server gate: only a pending intent with no run can be edited. */
export function intentPlanEditable(intent: Intent): boolean {
  return intent.status === 'pending' && !intent.run_id && (intent.attempts ?? 0) === 0
}

export function IntentPlanEditor({ intent, onSaved }: { intent: Intent; onSaved?: () => void }) {
  const [open, setOpen] = useState(false)
  const [base, setBase] = useState<PlanDraft>(() => fromIntent(intent))
  const [draft, setDraft] = useState<PlanDraft>(() => fromIntent(intent))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [code, setCode] = useState('')
  const [note, setNote] = useState('')

  if (!intentPlanEditable(intent)) return null

  const changes = changedFields(base, draft)
  const dirty = Object.keys(changes).length > 0

  async function save() {
    if (busy || !dirty) return
    const title = draft.title.trim()
    if (!title) {
      setError('标题不能为空')
      setCode('')
      return
    }
    setBusy(true)
    setError('')
    setCode('')
    setNote('')
    try {
      if (changes.title !== undefined) changes.title = title
      const updated = await api.editIntent(intent.id, {
        version: base.version,
        ...changes,
      })
      const next = fromIntent(updated)
      setBase(next)
      setDraft(next)
      setNote(`已保存，计划正文 v${next.version}`)
      onSaved?.()
    } catch (cause) {
      const failure = cause instanceof ApiError ? cause : undefined
      setCode(failure?.code ?? '')
      setError(cause instanceof Error ? cause.message : '保存失败')
    } finally {
      setBusy(false)
    }
  }

  // Apply local edits to the latest server text before advancing the CAS token.
  async function loadLatest() {
    setBusy(true)
    setError('')
    setCode('')
    setNote('')
    try {
      const { intent: fresh } = await api.intent(intent.id)
      const next = fromIntent(fresh)
      setDraft((current) => ({ ...next, ...changedFields(base, current) }))
      setBase(next)
      setNote(`已载入最新版本 v${next.version}，保留了你修改的字段。`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '读取失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      data-intent-plan-editor
      className="rounded-lg border border-hairline bg-surface-2/40 px-2.5 py-2"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="saker-eyebrow normal-case tracking-normal text-ink-2">计划正文</span>
        <Badge
          size="sm"
          variant="muted"
          title="edit_version：每次保存 +1；保存时带上打开时的版本"
        >
          正文 v{base.version}
        </Badge>
        {intent.edited_by && (
          <span className="text-[11px] text-ink-3">最后由 {intent.edited_by} 编辑</span>
        )}
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen((value) => !value)}>
          {open ? '收起' : '编辑'}
        </Button>
      </div>
      {!open && (
        <div className="mt-1 text-[11px] leading-relaxed text-ink-3">
          派发前可改标题、说明与验收标准；派发后正文随任务书归档，运行中的变更走插话或续话。
        </div>
      )}
      {open && (
        <div className="mt-2 space-y-2.5 border-t border-hairline pt-2.5">
          <div>
            <div className="mb-1 text-[12px] font-medium text-ink-2">标题</div>
            <Input
              aria-label="意图标题"
              value={draft.title}
              onChange={(event) => setDraft((prev) => ({ ...prev, title: event.target.value }))}
              className="h-8 text-[12px]"
            />
          </div>
          <div>
            <div className="mb-1 text-[12px] font-medium text-ink-2">说明</div>
            <Textarea
              aria-label="意图说明"
              rows={4}
              value={draft.description}
              onChange={(event) => setDraft((prev) => ({ ...prev, description: event.target.value }))}
              placeholder="这一步要做什么；不规定具体打法"
              className="text-[12px]"
            />
          </div>
          <div>
            <div className="mb-1 text-[12px] font-medium text-ink-2">
              验收标准
              <span className="ml-1 font-normal text-ink-3">怎样判断这一步完成；留空表示不设</span>
            </div>
            <Textarea
              aria-label="验收标准"
              rows={3}
              value={draft.eval}
              onChange={(event) => setDraft((prev) => ({ ...prev, eval: event.target.value }))}
              placeholder="例如：拿到 /admin 的 200 响应原文"
              className="text-[12px]"
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={busy || !dirty || !draft.title.trim()} onClick={() => void save()}>
              {busy ? '保存中…' : '保存正文'}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy || !dirty} onClick={() => setDraft(base)}>
              撤销修改
            </Button>
            <span className="saker-nums font-mono text-[11px] text-ink-3">将以 v{base.version} 保存</span>
            {dirty && (
              <Badge size="sm" variant="warning">
                未保存
              </Badge>
            )}
          </div>
          {error && (
            <Callout tone="warning">
              {error}
              {code === 'intent_edit_conflict' && (
                <div className="mt-1.5">
                  <div className="text-[11px]">正文已被他人修改。载入最新版本会保留你修改的字段，其余字段使用最新版。</div>
                  <Button size="sm" variant="outline" className="mt-1.5" disabled={busy} onClick={() => void loadLatest()}>
                    载入最新版本（保留草稿）
                  </Button>
                </div>
              )}
              {code === 'intent_not_editable' && (
                <div className="mt-1 text-[11px]">这条意图已经派发，正文不能再改；运行中的变更用插话或续话。</div>
              )}
            </Callout>
          )}
          {note && <div className="text-[11px] text-ink-3">{note}</div>}
        </div>
      )}
    </div>
  )
}
