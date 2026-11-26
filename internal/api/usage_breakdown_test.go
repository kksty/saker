package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// mustBucketInt reads a reported bucket field; nil means "not reported", which
// is a test failure only when a number was required.
func mustBucketInt(t *testing.T, label string, p *int) int {
	t.Helper()
	if p == nil {
		t.Fatalf("%s: field was not reported", label)
	}
	return *p
}

// findJournalKind returns the first journal row with the given kind.
func findJournalKind(t *testing.T, dir, runID, kind string) map[string]any {
	t.Helper()
	for _, row := range readJournalFileLines(t, dir, runID) {
		if row["kind"] == kind {
			return row
		}
	}
	t.Fatalf("run %s journal has no kind=%s row", runID, kind)
	return nil
}

// TestUsageBreakdownSplitsCommanderExecutorSupportAndJudge pins the whole
// contract: ledger purposes fill commander/executor/support; review/report runs
// are support, not executor; judge_usage rows in the project journal fill the
// JEV/LLM split; the three roll-ups are the documented additions. A run's own
// kind=usage row in the same journal must not be read as judge spend.
func TestUsageBreakdownSplitsCommanderExecutorSupportAndJudge(t *testing.T) {
	s, _ := newBindingServer(t)
	add := func(purpose string, usage model.Usage, tokens int) model.Run {
		t.Helper()
		run, err := s.Store.AddRun(model.Run{
			IntentID: "in_1", Node: "local", Purpose: purpose,
			Usage: usage, Tokens: tokens,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	commander := add("commander",
		model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Requests: 1, Known: true}, 150)
	add("", model.Usage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300, Requests: 2, Known: true}, 300)
	add("executor", model.Usage{InputTokens: 300, OutputTokens: 100, TotalTokens: 400, Requests: 1, Known: true}, 400)
	add("verifier", model.Usage{InputTokens: 30, OutputTokens: 10, TotalTokens: 50, Requests: 2, Known: true}, 50)
	add("report", model.Usage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7, Requests: 1, Known: true}, 7)

	// One real project journal with the two judge rows and the run's own usage
	// row, written through the production journal writer.
	openTestJournal(t, s, commander.ID)
	for _, ev := range []executor.Event{
		{Kind: executor.KindUsage, Tokens: 150, InputTokens: 100, OutputTokens: 50, UsageRequests: 1},
		{Kind: executor.KindJudgeUsage, Judge: executor.JudgeJEV, Emitter: executor.EmitterGuardrail,
			Input: 10, Output: 5, Total: 15, SessionID: "sess-1", TurnID: "turn-1"},
		{Kind: executor.KindJudgeUsage, Judge: executor.JudgeLLM, Emitter: executor.EmitterGuardrailLLM,
			Input: 4, Output: 2, Total: 6, SessionID: "sess-1", TurnID: "turn-1"},
	} {
		if err := s.appendRunEvent(commander.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	s.closeJournal(commander.ID)

	// The journal row is the contract the breakdown scans: kind, judge, the
	// documented input/output/total names, emitter and session/turn identity.
	row := findJournalKind(t, s.DataDir, commander.ID, executor.KindJudgeUsage)
	if row["judge"] != executor.JudgeJEV || row["emitter"] != executor.EmitterGuardrail {
		t.Fatalf("judge row identity = %+v", row)
	}
	for field, want := range map[string]float64{"input": 10, "output": 5, "total": 15} {
		if got, _ := row[field].(float64); got != want {
			t.Fatalf("judge row %s = %v, want %v (%+v)", field, row[field], want, row)
		}
	}
	if row["session_id"] != "sess-1" || row["turn_id"] != "turn-1" {
		t.Fatalf("judge row lost session/turn: %+v", row)
	}

	w := call(t, s, http.MethodGet, "/api/usage/breakdown", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("breakdown = %d: %s", w.Code, w.Body.String())
	}
	var got usageBreakdownView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	t.Logf("breakdown JSON: %s", strings.TrimSpace(w.Body.String()))
	if runs := mustBucketInt(t, "commander.runs", got.Commander.Runs); runs != 1 {
		t.Fatalf("commander runs = %d", runs)
	}
	if total := mustBucketInt(t, "commander.total", got.Commander.Total); total != 150 {
		t.Fatalf("commander total = %d", total)
	}
	if input := mustBucketInt(t, "commander.input", got.Commander.Input); input != 100 {
		t.Fatalf("commander input = %d", input)
	}
	if calls := mustBucketInt(t, "commander.calls", got.Commander.Calls); calls != 1 {
		t.Fatalf("commander calls = %d", calls)
	}

	// executor: only Purpose "" / "executor" (two direction runs).
	if runs := mustBucketInt(t, "executor.runs", got.Executor.Runs); runs != 2 {
		t.Fatalf("executor runs = %d", runs)
	}
	if total := mustBucketInt(t, "executor.total", got.Executor.Total); total != 700 {
		t.Fatalf("executor total = %d, want 700", total)
	}

	// support: review/report runs, kept out of executor but listed honestly.
	if runs := mustBucketInt(t, "support.runs", got.Support.Runs); runs != 2 {
		t.Fatalf("support runs = %d", runs)
	}
	if total := mustBucketInt(t, "support.total", got.Support.Total); total != 57 {
		t.Fatalf("support total = %d, want 57", total)
	}
	if runs := mustBucketInt(t, "executor_total.runs", got.ExecutorTotal.Runs); runs != 4 {
		t.Fatalf("executor_total runs = %d", runs)
	}
	if total := mustBucketInt(t, "executor_total.total", got.ExecutorTotal.Total); total != 757 {
		t.Fatalf("executor_total total = %d, want 757", total)
	}
	if calls := mustBucketInt(t, "executor_total.calls", got.ExecutorTotal.Calls); calls != 6 {
		t.Fatalf("executor_total calls = %d", calls)
	}

	// judge: JEV and LLM apart, then their total.
	if total := mustBucketInt(t, "judge.jev.total", got.Judge.JEV.Total); total != 15 {
		t.Fatalf("JEV total = %d", total)
	}
	if calls := mustBucketInt(t, "judge.jev.calls", got.Judge.JEV.Calls); calls != 1 {
		t.Fatalf("JEV calls = %d", calls)
	}
	if total := mustBucketInt(t, "judge.llm.total", got.Judge.LLM.Total); total != 6 {
		t.Fatalf("LLM total = %d", total)
	}
	if total := mustBucketInt(t, "judge.total.total", got.Judge.Total.Total); total != 21 {
		t.Fatalf("judge total = %d", total)
	}
	if calls := mustBucketInt(t, "judge.total.calls", got.Judge.Total.Calls); calls != 2 {
		t.Fatalf("judge calls = %d", calls)
	}

	// The documented roll-ups: combined = commander + executor_total,
	// all = combined + judge, and the judge is NOT part of executor or combined.
	if total := mustBucketInt(t, "combined.total", got.Combined.Total); total != 907 {
		t.Fatalf("combined total = %d, want 907", total)
	}
	if calls := mustBucketInt(t, "combined.calls", got.Combined.Calls); calls != 7 {
		t.Fatalf("combined calls = %d", calls)
	}
	if total := mustBucketInt(t, "all.total", got.All.Total); total != 928 {
		t.Fatalf("all total = %d, want 928", total)
	}
	if calls := mustBucketInt(t, "all.calls", got.All.Calls); calls != 9 {
		t.Fatalf("all calls = %d", calls)
	}
	if total := mustBucketInt(t, "all.input", got.All.Input); total != 648 {
		t.Fatalf("all input = %d", total)
	}
	if total := mustBucketInt(t, "all.output", got.All.Output); total != 270 {
		t.Fatalf("all output = %d", total)
	}

	if len(got.Unavailable) != 0 {
		t.Fatalf("complete data reported unavailable reasons: %v", got.Unavailable)
	}
	if got.Sources.LedgerRuns != 5 || got.Sources.Journals != 1 || got.Sources.JudgeRows != 2 {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

// TestUsageBreakdownRunsWithoutJournalReportsRunUsageAndNoJudge: run usage comes
// from the ledger and needs no journal; the judge only exists in journals, so its
// absence is reported as unavailable (and all, which needs the judge, is not
// guessed).
func TestUsageBreakdownRunsWithoutJournalReportsRunUsageAndNoJudge(t *testing.T) {
	s, _ := newBindingServer(t)
	for _, run := range []model.Run{
		{IntentID: "in_1", Node: "local", Purpose: "commander",
			Usage: model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Requests: 1, Known: true}},
		{IntentID: "in_1", Node: "local", Purpose: "",
			Usage: model.Usage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300, Requests: 2, Known: true}},
	} {
		if _, err := s.Store.AddRun(run); err != nil {
			t.Fatal(err)
		}
	}

	w := call(t, s, http.MethodGet, "/api/usage/breakdown", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("breakdown = %d: %s", w.Code, w.Body.String())
	}
	var got usageBreakdownView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if total := mustBucketInt(t, "commander.total", got.Commander.Total); total != 150 {
		t.Fatalf("commander total = %d", total)
	}
	if total := mustBucketInt(t, "executor.total", got.Executor.Total); total != 300 {
		t.Fatalf("executor total = %d", total)
	}
	if total := mustBucketInt(t, "combined.total", got.Combined.Total); total != 450 {
		t.Fatalf("combined total = %d", total)
	}
	if got.Judge.JEV.Total != nil || got.Judge.LLM.Total != nil || got.Judge.Total.Total != nil {
		t.Fatalf("judge token buckets must stay unknown without a journal: %+v", got.Judge)
	}
	if got.All.Total != nil {
		t.Fatalf("all must not silently omit the judge: %+v", got.All)
	}
	joined := strings.Join(got.Unavailable, "；")
	if !strings.Contains(joined, "没有 run journal") {
		t.Fatalf("missing journal was not reported: %v", got.Unavailable)
	}
	if got.Sources.Journals != 0 || got.Sources.LedgerRuns != 2 {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

// TestUsageBreakdownEmptyProjectIsHonest: an empty ledger plus no journal must
// not look like measured zeros. Counts are still reported (0 runs is true);
// every token number is absent and `unavailable` says why.
func TestUsageBreakdownEmptyProjectIsHonest(t *testing.T) {
	s, _ := newBindingServer(t)
	w := call(t, s, http.MethodGet, "/api/usage/breakdown", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("breakdown = %d: %s", w.Code, w.Body.String())
	}
	var got usageBreakdownView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	t.Logf("empty breakdown JSON: %s", strings.TrimSpace(w.Body.String()))
	if runs := mustBucketInt(t, "commander.runs", got.Commander.Runs); runs != 0 {
		t.Fatalf("commander runs = %d", runs)
	}
	for label, bucket := range map[string]usageBucket{
		"commander": got.Commander, "executor": got.Executor, "support": got.Support,
		"executor_total": got.ExecutorTotal, "combined": got.Combined, "all": got.All,
		"judge.jev": got.Judge.JEV, "judge.llm": got.Judge.LLM, "judge.total": got.Judge.Total,
	} {
		if bucket.Total != nil || bucket.Input != nil || bucket.Output != nil {
			t.Fatalf("%s invented token numbers for an empty project: %+v", label, bucket)
		}
	}
	joined := strings.Join(got.Unavailable, "；")
	if !strings.Contains(joined, "账本里没有 run") || !strings.Contains(joined, "没有 run journal") {
		t.Fatalf("empty project reasons = %v", got.Unavailable)
	}
	if got.Sources.LedgerRuns != 0 || got.Sources.Journals != 0 {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

// TestUsageBreakdownUnknownRunUsageIsFlagged: a run whose harness never reported
// usage must be visible as unknown, not counted as a measured zero.
func TestUsageBreakdownUnknownRunUsageIsFlagged(t *testing.T) {
	s, _ := newBindingServer(t)
	if _, err := s.Store.AddRun(model.Run{
		IntentID: "in_1", Node: "local", Purpose: "commander",
		Usage: model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Known: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.AddRun(model.Run{IntentID: "in_1", Node: "local", Purpose: ""}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, http.MethodGet, "/api/usage/breakdown", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("breakdown = %d: %s", w.Code, w.Body.String())
	}
	var got usageBreakdownView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if got.Executor.UnknownUsageRuns != 1 {
		t.Fatalf("executor unknown runs = %d", got.Executor.UnknownUsageRuns)
	}
	if got.Executor.Total != nil {
		t.Fatalf("an all-unknown executor bucket must not report a token total: %+v", got.Executor)
	}
	// The combined total would need the unknown executor side; it stays absent.
	if got.Combined.Total != nil {
		t.Fatalf("combined invented a total with an unknown side: %+v", got.Combined)
	}
	if total := mustBucketInt(t, "commander.total", got.Commander.Total); total != 150 {
		t.Fatalf("commander total = %d", total)
	}
	joined := strings.Join(got.Unavailable, "；")
	if !strings.Contains(joined, "没有上报 usage") {
		t.Fatalf("unknown run usage was not reported: %v", got.Unavailable)
	}
}

// TestUsageBreakdownOldJournalWithoutJudgeRowsSaysUnknown: journal exists (so run
// usage is journalable) but has no judge_usage row — the split is unknown, not 0.
func TestUsageBreakdownOldJournalWithoutJudgeRowsSaysUnknown(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{
		IntentID: "in_1", Node: "local", Purpose: "commander",
		Usage: model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Known: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	if err := s.appendRunEvent(run.ID, executor.Event{Kind: executor.KindUsage, Tokens: 150, InputTokens: 100, OutputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	s.closeJournal(run.ID)

	w := call(t, s, http.MethodGet, "/api/usage/breakdown", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("breakdown = %d: %s", w.Code, w.Body.String())
	}
	var got usageBreakdownView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if total := mustBucketInt(t, "commander.total", got.Commander.Total); total != 150 {
		t.Fatalf("commander total = %d", total)
	}
	if got.Judge.Total.Total != nil || got.Judge.JEV.Total != nil || got.Judge.LLM.Total != nil {
		t.Fatalf("judge totals must be unknown without judge_usage rows: %+v", got.Judge)
	}
	if got.All.Total != nil {
		t.Fatalf("all must stay unknown when the judge side is unknown: %+v", got.All)
	}
	joined := strings.Join(got.Unavailable, "；")
	if !strings.Contains(joined, "没有 judge_usage") {
		t.Fatalf("missing judge rows were not reported: %v", got.Unavailable)
	}
	if got.Sources.Journals != 1 || got.Sources.JudgeRows != 0 {
		t.Fatalf("sources = %+v", got.Sources)
	}
}
