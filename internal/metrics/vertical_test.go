package metrics

// P0-5 metric tests: every metric gets a positive case, the failure modes the
// task names (empty project, schema-inconsistent/canceled/unexecuted data,
// note/verified/unverified rows, missing operator-scope file), and the
// "structured only, never keyword" boundary. The tests build store.DB values
// directly so they never read data/ and stay fast.

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return t0.Add(time.Duration(seconds) * time.Second) }

func findMetric(t *testing.T, ms []Metric, key string) Metric {
	t.Helper()
	for _, m := range ms {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %q missing", key)
	return Metric{}
}

func notebookChange(version uint64, writer, runID, state string, written time.Time) store.Change {
	n := model.Notebook{
		ID:        collab.CommanderID,
		NoteRef:   model.NoteRef{Version: version, SHA256: store.NoteHash(state), Writer: writer, RunID: runID},
		State:     state,
		WrittenAt: written,
	}
	raw, _ := json.Marshal(n)
	return store.Change{Kind: "notebook", ID: collab.CommanderID, Operation: "update", Version: version, At: written, Value: raw}
}

// ---- 1. handoff receipt valid rate ---------------------------------------

func TestMetricHandoffReceiptValidRatePositiveAndNegative(t *testing.T) {
	receipt := model.NoteRef{Version: 1, SHA256: store.NoteHash("state-v1"), Writer: collab.CommanderID, RunID: "r_cmd"}
	run := model.Run{
		ID: "r_cmd", Purpose: "commander", Status: model.RunDone,
		StartedAt: at(0), EndedAt: ptrTime(at(60)),
		NoteWrites: []model.NoteRef{receipt},
	}
	db := store.DB{
		Runs:      []model.Run{run},
		Notebooks: []model.Notebook{{ID: collab.CommanderID, NoteRef: receipt, State: "state-v1", WrittenAt: at(10)}},
		Memory:    &store.Memory{History: []store.Change{notebookChange(1, collab.CommanderID, "r_cmd", "state-v1", at(10))}},
	}
	valid := Snapshot{DB: db, DecisionsPresent: true, Decisions: []Decision{
		{RunID: "r_cmd", Handoff: true, HandoffReceipt: &receipt, At: at(61)},
	}}
	m := findMetric(t, Compute(valid), KeyHandoffReceiptValidRate)
	if m.Unavailable != "" {
		t.Fatalf("unexpected unavailable: %s (%s)", m.Unavailable, m.Explanation)
	}
	if m.Denominator != 1 || m.Numerator != 1 || m.Value == nil || *m.Value != 1 {
		t.Fatalf("valid receipt metric = %+v, want 1/1", m)
	}

	// The ledger, not the decision line, decides: an operator-written note is
	// not a commander handoff receipt even when the decision log claims one.
	operator := db
	operator.Notebooks = []model.Notebook{{ID: collab.CommanderID, NoteRef: model.NoteRef{Version: 1, SHA256: store.NoteHash("state-v1"), Writer: collab.OperatorID}, State: "state-v1"}}
	operator.Memory = &store.Memory{History: []store.Change{notebookChange(1, collab.OperatorID, "", "state-v1", at(10))}}
	bad := Snapshot{DB: operator, Decisions: []Decision{{RunID: "r_cmd", Handoff: true, HandoffReceipt: &receipt, At: at(61)}}}
	m = findMetric(t, Compute(bad), KeyHandoffReceiptValidRate)
	if m.Numerator != 0 || m.Denominator != 1 || m.Value == nil || *m.Value != 0 {
		t.Fatalf("operator note counted as valid receipt: %+v", m)
	}
	if m.Breakdown["notebook_writer_not_commander"] != 1 {
		t.Fatalf("missing reason for operator note: %v", m.Breakdown)
	}

	// SHA mismatch between the run receipt and the stored note is invalid.
	tampered := db
	tampered.Runs = []model.Run{{
		ID: "r_cmd", Purpose: "commander", Status: model.RunDone,
		StartedAt: at(0), EndedAt: ptrTime(at(60)),
		NoteWrites: []model.NoteRef{{Version: 1, SHA256: "deadbeef", Writer: collab.CommanderID, RunID: "r_cmd"}},
	}}
	snap := Snapshot{DB: tampered, Decisions: []Decision{{RunID: "r_cmd", Handoff: true, At: at(61)}}}
	m = findMetric(t, Compute(snap), KeyHandoffReceiptValidRate)
	if m.Numerator != 0 || m.Breakdown["receipt_hash_mismatch"] != 1 {
		t.Fatalf("hash mismatch not rejected: %+v", m)
	}

	// A decision log receipt that disagrees with the ledger NoteRef is invalid:
	// the log cannot upgrade a different note write into this round's receipt.
	wrong := receipt
	wrong.SHA256 = "other"
	m = findMetric(t, Compute(Snapshot{DB: db, Decisions: []Decision{
		{RunID: "r_cmd", Handoff: true, HandoffReceipt: &wrong, At: at(61)},
	}}), KeyHandoffReceiptValidRate)
	if m.Numerator != 0 || m.Breakdown["decision_receipt_mismatch"] != 1 {
		t.Fatalf("decision receipt mismatch not rejected: %+v", m)
	}
}

func TestMetricHandoffReceiptWindowAttemptWithoutReceipt(t *testing.T) {
	// A commander round during which an operator notebook write happened is a
	// structured handoff attempt (historical data has no decision handoff flag).
	db := store.DB{
		Runs: []model.Run{{
			ID: "r_cmd", Purpose: "commander", Status: model.RunDone,
			StartedAt: at(0), EndedAt: ptrTime(at(60)),
		}},
		Notebooks: []model.Notebook{{ID: collab.CommanderID, NoteRef: model.NoteRef{Version: 3, Writer: collab.OperatorID}, State: "s"}},
		Memory:    &store.Memory{History: []store.Change{notebookChange(3, collab.OperatorID, "", "s", at(20))}},
	}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyHandoffReceiptValidRate)
	if m.Denominator != 1 || m.Numerator != 0 {
		t.Fatalf("window attempt = %+v, want 0/1", m)
	}
	if m.Breakdown["no_note_receipt"] != 1 {
		t.Fatalf("reasons = %v", m.Breakdown)
	}
}

func TestMetricHandoffReceiptUnavailableBoundaries(t *testing.T) {
	// Empty project: nothing to divide by.
	m := findMetric(t, Compute(Snapshot{}), KeyHandoffReceiptValidRate)
	if m.Unavailable != "no_commander_runs" || m.Value != nil {
		t.Fatalf("empty ledger metric = %+v", m)
	}
	// Commander rounds but no structured handoff evidence (old decision log with
	// only free-text notes): unavailable, not 0 — a keyword guess is forbidden.
	db := store.DB{Runs: []model.Run{{
		ID: "r_cmd", Purpose: "commander", Status: model.RunDone,
		StartedAt: at(0), EndedAt: ptrTime(at(60)),
	}}}
	m = findMetric(t, Compute(Snapshot{DB: db, DecisionsPresent: true, Decisions: []Decision{{RunID: "r_cmd", At: at(61)}}}), KeyHandoffReceiptValidRate)
	if m.Unavailable != "no_structured_handoff_rounds" {
		t.Fatalf("no handoff evidence reason = %q", m.Unavailable)
	}
}

// ---- 2. operator instruction consumption latency -------------------------

func TestMetricOperatorConsumptionLatencyAnchors(t *testing.T) {
	db := store.DB{
		Runs: []model.Run{
			{ID: "r_prompt", Purpose: "commander", Status: model.RunDone, StartedAt: at(2), ExecutionStartedAt: ptrTime(at(2))},
			{ID: "r_ioa", Purpose: "commander", Status: model.RunDone, StartedAt: at(0)},
		},
		Instructions: []model.Instruction{{ID: "i_1", CreatedAt: at(3), CreatedBy: collab.OperatorID}},
		Messages: []model.Message{
			{ID: "m_prompt", FromParticipantID: collab.OperatorID, CreatedAt: at(0), DeliveredRunID: "r_prompt", DeliveryPath: "prompt", UpdatedAt: at(2)},
			{ID: "m_ioa", FromParticipantID: collab.OperatorID, CreatedAt: at(0), DeliveredRunID: "r_ioa", DeliveryPath: "ioa", UpdatedAt: at(1)},
			{ID: "m_instr", FromParticipantID: collab.OperatorID, CreatedAt: at(0), InstructionID: "i_1"},
			{ID: "m_unconsumed", FromParticipantID: collab.OperatorID, CreatedAt: at(0)},
			{ID: "m_missing_run", FromParticipantID: collab.OperatorID, CreatedAt: at(0), DeliveredRunID: "r_gone", DeliveryPath: "prompt"},
			{ID: "m_clamped", FromParticipantID: collab.OperatorID, CreatedAt: at(5), DeliveredRunID: "r_prompt", DeliveryPath: "prompt"},
			{ID: "m_batch", FromParticipantID: collab.OperatorID, CreatedAt: at(0), DirectiveRecipients: []string{"a", "b"}, Kind: "directive_batch"},
		},
	}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyOperatorInstructionConsumptionLatency)
	if m.Unavailable != "" {
		t.Fatalf("unexpected unavailable: %s", m.Unavailable)
	}
	// 2000 + 1000 + 3000 + 0 (clamped) = 6000 ms over 4 consumed messages.
	if m.Denominator != 4 || m.Numerator != 6000 || m.Value == nil || *m.Value != 1500 {
		t.Fatalf("consumption metric = %+v, want 6000/4", m)
	}
	if m.Breakdown["run_prompt"] != 2 || m.Breakdown["run_live_ioa"] != 1 || m.Breakdown["instruction"] != 1 {
		t.Fatalf("anchors = %v", m.Breakdown)
	}
	details := map[string]float64{}
	for _, d := range m.Details {
		details[d.Name] = d.Value
	}
	if details["unconsumed_messages"] != 3 || details["missing_delivery_run"] != 1 || details["clamped_negative_latency"] != 1 || details["batch_container_messages"] != 1 {
		t.Fatalf("details = %v", details)
	}
}

func TestMetricOperatorConsumptionUnavailable(t *testing.T) {
	m := findMetric(t, Compute(Snapshot{}), KeyOperatorInstructionConsumptionLatency)
	if m.Unavailable != "no_operator_messages" {
		t.Fatalf("empty ledger = %q", m.Unavailable)
	}
	// Delivered state without any structured consumption anchor: unavailable,
	// with the unconsumed kind breakdown left visible.
	db := store.DB{Messages: []model.Message{{
		ID: "m1", FromParticipantID: collab.OperatorID, CreatedAt: at(0), Status: model.MessageAccepted, Kind: "directive",
	}}}
	m = findMetric(t, Compute(Snapshot{DB: db}), KeyOperatorInstructionConsumptionLatency)
	if m.Unavailable != "no_consumed_operator_messages" || m.Value != nil {
		t.Fatalf("unconsumed-only ledger = %+v", m)
	}
	if m.Breakdown["directive"] != 1 {
		t.Fatalf("unconsumed breakdown = %v", m.Breakdown)
	}
}

func TestMetricOperatorConsumptionInstructionSource(t *testing.T) {
	// An operator message promoted to a persistent instruction is consumed at the
	// moment that instruction becomes effective, recorded through
	// instruction.source_message_id (not by parsing the prose).
	db := store.DB{
		Instructions: []model.Instruction{{ID: "i_1", CreatedAt: at(4), CreatedBy: collab.OperatorID, SourceMessageID: "m_src"}},
		Messages: []model.Message{
			{ID: "m_src", FromParticipantID: collab.OperatorID, CreatedAt: at(0)},
			{ID: "m_other", FromParticipantID: collab.CommanderID, CreatedAt: at(0)},
		},
	}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyOperatorInstructionConsumptionLatency)
	if m.Unavailable != "" || m.Numerator != 4000 || m.Denominator != 1 {
		t.Fatalf("instruction_source anchor = %+v, want 4000ms/1", m)
	}
	if m.Breakdown["instruction_source"] != 1 {
		t.Fatalf("anchor attribution = %v", m.Breakdown)
	}
}

// ---- 3. facts after stop --------------------------------------------------

func TestMetricFactsAfterStopEntityAndTime(t *testing.T) {
	db := store.DB{Facts: []model.Fact{
		{ID: "f_before", EntityRef: "host:10.0.0.1", CreatedAt: at(5)},
		{ID: "f_after", EntityRef: "host:10.0.0.1", CreatedAt: at(11)},
		{ID: "f_boundary", EntityRef: "host:10.0.0.2", CreatedAt: at(10)}, // equal is not after
		{ID: "f_other", EntityRef: "host:10.0.0.3", CreatedAt: at(20)},
		{ID: "f_no_ref", CreatedAt: at(20)},
	}}
	scope := []OperatorScopeEntry{
		{V: 1, ID: "os_1", Kind: "stop", At: at(10), Actor: collab.OperatorID, EntityRefs: []string{"host:10.0.0.1", "host:10.0.0.2"}},
		{V: 1, ID: "os_2", Kind: "authorization", At: at(1), Actor: collab.OperatorID, EntityRefs: []string{"host:10.0.0.1"}},
		{V: 1, ID: "os_3", Kind: "stop", At: at(15), Actor: collab.OperatorID}, // no entity refs: recorded, matches nothing
	}
	m := findMetric(t, Compute(Snapshot{DB: db, OperatorScope: scope, OperatorScopePresent: true}), KeyFactsAfterStop)
	if m.Unavailable != "" {
		t.Fatalf("unexpected unavailable: %s", m.Unavailable)
	}
	if m.Numerator != 1 || m.Value == nil || *m.Value != 1 {
		t.Fatalf("late fact count = %+v, want 1 (f_after)", m)
	}
	details := map[string]float64{}
	for _, d := range m.Details {
		details[d.Name] = d.Value
	}
	if details["stop_entries"] != 2 || details["stop_entries_with_entity_refs"] != 1 || details["matched_fact_stop_pairs"] != 1 {
		t.Fatalf("details = %v", details)
	}
}

func TestMetricFactsAfterStopMissingFileIsZeroWithExplanation(t *testing.T) {
	db := store.DB{Facts: []model.Fact{{ID: "f1", EntityRef: "host:1", CreatedAt: at(0)}}}
	m := findMetric(t, Compute(Snapshot{DB: db, OperatorScopePresent: false}), KeyFactsAfterStop)
	if m.Unavailable != "" || m.Value == nil || *m.Value != 0 || m.Explanation == "" {
		t.Fatalf("missing sidecar metric = %+v, want available 0 with explanation", m)
	}
	if m.Denominator != 1 {
		t.Fatalf("denominator should be all facts, got %d", m.Denominator)
	}
	// Present but without stop entries: still 0, differently explained.
	m = findMetric(t, Compute(Snapshot{DB: db, OperatorScopePresent: true}), KeyFactsAfterStop)
	if m.Value == nil || *m.Value != 0 || m.Explanation == "" {
		t.Fatalf("empty sidecar metric = %+v", m)
	}
}

// ---- 4. unconsumed verified facts ----------------------------------------

func TestMetricUnconsumedVerifiedFacts(t *testing.T) {
	db := store.DB{
		Facts: []model.Fact{
			{ID: "f_premise", Kind: "vuln", Status: "verified"},
			{ID: "f_context", Kind: "host", Status: "verified"},
			{ID: "f_pin", Kind: "service", Status: "verified"},
			{ID: "f_unconsumed", Kind: "cred", Status: "verified"},
			{ID: "f_unverified", Kind: "vuln", Status: "unverified"},
			{ID: "f_note", Kind: "note", Status: "unverified"},
			{ID: "f_rejected", Kind: "vuln", Status: "rejected"},
		},
		Intents: []model.Intent{{
			ID: "in_1", FromFactIDs: []string{"f_premise"}, ContextFactIDs: []string{"f_context"}, PinnedFactIDs: []string{"f_pin"},
		}},
	}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyUnconsumedVerifiedFacts)
	if m.Numerator != 1 || m.Denominator != 4 || m.Value == nil || *m.Value != 1 {
		t.Fatalf("unconsumed metric = %+v, want 1/4", m)
	}
	if m.Breakdown["kind_cred"] != 1 {
		t.Fatalf("breakdown = %v", m.Breakdown)
	}
	details := map[string]float64{}
	for _, d := range m.Details {
		details[d.Name] = d.Value
	}
	if details["consumed_via_premise"] != 1 || details["consumed_via_reference"] != 1 || details["consumed_via_pin"] != 1 {
		t.Fatalf("consumption attribution = %v", details)
	}

	// Empty ledger: count is 0 and the denominator says there is nothing to divide.
	m = findMetric(t, Compute(Snapshot{}), KeyUnconsumedVerifiedFacts)
	if m.Value == nil || *m.Value != 0 || m.Denominator != 0 || m.Explanation == "" {
		t.Fatalf("empty ledger metric = %+v", m)
	}
}

// ---- 5. direction stall rate ---------------------------------------------

func TestMetricDirectionStallRate(t *testing.T) {
	db := store.DB{
		Runs: []model.Run{
			{ID: "r_new", Purpose: "commander", Status: model.RunDone, StartedAt: at(0), EndedAt: ptrTime(at(60))},
			{ID: "r_stall", Purpose: "commander", Status: model.RunDone, StartedAt: at(100), EndedAt: ptrTime(at(160))},
			{ID: "r_handoff", Purpose: "commander", Status: model.RunDone, StartedAt: at(200), EndedAt: ptrTime(at(260))},
			{ID: "r_unfinished", Purpose: "commander", Status: model.RunRunning, StartedAt: at(300)},
			{ID: "r_executor", Purpose: "", Status: model.RunDone, StartedAt: at(400), EndedAt: ptrTime(at(460))},
		},
		Intents: []model.Intent{
			{ID: "in_new", CreatedBy: collab.CommanderID, CreatedAt: at(30)},       // during r_new
			{ID: "in_delegated", CreatedBy: "pt_x", CreatedAt: at(40)},             // r_new delegated child
			{ID: "in_operator", CreatedBy: collab.OperatorID, CreatedAt: at(120)},  // not a commander direction
			{ID: "in_outside", CreatedBy: collab.CommanderID, CreatedAt: at(1000)}, // outside every window
		},
		Notebooks: []model.Notebook{{ID: collab.CommanderID, NoteRef: model.NoteRef{Version: 1, Writer: collab.OperatorID}, State: "s"}},
		Memory:    &store.Memory{History: []store.Change{notebookChange(1, collab.OperatorID, "", "s", at(210))}},
	}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyDirectionStallRate)
	if m.Unavailable != "" {
		t.Fatalf("unexpected unavailable: %s", m.Unavailable)
	}
	// Non-handoff rounds: r_new (new direction) and r_stall (none) = 1/2.
	if m.Numerator != 1 || m.Denominator != 2 || m.Value == nil || *m.Value != 0.5 {
		t.Fatalf("stall metric = %+v, want 1/2", m)
	}
	details := map[string]float64{}
	for _, d := range m.Details {
		details[d.Name] = d.Value
	}
	if details["handoff_rounds"] != 1 || details["handoff_rounds_stalled"] != 1 || details["unfinished_rounds"] != 1 {
		t.Fatalf("details = %v", details)
	}

	// All finished rounds are handoff rounds: the main denominator is empty.
	allHandoff := Snapshot{DB: store.DB{
		Runs:      []model.Run{{ID: "r_h", Purpose: "commander", Status: model.RunDone, StartedAt: at(0), EndedAt: ptrTime(at(60))}},
		Notebooks: []model.Notebook{{ID: collab.CommanderID, NoteRef: model.NoteRef{Version: 1, Writer: collab.OperatorID}, State: "s"}},
		Memory:    &store.Memory{History: []store.Change{notebookChange(1, collab.OperatorID, "", "s", at(10))}},
	}}
	m = findMetric(t, Compute(allHandoff), KeyDirectionStallRate)
	if m.Unavailable != "no_non_handoff_rounds" {
		t.Fatalf("all-handoff reason = %q", m.Unavailable)
	}
	if m := findMetric(t, Compute(Snapshot{}), KeyDirectionStallRate); m.Unavailable != "no_commander_runs" {
		t.Fatalf("empty reason = %q", m.Unavailable)
	}
}

// ---- 6. environment misjudgement ratio -----------------------------------

func TestMetricEnvironmentMisjudgementIsUnavailableNoKeywordClassification(t *testing.T) {
	base := store.DB{
		Facts: []model.Fact{
			{ID: "f_note", Kind: "note", Title: "环境不可达：目标网络拒绝连接", Summary: "firewall dropped", Status: "unverified"},
			{ID: "f_verified", Kind: "vuln", Title: "越权读取订单", Status: "verified"},
		},
	}
	m := findMetric(t, Compute(Snapshot{DB: base}), KeyEnvironmentMisjudgementRatio)
	if m.Unavailable != "no_structured_signal" || m.Value != nil {
		t.Fatalf("environment metric = %+v, want unavailable(no_structured_signal)", m)
	}
	if m.Denominator != 1 {
		t.Fatalf("verified denominator = %d", m.Denominator)
	}
	// Changing the note's prose must not change the metric: that is the proof
	// that no title/summary keyword classification happens.
	other := store.DB{Facts: []model.Fact{
		{ID: "f_note", Kind: "note", Title: "完全不同的自由文本", Summary: "DNS resolution failed", Status: "unverified"},
		{ID: "f_verified", Kind: "vuln", Title: "别的结论", Status: "verified"},
	}}
	m2 := findMetric(t, Compute(Snapshot{DB: other}), KeyEnvironmentMisjudgementRatio)
	if m2.Unavailable != m.Unavailable || m2.Numerator != m.Numerator || m2.Denominator != m.Denominator {
		t.Fatalf("prose changed the environment metric: %+v vs %+v", m, m2)
	}
}

// ---- 7. review queue and execution timing --------------------------------

func TestMetricReviewQueueAndExecutionTiming(t *testing.T) {
	five, hundred := int64(5), int64(100)
	db := store.DB{Runs: []model.Run{
		{ID: "v_measured", Purpose: "verifier", Status: model.RunDone, QueuedAt: ptrTime(at(0)),
			ExecutionStartedAt: ptrTime(at(0)), QueueMS: &hundred, ExecutionMS: &five, EndedAt: ptrTime(at(1))},
		{ID: "v_canceled_queued", Purpose: "verifier", Status: model.RunCanceled, QueuedAt: ptrTime(at(0)), QueueMS: &five},
		{ID: "v_timestamp_only", Purpose: "verifier", Status: model.RunDone, QueuedAt: ptrTime(at(10)),
			ExecutionStartedAt: ptrTime(at(12)), EndedAt: ptrTime(at(13))},
		{ID: "v_missing", Purpose: "verifier", Status: model.RunFailed},
		{ID: "executor", Purpose: "", Status: model.RunDone},
	}}
	m := findMetric(t, Compute(Snapshot{DB: db}), KeyReviewQueueAndExecutionMS)
	if m.Unavailable != "" {
		t.Fatalf("unexpected unavailable: %s", m.Unavailable)
	}
	// Queue measured: 100 (stored) + 5 (stored, canceled in queue) + 2000 (from
	// timestamps) = 2105 ms over 3 runs.
	if m.Denominator != 3 || m.Numerator != 2105 {
		t.Fatalf("queue metric = %+v, want 2105/3", m)
	}
	details := map[string]float64{}
	for _, d := range m.Details {
		details[d.Name] = d.Value
	}
	if details["verifier_runs"] != 4 || details["canceled_while_queued"] != 1 ||
		details["queue_from_timestamps"] != 1 || details["not_started_runs"] != 2 ||
		details["execution_measured_runs"] != 2 {
		t.Fatalf("details = %v", details)
	}

	// No verifier runs: unavailable, not a fake zero.
	m = findMetric(t, Compute(Snapshot{}), KeyReviewQueueAndExecutionMS)
	if m.Unavailable != "no_verifier_runs" {
		t.Fatalf("no verifier reason = %q", m.Unavailable)
	}
	// Verifier runs before timing fields existed: queue unavailable, canceled /
	// not-executed counts still reported.
	old := store.DB{Runs: []model.Run{
		{ID: "v_old", Purpose: "verifier", Status: model.RunDone},
		{ID: "v_canceled", Purpose: "verifier", Status: model.RunCanceled},
	}}
	m = findMetric(t, Compute(Snapshot{DB: old}), KeyReviewQueueAndExecutionMS)
	if m.Unavailable != "no_measured_queue_ms" || m.Value != nil {
		t.Fatalf("old verifier metric = %+v", m)
	}
	foundCanceled := false
	canceledNoQueue := 0.0
	for _, d := range m.Details {
		if d.Name == "canceled_while_queued" && d.Value == 1 {
			foundCanceled = true
		}
		if d.Name == "canceled_queued_without_queue_ms" {
			canceledNoQueue = d.Value
		}
	}
	if !foundCanceled {
		t.Fatalf("canceled-queued count missing: %v", m.Details)
	}
	if canceledNoQueue != 1 {
		t.Fatalf("canceled run without a recorded queue time must be reported, details=%v", m.Details)
	}
}

// ---- report contract ------------------------------------------------------

func TestMetricComputeIsStableAndComplete(t *testing.T) {
	// A snapshot with map-derived details (message kinds) exercises the ordering
	// that a JSON diff across runs would expose.
	snap := Snapshot{DB: store.DB{Messages: []model.Message{
		{ID: "m1", FromParticipantID: collab.OperatorID, CreatedAt: at(0), Kind: "say"},
		{ID: "m2", FromParticipantID: collab.OperatorID, CreatedAt: at(0), Kind: "directive"},
		{ID: "m3", FromParticipantID: collab.OperatorID, CreatedAt: at(0), Kind: "instruction"},
	}}}
	first := Compute(snap)
	second := Compute(snap)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Compute is not deterministic")
	}
	raw1, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw1) != string(raw2) {
		t.Fatal("Compute JSON differs between two calls on the same snapshot")
	}
	if len(first) != 13 {
		t.Fatalf("metric count = %d, want 7 vertical + 6 capability-use metrics", len(first))
	}
	seen := map[string]bool{}
	for i, m := range first {
		if m.Order != i+1 {
			t.Fatalf("metric %d has order %d", i, m.Order)
		}
		if seen[m.Key] {
			t.Fatalf("duplicate key %s", m.Key)
		}
		seen[m.Key] = true
		if m.Definition == "" {
			t.Fatalf("metric %s has no definition", m.Key)
		}
		if m.Value == nil && m.Unavailable == "" {
			t.Fatalf("metric %s has neither value nor unavailable reason", m.Key)
		}
		if m.Value != nil && m.Unavailable != "" {
			t.Fatalf("metric %s has both value and unavailable reason", m.Key)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
