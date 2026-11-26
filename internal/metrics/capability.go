package metrics

// P3-H capability-use metrics: five read-only measurements over the run journal
// and the HTTP traffic capture index, appended after the P0-5 seven (six metric
// keys: the round distribution is reported as a maximum and a mean). They answer
// "was the capability used, and did its structured outcome work" without
// touching the ledger schema:
//
//   - eval_pass_rate / eval_rounds_max / eval_rounds_avg: the harness
//     evaluator's structured verdict on run journal status events
//     (internal/executor.Event.EvalPass/EvalRound/EvalMaxRounds);
//   - guardrail_actions: the tool-approval audit lines the API writes as
//     kind=guardrail (internal/api/guardrail.go) plus the harness guardrail
//     runtime's own verdicts, kind=guardrail_decision / kind=guardrail_review
//     (internal/aopexec/guardrail.go);
//   - traffic_body_coverage: the body-fidelity flags of preserved HTTP captures
//     (internal/traffic.Capture.PreviewOnly/Truncated);
//   - media_preserved: the kind=media rows the executor emits for preserved
//     media (internal/aopexec/media.go, bytes carried in the event's Raw JSON).
//
// Ground rules are the same as vertical.go: only structured fields participate,
// a missing signal is unavailable with a reason (except where the task says 0 is
// the honest reading), no title/text/keyword classification happens, and no
// threshold is applied. The caller does the file IO and passes the parsed rows
// in the Snapshot, so a unit test can build journals/captures without touching a
// real project directory.

import (
	"encoding/json"

	"github.com/kksty/saker/internal/executor"
)

// Capability metric keys, appended to the P0-5 keys. Existing keys, units and
// numerator/denominator meanings are unchanged.
const (
	KeyEvalPassRate        = "eval_pass_rate"
	KeyEvalRoundsMax       = "eval_rounds_max"
	KeyEvalRoundsAvg       = "eval_rounds_avg"
	KeyGuardrailActions    = "guardrail_actions"
	KeyTrafficBodyCoverage = "traffic_body_coverage"
	KeyMediaPreserved      = "media_preserved"
)

// Capability-use metric orders. Orders 1-7 are the P0-5 vertical metrics.
const (
	orderEvalPassRate        = 8
	orderEvalRoundsMax       = 9
	orderEvalRoundsAvg       = 10
	orderGuardrailActions    = 11
	orderTrafficBodyCoverage = 12
	orderMediaPreserved      = 13
)

// Journal event kinds owned by other packages (no import: internal/api and
// internal/aopexec would create an import cycle through metrics).
const (
	journalKindGuardrail = "guardrail" // internal/api/guardrail.go
	journalKindMedia     = "media"     // internal/aopexec/media.go (MediaKind)
)

// Guardrail metric vocabulary. kind=guardrail is the operator's read/resolve
// audit line (internal/api/guardrail.go); kind=guardrail_decision /
// kind=guardrail_review are the harness guardrail runtime's own verdicts
// (executor.KindGuardrailDecision / KindGuardrailReview, decoded by
// internal/aopexec/guardrail.go). Each journal row counts in exactly one
// breakdown bucket so the breakdown sums to the metric value.
const (
	// guardrailAction* are the kind=guardrail resolve decisions, split by the
	// structured approve boolean.
	guardrailActionResolveApprove = "resolve_approve"
	guardrailActionResolveReject  = "resolve_reject"

	// decisionAction* are the kind=guardrail_decision verdicts
	// (executor.Event.GuardrailAction).
	decisionActionRecord = "record"
	decisionActionReview = "review"
	decisionActionBlock  = "block"

	// reviewState* are the kind=guardrail_review lifecycle values
	// (executor.Event.GuardrailState).
	reviewStatePending  = "pending"
	reviewStateApproved = "approved"
	reviewStateRejected = "rejected"
	reviewStateExpired  = "expired"
	reviewStateCanceled = "canceled"
)

// Evaluator status states (internal/aopexec: the harness's status payload).
const (
	evalStateStart = "eval_start"
	evalStateEnd   = "eval_end"
	evalStateError = "eval_error"
)

// JournalEvent mirrors the structured fields of one run-journal line that the
// capability-use metrics read. The journal is `json.Marshal(executor.Event)` or
// a synthetic control event (guardrail); decoding is tolerant and unknown fields
// are ignored, exactly as the console's event tail does.
//
// Raw is the event's opaque JSON payload and is only read for kind=media, where
// internal/aopexec.Media carries the preserved byte count.
type JournalEvent struct {
	Kind          string `json:"kind"`
	State         string `json:"state,omitempty"`
	EvalRound     int    `json:"eval_round,omitempty"`
	EvalMaxRounds int    `json:"eval_max_rounds,omitempty"`
	EvalPass      *bool  `json:"eval_pass,omitempty"`
	EvalReason    string `json:"eval_reason,omitempty"`
	EvalError     string `json:"eval_error,omitempty"`
	Action        string `json:"action,omitempty"`
	Approve       *bool  `json:"approve,omitempty"`
	// GuardrailAction / GuardrailState are the harness guardrail runtime's
	// structured verdict vocabulary on kind=guardrail_decision /
	// kind=guardrail_review rows (executor.Event.GuardrailAction/GuardrailState).
	// GuardrailReason / GuardrailTool are deliberately not decoded: this package
	// counts structured verdicts and never classifies their text.
	GuardrailAction string          `json:"guardrail_action,omitempty"`
	GuardrailState  string          `json:"guardrail_state,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

// RunJournal is one run's journal, keyed by its file name
// (<project>/runs/<run id>.jsonl). Lines counts every JSON row in the file
// (including rows whose kind the capability metrics ignore); Events holds only
// the rows CapabilityKind accepts. Malformed counts rows a crash left
// unparseable (typically one partial tail line) — they are never guessed at.
type RunJournal struct {
	RunID     string
	Lines     int
	Malformed int
	Events    []JournalEvent
}

// CapabilityKind reports whether a journal row kind carries capability-use
// signal. The caller may decode the cheap `kind` field first and only decode a
// full JournalEvent for these rows.
func CapabilityKind(kind string) bool {
	switch kind {
	case executor.KindStatus,
		journalKindGuardrail,
		executor.KindGuardrailDecision,
		executor.KindGuardrailReview,
		journalKindMedia:
		return true
	}
	return false
}

// CaptureRecord is one line of the HTTP capture index (<cases>/.http/*.json,
// internal/traffic.Capture). PreviewOnly/Truncated are pointers so a legacy row
// written before the flags existed is reported as "flag missing" instead of
// being silently read as false (and never re-derived from the preserved Error
// prose here).
type CaptureRecord struct {
	Ref           string `json:"ref"`
	RunID         string `json:"run_id,omitempty"`
	IntentID      string `json:"intent_id,omitempty"`
	Method        string `json:"method,omitempty"`
	Status        int32  `json:"status,omitempty"`
	Complete      bool   `json:"complete,omitempty"`
	PreviewOnly   *bool  `json:"preview_only,omitempty"`
	Truncated     *bool  `json:"truncated,omitempty"`
	RequestBytes  int    `json:"request_bytes,omitempty"`
	ResponseBytes int    `json:"response_bytes,omitempty"`
}

// mediaRaw is the Raw payload of a kind=media row (internal/aopexec.Media).
type mediaRaw struct {
	Path     string `json:"path"`
	MIMEType string `json:"mime,omitempty"`
	Bytes    int    `json:"bytes"`
}

// ---- 8. evaluation pass rate ---------------------------------------------

func metricEvalPassRate(s Snapshot) Metric {
	const key = KeyEvalPassRate
	m := Metric{
		Key:   key,
		Order: orderEvalPassRate,
		Unit:  "ratio",
		Definition: "评估通过率 = 结构化判定为通过的 run 数 / 有明确结构化判定的 run 数。" +
			"判定只取 run journal 中 kind=status 且 state=eval_end、携带 eval_pass 布尔字段的事件；同一 run 有多条判定时以最后一条为结算判定（判定条数单列）。" +
			"未判定 run（没有 journal、journal 无 eval_end 判定，或旧 journal 的 eval_end 缺 eval_pass 字段）单独计数，不算失败；eval_start/eval_error 只表示进度/错误，不含判定。" +
			"run 总体取账本 run 与 journal 文件名的并集；只记录、不设阈值。",
	}
	es := summarizeEval(s)
	details := []Detail{
		detailCount("ledger_runs", int64(es.ledgerRuns)),
		detailCount("runs_with_journal", int64(es.journals)),
		detailCount("runs_without_journal", int64(es.runsWithoutJournal)),
		detailCount("journal_only_runs", int64(es.journalOnlyRuns)),
		detailCount("judged_runs", int64(es.judgedRuns)),
		detailCount("unjudged_runs", int64(es.unjudgedRuns)),
		detailCount("pass_runs", int64(es.passingRuns)),
		detailCount("fail_runs", int64(es.failingRuns)),
		detailCount("eval_state_events", int64(es.evalEvents)),
		detailCount("eval_end_events", int64(es.evalEndEvents)),
		detailCount("eval_judgements", int64(es.judgements)),
		detailCount("eval_end_without_verdict", int64(es.endWithoutVerdict)),
		detailCount("multi_judgement_runs", int64(es.multiJudgementRuns)),
		detailCount("malformed_journal_lines", int64(es.malformed)),
	}
	m.Details = details
	if es.judgedRuns == 0 {
		m.Unavailable = "no_eval_signal"
		switch {
		case !s.JournalsPresent:
			m.Explanation = "项目没有 runs/ 运行事件日志目录：本轮之前落盘的 run 没有 journal，评估判定无从取得，按不可得而不是 0 处理。"
		case es.evalEvents == 0:
			m.Explanation = "run journal 中没有任何评估状态事件（eval_start/eval_end/eval_error）：没有结构化判定，按不可得而不是 0 处理。"
		default:
			m.Explanation = "run journal 有评估状态事件，但没有一条 eval_end 携带结构化 eval_pass（旧版宿主或旧 journal 可能只有进度/错误，或判定只存在于状态文本里）；未判定按不可得而不是失败处理。"
		}
		return m
	}
	m.Numerator = int64(es.passingRuns)
	m.Denominator = int64(es.judgedRuns)
	m.Value = ratioValue(m.Numerator, m.Denominator)
	return m
}

// evalSummary is the shared structured reading of the run journals behind the
// three evaluation metrics. No fields are derived from event text.
type evalSummary struct {
	ledgerRuns         int
	journals           int
	runsWithoutJournal int
	journalOnlyRuns    int
	judgedRuns         int
	unjudgedRuns       int
	passingRuns        int
	failingRuns        int
	evalEvents         int
	evalEndEvents      int
	judgements         int
	endWithoutVerdict  int
	multiJudgementRuns int
	roundsReported     int
	roundsMissing      int
	maxRound           int
	sumRounds          int64
	lastMaxRounds      int
	malformed          int
}

func summarizeEval(s Snapshot) evalSummary {
	es := evalSummary{ledgerRuns: len(s.DB.Runs), journals: len(s.Journals)}
	journalIDs := make(map[string]bool, len(s.Journals))
	regarded := make(map[string]bool, len(s.DB.Runs)+len(s.Journals))
	ledgerIDs := make(map[string]bool, len(s.DB.Runs))
	for _, r := range s.DB.Runs {
		ledgerIDs[r.ID] = true
		regarded[r.ID] = true
	}
	for i := range s.Journals {
		j := &s.Journals[i]
		journalIDs[j.RunID] = true
		regarded[j.RunID] = true
		es.malformed += j.Malformed
		var verdict *bool
		judgements := 0
		var lastRound, lastMaxRounds int
		for _, ev := range j.Events {
			if ev.Kind != executor.KindStatus {
				continue
			}
			switch ev.State {
			case evalStateStart, evalStateEnd, evalStateError:
				es.evalEvents++
			}
			if ev.State != evalStateEnd {
				continue
			}
			es.evalEndEvents++
			if ev.EvalPass == nil {
				es.endWithoutVerdict++
				continue
			}
			judgements++
			v := *ev.EvalPass
			verdict = &v
			lastRound, lastMaxRounds = ev.EvalRound, ev.EvalMaxRounds
		}
		if verdict == nil {
			continue
		}
		es.judgedRuns++
		es.judgements += judgements
		if judgements > 1 {
			es.multiJudgementRuns++
		}
		if *verdict {
			es.passingRuns++
		} else {
			es.failingRuns++
		}
		if lastMaxRounds > es.lastMaxRounds {
			es.lastMaxRounds = lastMaxRounds
		}
		// EvalRound == 0 means the harness reported no round number (the same
		// "0 is absent, not measured" convention the executor fields use). A
		// missing round is counted, never guessed as 1.
		if lastRound > 0 {
			es.roundsReported++
			es.sumRounds += int64(lastRound)
			if lastRound > es.maxRound {
				es.maxRound = lastRound
			}
		} else {
			es.roundsMissing++
		}
	}
	for id := range ledgerIDs {
		if !journalIDs[id] {
			es.runsWithoutJournal++
		}
	}
	for id := range journalIDs {
		if !ledgerIDs[id] {
			es.journalOnlyRuns++
		}
	}
	es.unjudgedRuns = len(regarded) - es.judgedRuns
	return es
}

// ---- 9-10. evaluation rounds ---------------------------------------------

func metricEvalRoundsMax(s Snapshot) Metric {
	const key = KeyEvalRoundsMax
	m := Metric{
		Key:   key,
		Order: orderEvalRoundsMax,
		Unit:  "round",
		Definition: "评估轮次最大值 = 有明确判定且报告了 eval_round 的 run 中，结算判定（最后一条带 eval_pass 的 eval_end）eval_round 的最大值；" +
			"分母是报告了轮次的判定 run 数，不是全部 run。eval_round 缺失（0，常见于本轮之前落盘的旧 journal）单列计数，不推断为 1。只记录、不设阈值。",
	}
	es := summarizeEval(s)
	details := []Detail{
		detailCount("judged_runs", int64(es.judgedRuns)),
		detailCount("rounds_reported_runs", int64(es.roundsReported)),
		detailCount("rounds_missing_runs", int64(es.roundsMissing)),
		detailCount("evaluator_max_rounds_ceiling", int64(es.lastMaxRounds)),
	}
	m.Details = details
	if es.judgedRuns == 0 {
		m.Unavailable = "no_eval_signal"
		m.Explanation = "没有结构化评估判定（见 eval_pass_rate），轮次分布不可得而不是 0。"
		return m
	}
	if es.roundsReported == 0 {
		m.Unavailable = "no_round_signal"
		m.Explanation = "有结构化判定，但所有判定事件的 eval_round 都是 0（旧 journal 没有轮次字段）；轮次分布不可得，不推断。"
		return m
	}
	m.Numerator = int64(es.maxRound)
	m.Denominator = int64(es.roundsReported)
	m.Value = countValue(m.Numerator)
	return m
}

func metricEvalRoundsAvg(s Snapshot) Metric {
	const key = KeyEvalRoundsAvg
	m := Metric{
		Key:   key,
		Order: orderEvalRoundsAvg,
		Unit:  "round",
		Definition: "评估轮次平均值 = 有明确判定且报告了 eval_round 的 run 的结算判定 eval_round 均值；分子为轮次总和，分母为报告了轮次的判定 run 数。" +
			"缺轮次字段的判定 run 单列、不进分母；只记录、不设阈值。",
	}
	es := summarizeEval(s)
	details := []Detail{
		detailCount("judged_runs", int64(es.judgedRuns)),
		detailCount("rounds_reported_runs", int64(es.roundsReported)),
		detailCount("rounds_missing_runs", int64(es.roundsMissing)),
		detailCount("rounds_sum", es.sumRounds),
	}
	m.Details = details
	if es.judgedRuns == 0 {
		m.Unavailable = "no_eval_signal"
		m.Explanation = "没有结构化评估判定（见 eval_pass_rate），轮次均值不可得而不是 0。"
		return m
	}
	if es.roundsReported == 0 {
		m.Unavailable = "no_round_signal"
		m.Explanation = "有结构化判定，但所有判定事件的 eval_round 都是 0（旧 journal 没有轮次字段）；轮次均值不可得，不推断。"
		return m
	}
	m.Numerator = es.sumRounds
	m.Denominator = int64(es.roundsReported)
	m.Value = f64(round(float64(es.sumRounds)/float64(es.roundsReported), 3))
	return m
}

// ---- 11. guardrail (tool approval) actions -------------------------------

func metricGuardrailActions(s Snapshot) Metric {
	const key = KeyGuardrailActions
	m := Metric{
		Key:   key,
		Order: orderGuardrailActions,
		Unit:  "count",
		Definition: "工具审批/裁判判定事件数 = run journal 中 guardrail 结构化审计事件总数，覆盖三类：kind=guardrail（操作员读取/解决待审批项）、" +
			"kind=guardrail_decision（裁判对每次工具调用的判定）、kind=guardrail_review（被拦截调用的审批生命周期）。每条事件恰好进入一个 breakdown 桶：" +
			"kind=guardrail → pending_read / pending_unavailable / resolve_approve / resolve_reject / resolve_failed（approve 缺失的 resolve 单列 resolve_missing_approve，未知 action 单列 unknown_action）；" +
			"kind=guardrail_decision → decision_record / decision_review / decision_block（未知 action 单列 unknown_decision_action）；" +
			"kind=guardrail_review → review_pending / review_approved / review_rejected / review_expired / review_canceled（未知 state 单列 unknown_review_state）。" +
			"未知值不丢弃；value/numerator 为三类事件总数，denominator 为带有任一 guardrail 事件的 run 数。" +
			"journal 没有任何 guardrail 事件时不可得(no_guardrail_signal)：工具审批只在操作员读取/决策时写 journal，没有事件不等于未配置，不能记为 0。只记录、不设阈值。",
	}
	breakdown := map[string]int64{}
	runsWithGuardrail := 0
	auditEvents, decisionEvents, reviewEvents := 0, 0, 0
	for i := range s.Journals {
		j := &s.Journals[i]
		has := false
		for _, ev := range j.Events {
			switch ev.Kind {
			case journalKindGuardrail:
				has = true
				auditEvents++
				switch ev.Action {
				case "pending_read":
					breakdown["pending_read"]++
				case "pending_unavailable":
					breakdown["pending_unavailable"]++
				case "resolve":
					switch {
					case ev.Approve == nil:
						breakdown["resolve_missing_approve"]++
					case *ev.Approve:
						breakdown[guardrailActionResolveApprove]++
					default:
						breakdown[guardrailActionResolveReject]++
					}
				case "resolve_failed":
					breakdown["resolve_failed"]++
				default:
					breakdown["unknown_action"]++
				}
			case executor.KindGuardrailDecision:
				has = true
				decisionEvents++
				switch ev.GuardrailAction {
				case decisionActionRecord:
					breakdown["decision_record"]++
				case decisionActionReview:
					breakdown["decision_review"]++
				case decisionActionBlock:
					breakdown["decision_block"]++
				default:
					// "unspecified", an action_<n> from a newer host, or a row that
					// lost the field: surfaced, never dropped.
					breakdown["unknown_decision_action"]++
				}
			case executor.KindGuardrailReview:
				has = true
				reviewEvents++
				switch ev.GuardrailState {
				case reviewStatePending:
					breakdown["review_pending"]++
				case reviewStateApproved:
					breakdown["review_approved"]++
				case reviewStateRejected:
					breakdown["review_rejected"]++
				case reviewStateExpired:
					breakdown["review_expired"]++
				case reviewStateCanceled:
					breakdown["review_canceled"]++
				default:
					breakdown["unknown_review_state"]++
				}
			}
		}
		if has {
			runsWithGuardrail++
		}
	}
	total := int64(0)
	for _, v := range breakdown {
		total += v
	}
	m.Details = []Detail{
		detailCount("journals_scanned", int64(len(s.Journals))),
		detailCount("runs_with_guardrail_events", int64(runsWithGuardrail)),
		detailCount("journal_guardrail_events", int64(auditEvents)),
		detailCount("decision_events", int64(decisionEvents)),
		detailCount("review_events", int64(reviewEvents)),
	}
	if total == 0 {
		m.Unavailable = "no_guardrail_signal"
		switch {
		case !s.JournalsPresent:
			m.Explanation = "项目没有 runs/ 运行事件日志目录：既没有工具审批事件，也无法证明审批能力被配置或使用过；按不可得而不是 0 处理。"
		default:
			m.Explanation = "run journal 中没有 kind=guardrail / guardrail_decision / guardrail_review 事件：工具审批只在操作员读取决策或宿主拦截调用时写 journal，没有任何事件并不等于没有配置，按不可得而不是 0 处理。"
		}
		return m
	}
	m.Numerator = total
	m.Denominator = int64(runsWithGuardrail)
	m.Value = countValue(total)
	m.Breakdown = breakdown
	return m
}

// ---- 12. HTTP body coverage ----------------------------------------------

func metricTrafficBodyCoverage(s Snapshot) Metric {
	const key = KeyTrafficBodyCoverage
	m := Metric{
		Key:   key,
		Order: orderTrafficBodyCoverage,
		Unit:  "ratio",
		Definition: "HTTP 正文保真覆盖率 = 结构化标记为非 preview-only 的 Capture 数 / 带预览标志的 Capture 数。" +
			"Capture 为 <cases>/.http/*.json 索引行（internal/traffic.Capture）；preview_only=true 表示扫描节点从未持久化正文（只能看受限预览），单列计数。" +
			"truncated=true 表示保存的正文短于观测正文，与 preview 不同，单列计数、不参与分子分母。旧索引行缺少 preview_only 字段时计入 flags_missing 并从分母剔除，" +
			"不按 error 文本或标题推断。无捕获时不可得(no_traffic_signal)。只记录、不设阈值。",
	}
	total, flagKnown, notPreview, previewOnly, flagMissing := 0, 0, 0, 0, 0
	truncated, truncatedMissing := 0, 0
	complete, incomplete := 0, 0
	var requestBytes, responseBytes int64
	for _, c := range s.Captures {
		total++
		requestBytes += int64(c.RequestBytes)
		responseBytes += int64(c.ResponseBytes)
		if c.Complete {
			complete++
		} else {
			incomplete++
		}
		if c.PreviewOnly == nil {
			flagMissing++
		} else {
			flagKnown++
			if !*c.PreviewOnly {
				notPreview++
			} else {
				previewOnly++
			}
		}
		if c.Truncated == nil {
			truncatedMissing++
		} else if *c.Truncated {
			truncated++
		}
	}
	m.Details = []Detail{
		detailCount("captures_total", int64(total)),
		detailCount("preview_flag_missing", int64(flagMissing)),
		detailCount("preview_only", int64(previewOnly)),
		detailCount("not_preview_only", int64(notPreview)),
		detailCount("truncated", int64(truncated)),
		detailCount("truncated_flag_missing", int64(truncatedMissing)),
		detailCount("complete_captures", int64(complete)),
		detailCount("incomplete_captures", int64(incomplete)),
		detailCount("malformed_index_rows", int64(s.CapturesMalformed)),
		detailBytes("request_bytes_sum", requestBytes),
		detailBytes("response_bytes_sum", responseBytes),
	}
	if total == 0 {
		m.Unavailable = "no_traffic_signal"
		if !s.CapturesPresent {
			m.Explanation = "项目没有 cases/.http 捕获索引目录：没有 HTTP 观察，正文保真覆盖率不可得而不是 100%。"
		} else {
			m.Explanation = "cases/.http 目录存在但没有可解析的 Capture 索引行：没有 HTTP 观察，正文保真覆盖率不可得而不是 100%。"
		}
		return m
	}
	if flagKnown == 0 {
		m.Unavailable = "no_structured_body_flags"
		m.Explanation = "所有 Capture 索引行都早于 preview_only 字段（旧索引行），没有结构化正文标志可判定；不按 error 文本推断，按不可得处理。"
		return m
	}
	m.Numerator = int64(notPreview)
	m.Denominator = int64(flagKnown)
	m.Value = ratioValue(m.Numerator, m.Denominator)
	return m
}

// ---- 13. media preservation ----------------------------------------------

func metricMediaPreserved(s Snapshot) Metric {
	const key = KeyMediaPreserved
	m := Metric{
		Key:   key,
		Order: orderMediaPreserved,
		Unit:  "count",
		Definition: "媒体保全数 = run journal 中 kind=media 事件数（value 即事件数）；总字节取事件 raw 字段中的 bytes（internal/aopexec.Media）。" +
			"raw 缺失或不可解析的事件单列，不计入总字节也不推断为 0 字节。媒体只保全消息/工具结果内联的公开媒体（单条 ≤8 MiB、单事件 ≤64 MiB）；" +
			"没有 media 事件时按 0 计并说明口径，不视为不可得（保全是无媒体可保全时的正常空集）。只记录、不设阈值。",
	}
	mediaEvents, mediaBytes := 0, int64(0)
	rawMissing, rawMalformed, bytesZero := 0, 0, 0
	journalEvents, journalsScanned, journalMalformed := 0, len(s.Journals), 0
	for i := range s.Journals {
		j := &s.Journals[i]
		journalEvents += j.Lines
		journalMalformed += j.Malformed
		for _, ev := range j.Events {
			if ev.Kind != journalKindMedia {
				continue
			}
			mediaEvents++
			if len(ev.Raw) == 0 {
				rawMissing++
				continue
			}
			var raw mediaRaw
			if err := json.Unmarshal(ev.Raw, &raw); err != nil {
				rawMalformed++
				continue
			}
			if raw.Bytes <= 0 {
				bytesZero++
				continue
			}
			mediaBytes += int64(raw.Bytes)
		}
	}
	m.Numerator = int64(mediaEvents)
	m.Denominator = int64(journalEvents)
	m.Value = countValue(m.Numerator)
	m.Details = []Detail{
		detailCount("journals_scanned", int64(journalsScanned)),
		detailCount("journal_events", int64(journalEvents)),
		detailCount("media_events", int64(mediaEvents)),
		detailBytes("media_total_bytes", mediaBytes),
		detailCount("media_raw_missing", int64(rawMissing)),
		detailCount("media_raw_malformed", int64(rawMalformed)),
		detailCount("media_bytes_nonpositive", int64(bytesZero)),
		detailCount("malformed_journal_lines", int64(journalMalformed)),
	}
	if mediaEvents == 0 {
		m.Explanation = "run journal 中没有 kind=media 事件，媒体保全数按 0 计：媒体只在消息/工具结果携带内联公开媒体时保全，空集是正常读数而非不可得。"
	} else if mediaBytes == 0 {
		m.Explanation = "存在 kind=media 事件，但没有一条带有可用的结构化 bytes 字段；总字节不可得，事件数仍如实计数（不推断为 0 字节）。"
	}
	return m
}

func detailBytes(name string, n int64) Detail {
	return Detail{Name: name, Value: float64(n), Unit: "byte"}
}
