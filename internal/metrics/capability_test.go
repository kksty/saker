package metrics

// P3-H capability-use metric tests. They build the parsed journal/capture rows
// directly (the caller in cmd/saker-metrics owns the file IO), so no test touches a
// project directory. Every metric gets a positive case, its no-signal boundary,
// and the rules the task calls out by name: "not judged is not failed", old data
// missing the new structured fields, and structured-field-only derivation.

import (
	"encoding/json"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func boolPtr(v bool) *bool { return &v }

func evalEvent(state string, round, maxRounds int, pass *bool) JournalEvent {
	return JournalEvent{
		Kind: "status", State: state,
		EvalRound: round, EvalMaxRounds: maxRounds, EvalPass: pass,
		EvalReason: "判定文本不参与指标", EvalError: "",
	}
}

func eventJournal(runID string, events ...JournalEvent) RunJournal {
	return RunJournal{RunID: runID, Lines: len(events), Events: events}
}

func guardrailEvent(action string, approve *bool) JournalEvent {
	return JournalEvent{Kind: "guardrail", Action: action, Approve: approve}
}

// decisionEvent / reviewEvent pin the on-wire journal vocabulary of the harness
// guardrail runtime (executor.KindGuardrailDecision / KindGuardrailReview and
// their Guardrail* JSON fields) as literals, so this test fails if the journal
// contract is renamed rather than silently reading something else.
func decisionEvent(guardrailAction string) JournalEvent {
	return JournalEvent{Kind: "guardrail_decision", GuardrailAction: guardrailAction}
}

func reviewEvent(guardrailState string) JournalEvent {
	return JournalEvent{Kind: "guardrail_review", GuardrailState: guardrailState}
}

func mediaEvent(raw string) JournalEvent {
	return JournalEvent{Kind: "media", Raw: json.RawMessage(raw)}
}

func detailValues(m Metric) map[string]float64 {
	out := make(map[string]float64, len(m.Details))
	for _, d := range m.Details {
		out[d.Name] = d.Value
	}
	return out
}

func runs(ids ...string) store.DB {
	db := store.DB{}
	for _, id := range ids {
		db.Runs = append(db.Runs, model.Run{ID: id})
	}
	return db
}

// ---- 8. evaluation pass rate ---------------------------------------------

// A verdict only exists when eval_end carries the structured eval_pass pointer.
// A run that was never judged (eval_start only, legacy eval_end without the
// field, or a journal with no eval rows) must be counted as unjudged, never as a
// failure, and never in the denominator.
func TestMetricEvalPassRateJudgedOnlyAndUnjudgedNotFailed(t *testing.T) {
	pass, fail := boolPtr(true), boolPtr(false)
	journals := []RunJournal{
		eventJournal("r_pass", evalEvent("eval_end", 2, 3, pass)),
		eventJournal("r_fail", evalEvent("eval_end", 3, 3, fail)),
		eventJournal("r_start_only", evalEvent("eval_start", 1, 3, nil)),
		// A journal written before the structured verdict existed: state text
		// exists, eval_pass does not. Not judged, not failed.
		eventJournal("r_legacy_end", evalEvent("eval_end", 0, 0, nil)),
		eventJournal("r_no_eval",
			JournalEvent{Kind: "message"}, JournalEvent{Kind: "tool_result"}),
	}
	// r_no_journal is a ledger run with no journal file: also unjudged.
	db := runs("r_pass", "r_fail", "r_start_only", "r_legacy_end", "r_no_eval", "r_no_journal")
	m := findMetric(t, Compute(Snapshot{DB: db, Journals: journals, JournalsPresent: true}), KeyEvalPassRate)
	if m.Unavailable != "" || m.Value == nil || *m.Value != 0.5 {
		t.Fatalf("pass rate = %+v, want 1/2 available", m)
	}
	if m.Numerator != 1 || m.Denominator != 2 {
		t.Fatalf("pass rate numerator/denominator = %d/%d, want 1/2", m.Numerator, m.Denominator)
	}
	details := detailValues(m)
	if details["judged_runs"] != 2 || details["unjudged_runs"] != 4 ||
		details["pass_runs"] != 1 || details["fail_runs"] != 1 {
		t.Fatalf("judged/unjudged details = %v", details)
	}
	if details["eval_end_without_verdict"] != 1 {
		t.Fatalf("legacy eval_end without verdict not reported: %v", details)
	}
	if details["runs_without_journal"] != 1 || details["ledger_runs"] != 6 {
		t.Fatalf("run population details = %v", details)
	}
	// The unused prose (EvalReason) must not influence the metric: changing it
	// changes nothing.
	other := append([]RunJournal(nil), journals...)
	other[0].Events = []JournalEvent{evalEvent("eval_end", 2, 3, boolPtr(true))}
	other[0].Events[0].EvalReason = "完全不同的自由文本"
	m2 := findMetric(t, Compute(Snapshot{DB: db, Journals: other, JournalsPresent: true}), KeyEvalPassRate)
	if m2.Numerator != m.Numerator || m2.Denominator != m.Denominator || m2.Value == nil || *m2.Value != *m.Value {
		t.Fatalf("event prose changed the pass rate: %+v vs %+v", m, m2)
	}
}

func TestMetricEvalPassRateMultipleJudgementsUseLastOne(t *testing.T) {
	// The evaluator may judge more than once in a run; the settled verdict is
	// the last structured one, and the judgement count is reported separately.
	journals := []RunJournal{eventJournal("r_multi",
		evalEvent("eval_end", 1, 3, boolPtr(false)),
		evalEvent("eval_end", 2, 3, boolPtr(true)),
	)}
	m := findMetric(t, Compute(Snapshot{Journals: journals, JournalsPresent: true}), KeyEvalPassRate)
	if m.Value == nil || *m.Value != 1 || m.Denominator != 1 {
		t.Fatalf("multi-judgement run = %+v, want last verdict (pass) over 1 run", m)
	}
	details := detailValues(m)
	if details["eval_judgements"] != 2 || details["multi_judgement_runs"] != 1 {
		t.Fatalf("judgement details = %v", details)
	}
}

func TestMetricEvalPassRateUnavailableBoundaries(t *testing.T) {
	// No journals directory at all: unavailable, not 0%.
	m := findMetric(t, Compute(Snapshot{}), KeyEvalPassRate)
	if m.Unavailable != "no_eval_signal" || m.Value != nil {
		t.Fatalf("empty snapshot = %+v, want unavailable(no_eval_signal)", m)
	}
	// Journal present, ledger run present, but only non-eval rows: still no
	// structured judgement.
	legacy := Snapshot{
		DB:              runs("r_old"),
		Journals:        []RunJournal{eventJournal("r_old", JournalEvent{Kind: "message"})},
		JournalsPresent: true,
	}
	m = findMetric(t, Compute(legacy), KeyEvalPassRate)
	if m.Unavailable != "no_eval_signal" || m.Value != nil {
		t.Fatalf("non-eval journal = %+v", m)
	}
	if detailValues(m)["unjudged_runs"] != 1 {
		t.Fatalf("unjudged run not reported: %v", m.Details)
	}
	// Eval rows exist but none carries a verdict (old harness): unavailable,
	// with the unjudged count visible.
	noVerdict := Snapshot{
		Journals:        []RunJournal{eventJournal("r_start", evalEvent("eval_start", 1, 3, nil))},
		JournalsPresent: true,
	}
	m = findMetric(t, Compute(noVerdict), KeyEvalPassRate)
	if m.Unavailable != "no_eval_signal" || m.Value != nil {
		t.Fatalf("eval-without-verdict journal = %+v", m)
	}
	if detailValues(m)["unjudged_runs"] != 1 {
		t.Fatalf("unjudged count missing: %v", m.Details)
	}
}

// ---- 9-10. evaluation rounds ---------------------------------------------

func TestMetricEvalRoundsMaxAndAverage(t *testing.T) {
	journals := []RunJournal{
		eventJournal("r_round2", evalEvent("eval_end", 2, 5, boolPtr(true))),
		eventJournal("r_round3", evalEvent("eval_end", 3, 5, boolPtr(false))),
		// Judged, but the old journal has no round number: counted as missing,
		// not assumed to be round 1.
		eventJournal("r_no_round", evalEvent("eval_end", 0, 0, boolPtr(true))),
	}
	snap := Snapshot{DB: runs("r_round2", "r_round3", "r_no_round", "r_no_journal"),
		Journals: journals, JournalsPresent: true}
	maxM := findMetric(t, Compute(snap), KeyEvalRoundsMax)
	if maxM.Unavailable != "" || maxM.Value == nil || *maxM.Value != 3 || maxM.Numerator != 3 || maxM.Denominator != 2 {
		t.Fatalf("rounds max = %+v, want 3 over 2 judged runs", maxM)
	}
	avgM := findMetric(t, Compute(snap), KeyEvalRoundsAvg)
	if avgM.Unavailable != "" || avgM.Value == nil || *avgM.Value != 2.5 || avgM.Numerator != 5 || avgM.Denominator != 2 {
		t.Fatalf("rounds avg = %+v, want 2.5 (5/2)", avgM)
	}
	details := detailValues(maxM)
	if details["rounds_reported_runs"] != 2 || details["rounds_missing_runs"] != 1 {
		t.Fatalf("round presence details = %v", details)
	}
	if details["evaluator_max_rounds_ceiling"] != 5 {
		t.Fatalf("evaluator ceiling missing: %v", details)
	}
}

func TestMetricEvalRoundsUnavailable(t *testing.T) {
	for _, key := range []string{KeyEvalRoundsMax, KeyEvalRoundsAvg} {
		m := findMetric(t, Compute(Snapshot{}), key)
		if m.Unavailable != "no_eval_signal" || m.Value != nil {
			t.Fatalf("%s empty snapshot = %+v", key, m)
		}
		noRound := Snapshot{
			Journals:        []RunJournal{eventJournal("r_old", evalEvent("eval_end", 0, 0, boolPtr(true)))},
			JournalsPresent: true,
		}
		m = findMetric(t, Compute(noRound), key)
		if m.Unavailable != "no_round_signal" || m.Value != nil {
			t.Fatalf("%s judged-without-round = %+v", key, m)
		}
	}
}

// ---- 11. guardrail actions -----------------------------------------------

func TestMetricGuardrailActionsBreakdown(t *testing.T) {
	journals := []RunJournal{
		eventJournal("r_a",
			guardrailEvent("pending_read", nil),
			guardrailEvent("pending_unavailable", nil),
			guardrailEvent("resolve", boolPtr(true)),
			decisionEvent("record"),
			decisionEvent("review"),
			reviewEvent("pending"),
		),
		eventJournal("r_b",
			guardrailEvent("resolve", boolPtr(false)),
			guardrailEvent("resolve_failed", boolPtr(true)),
			guardrailEvent("resolve", nil), // malformed decision: no approve field
			guardrailEvent("some_future_action", nil),
			JournalEvent{Kind: "message"},
			decisionEvent("block"),
			decisionEvent("quarantine"), // unknown verdict action
			reviewEvent("approved"),
			reviewEvent("expired"),
			reviewEvent("timeout"), // unknown lifecycle state
		),
	}
	m := findMetric(t, Compute(Snapshot{Journals: journals, JournalsPresent: true}), KeyGuardrailActions)
	if m.Unavailable != "" || m.Value == nil {
		t.Fatalf("guardrail actions = %+v", m)
	}
	want := map[string]int64{
		// kind=guardrail (operator read/resolve audit)
		"pending_read": 1, "pending_unavailable": 1,
		"resolve_approve": 1, "resolve_reject": 1, "resolve_failed": 1,
		"resolve_missing_approve": 1, "unknown_action": 1,
		// kind=guardrail_decision (harness verdict per tool call)
		"decision_record": 1, "decision_review": 1, "decision_block": 1,
		"unknown_decision_action": 1,
		// kind=guardrail_review (approval lifecycle)
		"review_pending": 1, "review_approved": 1, "review_expired": 1,
		"unknown_review_state": 1,
	}
	for k, v := range want {
		if m.Breakdown[k] != v {
			t.Fatalf("breakdown[%s] = %d, want %d (all=%v)", k, m.Breakdown[k], v, m.Breakdown)
		}
	}
	sum := int64(0)
	for _, v := range m.Breakdown {
		sum += v
	}
	if sum != 15 {
		t.Fatalf("breakdown does not sum to the event total: sum=%d all=%v", sum, m.Breakdown)
	}
	if m.Numerator != 15 || *m.Value != 15 {
		t.Fatalf("guardrail total = %+v, want 15 events", m)
	}
	if m.Denominator != 2 {
		t.Fatalf("runs with guardrail events = %d, want 2", m.Denominator)
	}
	details := detailValues(m)
	if details["journal_guardrail_events"] != 7 || details["decision_events"] != 4 || details["review_events"] != 4 {
		t.Fatalf("event family details = %v", details)
	}
}

// Data written before the guardrail runtime kinds existed has only kind=guardrail:
// the breakdown must keep exactly the old buckets, and unknown values of the new
// kinds must stay visible rather than being dropped or folded into a known one.
func TestMetricGuardrailActionsLegacyAndUnknownValues(t *testing.T) {
	legacy := Snapshot{Journals: []RunJournal{eventJournal("r_old",
		guardrailEvent("pending_read", nil),
		guardrailEvent("resolve", boolPtr(true)),
	)}, JournalsPresent: true}
	m := findMetric(t, Compute(legacy), KeyGuardrailActions)
	if m.Value == nil || *m.Value != 2 || m.Denominator != 1 {
		t.Fatalf("legacy guardrail metric = %+v, want 2 events over 1 run", m)
	}
	if m.Breakdown["pending_read"] != 1 || m.Breakdown["resolve_approve"] != 1 {
		t.Fatalf("legacy breakdown = %v", m.Breakdown)
	}
	for _, key := range []string{"decision_record", "review_pending", "unknown_decision_action", "unknown_review_state"} {
		if _, ok := m.Breakdown[key]; ok {
			t.Fatalf("legacy data produced %q: %v", key, m.Breakdown)
		}
	}
	details := detailValues(m)
	if details["decision_events"] != 0 || details["review_events"] != 0 {
		t.Fatalf("legacy event family details = %v", details)
	}

	// Unknown action/state on the new kinds are counted, not dropped.
	unknown := Snapshot{Journals: []RunJournal{eventJournal("r_new",
		decisionEvent("quarantine"),
		reviewEvent("timeout"),
		decisionEvent(""), // field missing on a malformed row
		reviewEvent(""),   // field missing on a malformed row
	)}, JournalsPresent: true}
	m = findMetric(t, Compute(unknown), KeyGuardrailActions)
	if m.Value == nil || *m.Value != 4 || m.Unavailable != "" {
		t.Fatalf("unknown-values metric = %+v, want 4 events", m)
	}
	if m.Breakdown["unknown_decision_action"] != 2 || m.Breakdown["unknown_review_state"] != 2 {
		t.Fatalf("unknown values not surfaced: %v", m.Breakdown)
	}
}

func TestMetricGuardrailActionsUnavailableWithoutSignal(t *testing.T) {
	m := findMetric(t, Compute(Snapshot{}), KeyGuardrailActions)
	if m.Unavailable != "no_guardrail_signal" || m.Value != nil {
		t.Fatalf("empty snapshot = %+v, want unavailable(no_guardrail_signal)", m)
	}
	// Journals exist but no guardrail row: not configured vs. never used cannot
	// be told apart from journals alone, so the metric stays unavailable.
	snap := Snapshot{
		DB:              runs("r_1"),
		Journals:        []RunJournal{eventJournal("r_1", JournalEvent{Kind: "message"})},
		JournalsPresent: true,
	}
	m = findMetric(t, Compute(snap), KeyGuardrailActions)
	if m.Unavailable != "no_guardrail_signal" || m.Value != nil {
		t.Fatalf("journal without guardrail rows = %+v", m)
	}
}

// ---- 12. traffic body coverage -------------------------------------------

func TestMetricTrafficBodyCoverageStructuredFlagsOnly(t *testing.T) {
	captures := []CaptureRecord{
		{Ref: "a", PreviewOnly: boolPtr(true), Truncated: boolPtr(true)},
		{Ref: "b", PreviewOnly: boolPtr(false), Truncated: boolPtr(false), Complete: true, RequestBytes: 10, ResponseBytes: 20},
		// Legacy index rows: no structured flags. Counted and reported as flag
		// missing, excluded from the denominator, never inferred from Error text.
		{Ref: "c", Complete: true, RequestBytes: 1, ResponseBytes: 2},
		{Ref: "d"},
	}
	m := findMetric(t, Compute(Snapshot{Captures: captures, CapturesPresent: true}), KeyTrafficBodyCoverage)
	if m.Unavailable != "" || m.Value == nil || *m.Value != 0.5 {
		t.Fatalf("coverage = %+v, want 1/2 available", m)
	}
	if m.Numerator != 1 || m.Denominator != 2 {
		t.Fatalf("coverage numerator/denominator = %d/%d", m.Numerator, m.Denominator)
	}
	details := detailValues(m)
	if details["captures_total"] != 4 || details["preview_only"] != 1 ||
		details["preview_flag_missing"] != 2 || details["truncated"] != 1 ||
		details["complete_captures"] != 2 {
		t.Fatalf("coverage details = %v", details)
	}
	if details["request_bytes_sum"] != 11 || details["response_bytes_sum"] != 22 {
		t.Fatalf("byte sums = %v", details)
	}
}

func TestMetricTrafficBodyCoverageUnavailable(t *testing.T) {
	m := findMetric(t, Compute(Snapshot{}), KeyTrafficBodyCoverage)
	if m.Unavailable != "no_traffic_signal" || m.Value != nil {
		t.Fatalf("no captures = %+v, want unavailable(no_traffic_signal)", m)
	}
	// Captures exist, but every index row predates the flags: no structured
	// body signal to divide by.
	legacy := Snapshot{Captures: []CaptureRecord{{Ref: "old-1"}, {Ref: "old-2"}}, CapturesPresent: true}
	m = findMetric(t, Compute(legacy), KeyTrafficBodyCoverage)
	if m.Unavailable != "no_structured_body_flags" || m.Value != nil {
		t.Fatalf("legacy captures = %+v", m)
	}
	if detailValues(m)["captures_total"] != 2 {
		t.Fatalf("legacy capture count missing: %v", m.Details)
	}
}

// ---- 13. media preservation ----------------------------------------------

func TestMetricMediaPreservedCountsAndBytes(t *testing.T) {
	journals := []RunJournal{
		eventJournal("r_1",
			mediaEvent(`{"path":".saker/media/e-0.png","mime":"image/png","bytes":100}`),
			mediaEvent(`{"path":".saker/media/e-1.jpg","mime":"image/jpeg","bytes":200}`),
			JournalEvent{Kind: "message"}, // total journal rows behind the denominator
		),
		eventJournal("r_2",
			mediaEvent(`{"path":".saker/media/e-2.bin"}`),           // raw present, no bytes
			mediaEvent(`{"path":".saker/media/e-3.bin","bytes":0}`), // reported as not positive
		),
		eventJournal("r_3", JournalEvent{Kind: "media"}), // raw missing entirely
	}
	m := findMetric(t, Compute(Snapshot{Journals: journals, JournalsPresent: true}), KeyMediaPreserved)
	if m.Unavailable != "" || m.Value == nil || *m.Value != 5 {
		t.Fatalf("media metric = %+v, want 5 events", m)
	}
	if m.Numerator != 5 || m.Denominator != 6 {
		t.Fatalf("media numerator/denominator = %d/%d, want 5/6", m.Numerator, m.Denominator)
	}
	details := detailValues(m)
	if details["media_total_bytes"] != 300 {
		t.Fatalf("media bytes = %v, want 300", details)
	}
	if details["media_raw_missing"] != 1 || details["media_bytes_nonpositive"] != 2 {
		t.Fatalf("media byte-attribution details = %v", details)
	}
}

func TestMetricMediaPreservedZeroIsExplainedNotUnavailable(t *testing.T) {
	// No media anywhere: 0 is the honest reading (nothing to preserve), not an
	// unavailable metric. The explanation states the scope.
	m := findMetric(t, Compute(Snapshot{}), KeyMediaPreserved)
	if m.Unavailable != "" || m.Value == nil || *m.Value != 0 || m.Explanation == "" {
		t.Fatalf("no-media snapshot = %+v, want available 0 with explanation", m)
	}
	// A malformed raw payload is reported, not silently dropped.
	bad := Snapshot{Journals: []RunJournal{eventJournal("r_1", mediaEvent(`{"bytes":"many"}`))}, JournalsPresent: true}
	m = findMetric(t, Compute(bad), KeyMediaPreserved)
	if m.Value == nil || *m.Value != 1 || detailValues(m)["media_raw_malformed"] != 1 {
		t.Fatalf("malformed media raw = %+v", m)
	}
}

// ---- shared contract -----------------------------------------------------

func TestCapabilityKindCoversJournalSignals(t *testing.T) {
	for _, kind := range []string{
		"status", "guardrail", "guardrail_decision", "guardrail_review", "media",
	} {
		if !CapabilityKind(kind) {
			t.Fatalf("kind %q should be a capability row", kind)
		}
	}
	for _, kind := range []string{"message", "tool_call", "tool_result", "raw", ""} {
		if CapabilityKind(kind) {
			t.Fatalf("kind %q should not be retained", kind)
		}
	}
}
