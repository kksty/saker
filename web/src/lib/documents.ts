// Three plain-text documents, no entries.
//
// 起点 says what is known and who is in scope, 目标 is the end state that can be
// judged, 约束 carries the one hard line and the cleanup obligation. The console
// only shows their current text, edits whole documents, and lists revisions —
// there is no per-item authorization to add, revoke or scope.
import { ApiError } from './api.ts'
import { fmtTime } from './format.ts'
import type { DocumentKind } from './types.ts'

export const DOCUMENT_KINDS: readonly DocumentKind[] = ['scope', 'goal', 'constraints']

export const DOCUMENT_META: Record<DocumentKind, { title: string; hint: string; placeholder: string }> = {
  scope: {
    title: '起点',
    hint: '资产与背景：要测什么、已知什么条件。当前这一版就是模型每轮读到的起点。',
    placeholder: '例如：\n- 授权资产：<清单>\n- 已知入口与账号清单（不含凭据）\n- 测试窗口：<时间>',
  },
  goal: {
    title: '终点',
    hint: '终点：一句能判定成立与否的话；需要验收标准时一起写在这里。当前这一版就是模型每轮读到的终点。',
    placeholder: '例如：拿到至少一个可复现的控制点，并留下可复核的证据。',
  },
  constraints: {
    title: '内置约束',
    hint: '唯一硬线与清理记录要求。只有操作员能改；模型每轮读到的是这一版原文。',
    placeholder: '约束（唯一硬线）：不要删除或修改真实数据……',
  },
}

/** A Go zero `time.Time` renders as year 1, and an absent field is not a date at
 *  all: both mean "not recorded", so neither may reach the screen as a time. */
export function recordedTime(value?: string): string | undefined {
  if (!value) return undefined
  if (value.startsWith('0001-01-01')) return undefined
  return value
}

/** One line: which revision, when, and who saved it. */
export function revisionLine(revision: {
  revision: number
  updated_at?: string
  updated_by?: string
}): string {
  const at = recordedTime(revision.updated_at)
  return `第 ${revision.revision} 版 · ${at ? fmtTime(at) : '（时间未记录）'} · ${revision.updated_by || '（未记录）'}`
}

/** What the card says under the text. The goal document has no saved-at field of
 *  its own (its revision is the goal row's version), so it never claims a time;
 *  a document nobody edited yet says that instead of showing v0. */
export function documentRevisionNote(
  kind: DocumentKind,
  doc: { revision: number; updated_at?: string; updated_by?: string },
): string {
  if (doc.revision <= 0) return '还没在这里改过：显示的是项目里的原文，保存一次就会有第 1 版'
  if (kind === 'goal' && !recordedTime(doc.updated_at)) return `第 ${doc.revision} 版 · 修订号取自这条目标本身`
  return revisionLine(doc)
}

/** A 409 means someone saved after this page read the document: say so and keep
 *  the operator's draft instead of pretending the save worked. */
export function documentSaveError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'document_conflict') {
      return '文档已被改过：刷新看当前版再改，别覆盖刚保存的内容。'
    }
    if (error.code === 'document_no_open_goal') {
      return '现在没有未收口的目标：先在概览追加或恢复目标，再来改目标文档。'
    }
    if (error.status === 403) return '只有操作员能改这三份文档。'
    if (error.code === 'document_invalid') return error.message
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}
/** The save body: the new text plus the revision this page actually read. Without
 *  the second half a save silently overwrites whatever landed in between. */
export function documentSaveBody(
  doc: { revision: number; goal_id?: string },
  text: string,
): { text: string; revision: number; goal_id?: string } {
  const body = { text, revision: doc.revision }
  return doc.goal_id ? { ...body, goal_id: doc.goal_id } : body
}
