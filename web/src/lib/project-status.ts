import type { BoardResponse, ProjectAction, ProjectActions, ProjectNote } from './types.ts'

/**
 * The overview's first block, as data: what the engagement is doing right now
 * and what happens next.
 *
 * The action contract (`project_actions`, internal/api/project_actions.go) is
 * authoritative when present: it decides 「已收口」 from open goals, pending
 * directions, unread messages and a running closeout, and it says why a button
 * is unavailable. This module never re-derives that verdict — it only orders
 * what the backend returned. The counters are the fallback for an older board.
 */
export interface ProjectStatusSummary {
  paused: boolean
  /** The backend's pause reason; empty while running. */
  reason: string
  /** Machine code for the pause (`manual_pause`, `completion_standby`, …). */
  kind: string
  goalsMet: number
  goalsTotal: number
  /** Directions the commander still has to dispatch. */
  directionsPending: number
  /** Frontier directions whose premise does not hold. */
  directionsBlocked: number
  /** Directions that yielded their turn (children/operator/external action). */
  waiting: number
  failedRuns: number
  running: number
  /** Backend sentences that must be said next to the buttons (a closed goal and
   *  open work can both be true); empty when there is nothing two-sided to say. */
  notes: ProjectNote[]
  /** The action contract's entries, so the banner and this block read one source. */
  resume?: ProjectAction
  pause?: ProjectAction
  /** One line answering "what happens next". */
  nextStep: string
}

function actionOf(contract: ProjectActions | undefined, id: string): ProjectAction | undefined {
  return contract?.actions.find((action) => action.id === id)
}

export function projectStatusSummary(
  board: BoardResponse | null | undefined,
): ProjectStatusSummary | undefined {
  if (!board) return undefined
  const contract = board.project_actions
  const paused = contract ? contract.state.paused : Boolean(board.project_paused || board.decider.paused)
  const reason = paused ? (contract?.state.reason ?? board.decider.paused_reason ?? '').trim() : ''
  const goalsMet = contract ? contract.facts.goals_met : board.goals.met
  const goalsTotal = contract ? contract.facts.goals_total : board.goals.total
  const directionsPending = contract
    ? contract.facts.pending_directions
    : Math.max(0, board.intents.pending - (board.intents.held ?? 0))
  const frontier = board.frontier ?? []
  // `ready:false` on its own is not a premise signal: the board also clears it
  // for failed / review_failed / held intents (internal/board/board.go:114-121),
  // and those need a retry or an explicit resume, not a premise check. Only the
  // blocked list means "a from_fact is gone or rejected", so only it counts here.
  const directionsBlocked = frontier.filter((item) => (item.blocked_on?.length ?? 0) > 0).length
  const waiting = board.waiting_intents?.length ?? 0
  const failedRuns = board.runs.failed
  const running = board.runs.running
  const notes = contract?.notes ?? []
  const resume = actionOf(contract, 'resume')
  const pause = actionOf(contract, 'pause')

  let nextStep: string
  if (notes.length > 0) {
    // The backend's own sentence(s): it can say two things at once, which is
    // exactly the case a single status word loses.
    nextStep = notes.map((note) => note.text).join('；')
  } else if (paused) {
    // No contract on an older board: say the reason and the one way out, and
    // never rule on whether 继续 would be refused — only the backend knows.
    nextStep = resume
      ? resume.reason
      : `${reason ? `暂停中：${reason}。` : '暂停中。'}点「继续项目」恢复决策。`
  } else if (failedRuns > 0) {
    nextStep = `有 ${failedRuns} 个失败运行：先去「运行」看那一轮的错误，再决定是否重试。`
  } else if (directionsBlocked > 0) {
    nextStep = `有 ${directionsBlocked} 个意图被前提挡住：在图谱里核对前提是否还成立，再决定改道或停止。`
  } else if (waiting > 0) {
    nextStep = `${waiting} 个意图在等下游意图、你或外部动作；它们已让出槽位，不算卡住。`
  } else if (directionsPending > 0) {
    nextStep = `${directionsPending} 个意图待派发：指挥官下一轮会处理。`
  } else if (running > 0) {
    nextStep = `${running} 个 run 在跑：等这一轮结束，或直接下发一条新指令。`
  } else {
    nextStep = '没有排队、没有阻塞：下发一条指令，或新建一个目标。'
  }

  return {
    paused,
    reason,
    kind: contract?.state.kind ?? board.project_pause_kind ?? '',
    goalsMet,
    goalsTotal,
    directionsPending,
    directionsBlocked,
    waiting,
    failedRuns,
    running,
    notes,
    resume,
    pause,
    nextStep,
  }
}
