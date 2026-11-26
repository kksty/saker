// The harness evaluator's round verdict, as it lands in a run's status events.
//
// saker folds the evaluator detail into the status text
// (`eval_end 第2/3轮：通过 找到可复现 SQLi`, see internal/aopexec/eval.go), so the
// console parses that text instead of assuming a second wire schema. Every
// parser here returns null/absent for events that are not evaluator statuses:
// "not judged" must never render as "failed".

import type { Event } from './types.ts'

export type EvaluatorVerdict = 'pass' | 'fail' | 'error' | 'start' | 'unknown'

export interface EvaluatorResult {
  /** eval_start | eval_end | eval_error */
  state: string
  verdict: EvaluatorVerdict
  round?: number
  maxRounds?: number
  /** The status text with the prefix/round/verdict stripped; '' when none. */
  reason: string
  /** The status text as journaled. */
  text: string
  at?: string
  /** Index in the event array, for highlighting. */
  eventIndex: number
}

const EVAL_STATES = new Set(['eval_start', 'eval_end', 'eval_error'])

/**
 * Who judges: the harness evaluator is an independent model request made with
 * the *current model connection* (the same model the executor ran), not JEV.
 * JEV is only a guardrail judge/accelerator and has nothing to do with this
 * pass/fail verdict — the run page must say so or the verdict is misread.
 */
export const evaluatorProvenanceText =
  '评估由 harness 用当前模型连接（与执行者同一模型）独立请求执行，不是 JEV；JEV 只作工具裁判/加速，与此处的通过/未通过判定无关。'

function evaluatorState(event: Event): string {
  if (event.state && EVAL_STATES.has(event.state)) return event.state
  // The folded text is only trusted on a status event: a tool call whose text
  // happens to start with "eval_end" is not an evaluator verdict.
  if (event.kind !== 'status') return ''
  const match = /^(eval_start|eval_end|eval_error)\b/.exec((event.text ?? '').trim())
  return match ? match[1] : ''
}

/** Parse one status event; null for anything that is not an evaluator status. */
export function parseEvaluatorEvent(event: Event, index = -1): EvaluatorResult | null {
  const state = evaluatorState(event)
  if (!state) return null
  const text = (event.text ?? '').trim()
  const rounds = /第\s*(\d+)\s*\/\s*(\d+)\s*轮/.exec(text)
  let verdict: EvaluatorVerdict
  if (state === 'eval_start') verdict = 'start'
  else if (state === 'eval_error') verdict = 'error'
  else if (text.includes('未通过')) verdict = 'fail'
  else if (text.includes('通过')) verdict = 'pass'
  else verdict = 'unknown'
  // Strip the machine prefix so the reason reads as the evaluator's sentence:
  // "eval_end 第2/3轮：通过 找到可复现 SQLi" → "找到可复现 SQLi".
  const reason = text
    .replace(/^eval_(start|end|error)\s*/, '')
    .replace(/第\s*\d+\s*\/\s*\d+\s*轮\s*/, '')
    .replace(/^[：:]\s*(未通过|通过)\s*/, '')
    .replace(/^(未通过|通过)\s*/, '')
    .trim()
  const result: EvaluatorResult = {
    state,
    verdict,
    reason,
    text,
    eventIndex: index,
  }
  if (rounds) {
    result.round = Number(rounds[1])
    result.maxRounds = Number(rounds[2])
  }
  if (event.at) result.at = event.at
  return result
}

/** Every evaluator status in the stream, in journal order. */
export function parseEvaluatorResults(events: Event[]): EvaluatorResult[] {
  const out: EvaluatorResult[] = []
  events.forEach((event, index) => {
    const parsed = parseEvaluatorEvent(event, index)
    if (parsed) out.push(parsed)
  })
  return out
}

/**
 * The verdict the run page should surface: the last judged round (pass/fail/
 * error). A lone eval_start (still running or killed before judging) is shown
 * as "进行中" instead of inventing a verdict.
 */
export function latestEvaluatorResult(results: EvaluatorResult[]): EvaluatorResult | null {
  for (let i = results.length - 1; i >= 0; i--) {
    if (results[i].verdict !== 'start') return results[i]
  }
  return results.length > 0 ? results[results.length - 1] : null
}

export function evaluatorVerdictLabel(result: EvaluatorResult): string {
  switch (result.verdict) {
    case 'pass':
      return '评估通过'
    case 'fail':
      return '评估未通过'
    case 'error':
      return '评估错误'
    case 'start':
      return '评估进行中'
    default:
      return '评估无结论'
  }
}

export function evaluatorBadgeVariant(
  result: EvaluatorResult,
): 'success' | 'warning' | 'danger' | 'info' | 'muted' {
  switch (result.verdict) {
    case 'pass':
      return 'success'
    case 'fail':
      return 'warning'
    case 'error':
      return 'danger'
    case 'start':
      return 'info'
    default:
      return 'muted'
  }
}

/** `第2/3轮`, or '' when the host did not report round numbers. */
export function evaluatorRoundText(result: EvaluatorResult): string {
  if (result.round === undefined && result.maxRounds === undefined) return ''
  const round = result.round ?? '?'
  const max = result.maxRounds ?? '?'
  return `第${round}/${max}轮`
}

/** The evaluator's sentence, or an honest fallback per verdict. */
export function evaluatorSummaryText(result: EvaluatorResult): string {
  if (result.reason) return result.reason
  switch (result.verdict) {
    case 'pass':
      return '宿主判定本轮达到验收条件。'
    case 'fail':
      return '宿主判定本轮未达到验收条件，但没有给出原因。'
    case 'error':
      return '评估器自身失败，本轮没有被判定。'
    case 'start':
      return '评估已开始，等待本轮结论。'
    default:
      return '状态文本里没有可判定的结论。'
  }
}
