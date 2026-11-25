package metrics

// P0-5 vertical metrics: seven read-only, machine-checkable measurements over a
// committed ledger snapshot plus the project sidecar files. The taskbook asks for
// measurements, not goals: every metric is "只记录、不设阈值", carries its own
// definition string, and says explicitly when the available structured data
// cannot support it (unavailable reason) instead of substituting a keyword guess.
//
// The functions here are pure: the caller (cmd/tw-metrics) does all file IO and
// passes a Snapshot. That keeps the unit tests off the real data directory and
// makes "source data zero writes" a property of the caller, testable in isolation.
//
// Ground rules (agent.md):
//   - structured fields only: run purpose/status/window, ledger note writes and
//     notebook versions, message delivery rows, verifier timing fields, and the
//     explicit operator-scope sidecar. No title/summary/tag keyword rules, no
//     model-name special cases.
//   - a missing structured signal is reported as unavailable with a reason; it
//     is never reconstructed from prose.
//   - token counts are not part of these metrics and no threshold is applied.

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// SchemaVersion is the stable output contract of Compute. Bump it when a metric
// key, unit or numerator/denominator meaning changes.
//
// v2 appends the P3-H capability-use metrics (orders 8-13, capability.go); the
// seven v1 metrics keep their keys, units, orders and numerator/denominator
// meanings, so a v1 consumer that reads them by key is unaffected.
const SchemaVersion = 2

// Metric keys, in taskbook order.
const (
	KeyHandoffReceiptValidRate               = "handoff_receipt_valid_rate"
	KeyOperatorInstructionConsumptionLatency = "operator_instruction_consumption_latency"
	KeyFactsAfterStop                        = "facts_after_stop"
	KeyUnconsumedVerifiedFacts               = "unconsumed_verified_facts"
	KeyDirectionStallRate                    = "direction_stall_rate"
	KeyEnvironmentMisjudgementRatio          = "environment_misjudgement_ratio"
	KeyReviewQueueAndExecutionMS             = "review_queue_and_execution_ms"
)

// Metric is one measurement. Value is nil exactly when Unavailable is non-empty;
// for a count metric Value equals Numerator, for a rate or mean it is derived
// from Numerator/Denominator and rounded so the JSON is stable.
type Metric struct {
	Key         string           `json:"key"`
	Order       int              `json:"order"`
	Value       *float64         `json:"value"`
	Unit        string           `json:"unit"`
	Numerator   int64            `json:"numerator"`
	Denominator int64            `json:"denominator"`
	Definition  string           `json:"definition"`
	Unavailable string           `json:"unavailable,omitempty"`
	Explanation string           `json:"explanation,omitempty"`
	Breakdown   map[string]int64 `json:"breakdown,omitempty"`
	Details     []Detail         `json:"details,omitempty"`
}

// Detail is one named secondary number of a metric (counts, medians, maxima).
type Detail struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Snapshot is the complete read-only input of Compute: one committed ledger and
// the project files that carry structured control-plane records. Present flags
// distinguish "file absent" from "file present but empty" so a metric can say
// why it is unavailable instead of silently reporting zero.
type Snapshot struct {
	DB store.DB

	Decisions          []Decision
	DecisionsPresent   bool
	DecisionsMalformed int

	OperatorScope          []OperatorScopeEntry
	OperatorScopePresent   bool
	OperatorScopeMalformed int

	// Capability-use sources (P3-H). The caller parses <project>/runs/*.jsonl and
	// <project>/cases/.http/*.json and passes the structured rows; malformed rows
	// are counted by the caller or per source, never guessed at here.
	Journals        []RunJournal
	JournalsPresent bool

	Captures          []CaptureRecord
	CapturesPresent   bool
	CapturesMalformed int
}

// Decision mirrors the structured fields of one line of <project>/decisions.jsonl
// (internal/decider.Decision). Only the fields the vertical metrics use are
// modelled; the project file is a control-plane record, not a second fact store.
type Decision struct {
	RunID          string           `json:"run_id,omitempty"`
	Handoff        bool             `json:"handoff,omitempty"`
	HandoffReceipt *model.NoteRef   `json:"handoff_receipt,omitempty"`
	Actions        []DecisionAction `json:"actions,omitempty"`
	NewFacts       int              `json:"new_facts,omitempty"`
	Round          int              `json:"round,omitempty"`
	At             time.Time        `json:"at,omitempty"`
}

// DecisionAction is one ledger mutation the round's API handler recorded.
type DecisionAction struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

// OperatorScopeEntry is one line of <project>/operator-scope.jsonl (P0-4
// contract). Only kind, at and entity_refs participate in metrics; the other
// fields are parsed so a malformed row is still visible and so the file is read
// per its published contract.
type OperatorScopeEntry struct {
	V          int       `json:"v"`
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Summary    string    `json:"summary"`
	IntentIDs  []string  `json:"intent_ids,omitempty"`
	EntityRefs []string  `json:"entity_refs,omitempty"`
	Note       string    `json:"note,omitempty"`
}

// Compute returns the metrics in taskbook order: the seven P0-5 vertical
// metrics (orders 1-7) followed by the appended P3-H capability-use metrics
// (orders 8-13). It is deterministic for a given Snapshot; Maps are encoded with
// sorted keys by encoding/json.
func Compute(s Snapshot) []Metric {
	out := []Metric{
		metricHandoffReceiptValid(s),
		metricOperatorConsumption(s),
		metricFactsAfterStop(s),
		metricUnconsumedVerifiedFacts(s),
		metricDirectionStall(s),
		metricEnvironmentMisjudgement(s),
		metricReviewTiming(s),
		metricEvalPassRate(s),
		metricEvalRoundsMax(s),
		metricEvalRoundsAvg(s),
		metricGuardrailActions(s),
		metricTrafficBodyCoverage(s),
		metricMediaPreserved(s),
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// ---- shared helpers -------------------------------------------------------

func f64(v float64) *float64 { return &v }

func ratioValue(numerator, denominator int64) *float64 {
	if denominator <= 0 {
		return nil
	}
	return f64(round(float64(numerator)/float64(denominator), 4))
}

func countValue(n int64) *float64 { return f64(float64(n)) }

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

func median(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return float64(s[(len(s)-1)/2])
}

func percentileInt64(xs []int64, p int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (len(s) - 1) * p / 100
	return float64(s[idx])
}

func maxInt64(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, v := range xs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func detailCount(name string, n int64) Detail {
	return Detail{Name: name, Value: float64(n), Unit: "count"}
}
func detailMS(name string, v float64) Detail {
	return Detail{Name: name, Value: round(v, 3), Unit: "ms"}
}

// notebookWrite is one version of the commander notebook as recovered from the
// ledger's version history. It is the structured memory of every handoff attempt:
// a handoff round is expected to write one of these.
type notebookWrite struct {
	Version uint64
	Writer  string
	RunID   string
	Round   int
	At      time.Time
	SHA256  string
	State   string
}

func notebookWrites(db store.DB) []notebookWrite {
	var out []notebookWrite
	seen := map[uint64]bool{}
	if db.Memory != nil {
		for _, c := range db.Memory.History {
			if c.Kind != "notebook" || c.Operation == "delete" {
				continue
			}
			var n model.Notebook
			if json.Unmarshal(c.Value, &n) != nil {
				continue
			}
			if seen[n.Version] {
				continue
			}
			seen[n.Version] = true
			out = append(out, notebookWrite{
				Version: n.Version, Writer: n.Writer, RunID: n.RunID, Round: n.Round,
				At: c.At, SHA256: n.SHA256, State: n.State,
			})
		}
	}
	for _, n := range db.Notebooks {
		if seen[n.Version] {
			continue
		}
		seen[n.Version] = true
		out = append(out, notebookWrite{
			Version: n.Version, Writer: n.Writer, RunID: n.RunID, Round: n.Round,
			At: n.WrittenAt, SHA256: n.SHA256, State: n.State,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

func findNotebookWrite(writes []notebookWrite, version uint64) (notebookWrite, bool) {
	for _, w := range writes {
		if w.Version == version {
			return w, true
		}
	}
	return notebookWrite{}, false
}

func runWithin(r model.Run, at time.Time) bool {
	if r.EndedAt == nil {
		return false
	}
	return !at.Before(r.StartedAt) && !at.After(*r.EndedAt)
}

func notebookWriteInWindow(r model.Run, writes []notebookWrite) bool {
	for _, w := range writes {
		if runWithin(r, w.At) {
			return true
		}
	}
	return false
}

// ---- 1. handoff receipt valid rate ---------------------------------------

func metricHandoffReceiptValid(s Snapshot) Metric {
	const key = KeyHandoffReceiptValidRate
	m := Metric{
		Key:   key,
		Order: 1,
		Unit:  "ratio",
		Definition: "交接回执有效率 = 有效回执的指挥官交接轮数 / 可判定为交接轮的指挥官轮数。" +
			"交接轮只由结构化证据判定：账本 notebook 版本写入的写入时间落在 purpose=commander 的 run 窗口 [started_at, ended_at] 内，或 decisions.jsonl 该轮结构化字段 handoff=true。" +
			"有效回执要求：该 run 状态为 done；run.note_writes 中存在 writer=commander 且 run_id=本轮的 NoteRef；该 NoteRef 对应的 notebook 历史版本存在、writer=commander、run_id=本轮、SHA-256 与正文一致；若 decisions.jsonl 记录了 handoff_receipt，则必须与账本 NoteRef 完全一致。" +
			"历史账本笔记写者全为 operator，故历史有效率为 0；只记录、不设阈值。",
	}
	db := s.DB
	commanderRuns := make([]model.Run, 0, len(db.Runs))
	runByID := make(map[string]model.Run, len(db.Runs))
	for _, r := range db.Runs {
		runByID[r.ID] = r
		if r.Purpose == "commander" {
			commanderRuns = append(commanderRuns, r)
		}
	}
	writes := notebookWrites(db)

	// Attempts are keyed by commander run id; the evidence basis is kept for the
	// breakdown so a reader can see whether a round came from the ledger window
	// or the structured decision flag.
	basis := map[string]map[string]bool{}
	addBasis := func(runID, b string) {
		if runID == "" {
			return
		}
		if basis[runID] == nil {
			basis[runID] = map[string]bool{}
		}
		basis[runID][b] = true
	}
	decReceipt := map[string]*model.NoteRef{}
	for _, d := range s.Decisions {
		if !d.Handoff {
			continue
		}
		addBasis(d.RunID, "decisions_handoff")
		if d.HandoffReceipt != nil {
			// The decision file is append-only and chronological, so the last
			// receipt seen for a run is the one that run settled with.
			decReceipt[d.RunID] = d.HandoffReceipt
		}
	}
	for _, r := range commanderRuns {
		if notebookWriteInWindow(r, writes) {
			addBasis(r.ID, "notebook_write_in_window")
		}
	}

	runIDs := make([]string, 0, len(basis))
	for id := range basis {
		runIDs = append(runIDs, id)
	}
	sort.Strings(runIDs)

	if len(runIDs) == 0 {
		if len(commanderRuns) == 0 {
			m.Unavailable = "no_commander_runs"
			m.Explanation = "账本没有 purpose=commander 的 run，无法判定交接轮。"
			return m
		}
		m.Unavailable = "no_structured_handoff_rounds"
		m.Explanation = "账本没有可结构化判定的交接轮：没有 notebook 版本写入落在 commander run 窗口内，decisions.jsonl 也没有 handoff=true 的轮次。" +
			"旧 decision 行只留下自由文本 notes，按约定不用关键词分类，因此本节不可得而不是 0。"
		m.Details = []Detail{detailCount("commander_runs", int64(len(commanderRuns)))}
		return m
	}

	invalid := map[string]int64{}
	basisCounts := map[string]int64{}
	valid := 0
	noteWriters := map[string]int64{}
	for _, id := range runIDs {
		for b := range basis[id] {
			basisCounts[b]++
		}
		r, ok := runByID[id]
		if !ok {
			invalid["run_missing"]++
			continue
		}
		okReceipt, reason := validHandoffReceipt(r, writes, decReceipt[id])
		if okReceipt {
			valid++
			noteWriters["commander"]++
			continue
		}
		invalid[reason]++
		if len(r.NoteWrites) == 0 {
			noteWriters["none"]++
		} else {
			noteWriters[r.NoteWrites[0].Writer]++
		}
	}
	m.Numerator = int64(valid)
	m.Denominator = int64(len(runIDs))
	m.Value = ratioValue(m.Numerator, m.Denominator)
	m.Breakdown = invalid
	for k, v := range basisCounts {
		m.Breakdown["attempt_basis_"+k] = v
	}
	for k, v := range noteWriters {
		m.Breakdown["note_writer_"+k] = v
	}
	m.Details = []Detail{
		detailCount("commander_runs", int64(len(commanderRuns))),
		detailCount("handoff_rounds", int64(len(runIDs))),
		detailCount("valid_receipts", int64(valid)),
		detailCount("notebook_versions", int64(len(writes))),
	}
	return m
}

// validHandoffReceipt recomputes receipt validity from the committed ledger, not
// from the decision log's claim. The decision receipt is only cross-checked: a
// log line cannot make an operator-written note into a commander handoff.
func validHandoffReceipt(r model.Run, writes []notebookWrite, recorded *model.NoteRef) (bool, string) {
	if r.Status != model.RunDone {
		return false, "run_not_done"
	}
	var ref *model.NoteRef
	for i := range r.NoteWrites {
		nw := r.NoteWrites[i]
		if nw.Writer == collab.CommanderID && nw.RunID == r.ID {
			ref = &nw
			break
		}
	}
	if ref == nil {
		for _, nw := range r.NoteWrites {
			if nw.Writer != collab.CommanderID {
				return false, "writer_not_commander"
			}
		}
		return false, "no_note_receipt"
	}
	w, ok := findNotebookWrite(writes, ref.Version)
	if !ok {
		return false, "receipt_not_in_notebook_history"
	}
	if w.Writer != collab.CommanderID {
		return false, "notebook_writer_not_commander"
	}
	if w.RunID != r.ID {
		return false, "notebook_run_mismatch"
	}
	if w.SHA256 != ref.SHA256 || store.NoteHash(w.State) != ref.SHA256 {
		return false, "receipt_hash_mismatch"
	}
	if recorded != nil && *recorded != *ref {
		return false, "decision_receipt_mismatch"
	}
	return true, ""
}

// ---- 2. operator instruction consumption latency -------------------------

func metricOperatorConsumption(s Snapshot) Metric {
	const key = KeyOperatorInstructionConsumptionLatency
	m := Metric{
		Key:   key,
		Order: 2,
		Unit:  "ms",
		Definition: "操作员指令消费延迟 = 操作员消息创建到它第一次被结构化引用（回合输入或持续指令）的平均毫秒数，分子为延迟总和、分母为可判定消费的消息数。" +
			"消费锚点只取账本/旁路结构化字段：(a) message.delivered_run_id 指向的 run（delivery_path=prompt 取该 run 执行开始时间，delivery_path=ioa 取消息投递更新时间，即消息被注入活会话的时刻）；" +
			"(b) instruction 广播消息的 message.instruction_id 对应 instruction.created_at（持续指令生效）；(c) instruction.source_message_id 指回该操作员消息。已投递但没有上述结构化引用、或投递目标 run 不在账本的消息计为未消费。" +
			"延迟为负（时钟/顺序不一致）时按 0 计并单独计数；只记录、不设阈值。",
	}
	db := s.DB
	runByID := make(map[string]model.Run, len(db.Runs))
	for _, r := range db.Runs {
		runByID[r.ID] = r
	}
	instrByID := make(map[string]model.Instruction, len(db.Instructions))
	instrBySource := map[string]model.Instruction{}
	for _, in := range db.Instructions {
		instrByID[in.ID] = in
		if in.SourceMessageID != "" {
			prev, ok := instrBySource[in.SourceMessageID]
			if !ok || in.CreatedAt.Before(prev.CreatedAt) {
				instrBySource[in.SourceMessageID] = in
			}
		}
	}

	total, consumed := 0, 0
	var latencies []int64
	var sum int64
	anchor := map[string]int64{}
	unconsumedKinds := map[string]int64{}
	kinds := map[string]int64{}
	clamped, missingRun, batch := int64(0), int64(0), int64(0)
	toCanceled := int64(0)
	for _, msg := range db.Messages {
		if msg.FromParticipantID != collab.OperatorID {
			continue
		}
		total++
		if msg.Kind != "" {
			kinds[msg.Kind]++
		}
		if len(msg.DirectiveRecipients) > 0 {
			// A fan-out batch row is a container: each recipient got its own
			// message row, so the container itself is not referenced by a run.
			batch++
		}
		var anchors []int64
		var anchorNames []string
		collect := func(at time.Time, name string) {
			if at.IsZero() || msg.CreatedAt.IsZero() {
				return
			}
			ms := at.Sub(msg.CreatedAt).Milliseconds()
			if ms < 0 {
				clamped++
				ms = 0
			}
			anchors = append(anchors, ms)
			anchorNames = append(anchorNames, name)
		}
		if msg.DeliveredRunID != "" {
			if run, ok := runByID[msg.DeliveredRunID]; ok {
				switch msg.DeliveryPath {
				case "prompt":
					collect(executionStart(run), "run_prompt")
				case "ioa":
					collect(msg.UpdatedAt, "run_live_ioa")
				default:
					at := msg.UpdatedAt
					if at.IsZero() {
						at = executionStart(run)
					}
					collect(at, "run_other")
				}
				if run.Status == model.RunCanceled {
					toCanceled++
				}
			} else {
				missingRun++
			}
		}
		if msg.InstructionID != "" {
			if in, ok := instrByID[msg.InstructionID]; ok {
				collect(in.CreatedAt, "instruction")
			}
		}
		if in, ok := instrBySource[msg.ID]; ok {
			collect(in.CreatedAt, "instruction_source")
		}
		if len(anchors) == 0 {
			if msg.Kind != "" {
				unconsumedKinds[msg.Kind]++
			} else {
				unconsumedKinds[""]++
			}
			continue
		}
		consumed++
		best := anchors[0]
		for _, v := range anchors[1:] {
			if v < best {
				best = v
			}
		}
		for i, v := range anchors {
			if v == best {
				anchor[anchorNames[i]]++
				break
			}
		}
		sum += best
		latencies = append(latencies, best)
	}
	if int64(total) == 0 {
		m.Unavailable = "no_operator_messages"
		m.Explanation = "账本没有任何 from participant=operator 的消息，无法计算消费延迟。"
		return m
	}
	details := []Detail{
		detailCount("operator_messages", int64(total)),
		detailCount("consumed_messages", int64(consumed)),
		detailCount("unconsumed_messages", int64(total-consumed)),
		detailCount("batch_container_messages", batch),
		detailCount("missing_delivery_run", missingRun),
		detailCount("clamped_negative_latency", clamped),
		detailCount("consumed_to_canceled_run", toCanceled),
	}
	// Details order must not depend on map iteration: the JSON is a stable
	// artifact operators diff across runs.
	kindNames := make([]string, 0, len(kinds))
	for k := range kinds {
		kindNames = append(kindNames, k)
	}
	sort.Strings(kindNames)
	for _, k := range kindNames {
		details = append(details, detailCount("kind_"+k, kinds[k]))
	}
	if consumed == 0 {
		m.Unavailable = "no_consumed_operator_messages"
		m.Explanation = "存在操作员消息，但没有一条具有账本可判定的消费锚点（delivered_run_id 指向的 run 或持续指令引用）；旧账本可能只记录了投递状态。"
		m.Details = details
		m.Breakdown = unconsumedKinds
		return m
	}
	m.Numerator = sum
	m.Denominator = int64(consumed)
	m.Value = f64(round(float64(sum)/float64(consumed), 3))
	m.Breakdown = anchor
	m.Details = append(details,
		detailMS("latency_median_ms", median(latencies)),
		detailMS("latency_p90_ms", percentileInt64(latencies, 90)),
		detailMS("latency_max_ms", float64(maxInt64(latencies))),
	)
	if consumed < total {
		m.Details = append(m.Details, Detail{Name: "consumption_rate", Value: round(float64(consumed)/float64(total), 4), Unit: "ratio"})
	}
	return m
}

func executionStart(r model.Run) time.Time {
	if r.ExecutionStartedAt != nil {
		return *r.ExecutionStartedAt
	}
	return r.StartedAt
}

// ---- 3. facts after stop --------------------------------------------------

func metricFactsAfterStop(s Snapshot) Metric {
	const key = KeyFactsAfterStop
	m := Metric{
		Key:   key,
		Order: 3,
		Unit:  "count",
		Definition: "停测后事实数 = 记录时间晚于 operator-scope.jsonl 中 kind=stop 条目 at、且 fact.entity_ref 显式属于该条目 entity_refs 的事实数（同一事实被多个 stop 命中只计一次）。" +
			"只按显式实体身份与记录时间派生，不按标题/摘要/关键词匹配；operator-scope.jsonl 缺失时记 0 并注明，不推断。",
	}
	details := []Detail{
		detailCount("facts_total", int64(len(s.DB.Facts))),
	}
	if !s.OperatorScopePresent {
		m.Value = countValue(0)
		m.Numerator = 0
		m.Denominator = int64(len(s.DB.Facts))
		m.Explanation = "项目目录没有 operator-scope.jsonl（P0-4 旁路文件；历史项目在 P0-4 落地前没有它），按 0 条停测记录处理。"
		m.Details = append(details, detailCount("operator_scope_present", 0))
		return m
	}
	stops := make([]OperatorScopeEntry, 0, len(s.OperatorScope))
	for _, e := range s.OperatorScope {
		if e.Kind == "stop" {
			stops = append(stops, e)
		}
	}
	entityFacts := 0
	for _, f := range s.DB.Facts {
		if f.EntityRef != "" {
			entityFacts++
		}
	}
	late := map[string]bool{}
	matchedPairs := int64(0)
	stopsWithRefs := 0
	for _, stop := range stops {
		if len(stop.EntityRefs) == 0 {
			continue
		}
		stopsWithRefs++
		refs := make(map[string]bool, len(stop.EntityRefs))
		for _, ref := range stop.EntityRefs {
			refs[ref] = true
		}
		for _, f := range s.DB.Facts {
			if f.EntityRef == "" || !refs[f.EntityRef] {
				continue
			}
			if f.CreatedAt.After(stop.At) {
				late[f.ID] = true
				matchedPairs++
			}
		}
	}
	m.Value = countValue(int64(len(late)))
	m.Numerator = int64(len(late))
	m.Denominator = int64(len(s.DB.Facts))
	details = append(details,
		detailCount("operator_scope_present", 1),
		detailCount("stop_entries", int64(len(stops))),
		detailCount("stop_entries_with_entity_refs", int64(stopsWithRefs)),
		detailCount("facts_with_entity_ref", int64(entityFacts)),
		detailCount("matched_fact_stop_pairs", matchedPairs),
	)
	m.Details = details
	if len(stops) == 0 {
		m.Explanation = "operator-scope.jsonl 存在但没有 kind=stop 条目，停测后事实数为 0。"
	}
	return m
}

// ---- 4. unconsumed verified facts ----------------------------------------

func metricUnconsumedVerifiedFacts(s Snapshot) Metric {
	const key = KeyUnconsumedVerifiedFacts
	m := Metric{
		Key:   key,
		Order: 4,
		Unit:  "count",
		Definition: "未消费已验证事实数 = status=verified 且未被任何意图以 from_fact_ids（硬前提）、context_fact_ids（参考）或 pinned_fact_ids（钉住）引用的事实数；同一事实出现在多处只计一次，按前提>参考>钉住的顺序归因。" +
			"未验证事实、note 以及被拒绝事实不计入；只记录、不设阈值。",
	}
	premise := map[string]bool{}
	reference := map[string]bool{}
	pin := map[string]bool{}
	for _, in := range s.DB.Intents {
		for _, id := range in.FromFactIDs {
			premise[id] = true
		}
		for _, id := range in.ContextFactIDs {
			reference[id] = true
		}
		for _, id := range in.PinnedFactIDs {
			pin[id] = true
		}
	}
	verified := 0
	var unconsumed []model.Fact
	byPremise, byReference, byPin := 0, 0, 0
	breakdown := map[string]int64{}
	for _, f := range s.DB.Facts {
		if f.Status != "verified" {
			continue
		}
		verified++
		switch {
		case premise[f.ID]:
			byPremise++
		case reference[f.ID]:
			byReference++
		case pin[f.ID]:
			byPin++
		default:
			unconsumed = append(unconsumed, f)
			if f.Kind != "" {
				breakdown["kind_"+f.Kind]++
			}
		}
	}
	m.Numerator = int64(len(unconsumed))
	m.Denominator = int64(verified)
	m.Value = countValue(m.Numerator)
	m.Details = []Detail{
		detailCount("facts_total", int64(len(s.DB.Facts))),
		detailCount("verified_facts", int64(verified)),
		detailCount("consumed_via_premise", int64(byPremise)),
		detailCount("consumed_via_reference", int64(byReference)),
		detailCount("consumed_via_pin", int64(byPin)),
	}
	if verified > 0 {
		m.Details = append(m.Details, Detail{Name: "unconsumed_ratio", Value: round(float64(len(unconsumed))/float64(verified), 4), Unit: "ratio"})
	} else {
		m.Explanation = "账本没有 status=verified 的事实，未消费已验证事实数为 0（分母为 0）。"
	}
	if len(unconsumed) > 0 {
		m.Breakdown = breakdown
	}
	return m
}

// ---- 5. direction stall rate ---------------------------------------------

func metricDirectionStall(s Snapshot) Metric {
	const key = KeyDirectionStallRate
	m := Metric{
		Key:   key,
		Order: 5,
		Unit:  "ratio",
		Definition: "意图停顿率 = 没有产生新意图的非交接指挥官轮数 / 可判定的非交接指挥官轮数。" +
			"指挥官轮 = purpose=commander 且已结束（ended_at 存在）的 run；新意图 = 在 run 窗口 [started_at, ended_at] 内创建、且 created_by=commander 或 delegated_by_run_id=本轮 run 的意图。" +
			"交接轮 = notebook 版本写入落在该 run 窗口内或 decisions.jsonl handoff=true 的指挥官轮；交接轮按设计不派新意图，单独记录、不计入主分母。只记录、不设阈值。",
	}
	db := s.DB
	writes := notebookWrites(db)
	decHandoff := map[string]bool{}
	for _, d := range s.Decisions {
		if d.Handoff {
			decHandoff[d.RunID] = true
		}
	}
	rounds := make([]model.Run, 0, len(db.Runs))
	for _, r := range db.Runs {
		if r.Purpose == "commander" {
			rounds = append(rounds, r)
		}
	}
	if len(rounds) == 0 {
		m.Unavailable = "no_commander_runs"
		m.Explanation = "账本没有 purpose=commander 的 run，无法计算指挥官轮停顿率。"
		return m
	}
	unfinished := 0
	handoff := 0
	considered, stalled := 0, 0
	stalledHandoff := 0
	handoffTotal := 0
	for _, r := range rounds {
		isHandoff := notebookWriteInWindow(r, writes) || decHandoff[r.ID]
		if isHandoff {
			handoffTotal++
		}
		if r.EndedAt == nil {
			unfinished++
			continue
		}
		newDirection := false
		for _, in := range db.Intents {
			if in.CreatedBy != collab.CommanderID {
				continue
			}
			if runWithin(r, in.CreatedAt) {
				newDirection = true
				break
			}
		}
		if isHandoff {
			handoff++
			if !newDirection {
				stalledHandoff++
			}
			continue
		}
		considered++
		if !newDirection {
			stalled++
		}
	}
	m.Details = []Detail{
		detailCount("commander_rounds", int64(len(rounds))),
		detailCount("finished_rounds", int64(len(rounds)-unfinished)),
		detailCount("unfinished_rounds", int64(unfinished)),
		detailCount("handoff_rounds", int64(handoffTotal)),
		detailCount("handoff_rounds_stalled", int64(stalledHandoff)),
	}
	if handoffTotal > 0 {
		m.Details = append(m.Details, Detail{Name: "handoff_stall_ratio", Value: round(float64(stalledHandoff)/float64(handoffTotal), 4), Unit: "ratio"})
	}
	if considered == 0 {
		m.Unavailable = "no_non_handoff_rounds"
		m.Explanation = "所有已结束的指挥官轮都被判定为交接轮，非交接轮分母为 0；交接轮停顿单列在 details 中。"
		m.Details = append(m.Details, detailCount("handoff_rounds_considered", int64(handoff)))
		return m
	}
	m.Numerator = int64(stalled)
	m.Denominator = int64(considered)
	m.Value = ratioValue(m.Numerator, m.Denominator)
	return m
}

// ---- 6. environment misjudgement ratio -----------------------------------

func metricEnvironmentMisjudgement(s Snapshot) Metric {
	const key = KeyEnvironmentMisjudgementRatio
	m := Metric{
		Key:   key,
		Order: 6,
		Unit:  "ratio",
		Definition: "环境误判比例 = 结构化标记为环境类的 note 数 / status=verified 的结论数。" +
			"只允许由结构化字段或显式状态派生：当前 model.Fact 没有“环境类”字段——kind 只有 host/service/cred/vuln/loot/artifact/note，note 是通用线索而非环境分类；tags 是模型自填检索标签，不是显式状态；" +
			"按 title/summary/doc 的关键词分类会引入平台关键词规则，违反 agent.md。因此现有数据无法结构化区分，输出 unavailable(no_structured_signal)，不用关键词猜测。",
	}
	notes := 0
	verified := 0
	for _, f := range s.DB.Facts {
		if f.Kind == "note" {
			notes++
		}
		if f.Status == "verified" {
			verified++
		}
	}
	m.Unavailable = "no_structured_signal"
	m.Explanation = "现有事实模型没有环境类结构化字段：note 是通用线索类型，tags 是模型自填标签而非显式状态；平台不按标题/摘要/关键词分类。分母（已验证结论数）记录如下，分子不可得。"
	m.Denominator = int64(verified)
	m.Numerator = 0
	m.Details = []Detail{
		detailCount("facts_total", int64(len(s.DB.Facts))),
		detailCount("note_facts", int64(notes)),
		detailCount("verified_conclusions", int64(verified)),
		detailCount("structured_environment_signals", 0),
	}
	return m
}

// ---- 7. review queue and execution time ---------------------------------

func metricReviewTiming(s Snapshot) Metric {
	const key = KeyReviewQueueAndExecutionMS
	m := Metric{
		Key:   key,
		Order: 7,
		Unit:  "ms",
		Definition: "复核排队/执行耗时 = purpose=verifier 的 run 的平均排队毫秒数（分子为 queued_at→execution_started_at 或已存 queue_ms 的总和，分母为可测得排队的复核 run 数）；" +
			"queue_ms/execution_ms 缺失时如实计数，不推断为 0；仅有时间戳时用差值并单独标注；取消于队列、未开始执行的运行保留排队时长、执行时长为空，不伪记为已执行；执行耗时分布列在 details。" +
			"只记录、不设阈值。",
	}
	var queue []int64
	var execution []int64
	var sumQueue int64
	verifierRuns, executed := 0, 0
	queueMissing, queueFallback := 0, 0
	executionMissing, notStarted := 0, 0
	canceledQueued := 0
	canceledQueuedNoQueue := int64(0)
	clamped := int64(0)
	for _, r := range s.DB.Runs {
		if r.Purpose != "verifier" {
			continue
		}
		verifierRuns++
		started := r.ExecutionStartedAt != nil || r.ExecutionMS != nil
		if started {
			executed++
		} else {
			notStarted++
			if r.Status == model.RunCanceled {
				canceledQueued++
			}
		}
		var q int64 = -1
		switch {
		case r.QueueMS != nil:
			q = *r.QueueMS
		case r.QueuedAt != nil && r.ExecutionStartedAt != nil:
			q = r.ExecutionStartedAt.Sub(*r.QueuedAt).Milliseconds()
			if q < 0 {
				q = 0
				clamped++
			}
			queueFallback++
		}
		if q >= 0 {
			queue = append(queue, q)
			sumQueue += q
		} else if started {
			queueMissing++
		} else if r.Status == model.RunCanceled {
			// Canceled while queued: the still-queued run must keep its queue
			// time; a missing value is reported, not reconstructed.
			canceledQueuedNoQueue++
		}
		switch {
		case r.ExecutionMS != nil:
			execution = append(execution, *r.ExecutionMS)
		case r.ExecutionStartedAt != nil && r.EndedAt != nil:
			v := r.EndedAt.Sub(*r.ExecutionStartedAt).Milliseconds()
			if v < 0 {
				v = 0
				clamped++
			}
			execution = append(execution, v)
			executionMissing++
		case started:
			executionMissing++
		}
	}
	details := []Detail{
		detailCount("verifier_runs", int64(verifierRuns)),
		detailCount("executed_runs", int64(executed)),
		detailCount("not_started_runs", int64(notStarted)),
		detailCount("canceled_while_queued", int64(canceledQueued)),
		detailCount("canceled_queued_without_queue_ms", canceledQueuedNoQueue),
		detailCount("queue_measured_runs", int64(len(queue))),
		detailCount("queue_missing_runs", int64(queueMissing)),
		detailCount("queue_from_timestamps", int64(queueFallback)),
		detailCount("execution_missing_runs", int64(executionMissing)),
		detailCount("clamped_negative_durations", clamped),
	}
	if verifierRuns == 0 {
		m.Unavailable = "no_verifier_runs"
		m.Explanation = "账本没有 purpose=verifier 的复核 run。"
		m.Details = details
		return m
	}
	if len(queue) == 0 {
		m.Unavailable = "no_measured_queue_ms"
		m.Explanation = "存在复核 run，但没有可测得的排队时长：queue_ms 为空且 queued_at/execution_started_at 时间戳不齐（历史 run 早于该字段）。取消/未执行的计数见 details，不推断为 0。"
		m.Details = details
		return m
	}
	m.Numerator = sumQueue
	m.Denominator = int64(len(queue))
	m.Value = f64(round(float64(sumQueue)/float64(len(queue)), 3))
	details = append(details,
		detailMS("queue_median_ms", median(queue)),
		detailMS("queue_p90_ms", percentileInt64(queue, 90)),
		detailMS("queue_max_ms", float64(maxInt64(queue))),
	)
	if len(execution) > 0 {
		details = append(details,
			detailMS("execution_median_ms", median(execution)),
			detailMS("execution_p90_ms", percentileInt64(execution, 90)),
			detailMS("execution_max_ms", float64(maxInt64(execution))),
			detailCount("execution_measured_runs", int64(len(execution))),
		)
	}
	m.Details = details
	return m
}
