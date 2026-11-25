// The 项目文档 page: three whole documents, one revision line, no entry list.
//
// These cases pin the parts a screenshot cannot: a stale save must say so, the
// page must not grow per-item vocabulary back, and the revision line must name
// the version the model is actually reading.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ApiError } from '../src/lib/api.ts'
import {
  DOCUMENT_KINDS,
  DOCUMENT_META,
  documentSaveBody,
  documentSaveError,
  documentRevisionNote,
  recordedTime,
  revisionLine,
} from '../src/lib/documents.ts'

test('the three documents are the whole surface, each with its own wording', () => {
  assert.deepEqual([...DOCUMENT_KINDS], ['scope', 'goal', 'constraints'])
  for (const kind of DOCUMENT_KINDS) {
    const meta = DOCUMENT_META[kind]
    assert.ok(meta.title.trim().length > 0, `${kind}: title`)
    assert.ok(meta.hint.trim().length > 0, `${kind}: hint`)
  }
  assert.equal(DOCUMENT_META.scope.title, '起点')
  // The third document is the endpoint of the current goal, not "目标" — the
  // console says 终点 everywhere so it cannot be confused with a goal row.
  assert.equal(DOCUMENT_META.goal.title, '终点')
  assert.equal(DOCUMENT_META.constraints.title, '内置约束')
})

test('a stale save is a conflict the operator can act on, not a generic failure', () => {
  assert.match(documentSaveError(new ApiError(409, 'conflict', 'document_conflict')), /刷新看当前版/)
  assert.match(documentSaveError(new ApiError(403, 'forbidden', 'forbidden')), /只有操作员/)
  assert.match(documentSaveError(new ApiError(400, '正文为空', 'document_invalid')), /正文为空/)
  // The goal document has no open goal to write: say what to do, not "conflict".
  assert.match(documentSaveError(new ApiError(409, '没有未收口的目标可写', 'document_no_open_goal')), /先在概览/)
  assert.equal(documentSaveError(new Error('网络错误')), '网络错误')
  // A conflict must not read like "saved": the operator would stop looking.
  assert.doesNotMatch(documentSaveError(new ApiError(409, 'conflict', 'document_conflict')), /已保存|保存成功/)
})

test('the revision line names the version being read, with the time it was saved', () => {
  const line = revisionLine({ revision: 7, updated_at: '2026-11-21T21:00:00+08:00', updated_by: 'operator' })
  assert.match(line, /第 7 版/)
  assert.match(line, /operator/)
  assert.match(line, /11-21/)
  // Missing provenance is said out loud, never left blank.
  assert.match(revisionLine({ revision: 1 }), /时间未记录/)
  assert.match(revisionLine({ revision: 1 }), /未记录/)
  // A Go zero time is not a date: year 1 must never reach the screen.
  assert.match(revisionLine({ revision: 2, updated_at: '0001-01-01T00:00:00Z' }), /时间未记录/)
  assert.equal(recordedTime('0001-01-01T00:00:00Z'), undefined)
  assert.equal(recordedTime('2026-11-21T21:00:00+08:00'), '2026-11-21T21:00:00+08:00')
})

test('a document nobody edited says so, and the goal document never invents a time', () => {
  assert.match(documentRevisionNote('scope', { revision: 0 }), /还没在这里改过/)
  // The goal revision is the goal row's version: there is no saved-at field, so
  // the card says where the number comes from instead of "时间未记录".
  assert.equal(
    documentRevisionNote('goal', { revision: 3 }),
    '第 3 版 · 修订号取自这条目标本身',
  )
  // A saved scope/constraints revision still names who and when.
  assert.match(
    documentRevisionNote('constraints', { revision: 5, updated_at: '2026-11-21T21:00:00+08:00', updated_by: 'operator' }),
    /第 5 版 · 11-21 .* · operator/,
  )
})

test('the page keeps the document-only shape and does not reintroduce entries', () => {
  const source = readFileSync(
    fileURLToPath(new URL('../src/views/Documents.tsx', import.meta.url)),
    'utf8',
  )
  assert.match(source, /api\.documents/)
  assert.match(source, /api\.documentHistory/)
  assert.match(source, /api\.saveDocument\(kind, documentSaveBody\(editBase, draft\)\)/)
  // Per-item authorization vocabulary must not come back through this page.
  for (const banned of ['as_authorization', 'visible_to_commander', 'scope: ', 'revoked']) {
    assert.equal(source.includes(banned), false, `Documents must not mention ${banned}`)
  }
})
test('a save carries the revision it read, so a newer write is never overwritten', () => {
  assert.deepEqual(documentSaveBody({ revision: 7 }, 'new text'), { text: 'new text', revision: 7 })
  // Empty text is a legal document (clearing a section); it still carries the revision.
  assert.deepEqual(documentSaveBody({ revision: 2 }, ''), { text: '', revision: 2 })
})

test('the page and its nav entry are named 项目文档, and the third document is not called 目标', () => {
  const page = readFileSync(
    fileURLToPath(new URL('../src/views/Documents.tsx', import.meta.url)),
    'utf8',
  )
  assert.match(page, /title="项目文档"/)
  assert.match(page, /起点、终点、内置约束三份文档/)
  assert.doesNotMatch(page, /目标（当前）/, 'the endpoint document is 终点, never 目标')
  const nav = readFileSync(fileURLToPath(new URL('../src/views/GraphView.tsx', import.meta.url)), 'utf8')
  assert.match(nav, /\{ id: 'docs', label: '项目文档'/, 'the nav entry names all three documents, not only one')
})

test('the goal card names the current goal and leaves the other open goals alone', () => {
  const source = readFileSync(
    fileURLToPath(new URL('../src/views/Documents.tsx', import.meta.url)),
    'utf8',
  )
  // One goal is the document; the card says so instead of leaving "目标" ambiguous.
  assert.match(source, /`\$\{meta\.title\}（当前）`/)
  assert.match(source, /另有 \{otherOpenGoals\} 条未收口目标，不在此处编辑/)
  assert.match(source, /api\.goals/)
  // The card points at the goal the server says it read, not at an inferred one.
  assert.match(source, /doc\.goal_id/)
})

test('an edit from the goal editor carries a revision, so a stale save is refused', () => {
  const api = readFileSync(fileURLToPath(new URL('../src/lib/api.ts', import.meta.url)), 'utf8')
  assert.match(
    api,
    /updateGoal: \(id: string, statement: string, start: string, revision: number\) =>[\s\S]{0,120}\{statement,start,revision\}/,
    'updateGoal must send the revision; without it the server answers 409',
  )
  const dialog = readFileSync(
    fileURLToPath(new URL('../src/components/GoalEditorDialog.tsx', import.meta.url)),
    'utf8',
  )
  assert.match(dialog, /api\.updateGoal\(goal\.id,statement,start,revision as number\)/)
  // No revision means no save: never a silent overwrite.
  assert.match(dialog, /revision\s*===\s*undefined/)
})
// Revisions are scoped to an individual goal, so a save must also carry its identity.
test('a goal document save keeps the goal id from the draft base', () => {
  assert.deepEqual(documentSaveBody({ revision: 1, goal_id: 'first' }, 'Draft'), {
    text: 'Draft', revision: 1, goal_id: 'first',
  })
})
