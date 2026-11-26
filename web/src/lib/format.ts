import type { BadgeProps } from '@cyber/ui'
import type { StatusKind } from '@cyber/ui'
import type { FrontierItem, Intent, IntentWait, Run } from './types'

type BadgeVariant = NonNullable<BadgeProps['variant']>

/** Intent / run status → the status chip's visual language. */
export function statusKind(status?: string): StatusKind {
  switch (status) {
    case 'running':
    case 'claimed':
    case 'draft':
      return 'running'
    case 'pending':
    case 'waiting':
      return 'pending'
    case 'reviewing':
      return 'running'
    case 'done':
    case 'ready':
    case 'pass':
    case 'verified':
      return 'success'
    case 'failed':
    case 'error':
    case 'incomplete':
    case 'reject':
    case 'rejected':
    case 'review_failed':
      return 'error'
    case 'canceled':
    case 'stale':
    case 'withdrawn':
    case 'needs_evidence':
      return 'warning'
    case 'parked':
    case 'unverified':
      return 'idle'
    default:
      return 'info'
  }
}

const statusLabels: Record<string, string> = {
  pending: '待办',
  claimed: '认领',
  running: '运行中',
  waiting: '等待意图',
  reviewing: '复核中',
  done: '完成',
  failed: '失败',
  review_failed: '复核未通过',
  parked: '停放',
  canceled: '已取消',
  ready: '可用',
  stale: '已过期',
  draft: '生成中',
  incomplete: '校验未通过',
  unverified: '未复核',
  verified: '已复核',
  rejected: '已否决',
  withdrawn: '已撤回',
  pass: '通过',
  needs_evidence: '缺证据',
  reject: '否决',
}

export function statusLabel(status?: string): string {
  if (!status) return '—'
  return statusLabels[status] ?? status
}

/**
 * One sentence for the persisted wait (`intent.wait`), in the operator's words.
 * Children carry their current state, so 「等下游意图」 tells which one is still
 * running without opening each direction; a child that is no longer in the
 * ledger is said as such instead of being dropped from the sentence.
 */
export function intentWaitText(
  wait?: IntentWait,
): string {
  if (!wait) return ''
  if (wait.actions?.length) {
    return `等待 ${wait.actions.length} 个外部动作（${wait.mode === 'any' ? '任一结果' : '全部结果'}）`
  }
  if (wait.operator) {
    const state = wait.operator.resolution === 'unavailable' ? '（无法完成）' : ''
    return `等待人工：${wait.operator.request}${state}`
  }
  return ""
}

/**
 * What a run is for. An empty purpose is an executor attempt (it must have an
 * intent); commander/verifier/report are support runs that belong to no
 * direction, so the run page has to say which is which in words, not raw enum.
 */
export function purposeLabel(purpose?: string): string {
  switch (purpose) {
    case 'commander':
      return '指挥官'
    case 'verifier':
      return '复核'
    case 'report':
      return '报告'
    case 'embedding':
      return '检索向量化'
    case '':
    case undefined:
      return ''
    default:
      return purpose
  }
}

/** Run statuses need one more distinction: a steer is a continuation, not a stop. */
export function runStatusLabel(run?: { status?: string; cancel_cause?: string }): string {
  if (run?.status === 'canceled') {
    switch (run.cancel_cause) {
      case 'steer':
        return '转续话'
      case 'stop_loss':
        return '止损取消'
      case 'handoff':
        return '交接取消'
      case 'restart':
        return '重启取消'
      case 'watchdog':
        return '守卫取消'
      case 'operator':
        return '人工取消'
      case 'retry':
        return '重派取消'
      case 'worker_kill':
        return '被终止'
      case 'operator_wait':
        return '等待人工'
      case 'action_wait':
        return '等待外部结果'

      case 'pause':
        return '项目暂停'
      case 'review_pause':
        return '复核暂停'
      case 'model_stall':
        return '模型停滞'
      case 'round_timeout':
        return '单轮超时'
      case 'project_stop':
        return '项目删除取消'
    }
  }
  return statusLabel(run?.status)
}

/**
 * One sentence for *why* a canceled run was stopped, for the tooltip behind the
 * short label. The label is the same table runStatusLabel uses; this adds the
 * mechanism (who decides, what happens next), not a second vocabulary.
 */
export function cancelCauseText(cause?: string): string {
  switch (cause) {
    case 'steer':
      return '指挥官取消当前 turn、带轨迹续一轮：会话与已写证据保留'
    case 'retry':
      return '重派：本次 run 让位给同意图的新一次执行'
    case 'stop_loss':
      return '止损：收到停止当前运行的明确决定'
    case 'handoff':
      return '新执行者接班：旧 run 先停，新执行者从同一意图继续（同一统一模型连接）'
    case 'restart':
      return '进程重启中断运行，按记录完成对账'
    case 'watchdog':
      return '守卫取消：触发依据见该运行的取消记录'
    case 'operator':
      return '操作员在控制台人工取消（或重派前取消旧 run）'
    case 'worker_kill':
      return '执行者被终止：由执行者或操作员主动终止，意图不再自动重派'
    case 'operator_wait':
      return '人工交接：保留现场并释放执行槽，操作员完成后继续'
    case 'action_wait':
      return '等待外部动作：保留会话并释放执行槽，只在匹配结果或超时后继续'
    case 'pause':
      return '项目暂停：停止指挥官与所有执行者'
    case 'review_pause':
      return '复核暂停：取消正在跑的复核，不动指挥官与执行者（项目与复核是两个开关）'
    case 'project_stop':
      return '项目停止/删除：操作员终止整个项目，保留账本与证据'
    case 'model_stall':
      return '模型停滞：连续无有效输出，存活检查停止该 run'
    case 'round_timeout':
      return '指挥官单轮超过墙钟上限，平台取消该轮'
    case '':
    case undefined:
      return ''
    default:
      return `取消原因：${cause}`
  }
}

/**
 * Who set a direction's sticky stop (`intent.held_by`), in operator language.
 * A parent executor is recorded as its participant id (`pt_<intent>`); an
 * unknown/legacy value stays verbatim instead of being guessed into a role.
 */
export function holdActorLabel(actor?: string): string {
  if (!actor) return ''
  switch (actor) {
    case 'operator':
      return '操作员'
    case 'commander':
      return '指挥官'
    case 'watchdog':
      return '看门狗'
    case 'worker':
      return '执行者'
    case 'unknown':
      return '未记录'
    default:
      return actor.startsWith('pt_') ? '发起执行者' : actor
  }
}

/**
 * exit_reason often opens by naming the same category the label already says
 * ("止损：操作员指令：…"), so the card read "止损取消 · 止损：…". Drop only that
 * repeated head; the rest is the canceller's own words and stays verbatim —
 * a criterion like "连续多轮没有新事实或派发" must survive. The test is the
 * data itself: the token before the colon and the label are prefixes of each
 * other ("止损" / "止损取消"), so no second vocabulary is needed.
 */
function withoutRepeatedCause(raw: string, label: string): string {
  if (!label) return raw
  const sep = raw.search(/[：:]/)
  if (sep <= 0) return raw
  const head = raw.slice(0, sep).trim()
  if (!head) return raw
  const repeated = head === label || label.startsWith(head) || head.startsWith(label)
  return repeated ? raw.slice(sep + 1).trim() : raw
}

/**
 * A canceled run's own words, without the category: for surfaces that already
 * show runStatusLabel at the head of the row, so the body is only "who + why".
 */
export function runCancelDetail(run?: {
  status?: string
  cancel_cause?: string
  exit_reason?: string
}): string {
  if (run?.status !== 'canceled') return ''
  const raw = (run?.exit_reason ?? '').trim()
  if (!raw) return '（原因未记录）'
  return withoutRepeatedCause(raw, runStatusLabel(run))
}

/**
 * The visible "取消原因" sentence for a canceled run: the category once, then the
 * canceller's recorded reason. exit_reason is what the canceller actually wrote
 * (e.g. "worker kill：operator", "项目停止：复核被取消"); prefer it verbatim, and
 * say so plainly when older rows have none.
 */
export function runCancelReason(run?: {
  status?: string
  cancel_cause?: string
  exit_reason?: string
}): string {
  if (run?.status !== 'canceled') return ''
  const label = runStatusLabel(run)
  const detail = runCancelDetail(run)
  return detail === '（原因未记录）' ? `${label}${detail}` : `${label} · ${detail}`
}

const kindVariants: Record<string, BadgeVariant> = {
  vuln: 'danger',
  cred: 'caution',
  host: 'info',
  service: 'info',
  loot: 'success',
  artifact: 'primary',
  note: 'muted',
}

export function kindVariant(kind?: string): BadgeVariant {
  return kindVariants[kind ?? ''] ?? 'muted'
}

const originVariants: Record<string, BadgeVariant> = {
  declared: 'success',
  derived: 'info',
  heuristic: 'warning',
}

export function originVariant(origin?: string): BadgeVariant {
  return originVariants[origin ?? ''] ?? 'secondary'
}

/**
 * Severity vocabulary shared by the template library, vulnerability rows and the
 * graph inspector: one word must look the same everywhere, or the same finding
 * reads as two different levels depending on the page. The tiers are
 * critical / high / medium / low; the split between critical and high is "does
 * it need a prerequisite", decided at declaration, not a score. `info` stays
 * mapped because imported templates carry it (it is not a vulnerability fact).
 *
 * This module only paints and labels. Ordering and any ranking come from the
 * server — the console never sorts by severity on its own.
 */
const severityVariants: Record<string, BadgeVariant> = {
  critical: 'danger',
  high: 'caution',
  medium: 'warning',
  low: 'info',
  info: 'muted',
}

export function severityVariant(severity?: string): BadgeVariant {
  return severityVariants[(severity ?? '').toLowerCase()] ?? 'muted'
}

const severityDots: Record<string, string> = {
  critical: 'bg-destructive',
  high: 'bg-caution',
  medium: 'bg-warning',
  low: 'bg-info',
  info: 'bg-ink-3',
}

export function severityDot(severity?: string): string {
  return severityDots[(severity ?? '').toLowerCase()] ?? 'bg-ink-3'
}

const severityLabels: Record<string, string> = {
  critical: '严重',
  high: '高危',
  medium: '中危',
  low: '低危',
  info: '提示',
}

/** The operator-facing word; an unknown value is shown as-is, never guessed. */
export function severityLabel(severity?: string): string {
  const key = (severity ?? '').toLowerCase()
  return severityLabels[key] ?? severity ?? ''
}

/** The bucket a row falls in for filtering: a lowercase tier, or '' when the
 *  fact has no level. Unlabelled must stay distinct from `low` — an unjudged
 *  finding is not a low-risk one. */
export function severityBucket(severity?: string): string {
  return (severity ?? '').trim().toLowerCase()
}

/** Chip order for filters, as agreed: critical, high, medium, low, then
 *  unlabelled. This is the order of the controls, not of the rows: row order is
 *  the server's. */
export const SEVERITY_TIERS = ['critical', 'high', 'medium', 'low'] as const

// Pinned locale and `hourCycle: 'h23'` make the numeric fields independent of
// the host's locale/ICU default: an unpinned locale may render non-Latin digits
// or "24:00" for midnight with `hour12: false`. Only the numeric parts are used,
// so the output stays exactly "MM-DD HH:mm:ss"; the Date still carries the
// operator's local zone. `fmtTime(iso, timeZone)` is the deterministic test path.
const timeFields: Intl.DateTimeFormatOptions = {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23',
}

const localTimeFormat = new Intl.DateTimeFormat('en-US', timeFields)
const zonedTimeFormats = new Map<string, Intl.DateTimeFormat>()

function timeFormatFor(timeZone: string): Intl.DateTimeFormat {
  const cached = zonedTimeFormats.get(timeZone)
  if (cached) return cached
  const format = new Intl.DateTimeFormat('en-US', { ...timeFields, timeZone })
  zonedTimeFormats.set(timeZone, format)
  return format
}

function timeField(parts: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes): string {
  return parts.find((part) => part.type === type)?.value ?? ''
}

/** 2026-11-01T02:03:04+08:00 → 11-01 02:03:04 (local, no timezone noise).
 *  Pass `timeZone` (IANA name) for a fixed zone instead of the host's. */
export function fmtTime(iso?: string, timeZone?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const format = timeZone ? timeFormatFor(timeZone) : localTimeFormat
  const parts = format.formatToParts(d)
  return `${timeField(parts, 'month')}-${timeField(parts, 'day')} ${timeField(parts, 'hour')}:${timeField(parts, 'minute')}:${timeField(parts, 'second')}`
}

/** Duration since `from`; a still-running row uses `now`, injectable so callers
 *  and tests can compute a deterministic value without a wall clock. */
export function fmtDuration(from?: string, to?: string, now: number = Date.now()): string {
  if (!from) return '—'
  const start = new Date(from).getTime()
  const end = to ? new Date(to).getTime() : now
  if (Number.isNaN(start) || Number.isNaN(end)) return '—'
  const s = Math.max(0, Math.round((end - start) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/** Seconds since `iso`, or null when the value is absent/unparseable. Kept
 *  separate from fmtDuration because a caller that says "N 秒前" must fall back
 *  to the unknown placeholder, never to NaN. */
export function secondsSince(iso: string | undefined | null, now: number = Date.now()): number | null {
  if (!iso) return null
  const at = Date.parse(iso)
  if (!Number.isFinite(at)) return null
  return Math.max(0, Math.round((now - at) / 1000))
}

/** A known percentage (including a real 0) renders with its unit; a missing or
 *  non-finite observation is "未上报", not "—%" or "0%". */
export function fmtPercent(value?: number | null): string {
  if (value === undefined || value === null || !Number.isFinite(value)) return '未上报'
  return `${value}%`
}

/**
 * 相对时间，用于密集行：`刚刚` / `N 分钟前` /
 * `N 小时前`，超过一天就退回日期 `MM-DD`（绝对时刻仍由调用方放进 title）。
 * `now` 可注入，用例不依赖墙钟。
 */
export function fmtAgo(iso?: string, now: number = Date.now()): string {
  if (!iso) return '—'
  const at = new Date(iso).getTime()
  if (Number.isNaN(at)) return iso
  const s = Math.round((now - at) / 1000)
  if (s < 0) return '刚刚'
  if (s < 60) return '刚刚'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  // Older than a day: a date reads better than "31 小时前"; the exact time stays in title.
  const d = new Date(at)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// Byte units are not an Intl concept: the 1024 divisor and the "one decimal
// below 10" rule are the console's existing display contract, so keep the hand
// scaling instead of routing the number through a formatter that would change it.
export function fmtBytes(n?: number): string {
  if (!n || n < 0) return '—'
  const units = ['B', 'K', 'M', 'G', 'T']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)}${units[i]}`
}

// Built once: the row tables call this per cell, and constructing a formatter
// per cell is the expensive part. en-US keeps the grouping/digits the previous
// `toLocaleString('en-US')` produced, independent of the host locale.
const integerFormat = new Intl.NumberFormat('en-US')

export function fmtInt(n?: number): string {
  if (n === undefined || n === null) return '0'
  return integerFormat.format(n)
}

/** Seconds → "3h12m" / "42m"; the unattended uptime chip. */
export function fmtUptime(seconds?: number): string {
  if (!seconds || seconds <= 0) return '—'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h${String(m).padStart(2, '0')}m`
  return `${Math.max(1, m)}m`
}

/** First line, clipped — for table cells that must stay one row. */
export function oneLine(s?: string, max = 120): string {
  if (!s) return ''
  const line = s.split(/\r?\n/).find((l) => l.trim()) ?? ''
  return line.length > max ? `${line.slice(0, max)}…` : line
}

export function shortID(id?: string): string {
  if (!id) return '—'
  return id.length > 12 ? id.slice(0, 12) : id
}

/**
 * True only when the direction cites premise facts that are gone or rejected.
 * `frontier.ready` is also false for failed/held directions (they cannot be
 * dispatched either), so readiness alone is not a premise signal — the blocked
 * list is. The badge must not call every non-dispatchable direction a broken
 * premise.
 */
export function hasBrokenPremise(frontier?: FrontierItem): boolean {
  return (frontier?.blocked_on?.length ?? 0) > 0
}

/** One badge-sized marker on a frontier row; `detail` is the full tooltip. */
export interface FrontierMarker {
  key: 'premise_changed' | 'no_premise' | 'prerequisites'
  label: string
  detail: string
  tone: 'warning' | 'info' | 'muted' | 'secondary'
}

/** Compact fact-id list for a tooltip/one-line hint ("f_a、f_b 等 4 条"). */
export function frontierFactList(ids?: string[], cap = 3): string {
  const list = (ids ?? []).filter(Boolean)
  if (list.length === 0) return ''
  const shown = list.slice(0, cap)
  const hidden = list.length - shown.length
  return hidden > 0 ? `${shown.join('、')} 等 ${list.length} 条` : shown.join('、')
}

/**
 * Badges for one frontier row. Only fields the server actually reported are
 * emitted: an old board that predates the new fields yields no marker rather
 * than a guessed "无前提" or "0 前提". `no_premise` and `prerequisites` are read
 * independently so a malformed row is shown as reported instead of corrected.
 */
export function frontierMarkers(frontier?: FrontierItem): FrontierMarker[] {
  if (!frontier) return []
  const out: FrontierMarker[] = []
  const changed = frontier.premise_changed ?? []
  if (changed.length > 0) {
    out.push({
      key: 'premise_changed',
      label: `前提已变 ${changed.length}`,
      detail: `意图创建后，这些前提事实的断言被修改过：${frontierFactList(changed, 4)}。旧断言上的复核不能证明新内容；派发前请核对前提或重做复核。`,
      tone: 'warning',
    })
  }
  if (typeof frontier.prerequisites === 'number' && frontier.prerequisites > 0) {
    out.push({
      key: 'prerequisites',
      label: `前提 ${frontier.prerequisites}`,
      detail: `该意图依赖 ${frontier.prerequisites} 条硬前提（from_fact_ids）；缺一条或前提被否决都不能派发。`,
      tone: 'muted',
    })
  }
  if (frontier.no_premise) {
    out.push({
      key: 'no_premise',
      label: '无前提',
      detail:
        '该意图没有登记硬前提（from_fact_ids 为空）。引用/固定只是阅读上下文，不是前提。',
      tone: 'info',
    })
  }
  return out
}

/** One-line premise-changed warning for the row body; '' when nothing changed. */
export function frontierPremiseChangedText(frontier?: FrontierItem): string {
  const changed = frontier?.premise_changed ?? []
  return changed.length > 0 ? `前提已变：${frontierFactList(changed)}` : ''
}

/** Human label for the only project premise policy. */
export function premisePolicyLabel(policy?: string): string {
  return policy === 'verified_only' || !policy ? '证明优先' : policy
}

/** What the fixed policy actually enforces, in the operator's words. */
export function premisePolicyHint(policy?: string): string {
  if (!policy || policy === 'verified_only') {
    return '全部必要前提必须是可复核类型，且当前断言已独立复核、核验收据绑定当前断言、原件完整；待核验依赖可先登记，核验前不能派发。成功意图的 vuln/cred 结论会先进入复核。'
  }
  return ''
}

/**
 * The run page's filter. One box has to answer both kinds of question an
 * operator arrives with: "what is r_abc?" (run id) and "where did in_abc go?"
 * (intent id/title), plus the fields people remember (purpose, model, node).
 */
export function runMatchesFilter(run: Run, intent: Intent | undefined, needle: string): boolean {
  const q = needle.trim().toLowerCase()
  if (!q) return true
  return [
    run.id,
    run.intent_id,
    intent?.title,
    run.purpose,
    purposeLabel(run.purpose),
    run.model,
    run.resolved_profile,
    run.node,
    run.status,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(q)
}

