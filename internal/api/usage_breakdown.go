package api

// Project-level token accounting (GET /api/usage/breakdown).
//
// The run ledger already records per-run usage, but it cannot answer "who spent
// the tokens": a commander round, a direction run, a review/report run and the
// guardrail judge are four different spenders, and the judge does not even have
// a run row. This handler is the read-only roll-up over the project's own data:
//
//	commander       ledger runs with Purpose=commander
//	executor        ledger runs with Purpose="" or "executor" (direction runs)
//	support         every other run purpose that ran a model (verifier/report/…)
//	executor_total  executor + support
//	judge           runs/*.jsonl rows with kind=judge_usage, split jev/llm
//	combined        commander + executor_total (the engagement's own work)
//	all             combined + judge (everything the project's models spent)
//
// Honesty rules, inherited from docs/01 §10.5 and the console contract:
//   - a number is model-reported or it is absent. An absent bucket/field is
//     UNKNOWN (the console renders "—"), never a fabricated measured 0;
//   - a run whose harness did not report usage raises unknown_usage_runs and an
//     `unavailable` reason instead of silently counting as zero;
//   - no judge_usage row at all means the judge's spend is unknown (old journal
//     or judge not engaged), not 0; the reason is reported;
//   - this is a structured project statistic, not a provider bill, and it is
//     read-only: no threshold, no prompt, no acceptance target.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// usageBucket is one row of the breakdown. A nil field means "not reported" and
// is omitted from the JSON; a present 0 is a real observation (an empty bucket
// or a measured zero). web/src/lib/usage-breakdown.ts relies on that difference.
type usageBucket struct {
	// Runs is the bucket's ledger run count. Run buckets always report it;
	// judge buckets count calls instead.
	Runs *int `json:"runs,omitempty"`
	// Calls counts model calls: run usage requests, or judge usage rows.
	Calls *int `json:"calls,omitempty"`
	// Input / Output / Total are provider-reported tokens.
	Input  *int `json:"input,omitempty"`
	Output *int `json:"output,omitempty"`
	Total  *int `json:"total,omitempty"`
	// UnknownUsageRuns counts the bucket's runs whose harness never reported
	// usage. Their tokens are NOT part of input/output/total.
	UnknownUsageRuns int `json:"unknown_usage_runs,omitempty"`
	// MissingUsage counts model calls inside the reported runs that returned no
	// usage (the harness's own usage_missing).
	MissingUsage int `json:"missing_usage,omitempty"`
	// MissingTotals counts judge_usage rows that carried input/output but no
	// total; they are not included in total.
	MissingTotals int `json:"missing_totals,omitempty"`
}

// usageTally is the accumulator behind one usageBucket. A false flag means the
// corresponding number is unknown, never that it is 0.
type usageTally struct {
	runs, calls, input, output, total        int
	unknownRuns, missingUsage, missingTotals int
	// breakdown says the input/output split is reportable (a fallback total
	// from an old row is a total without a breakdown).
	breakdown bool
	// totalKnown says the bucket's total is reportable.
	totalKnown bool
	// callsKnown says the call count is reportable.
	callsKnown bool
}

// addRun folds one ledger run into the tally. Purpose classification happens at
// the call site; this only reads the usage facts.
func (t *usageTally) addRun(run model.Run) {
	t.runs++
	if run.Usage.Known {
		t.totalKnown, t.breakdown, t.callsKnown = true, true, true
		t.calls += run.Usage.Requests
		t.input += run.Usage.InputTokens
		t.output += run.Usage.OutputTokens
		t.total += run.Usage.TotalTokens
		t.missingUsage += run.Usage.Missing
		return
	}
	t.unknownRuns++
	// Old ledger rows may carry observed tokens without the Known flag. The
	// existing intent summary counts them in total; do the same, but never claim
	// an input/output split that was not reported.
	if run.Tokens > 0 {
		t.total += run.Tokens
		t.totalKnown = true
	}
}

// markEmptyMeasured records that a ledger bucket with no runs has a known spend
// of zero: the ledger was read, this class simply has no rows yet. Without this
// a review-less project's executor_total would stay "unknown" forever.
func (t *usageTally) markEmptyMeasured() {
	if t.runs == 0 {
		t.totalKnown, t.breakdown, t.callsKnown = true, true, true
	}
}

// sumTallies combines independent buckets for the documented roll-ups. A number
// is summed only when every part reports it: a total missing one side would be a
// wrong number, not an honest partial sum.
func sumTallies(parts ...usageTally) usageTally {
	out := usageTally{breakdown: true, totalKnown: true, callsKnown: true}
	for _, p := range parts {
		out.runs += p.runs
		out.calls += p.calls
		out.input += p.input
		out.output += p.output
		out.total += p.total
		out.unknownRuns += p.unknownRuns
		out.missingUsage += p.missingUsage
		out.missingTotals += p.missingTotals
		out.breakdown = out.breakdown && p.breakdown
		out.totalKnown = out.totalKnown && p.totalKnown
		out.callsKnown = out.callsKnown && p.callsKnown
	}
	return out
}

// bucket renders the tally. includeRuns distinguishes ledger buckets (which
// always know their run count, 0 included) from judge buckets (which count
// calls, not runs).
func (t usageTally) bucket(includeRuns bool) usageBucket {
	out := usageBucket{
		UnknownUsageRuns: t.unknownRuns,
		MissingUsage:     t.missingUsage,
		MissingTotals:    t.missingTotals,
	}
	if includeRuns {
		runs := t.runs
		out.Runs = &runs
	}
	if t.callsKnown {
		calls := t.calls
		out.Calls = &calls
	}
	if t.totalKnown {
		total := t.total
		out.Total = &total
	}
	if t.breakdown {
		input, output := t.input, t.output
		out.Input, out.Output = &input, &output
	}
	return out
}

// judgeUsageBreakdown is the judge split: JEV and the LLM judge are kept apart
// on purpose (their prompts, models and costs differ).
type judgeUsageBreakdown struct {
	JEV   usageBucket `json:"jev"`
	LLM   usageBucket `json:"llm"`
	Total usageBucket `json:"total"`
}

// usageSources names the two inputs the breakdown was computed from and how much
// of each was actually available. The console prints the scalar entries; the
// object never implies coverage it did not have.
type usageSources struct {
	RunUsage   string `json:"run_usage"`   // where run tokens came from
	JudgeUsage string `json:"judge_usage"` // where judge tokens came from
	LedgerRuns int    `json:"ledger_runs"`
	Journals   int    `json:"journals_scanned"`
	JudgeRows  int    `json:"judge_usage_rows"`
	// UnreadableJournals counts project journals that could not be read; the
	// judge total is then a lower bound, said out loud in `unavailable`.
	UnreadableJournals int `json:"unreadable_journals,omitempty"`
}

// usageBreakdownView is the GET /api/usage/breakdown response body. Buckets are
// always present as objects; their numeric fields appear only when reported.
type usageBreakdownView struct {
	Commander     usageBucket         `json:"commander"`
	Executor      usageBucket         `json:"executor"`
	Support       usageBucket         `json:"support"`
	ExecutorTotal usageBucket         `json:"executor_total"`
	Judge         judgeUsageBreakdown `json:"judge"`
	Combined      usageBucket         `json:"combined"`
	All           usageBucket         `json:"all"`
	Sources       usageSources        `json:"sources"`
	// Unavailable says why a bucket is missing data. Empty means everything the
	// project has was reported.
	Unavailable []string `json:"unavailable,omitempty"`
}

func (s *Server) usageBreakdown(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusInternalServerError, "项目账本未就绪，无法汇总 token")
		return
	}
	writeJSON(w, http.StatusOK, s.projectUsageBreakdown())
}

// projectUsageBreakdown computes the roll-up from the project's ledger runs and
// its runs/*.jsonl judge_usage rows. It performs only reads.
func (s *Server) projectUsageBreakdown() usageBreakdownView {
	view := usageBreakdownView{Sources: usageSources{
		RunUsage:   "ledger run rows",
		JudgeUsage: "runs/*.jsonl kind=judge_usage",
	}}
	var commander, direction, support usageTally
	runs := s.Store.Runs()
	view.Sources.LedgerRuns = len(runs)
	for _, run := range runs {
		switch run.Purpose {
		case "commander":
			commander.addRun(run)
		case "", "executor":
			direction.addRun(run)
		default:
			// Reviews, report drafts, embeddings and any future model-running
			// purpose: operator-side support work, kept out of the executor bucket
			// but honestly visible as its own row.
			support.addRun(run)
		}
	}
	if len(runs) == 0 {
		// No ledger runs at all: a zero would look like a measurement. Mark the
		// run buckets unknown while still reporting the (true) run count 0.
		commander = usageTally{}
		direction = usageTally{}
		support = usageTally{}
		view.Unavailable = append(view.Unavailable,
			"账本里没有 run：指挥官、执行者、复核/报告的 token 不可得（不是 0）")
	} else {
		// The ledger was read, so a class with no runs genuinely spent 0: keep
		// that as a measured zero instead of an unknown. This is what lets
		// executor_total/combined stay meaningful before the first review or
		// report run exists.
		commander.markEmptyMeasured()
		direction.markEmptyMeasured()
		support.markEmptyMeasured()
		if unknown := commander.unknownRuns + direction.unknownRuns + support.unknownRuns; unknown > 0 {
			view.Unavailable = append(view.Unavailable, fmt.Sprintf(
				"%d 个 run 没有上报 usage：它们的 token 未知，不计入 input/output/total", unknown))
		}
	}

	judge := s.scanJudgeUsage()
	view.Sources.Journals = judge.journals
	view.Sources.JudgeRows = judge.rows
	view.Sources.UnreadableJournals = judge.unreadable
	view.Unavailable = append(view.Unavailable, judge.unavailable...)

	view.Commander = commander.bucket(true)
	view.Executor = direction.bucket(true)
	view.Support = support.bucket(true)
	executorTotal := sumTallies(direction, support)
	view.ExecutorTotal = executorTotal.bucket(true)
	combined := sumTallies(commander, executorTotal)
	view.Combined = combined.bucket(true)

	jevTotal := sumTallies(judge.jev)
	llmTotal := sumTallies(judge.llm)
	judgeTotal := sumTallies(judge.jev, judge.llm)
	view.Judge = judgeUsageBreakdown{
		JEV:   jevTotal.bucket(false),
		LLM:   llmTotal.bucket(false),
		Total: judgeTotal.bucket(false),
	}
	view.All = sumTallies(combined, judgeTotal).bucket(true)

	return view
}

// judgeScan is the project-wide read of runs/*.jsonl for kind=judge_usage rows.
type judgeScan struct {
	jev, llm usageTally
	// journals is how many run journals were opened and read.
	journals int
	// rows counts every kind=judge_usage line, including unassigned ones.
	rows int
	// tokensSeen records that at least one row carried token numbers, so the
	// judge split is a measurement rather than an absence.
	tokensSeen    bool
	unreadable    int
	unassigned    int // judge_usage rows with neither judge nor a known emitter
	missingTotals int // rows with input/output but no total
	// unavailable explains any gap in the judge attribution.
	unavailable []string
}

// scanJudgeUsage reads every project run journal and sums its judge_usage rows.
// It is a streaming read: engagement journals can be large and only the
// judge_usage lines matter. A malformed line (a half-written tail) is skipped;
// a judge_usage row whose shape this version cannot place is counted, never
// dropped silently.
func (s *Server) scanJudgeUsage() judgeScan {
	var scan judgeScan
	dir := filepath.Join(s.DataDir, "runs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		// There is no runs/ directory (young project) or it cannot be listed:
		// there is no journal lane to audit at all.
		scan.unavailable = append(scan.unavailable,
			"项目没有 run journal（runs/*.jsonl）：裁判 token 不可得（不是 0）")
		return scan
	}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, ent.Name()))
		if err != nil {
			scan.unreadable++
			continue
		}
		scan.journals++
		reader := bufio.NewReader(f)
		first := true
		for {
			line, readErr := reader.ReadString('\n')
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				if first {
					first = false
					// A journal from another build is not counted here: its rows
					// may mean something else, and a silent 0 would read as "the
					// judge spent nothing".
					if journalFileVersion([]byte(trimmed)) != executor.JournalSchemaVersion {
						scan.unreadable++
						scan.unavailable = append(scan.unavailable,
							journalVersionRefusal(journalFileVersion([]byte(trimmed)))+"（"+ent.Name()+"）")
						break
					}
				}
				scan.scanLine(trimmed)
			}
			if readErr != nil {
				// EOF, or an unreadable tail: what was read is still audited.
				break
			}
		}
		_ = f.Close()
	}

	// judge_usage rows are written by this control plane, so once one exists
	// the judge lane is instrumented: a judge with zero rows then genuinely
	// spent zero (it was not engaged). A judge that does have rows keeps only
	// what those rows reported: a missing total stays unknown.
	if scan.rows > 0 {
		scan.jev.callsKnown, scan.llm.callsKnown = true, true
	}
	if scan.tokensSeen {
		if scan.jev.calls == 0 {
			scan.jev.totalKnown, scan.jev.breakdown = true, true
		}
		if scan.llm.calls == 0 {
			scan.llm.totalKnown, scan.llm.breakdown = true, true
		}
	}
	if scan.unassigned > 0 {
		scan.unavailable = append(scan.unavailable, fmt.Sprintf(
			"%d 条 judge_usage 缺 judge/emitter 字段，无法归入 JEV 或 LLM，未计入裁判合计", scan.unassigned))
	}
	if scan.missingTotals > 0 {
		scan.unavailable = append(scan.unavailable, fmt.Sprintf(
			"%d 条 judge_usage 缺 total 字段，未计入裁判 total（input/output 仍按上报计）", scan.missingTotals))
	}
	if scan.unreadable > 0 {
		scan.unavailable = append(scan.unavailable, fmt.Sprintf(
			"%d 个 run journal 读不出来：裁判 token 可能缺失（当前合计是下界）", scan.unreadable))
	}
	switch {
	case scan.journals == 0:
		scan.unavailable = append(scan.unavailable,
			"项目没有 run journal（runs/*.jsonl）：裁判 token 不可得（不是 0）")
	case scan.rows == 0:
		scan.unavailable = append(scan.unavailable, fmt.Sprintf(
			"%d 个 run journal 里没有 judge_usage 事件（旧 journal 或裁判未参与）：裁判 token 不可得（不是 0）",
			scan.journals))
	case !scan.tokensSeen:
		scan.unavailable = append(scan.unavailable,
			"judge_usage 事件没有携带 token 数字：裁判 token 不可得（不是 0）")
	}
	return scan
}

// scanLine audits one journal line. Non-judge lines are the overwhelming
// majority and return immediately after the cheap kind probe.
func (scan *judgeScan) scanLine(line string) {
	var row struct {
		Kind    string `json:"kind"`
		Judge   string `json:"judge"`
		Emitter string `json:"emitter"`
		Input   int    `json:"input"`
		Output  int    `json:"output"`
		Total   int    `json:"total"`
	}
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return // malformed / half-written line, not a judge record
	}
	if row.Kind != executor.KindJudgeUsage {
		return
	}
	scan.rows++
	judge := row.Judge
	if judge == "" {
		// The emitter is part of the same record; guardrail always means JEV and
		// guardrail-llm its LLM judge. Filling from it is not a guess.
		judge = executor.JudgeOf(row.Emitter)
	}
	var tally *usageTally
	switch judge {
	case executor.JudgeJEV:
		tally = &scan.jev
	case executor.JudgeLLM:
		tally = &scan.llm
	default:
		scan.unassigned++
		return
	}
	tally.calls++
	tally.input += row.Input
	tally.output += row.Output
	tally.total += row.Total
	if row.Total > 0 || row.Input > 0 || row.Output > 0 {
		scan.tokensSeen = true
		tally.breakdown = true
	}
	if row.Total > 0 {
		tally.totalKnown = true
	} else if row.Input > 0 || row.Output > 0 {
		// input/output were reported without a total: count the row's tokens,
		// but report the total as missing instead of inventing input+output.
		scan.missingTotals++
		tally.missingTotals++
	} else {
		// The row exists but carries no token numbers. It still counts as a
		// call; its spend stays unknown rather than becoming a measured 0.
		scan.missingTotals++
		tally.missingTotals++
	}
}
