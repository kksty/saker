// Operator-authored bootstrap: a display name, the starting point (起点), the
// end state (终点), and the built-in constraint document.
//
// The name is only a display name: the directory id is a short random id
// (p-xxxxxxxx) decoupled from it, so renaming a project never moves its
// directory. 起点 becomes the project brief (scope); 终点 becomes the project's
// single endpoint goal at creation time — the commander never derives or splits
// either one. The standing constraints are a prefilled document the operator may
// edit or cancel (it becomes the project description).
import { useEffect, useRef, useState } from 'react'
import {
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Field,
  Input,
  Spinner,
  Switch,
  Textarea,
} from '@cyber/ui'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import { DEFAULT_PROJECT_CONSTRAINTS, projectDescriptionDraft } from '@/lib/project-defaults'
import { PROJECT_SCENARIOS, scenarioPreset, type ProjectScenario } from '@/lib/project-scenarios'
import { durationLabel } from '@/lib/settings'
import type { CreateProjectOptions } from '@/lib/types'

type ReviewChoice = 'inherit' | 'on' | 'off'

type RequiredField = 'name' | 'start' | 'end'

const REQUIRED_FIELDS: { field: RequiredField; message: string }[] = [
  { field: 'name', message: '项目名称不能为空：请填写一个显示名（不能用空白）。' },
  { field: 'start', message: '先写起点：它是项目简报，统领授权范围与已知状态。' },
  { field: 'end', message: '终点必填：它要能判断这次测试是否完成。' },
]

/** Reviews have two levels: the instance default and a per-project override.
 *  "跟随实例" is the absence of a value, so changing the default later still
 *  reaches this project; 开启/关闭 pin it. */
const REVIEW_CHOICES: { id: ReviewChoice; label: string }[] = [
  { id: 'inherit', label: '跟随实例' },
  { id: 'on', label: '开启' },
  { id: 'off', label: '关闭' },
]

export function NewProjectDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated?: () => void
}) {
  // The display name: required, and the only thing a rename would change.
  const [projectName, setProjectName] = useState('')
  // Where we start: becomes the project brief.
  const [start, setStart] = useState('')
  // What "done" means: becomes the project's single endpoint goal.
  const [end, setEnd] = useState('')
  // The built-in constraint document is on by default, editable, and can be
  // cancelled (off = no description); turning it back on restores the default.
  const [constraintsOn, setConstraintsOn] = useState(true)
  const [constraints, setConstraints] = useState(DEFAULT_PROJECT_CONSTRAINTS)
  // The scenario whose texts the two fields currently carry, if any. It is only
  // a shortcut: editing either field takes the chip back to "not applied".
  const [scenarioID, setScenarioID] = useState<string | null>(null)
  // Project options, applied before the first dispatch. Without them the operator
  // had to create the project, pause it, edit settings and resume — and the first
  // round already ran under the wrong defaults.
  //
  // Reviews are three-state because they have two levels: the instance default
  // (← --no-verify / the settings switch) and a per-project override. "跟随实例"
  // sends nothing, which is what makes the project follow a later instance change;
  // 开启/关闭 pin it.
  const [review, setReview] = useState<ReviewChoice>('inherit')
  const [maxRuns, setMaxRuns] = useState('')
  const [roundTimeout, setRoundTimeout] = useState('')
  const [maxRunsError, setMaxRunsError] = useState('')
  // Instance defaults, so the hints can name the number instead of saying
  // "follow the instance" and leaving the operator to guess it.
  const index = useLoad(api.projectIndex, [])
  const runtime = useLoad(api.runtimeSettings, [])
  const instanceMaxRuns = index.data?.max_runs
  const instanceRoundTimeout = index.data?.round_timeout_seconds
  const instanceReviewsOff = runtime.data?.reviews_disabled
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldErrors, setFieldErrors] = useState<Partial<Record<RequiredField, string>>>({})
  const nameRef = useRef<HTMLInputElement>(null)
  const startRef = useRef<HTMLTextAreaElement>(null)
  const endRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (open) {
      setFieldErrors({})
      setMaxRunsError('')
      setError('')
    }
  }, [open])

  const constraintsEdited = constraints !== DEFAULT_PROJECT_CONSTRAINTS

  /** The chip is lit only while the fields still carry that scenario's text. */
  const activeScenario = scenarioID ? PROJECT_SCENARIOS.find((scenario) => {
    if (scenario.id !== scenarioID) return false
    const preset = scenarioPreset(scenario)
    return Boolean(preset) && start === preset!.start && end === preset!.statement
  }) : undefined

  /**
   * The chip is a toggle, not a one-way fill. Pressing the lit one clears 起点 and
   * 终点 and returns to "no scenario"; pressing another fills from its preset.
   *
   * A scenario is starting text, not policy: it never turns 内置约束 on or off,
   * never widens what 起点 authorises, and never touches pause/resume. That
   * document belongs to whoever runs the engagement.
   */
  function applyScenario(scenario: ProjectScenario) {
    if (activeScenario?.id === scenario.id) {
      setScenarioID(null)
      setStart('')
      setEnd('')
      setFieldErrors((current) => ({ ...current, start: undefined, end: undefined }))
      return
    }
    const preset = scenarioPreset(scenario)
    if (!preset) return
    setScenarioID(scenario.id)
    setStart(preset.start)
    setEnd(preset.statement)
    setFieldErrors((current) => ({ ...current, start: undefined, end: undefined }))
  }

  function toggleConstraints(on: boolean) {
    setConstraintsOn(on)
    setConstraints(on ? DEFAULT_PROJECT_CONSTRAINTS : '')
  }

  /** First failing required field, in visual order, or null when complete. */
  function firstProblem(): { field: RequiredField; message: string } | null {
    if (!projectName.trim()) return REQUIRED_FIELDS[0]
    if (!start.trim()) return REQUIRED_FIELDS[1]
    if (!end.trim()) return REQUIRED_FIELDS[2]
    return null
  }

  async function submit(run: boolean) {
    if (busy) return
    const problem = firstProblem()
    if (problem) {
      // Validation belongs to the submit attempt, not to every keystroke: flag
      // all empty required fields at once, then route the focus to the first one
      // so its error is announced with the field.
      const next: Partial<Record<RequiredField, string>> = {}
      for (const rule of REQUIRED_FIELDS) {
        if (rule.field === 'name' ? !projectName.trim() : rule.field === 'start' ? !start.trim() : !end.trim()) next[rule.field] = rule.message
      }
      setFieldErrors(next)
      setError(problem.message)
      if (problem.field === 'name') nameRef.current?.focus()
      else if (problem.field === 'start') startRef.current?.focus()
      else endRef.current?.focus()
      return
    }
    setFieldErrors({})
    // Concurrency is the one option this form can reject locally; the request
    // would otherwise fail with a payload-level message and no field to land on.
    const runs = maxRuns.trim()
    if (runs && !/^\d+$/.test(runs)) {
      setMaxRunsError('执行者并发必须是非负整数；留空表示跟随实例。')
      setError('执行者并发必须是非负整数；留空表示跟随实例。')
      return
    }
    setMaxRunsError('')
    setBusy(true)
    setError('')
    try {
      // Only the keys the operator actually changed are sent: an empty field
      // means "inherit the instance", which is what an absent key already does.
      const options: CreateProjectOptions = {}
      if (review === 'on') options.reviews_disabled = false
      else if (review === 'off') options.reviews_disabled = true
      if (runs) options.max_runs = Number(runs)
      if (roundTimeout.trim()) options.round_timeout = roundTimeout.trim()
      const created = await api.createProject(
        projectName.trim(),
        start.trim(),
        end.trim(),
        projectDescriptionDraft(constraintsOn, constraints),
        options,
      )
      setProjectName('')
      setStart('')
      setEnd('')
      setScenarioID(null)
      setConstraintsOn(true)
      setConstraints(DEFAULT_PROJECT_CONSTRAINTS)
      setReview('inherit')
      setMaxRuns('')
      setRoundTimeout('')
      setMaxRunsError('')
      onOpenChange(false)
      onCreated?.()
      window.location.assign(
        `/p/${encodeURIComponent(created.id)}/?view=overview${run ? '&autorun=1' : ''}`,
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!busy) onOpenChange(next) }}>
      <DialogContent className="max-h-[90dvh] max-w-[calc(100%-2rem)] overflow-y-auto overscroll-contain sm:max-w-2xl" aria-busy={busy}>
        <DialogHeader>
          <DialogTitle>新建项目</DialogTitle>
          <DialogDescription>
            项目名称 + 起点 + 终点：起点写现在的授权范围与已知状态（成为项目简报），终点写这次要做到的可判真假状态（成为项目唯一目标）。名称只是显示名：目录使用稳定的短 ID（p-xxxxxxxx），之后改名不会改路径。内置约束文档随项目附带，可以编辑或取消。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <fieldset className="min-w-0 space-y-1.5">
            <legend className="mb-1.5 text-caption font-medium text-ink-2">场景（可选）</legend>
            <div className="flex flex-wrap items-center gap-1.5">
              {PROJECT_SCENARIOS.map((scenario) => (
                <Button
                  key={scenario.id}
                  type="button"
                  size="sm"
                  variant={activeScenario?.id === scenario.id ? 'default' : 'outline'}
                  aria-pressed={activeScenario?.id === scenario.id}
                  data-project-scenario={scenario.id}
                  onClick={() => applyScenario(scenario)}
                >
                  {scenario.label}
                </Button>
              ))}
            </div>
            <p className="text-[11px] leading-4 text-ink-3">
              选中一次只填起点与终点：不生成或改写内置约束，不改授权范围，也不改暂停语义。
            </p>
          </fieldset>

          <Field
            label="项目名称（必填）"
            hint="控制台与项目列表里的显示名，必填；目录使用与名称无关的稳定短 ID，改名不会改路径。"
          >
            <Input
              ref={nameRef}
              value={projectName}
              onChange={(e) => setProjectName(e.target.value)}
              required
              autoFocus
              aria-label="项目名称"
              aria-invalid={fieldErrors.name ? true : undefined} data-user-invalid={(fieldErrors.name ? true : undefined) ? 'true' : undefined}
              aria-describedby={fieldErrors.name ? 'new-project-name-error' : undefined}
              placeholder="例如：NX 门户外网评估"
            />
            {fieldErrors.name && <span id="new-project-name-error" className="block text-[11px] leading-4 text-destructive">{fieldErrors.name}</span>}
          </Field>

          <Field
            label="起点（必填）"
            hint={fieldErrors.start ? undefined : '授权范围、已知资产、背景与已经确认的部分；这段话会作为项目简报原样交给指挥官与执行者。'}
          >
            <Textarea
              ref={startRef}
              rows={4}
              value={start}
              onChange={(e) => setStart(e.target.value)}
              required
              aria-invalid={fieldErrors.start ? true : undefined} data-user-invalid={(fieldErrors.start ? true : undefined) ? 'true' : undefined}
              aria-describedby={fieldErrors.start ? 'new-project-start-error' : undefined}
              placeholder={
                '例如：评估 example.com 的外网门户，起点是首页和登录口，已知一台测试账号；\n' +
                '只在工作时间测试，不碰与门户无关的资产。'
              }
            />
            {fieldErrors.start && <span id="new-project-start-error" className="block text-[11px] leading-4 text-destructive">{fieldErrors.start}</span>}
          </Field>

          <Field
            label="终点（必填）"
            hint={fieldErrors.end ? undefined : '这次测试要达成的最终状态，写成能判断真假的一句话；不会自动评分，由你（和指挥官）对照事实判断是否达成。'}
          >
            <Textarea
              ref={endRef}
              rows={3}
              value={end}
              onChange={(e) => setEnd(e.target.value)}
              required
              aria-invalid={fieldErrors.end ? true : undefined} data-user-invalid={(fieldErrors.end ? true : undefined) ? 'true' : undefined}
              aria-describedby={fieldErrors.end ? 'new-project-end-error' : undefined}
              placeholder="例如：证明外网门户存在未授权访问或可复现的高危漏洞，并为每条结论留下可复核的原始证据"
            />
            {fieldErrors.end && <span id="new-project-end-error" className="block text-[11px] leading-4 text-destructive">{fieldErrors.end}</span>}
          </Field>

          <div className="rounded-lg border border-hairline bg-surface-2/40">
            <div className="flex items-start gap-3 px-3 py-2.5">
              <Switch
                checked={constraintsOn}
                onCheckedChange={toggleConstraints}
                id="builtin-constraints"
              />
              <label
                htmlFor="builtin-constraints"
                className="min-w-0 flex-1 cursor-pointer text-[12px] leading-relaxed text-ink-2"
              >
                <span className="block font-medium text-ink">内置约束文档（可编辑，可取消）</span>
                约定测试范围、时间和目标数据保护，随项目进入派发与决策。可编辑或关闭；证据与结论的验收要求由账本与平台规则持续执行。
              </label>
            </div>
            {constraintsOn ? (
              <div className="border-t border-hairline p-3">
                <Textarea
                  rows={10}
                  aria-label="内置约束文档"
                  className="font-mono text-[12px]"
                  value={constraints}
                  onChange={(e) => setConstraints(e.target.value)}
                />
                <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
                  <span className="saker-nums">
                    {constraintsEdited ? '已修改内置默认文本' : '内置默认文本'} · {constraints.trim().length} 字符
                  </span>
                  {constraintsEdited && (
                    <Button
                      type="button"
                      size="xs"
                      variant="ghost"
                      onClick={() => setConstraints(DEFAULT_PROJECT_CONSTRAINTS)}
                    >
                      恢复内置默认
                    </Button>
                  )}
                </div>
              </div>
            ) : (
              <div className="border-t border-hairline px-3 py-2 text-[11px] text-ink-3">
                已取消：本次创建不发送约束文档（项目描述为空）。
              </div>
            )}
          </div>

          {/* Options are applied before the first dispatch: without them the first
              round runs under the instance defaults and the operator has to pause,
              edit settings and resume to correct it. */}
          <div className="rounded-lg border border-hairline bg-surface-2/40">
            <div className="px-3 py-2.5">
              <div className="text-[12px] font-medium text-ink">项目选项（创建时生效）</div>
              <p className="mt-0.5 text-[11px] leading-relaxed text-ink-3">
                留空即跟随实例默认；选项在首次派发之前写入这个项目。
              </p>
            </div>
            <div className="space-y-3 border-t border-hairline px-3 py-2.5">
              <fieldset className="min-w-0 space-y-1.5">
                <legend className="mb-1.5 text-caption font-medium text-ink-2">复核</legend>
                <div className="flex flex-wrap gap-1.5">
                  {REVIEW_CHOICES.map((choice) => (
                    <Button
                      key={choice.id}
                      type="button"
                      size="sm"
                      variant={review === choice.id ? 'default' : 'outline'}
                      aria-pressed={review === choice.id}
                      data-review-choice={choice.id}
                      onClick={() => setReview(choice.id)}
                    >
                      {choice.label}
                    </Button>
                  ))}
                </div>
                <p className="text-[11px] leading-4 text-ink-3">
                  跟随实例时读实例默认（当前：{instanceReviewsOff === undefined ? '…' : instanceReviewsOff ? '关闭' : '开启'}）；选开启或关闭会把这个项目钉住，之后改实例默认它也不跟随。
                </p>
                <p className="text-[11px] leading-4 text-ink-3">
                  开启：意图结束后由独立复核者重跑，通过才算完成。关闭：本项目从一开始就不产生复核 run，未复核事实按原文可见。
                </p>
              </fieldset>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field
                  label="执行者并发"
                  hint={`留空跟随实例（${instanceMaxRuns ?? '…'}）；0 表示不限`}
                  error={maxRunsError || undefined}
                >
                  <Input
                    value={maxRuns}
                    onChange={(e) => { setMaxRunsError(''); setMaxRuns(e.target.value) }}
                    aria-label="新项目的执行者并发"
                    aria-invalid={maxRunsError ? true : undefined}
                    type="number"
                    min={0}
                    step={1}
                    inputMode="numeric"
                    placeholder="跟随实例"
                  />
                </Field>
                <Field label="指挥官单轮时限" hint={`留空跟随实例（${durationLabel(instanceRoundTimeout)}）；0 表示不限`}>
                  <Input
                    value={roundTimeout}
                    onChange={(e) => setRoundTimeout(e.target.value)}
                    aria-label="新项目的指挥官单轮时限"
                    spellCheck={false}
                    autoCapitalize="none"
                    placeholder="例如 15m / 90s"
                  />
                </Field>
              </div>
            </div>
          </div>

          {error && (
            <div role="alert">
              <Callout tone="destructive">{error}</Callout>
            </div>
          )}
          <Callout tone="info">
            「创建并开始」会立刻让指挥官跑一轮（需要已配置模型）；失败会在概览页提示，不会静默重试。只想先建项目就选「只创建」。
          </Callout>
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button
            variant="outline"
            onClick={() => void submit(false)}
            disabled={busy}
          >
            只创建
          </Button>
          <Button
            onClick={() => void submit(true)}
            disabled={busy}
            aria-busy={busy}
          >
            {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
            {busy ? '创建中…' : '创建并开始'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
