// Versioned messages from the operator to the commander.
//
// The surface exists because the one-shot 下发 composer must stay one-shot: a
// hint once sent there was silently turned into a standing rule and polluted
// every later dispatch. A persistent directive is the same sentence with a
// different lifetime, so it gets its own explicit create action and its own
// vocabulary — "guidance", never "authorization". Boundaries live in the three
// project documents alone.
import { ApiError } from './api.ts'
import { directiveKeyFor, randomRequestKey, type DirectiveKeyState } from './directive.ts'
import type {
  DirectiveRequest,
  DirectiveResponse,
  Instruction,
  InstructionHistoryResponse,
  Intent,
} from './types.ts'

/** The server's own limit (store.MaxInstructionRunes); mirrored so the counter
 *  and the server refusal cannot disagree. */
export const MAX_INSTRUCTION_RUNES = 4000

export const INSTRUCTION_GUIDANCE =
  '保存给指挥官的偏好、待办和提醒，可修改、可撤销。'

export const INSTRUCTION_EMPTY =
  '单次沟通可用通信页的「下发」。'

export type InstructionScope = 'project' | 'intent'

export interface InstructionDraft {
  text: string
  scope: InstructionScope
  intentID: string
}

/** Same content + same scope = same request; a retried submit must not create a
 *  second instruction row. */
export function instructionFingerprint(draft: InstructionDraft): string {
  return JSON.stringify([
    draft.text.trim(),
    draft.scope,
    draft.scope === 'intent' ? draft.intentID.trim() : '',
  ])
}

/** Reuse the key for an unchanged draft; regenerate as soon as anything moves. */
export function instructionKeyFor(
  state: DirectiveKeyState,
  fingerprint: string,
  generate: () => string = randomRequestKey,
): DirectiveKeyState {
  return directiveKeyFor(state, fingerprint, generate)
}

/** What both the create form and the inline edit refuse locally, before the
 *  server sees the body. */
function instructionTextError(text: string): string | undefined {
  const trimmed = text.trim()
  if (!trimmed) return '写一条消息再提交。'
  if ([...trimmed].length > MAX_INSTRUCTION_RUNES) return `消息最多 ${MAX_INSTRUCTION_RUNES} 字。`
  return undefined
}

/** What the create form must refuse locally, before the server sees it. */
export function instructionDraftError(draft: InstructionDraft): string | undefined {
  const textError = instructionTextError(draft.text)
  if (textError) return textError
  if (draft.scope === 'intent' && !draft.intentID.trim()) return '选一个关联意图，或选择整个项目。'
  return undefined
}

/** The same body rules for an edit: empty or overlong text never reaches the
 *  wire, so the counter and the save button cannot disagree with the server. */
export function instructionUpdateDraftError(text: string): string | undefined {
  return instructionTextError(text)
}

/** POST /api/directives: persistent=true is what makes this an instruction
 *  rather than a message; the server's validation keeps the two apart. */
export function instructionCreateBody(draft: InstructionDraft, idempotencyKey: string): DirectiveRequest {
  const body: DirectiveRequest = {
    text: draft.text.trim(),
    target: draft.scope,
    persistent: true,
    steer: false,
    idempotency_key: idempotencyKey,
  }
  if (draft.scope === 'intent') body.intent_id = draft.intentID.trim()
  return body
}

export function instructionCreateError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'directive_invalid') return error.message
    if (error.code === 'directive_target_missing') return '这个意图不在了：刷新后再选。'
    if (error.status === 403) return '只有操作员能保存持续指令。'
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}

/** Editing and revoking are both versioned updates: the version this page read
 *  travels back, so a row changed in another window is refused instead of
 *  silently overwritten. */
export interface InstructionWriteBody {
  version: number
  text: string
  state: Instruction['state']
}

function instructionWriteBody<S extends Instruction['state']>(
  instruction: Instruction,
  text: string,
  state: S,
): InstructionWriteBody & { state: S } {
  return { version: instruction.version, text: text.trim(), state }
}

/** The edited body keeps the row's own state; an edit never revives or revokes. */
export function instructionUpdateBody(instruction: Instruction, text: string): InstructionWriteBody {
  return instructionWriteBody(instruction, text, instruction.state)
}

/** Revoking keeps the text this page read, so the server can tell "same row" from
 *  "stale row". */
export function instructionRevokeBody(instruction: Instruction): InstructionWriteBody & { state: 'revoked' } {
  return instructionWriteBody(instruction, instruction.text, 'revoked')
}

/** Edit and revoke fail the same way; only the copy names the action. */
function instructionWriteError(
  error: unknown,
  copy: { conflict: string; missing: string; forbidden?: string },
): string {
  if (error instanceof ApiError) {
    if (error.status === 409) return copy.conflict
    if (error.status === 404) return copy.missing
    if (copy.forbidden && error.status === 403) return copy.forbidden
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}

export function instructionUpdateError(error: unknown): string {
  // Anything other than a conflict or a vanished row keeps the server's own
  // reason: a 400 (capacity/body) is more specific than any local copy.
  return instructionWriteError(error, {
    conflict: '正文已被其他窗口改过（版本冲突）：刷新后再改。',
    missing: '这条指令不在了：刷新后再试。',
  })
}

export function instructionRevokeError(error: unknown): string {
  return instructionWriteError(error, {
    conflict: '消息版本冲突。刷新后再撤销。',
    missing: '这条指令不在了：刷新后再试。',
    forbidden: '只有操作员能撤销持续指令。',
  })
}

export function instructionStateLabel(state: Instruction['state']): string {
  return state === 'revoked' ? '已撤销' : '生效中'
}

/** "整个项目" or the direction's title; an id the ledger cannot title stays an
 *  id instead of an empty quote. */
export function instructionScopeLabel(
  instruction: Instruction,
  titles: ReadonlyMap<string, string> | Record<string, string> = {},
): string {
  if (!instruction.intent_id) return '整个项目'
  const title = titles instanceof Map
    ? titles.get(instruction.intent_id)
    : (titles as Record<string, string>)[instruction.intent_id]
  return title ? `意图「${title}」` : `意图 ${instruction.intent_id}`
}

/** Active first, newest first inside each group: revoked rows are history and
 *  must not push the standing set down. */
export function orderInstructions(rows: readonly Instruction[]): Instruction[] {
  const at = (row: Instruction) => row.updated_at || row.created_at || ''
  return [...rows].sort((a, b) => {
    const state = Number(a.state === 'revoked') - Number(b.state === 'revoked')
    if (state !== 0) return state
    return at(b).localeCompare(at(a))
  })
}

export function activeInstructionCount(rows: readonly Instruction[]): number {
  return rows.filter((row) => row.state !== 'revoked').length
}

export interface InstructionRevision {
  version: number
  operation: string
  at?: string
  state: Instruction['state']
  text: string
}

const OPERATION_LABEL: Record<string, string> = {
  create: '新建',
  update: '修订',
  baseline: '初始',
  move: '位置变化',
}

export function instructionOperationLabel(operation?: string): string {
  if (!operation) return '修订'
  return OPERATION_LABEL[operation] ?? operation
}

/** Turn raw change rows into what the history list draws. `value` is the
 *  instruction at that revision, so its text/version/state are the revision's;
 *  the change row's own version is only a fallback. */
export function instructionRevisions(
  page: InstructionHistoryResponse | null | undefined,
): InstructionRevision[] {
  return (page?.history ?? [])
    .map((change) => ({
      version: change.value?.version ?? change.version,
      operation: instructionOperationLabel(change.operation),
      at: change.at,
      state: change.value?.state ?? 'active',
      text: change.value?.text ?? '',
    }))
    .sort((a, b) => b.version - a.version)
}

export function instructionCreatedReceipt(result: DirectiveResponse): string {
  if (result.created === false) return '重复提交，已复用同一条持续指令。'
  return '已保存，指挥官后续可见。'
}

export function instructionRevokedReceipt(instruction: Instruction): string {
  return `已撤销 v${instruction.version}，可查阅历史版本。`
}

export function instructionUpdatedReceipt(instruction: Instruction): string {
  return `已修订至 v${instruction.version}，指挥官下一轮可见。`
}

/** A message may refer to any existing intent, including completed intents. */
export function instructionIntentOptions(
  intents: readonly Pick<Intent, 'id' | 'title' | 'status'>[],
): Pick<Intent, 'id' | 'title' | 'status'>[] {
  return intents.map((intent) => ({ id: intent.id, title: intent.title, status: intent.status }))
}
