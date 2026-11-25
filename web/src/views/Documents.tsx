// 约束: the project's three documents, read and edited as whole text.
//
// The page exists so the operator can answer one question without opening the
// database: "what does the model read as 起点 / 目标 / 约束 right now, and what
// did it read before?" Editing is whole-document, saves carry the revision they
// read, and every save leaves a revision behind. There is no per-item list.
import { useState } from 'react'
import { History, Pencil, Save, X } from 'lucide-react'
import { Badge, Button, Callout, SkeletonCard, Textarea } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { api } from '@/lib/api'
import {
  DOCUMENT_KINDS,
  DOCUMENT_META,
  documentRevisionNote,
  documentSaveBody,
  documentSaveError,
  revisionLine,
} from '@/lib/documents'
import { useLoad } from '@/lib/hooks'
import type { DocumentKind, ProjectDocument } from '@/lib/types'

export function Documents() {
  const documents = useLoad(api.documents, [])
  // Only the earliest open goal is the `goal` document. Any other open goal is
  // announced here and left alone: editing two goals from one card would make
  // "which goal did that save land on" unanswerable.
  const goals = useLoad(api.goals, [])
  // 未收口 = neither sealed (met) nor withdrawn (closed). Only the earliest one is
  // the `goal` document; the rest are announced and left alone.
  const openGoals = (goals.data ?? []).filter((goal) => !goal.closed && !goal.met)
  const otherOpenGoals = Math.max(0, openGoals.length - 1)
  const byKind = documents.data?.documents

  return (
    <Page
      title="项目文档"
      subtitle="起点、终点、内置约束三份文档；模型每轮读当前版，每次保存留一版历史。"
    >
      {documents.error && (
        <Callout tone="destructive" className="mb-4">
          文档没读到：{documents.error}
        </Callout>
      )}
      <div className={documents.loading && !documents.data ? 'grid gap-4' : 'grid gap-4 2xl:grid-cols-2'}>
        {DOCUMENT_KINDS.map((kind) => (
          <DocumentCard
            key={kind}
            kind={kind}
            doc={byKind?.[kind]}
            loading={documents.loading && !documents.data}
            otherOpenGoals={kind === 'goal' ? otherOpenGoals : 0}
            onSaved={documents.reload}
          />
        ))}
      </div>
    </Page>
  )
}

function DocumentCard({
  kind,
  doc,
  loading,
  otherOpenGoals,
  onSaved,
}: {
  kind: DocumentKind
  doc?: ProjectDocument
  loading: boolean
  otherOpenGoals: number
  onSaved: () => void
}) {
  const meta = DOCUMENT_META[kind]
  // The goal document is the statement of the one current goal; say so in the
  // title instead of leaving "目标" to mean whichever goal was edited last.
  const title = kind === 'goal' ? `${meta.title}（当前）` : meta.title
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [editBase, setEditBase] = useState<ProjectDocument>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [showHistory, setShowHistory] = useState(false)
  const history = useLoad(
    () => (showHistory && doc ? api.documentHistory(kind) : Promise.resolve(null)),
    [showHistory, kind, doc?.goal_id, doc?.revision],
  )

  if (loading) return <SkeletonCard />

  function startEdit() {
    setDraft(doc?.text ?? '')
    setEditBase(doc)
    setEditing(true)
    setError('')
  }

  function cancelEdit() {
    setEditing(false)
    setError('')
  }

  async function save() {
    if (!editBase || busy) return
    setBusy(true)
    setError('')
    try {
      await api.saveDocument(kind, documentSaveBody(editBase, draft))
      setEditing(false)
      onSaved()
    } catch (e) {
      setError(documentSaveError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      data-testid={`document-${kind}`}
      title={
        <span className="flex items-center gap-2">
          {title}
          {doc && (
            <Badge size="sm" variant="muted" className="font-mono tw-nums">
              {doc.revision > 0 ? `v${doc.revision}` : '未改过'}
            </Badge>
          )}
        </span>
      }
      extra={
        doc && !editing ? (
          <div className="flex items-center gap-1.5">
            <Button size="xs" variant="ghost" onClick={() => setShowHistory((open) => !open)} aria-pressed={showHistory}>
              <History className="h-3.5 w-3.5" /> 历史
            </Button>
            <Button size="xs" variant="outline" onClick={startEdit}>
              <Pencil className="h-3.5 w-3.5" /> 编辑
            </Button>
          </div>
        ) : undefined
      }
      bodyClassName="space-y-2.5"
    >
      <p className="text-[11px] leading-4 text-ink-3">{meta.hint}</p>
      {otherOpenGoals > 0 && (
        <p className="text-[11px] leading-4 text-ink-3">
          另有 {otherOpenGoals} 条未收口目标，不在此处编辑。
          <a
            href="?view=overview"
            className="ml-1 text-ink-2 underline decoration-hairline underline-offset-2 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            去概览看
          </a>
        </p>
      )}
      {!doc ? (
        <p className="text-[12px] text-ink-3">这份文档没读到，刷新后再看。</p>
      ) : editing ? (
        <>
          <Textarea
            rows={10}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder={meta.placeholder}
            aria-label={`${title}正文`}
            className="min-h-[220px] text-[12.5px] leading-relaxed"
          />
          {error && (
            <p role="alert" className="text-[11px] leading-4 text-destructive">
              {error}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="xs" onClick={() => void save()} disabled={busy}>
              <Save className="h-3.5 w-3.5" /> {busy ? '保存中…' : '保存'}
            </Button>
            <Button size="xs" variant="ghost" onClick={cancelEdit} disabled={busy}>
              <X className="h-3.5 w-3.5" /> 取消
            </Button>
            <span className="text-[11px] text-ink-3">
              保存后立刻成为新的当前版；旧的这一版留在历史里。
            </span>
          </div>
        </>
      ) : (
        <>
          <div className="tw-break max-h-[42vh] overflow-y-auto whitespace-pre-wrap rounded-lg border border-hairline bg-surface-2/40 px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">
            {doc.text || '（空白）'}
          </div>
          <div className="flex flex-wrap items-center gap-x-2 text-[11px] text-ink-3">
            <span>{documentRevisionNote(kind, doc)}</span>
            {doc.goal_id && <span className="font-mono">目标 {doc.goal_id}</span>}
            <span className="ml-auto">每轮按当前版读</span>
          </div>
        </>
      )}

      {showHistory && !editing && (
        <div className="space-y-1.5 border-t border-hairline pt-2">
          {history.loading && <p className="text-[11px] text-ink-3">读历史中…</p>}
          {history.error && (
            <p className="text-[11px] text-destructive">历史没读到：{history.error}</p>
          )}
          {(history.data?.revisions ?? []).map((revision, index) => (
            <details key={revision.revision} className="rounded-md border border-hairline px-2 py-1.5" open={index === 0}>
              <summary className="cursor-pointer text-[11px] text-ink-2">
                {revisionLine(revision)}
                {index === 0 ? <span className="ml-2 text-ink-3">当前</span> : null}
              </summary>
              <pre className="tw-break mt-1.5 max-h-56 overflow-auto whitespace-pre-wrap font-mono text-[11px] leading-relaxed text-ink-3">
                {revision.diff ?? revision.text}
              </pre>
            </details>
          ))}
          {!history.loading && !history.error && (history.data?.revisions ?? []).length === 0 && (
            <p className="text-[11px] text-ink-3">还没有历史版本。</p>
          )}
        </div>
      )}
    </Panel>
  )
}
