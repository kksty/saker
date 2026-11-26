// Display-rule tests for the console. node:test + type stripping, no DOM:
// these functions decide what the run page shows/filters and whether a
// direction may call itself "前提不成立", which is exactly where a wrong
// label was shipped before (failed/held directions were mislabelled).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  cancelCauseText,
  fmtAgo,
  hasBrokenPremise,
  purposeLabel,
  runCancelDetail,
  runCancelReason,
  runStatusLabel,
  runMatchesFilter,
} from '../src/lib/format.ts'
import type { FrontierItem, Intent, Run } from '../src/lib/types.ts'

test('operator handoff has the same label in run detail, fleet rows and timeline', () => {
  assert.equal(runStatusLabel({status: 'canceled', cancel_cause: 'operator_wait'}), '等待人工')
  assert.equal(runStatusLabel({status: 'canceled', cancel_cause: 'action_wait'}), '等待外部结果')
  // The review pause is its own switch (it cancels verifier runs only); a
  // canceled verifier run must not read as a plain operator stop, and the
  // project-deletion stop must not fall back to the raw enum.
  assert.equal(runStatusLabel({status: 'canceled', cancel_cause: 'review_pause'}), '复核暂停')
  assert.equal(runStatusLabel({status: 'canceled', cancel_cause: 'project_stop'}), '项目删除取消')
  assert.match(cancelCauseText('action_wait'), /保留会话并释放执行槽/)
  assert.equal(runStatusLabel({status: 'canceled', cancel_cause: 'operator'}), '人工取消')
})

function run(over: Partial<Run> = {}): Run {
  return {
    id: 'r_c23841ed',
    intent_id: 'in_f465cf06',
    node: 'local',
    status: 'canceled',
    started_at: '2026-11-05T09:11:20+08:00',
    events: 290,
    declared_facts: 0,
    ...over,
  } as Run
}

function intent(over: Partial<Intent> = {}): Intent {
  return {
    id: 'in_f465cf06',
    title: '8005 同源镜像疑似绕过 8040 WAF：/api 代理与文件下载重试',
    description: '',
    status: 'failed',
    attempts: 0,
    created_at: '2026-11-05T09:11:12+08:00',
    updated_at: '2026-11-05T09:28:56+08:00',
    ...over,
  } as Intent
}

test('purposeLabel names support runs in the operator language', () => {
  assert.equal(purposeLabel('commander'), '指挥官')
  assert.equal(purposeLabel('verifier'), '复核')
  assert.equal(purposeLabel('report'), '报告')
  assert.equal(purposeLabel(''), '')
  assert.equal(purposeLabel(undefined), '')
  assert.equal(purposeLabel('custom'), 'custom')
})

test('hasBrokenPremise only fires for missing/rejected from_facts', () => {
  const ready = (over: Partial<FrontierItem> = {}): FrontierItem => ({
    id: 'in_f465cf06',
    title: 'x',
    ready: true,
    ...over,
  })

  assert.equal(hasBrokenPremise(undefined), false)
  assert.equal(hasBrokenPremise(ready()), false)
  // failed/held directions are not dispatchable either, but that is a status,
  // not a broken premise: the detail already shows 失败/叫停.
  assert.equal(hasBrokenPremise(ready({ ready: false })), false)
  assert.equal(hasBrokenPremise(ready({ ready: false, blocked_on: [] })), false)
  assert.equal(hasBrokenPremise(ready({ ready: false, blocked_on: ['f_1（已否定）'] })), true)
})

test('runCancelReason surfaces who and why for a canceled run', () => {
  assert.equal(runCancelReason(undefined), '')
  assert.equal(runCancelReason(run({ status: 'done', exit_reason: '正常结束' })), '')
  assert.equal(
    runCancelReason(
      run({ status: 'canceled', cancel_cause: 'worker_kill', exit_reason: 'worker kill：operator' }),
    ),
    '被终止 · worker kill：operator',
  )
  assert.equal(
    runCancelReason(run({ status: 'canceled', cancel_cause: 'pause', exit_reason: '项目停止：复核被取消' })),
    '项目暂停 · 项目停止：复核被取消',
  )
  // Old rows carry the cause but no free-text reason: say so instead of hiding it.
  assert.equal(
    runCancelReason(run({ status: 'canceled', cancel_cause: 'restart', exit_reason: '' })),
    '重启取消（原因未记录）',
  )
})

test('the category is not said twice, and the reason after it stays verbatim', () => {
  // The operator's own words, as recorded on the run that was stopped.
  const operatorInstruction =
    '止损：操作员指令：白盒优先（本来就是想复现）。先停黑盒摸索，等白盒 in_epsagwl 给出各级 file:line 判定与 PoC 材料后再续跑'
  assert.equal(
    runCancelDetail(run({ status: 'canceled', cancel_cause: 'stop_loss', exit_reason: operatorInstruction })),
    '操作员指令：白盒优先（本来就是想复现）。先停黑盒摸索，等白盒 in_epsagwl 给出各级 file:line 判定与 PoC 材料后再续跑',
  )
  assert.equal(
    runCancelReason(run({ status: 'canceled', cancel_cause: 'stop_loss', exit_reason: operatorInstruction })),
    '止损取消 · 操作员指令：白盒优先（本来就是想复现）。先停黑盒摸索，等白盒 in_epsagwl 给出各级 file:line 判定与 PoC 材料后再续跑',
  )
  // Only the repeated head goes: a criterion behind it is the reason and must stay.
  assert.equal(
    runCancelDetail(run({ status: 'canceled', cancel_cause: 'stop_loss', exit_reason: '止损：连续多轮没有新事实或派发' })),
    '连续多轮没有新事实或派发',
  )
  assert.equal(runCancelDetail(run({ status: 'done' })), '')
})

test('cancelCauseText explains the mechanism behind the short label', () => {
  assert.match(cancelCauseText('worker_kill'), /终止/)
  assert.match(cancelCauseText('restart'), /重启/)
  assert.match(cancelCauseText('pause'), /暂停/)
  // A cancellation may leave the intent held. Its cause cannot promise a successor.
  assert.doesNotMatch(cancelCauseText('watchdog'), /自动重派/)
  assert.doesNotMatch(cancelCauseText('stop_loss'), /连续多轮|没有新事实/)
  // The review switch is a different stop than the project pause: a canceled
  // review must not read as "the whole engagement was paused".
  assert.match(cancelCauseText('review_pause'), /复核暂停/)
  assert.doesNotMatch(cancelCauseText('review_pause'), /项目暂停/)
  assert.equal(cancelCauseText(''), '')
  assert.equal(cancelCauseText(undefined), '')
})

test('runMatchesFilter finds a run from either side of the id boundary', () => {
  const r = run({ model: 'qwen3.8-27b', resolved_profile: '3' })
  const i = intent()

  assert.equal(runMatchesFilter(r, i, ''), true)
  assert.equal(runMatchesFilter(r, i, 'R_C23841ED'), true, 'run id, case-insensitive')
  assert.equal(runMatchesFilter(r, i, 'in_f465cf06'), true, 'intent id')
  assert.equal(runMatchesFilter(r, i, '同源镜像'), true, 'intent title')
  assert.equal(runMatchesFilter(r, i, 'qwen3.8'), true, 'model')
  assert.equal(runMatchesFilter(r, i, 'r_b95a0a62'), false)
})

test('runMatchesFilter searches support runs by purpose, not by a fake intent', () => {
  const commanderRun = run({ id: 'r_78e3ae5e', intent_id: '', purpose: 'commander' })

  assert.equal(runMatchesFilter(commanderRun, undefined, '指挥官'), true)
  assert.equal(runMatchesFilter(commanderRun, undefined, 'commander'), true)
  assert.equal(runMatchesFilter(commanderRun, undefined, 'r_78e3ae5e'), true)
  assert.equal(runMatchesFilter(commanderRun, undefined, '同源镜像'), false)
})

test('fmtAgo: minutes, hours, then a date past a day — the absolute time stays in title', () => {
  const now = new Date('2026-11-24T12:00:00+08:00').getTime()
  assert.equal(fmtAgo('2026-11-24T11:59:30+08:00', now), '刚刚')
  assert.equal(fmtAgo('2026-11-24T11:57:00+08:00', now), '3 分钟前')
  assert.equal(fmtAgo('2026-11-24T09:00:00+08:00', now), '3 小时前')
  assert.equal(fmtAgo('2026-11-22T09:00:00+08:00', now), '11-22')
  assert.equal(fmtAgo(undefined, now), '—')
  assert.equal(fmtAgo('not-a-time', now), 'not-a-time')
})
