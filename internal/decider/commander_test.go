package decider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/contract"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/state"
	"github.com/kksty/tianwu/internal/store"
)

// The project documents ride the round as their own text, verbatim. Nothing
// explains them, so the only thing to pin is: what the caller passes is what the
// model reads, and an empty document set adds no commentary of its own.
func TestCommanderRoundPromptCarriesDocumentTextVerbatim(t *testing.T) {
	docs := "## 约束\n不要删除或修改真实数据。\n\n## 起点\n10.0.0.0/24\n\n"
	p := CommanderRoundPrompt(board.View{}, "", docs, ContextStatus{}, RoundContext{})
	if strings.Count(p, docs) != 1 {
		t.Fatalf("documents rendered %d times, want 1:\n%s", strings.Count(p, docs), p)
	}
	if strings.Count(p, contract.OperatorPriority) != 1 {
		t.Fatalf("priority sentence rendered %d times, want 1:\n%s", strings.Count(p, contract.OperatorPriority), p)
	}
	empty := CommanderRoundPrompt(board.View{}, "", "", ContextStatus{}, RoundContext{})
	if strings.Contains(empty, "## 约束") || strings.Contains(empty, "## 起点") {
		t.Fatalf("empty documents must not create empty sections:\n%s", empty)
	}
	// The commander side is allowed to mention authorization elsewhere (the
	// identity talks about what the operator authorises); only the executor brief
	// is held to a zero-hit assertion.
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The commander cannot see its own context size; the round prompt is the only
// place that tells it. Without the number the 85% handoff can never fire.
func TestRoundPromptReportsContextPressure(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 100000, Window: 128000}, RoundContext{})
	if !strings.Contains(p, "100000/128000（78%）") {
		t.Fatalf("prompt does not report pressure:\n%s", p)
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("78%% must not be a handoff round:\n%s", p)
	}
}

// The round prompt is the one input that survives compaction/resume. It must
// state the rules the commander kept getting wrong: purpose runs do not consume
// max_runs, a fact still under review is not a from_fact_ids premise, and a
// ready pending direction must be run instead of waiting for the next round.
func TestCommanderPromptStatesConcurrencyAndPremiseDiscipline(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "", "", ContextStatus{}, RoundContext{})
	for _, want := range []string{
		"max_runs 只统计意图 run",
		"commander/verifier/report 不计入",
		"还在复核中的线索不要放进 from_fact_ids",
		"复核通过后重读相关 pending 意图的 readiness",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("commander prompt missing %q:\n%s", want, p)
		}
	}
}

// At 85% the round stops dispatching and rewrites the note; the harness then
// compacts, and the note is what survives (D80).
func TestHandoffRoundAtEightyFivePercent(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{})
	for _, needle := range []string{
		"上下文已经到 85%", "不派新意图", "## 交接（本轮必做）",
		"笔记 API", "整份重写", "留什么、删什么由你定", "你回来后的第一个动作",
		"不是轮次日志",
		"从账本重新推导", "上一版只当待核对清单", "当前存在的 fact id",
	} {
		if !strings.Contains(p, needle) {
			t.Fatalf("handoff prompt missing %q:\n%s", needle, p)
		}
	}
}

// The handoff note must be re-derived from the ledger, not rewritten/paraphrased
// from the previous version (P0-8). Ordering is part of the contract: the
// stale-fact list is processed first, then the previous version is treated as a
// to-check list and each entry is re-checked against facts/intents/goals, and
// only then the note is written. The derivation rule must also come before the
// "至少写清" checklist, so the model does not fill the form before checking.
func TestHandoffNoteDerivesFromTheLedger(t *testing.T) {
	for _, want := range []string{
		"从账本重新推导", "上一版只当待核对清单", "当前存在的 fact id",
		"先处理下方「事实已变化」清单", "逐条回到事实/意图/目标", "写完前逐条自查",
	} {
		if !strings.Contains(handoffSection, want) {
			t.Fatalf("handoff contract lost %q:\n%s", want, handoffSection)
		}
	}
	// The pre-existing contract stays: this is judgement and clues, not a round
	// log and not a second ledger, and the "至少写清" checklist survives.
	for _, kept := range []string{"至少写清", "不是轮次日志", "别抄事实正文"} {
		if !strings.Contains(handoffSection, kept) {
			t.Fatalf("handoff contract lost %q:\n%s", kept, handoffSection)
		}
	}
	derive := strings.Index(handoffSection, "从账本重新推导")
	checklist := strings.Index(handoffSection, "- 目标与证据进度")
	if derive < 0 || checklist < 0 || derive >= checklist {
		t.Fatalf("derivation rule must precede the checklist (derive=%d checklist=%d):\n%s", derive, checklist, handoffSection)
	}
	listFirst := strings.Index(handoffSection, "先处理")
	staleFirst := strings.Index(handoffSection, "先处理下方")
	entryByEntry := strings.Index(handoffSection, "逐条回到")
	if listFirst < 0 || staleFirst < 0 || entryByEntry < 0 || listFirst >= entryByEntry || staleFirst >= entryByEntry {
		t.Fatalf("stale list must be processed before the per-entry re-check (first=%d stale=%d entries=%d):\n%s",
			listFirst, staleFirst, entryByEntry, handoffSection)
	}
}

// The combination that matters: a handoff round whose note references a fact
// that changed since it was read. The same prompt must carry the stale-fact
// list (from stateAPILine) and the three-step derivation contract; the list is
// what makes "先处理清单→逐条核对→自查" concrete instead of generic advice.
func TestHandoffRoundWithStaleFactsCarriesListAndDerivation(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, testNote, "",
		ContextStatus{Used: 110000, Window: 128000},
		RoundContext{Resume: true, StaleNoteFacts: []string{"f_x"}})
	for _, want := range []string{
		"## 交接（本轮必做）",
		"笔记引用的事实已变化，按 ID 重读再判断：f_x",
		"从账本重新推导",
		"上一版只当待核对清单",
		"先处理下方",
		"逐条回到",
		"当前存在的 fact id",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("handoff prompt missing %q:\n%s", want, p)
		}
	}
	contract := strings.Index(p, "## 交接（本轮必做）")
	staleList := strings.Index(p, "笔记引用的事实已变化")
	if contract < 0 || staleList < 0 || staleList < contract {
		t.Fatalf("the stale-fact list must be present below the handoff contract (contract=%d list=%d):\n%s", contract, staleList, p)
	}
}

// The note is the commander's own file, so the prompt has to say what belongs
// in it (judgement and clues, not a round log and not a second ledger) and that
// it may delete/rewrite it, with no size quota attached. Otherwise the model
// treats state.md as an append-only activity log or as a place to copy facts,
// and every handoff carries dead weight (D106).
func TestStatePromptSaysTheNoteIsEditable(t *testing.T) {
	for _, rc := range []RoundContext{
		{Resume: false, StatePath: "/data/state.md"}, // full note
		{Resume: true, StatePath: "/data/state.md"},  // pointer only
	} {
		p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, rc)
		for _, needle := range []string{"不是轮次日志", "只写对下一轮有价值的判断与线索", "别抄正文", "直接删掉", "整份重写或删改"} {
			if !strings.Contains(p, needle) {
				t.Fatalf("note prompt (resume=%v) missing %q:\n%s", rc.Resume, needle, p)
			}
		}
	}
}

// 80–85% is the warning band: the note must be kept current before the handoff
// round arrives, but dispatching is still allowed.
func TestWarnRoundBeforeHandoff(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 104000, Window: 128000}, RoundContext{})
	if !strings.Contains(p, "接近交接线") {
		t.Fatalf("81%% must warn about the handoff line:\n%s", p)
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("81%% must not be a handoff round:\n%s", p)
	}
}

// After a compaction the model's recent memory is gone; the round must say so
// and point at the note instead of letting it re-scout from scratch.
func TestCompactedRoundContinuesFromNote(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 20000, Window: 128000, Compacted: true}, RoundContext{})
	for _, needle := range []string{"压缩过上下文", "按当前有效指令和新观察自行决定下一步", "当时条件与判断"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("post-compaction prompt missing %q:\n%s", needle, p)
		}
	}
	if strings.Contains(p, "## 交接（本轮必做）") {
		t.Fatalf("a compacted 16%% session is not a handoff round:\n%s", p)
	}
}

// An unconfigured window is "no signal", not 0% and not a handoff.
func TestUnknownWindowHasNoPressureSection(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 50000}, RoundContext{})
	if strings.Contains(p, "## 上下文") {
		t.Fatalf("unknown window must not invent pressure:\n%s", p)
	}
	if (ContextStatus{Used: 50000, Window: 0}).Handoff() {
		t.Fatal("unknown window must never schedule a handoff")
	}
}

// The note is rewritten once per pressure cycle and then the loop continues; the
// 85% line must not turn every later round into another handoff.
func TestHandoffHappensOncePerPressureCycle(t *testing.T) {
	c := ContextStatus{Used: 110000, Window: 128000}
	if !c.Handoff() {
		t.Fatal("85% must schedule the first handoff")
	}
	c.HandoffDone = true
	if c.Handoff() {
		t.Fatal("a second round in the same cycle repeated the handoff")
	}
}

// The runner carries the session's size and the compaction flag from one round
// to the next: that is how the 85% round and the post-compaction round happen.
func TestRunnerCarriesContextAcrossRounds(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		switch len(prompts) {
		case 1:
			return RoundResult{Tokens: 10, ContextTokens: 110000}, nil
		case 2:
			return RoundResult{Tokens: 5, ContextTokens: 20000, Compacted: true,
				RunID:   completeNoteRound(t, st, ctx, "交接"),
				Actions: []Action{{Kind: "state", Text: "交接"}}}, nil
		default:
			return RoundResult{Tokens: 1}, nil
		}
	}

	r.Run(context.Background(), "first", true)
	before := r.Status().Stalled
	d := r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("second round is not a handoff round:\n%s", prompts[1])
	}
	if r.Status().Stalled != before {
		t.Fatal("a handoff round must not count as a stalled direction")
	}
	if !hasNote(d.Notes, "交接轮") || hasNote(d.Notes, "没有写笔记") {
		t.Fatalf("handoff notes = %v", d.Notes)
	}

	r.Run(context.Background(), "third", true)
	if !strings.Contains(prompts[2], "压缩过上下文") {
		t.Fatalf("third round does not know it was compacted:\n%s", prompts[2])
	}
}

// A handoff round that never writes state.md is the failure mode the protocol
// exists to prevent; the decision log must say it, not hide it. The pressure
// cycle is not marked done: the next round retries the handoff instead of
// dispatching while the note is stale.
func TestHandoffRoundWithoutNoteIsRecorded(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000
	var rounds int
	var prompts []string
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		rounds++
		prompts = append(prompts, round)
		if rounds == 1 {
			return RoundResult{Tokens: 1, ContextTokens: 110000}, nil
		}
		return RoundResult{Tokens: 1, ContextTokens: 112000}, nil
	}
	r.Run(context.Background(), "first", true)
	d := r.Run(context.Background(), "handoff", true)
	if !hasNote(d.Notes, "没有写笔记") {
		t.Fatalf("missing handoff was not recorded: %v", d.Notes)
	}
	d = r.Run(context.Background(), "after", true)
	if !strings.Contains(prompts[2], "## 交接（本轮必做）") {
		t.Fatalf("a missing note must retry the handoff:\n%s", prompts[2])
	}
	if !hasNote(d.Notes, "没有写笔记") {
		t.Fatalf("the retry did not record the missing note: %v", d.Notes)
	}
}

// Compaction normally happens after the 85% handoff, in a later ordinary
// round. The verified handoff belongs to that whole pressure cycle.
func TestCompactionPreservesEarlierHandoff(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	r.ContextWindow = 128000
	r.lastContext = 110000
	var prompts []string
	r.Commander = func(ctx context.Context, prompt string, _ bool) (RoundResult, error) {
		prompts = append(prompts, prompt)
		switch len(prompts) {
		case 1:
			return RoundResult{RunID: completeNoteRound(t, r.Store, ctx, "交接"), ContextTokens: 111000, Actions: []Action{{Kind: "state"}}}, nil
		case 2:
			return RoundResult{ContextTokens: 20000, Compacted: true}, nil
		default:
			return RoundResult{ContextTokens: 21000}, nil
		}
	}
	for range 3 {
		r.Run(context.Background(), "manual", true)
	}
	if !strings.Contains(prompts[0], "## 交接（本轮必做）") {
		t.Fatal("fixture did not complete an initial handoff")
	}
	if strings.Contains(prompts[2], "## 交接（本轮必做）") {
		t.Fatal("compaction discarded the earlier handoff and requested a redundant repair")
	}
	if !strings.Contains(prompts[2], "压缩过上下文") {
		t.Fatal("post-compaction round lost the compaction notice")
	}
	// The compaction still starts a new pressure cycle.
	r.lastContext = 110000
	r.Run(context.Background(), "manual", true)
	if !strings.Contains(prompts[3], "## 交接（本轮必做）") {
		t.Fatal("the next pressure cycle skipped its handoff")
	}
}

func TestUnreadableNoteBlocksRoundUntilRepaired(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "fixture goal"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.md")
	// A directory at the note path fails ReadFile on every supported platform.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	mem := state.New(path)
	r := NewRunner(st, mem, "")
	r.ModelName = "fixture"
	calls := 0
	var received string
	r.Commander = func(_ context.Context, prompt string, _ bool) (RoundResult, error) {
		calls++
		received = prompt
		return RoundResult{Tokens: 1}, nil
	}
	before := st.CommanderAttention().DeliveredThrough
	d := r.Run(context.Background(), "manual", true)
	if d == nil || !strings.Contains(d.Error, "读取指挥官笔记") || d.Tokens != 0 || d.AttentionDelivered {
		t.Fatalf("unreadable memory was not reported as a blocked round: %+v", d)
	}
	if calls != 0 || st.CommanderAttention().DeliveredThrough != before {
		t.Fatal("a note read failure called the model or consumed its input")
	}
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("a note read failure marked undelivered context as shown")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := mem.Save("recovered note"); err != nil {
		t.Fatal(err)
	}
	if d = r.Run(context.Background(), "manual", true); d == nil || d.Error != "" || calls != 1 {
		t.Fatalf("repaired note did not unblock the round: %+v (calls=%d)", d, calls)
	}
	if !strings.Contains(received, "recovered note") {
		t.Fatal("recovered round did not receive its note")
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// A restart resumes the AOP session, so the harness context is not empty even
// though the Runner is new. Without seeding, the first rounds would never see
// the 85% line and the harness would compress without a fresh note (D80).
func TestSeedContextAfterRestart(t *testing.T) {
	st := newStore(t)
	run, err := st.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	st.ProgressRun(run.ID, 900000)
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.ContextWindow = 1000000 }); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ContextWindow = 128000 // the profile left it "auto"
	r.seedContext()
	if got := r.windowLocked(); got != 1000000 {
		t.Fatalf("seeded window = %d, want the harness-reported 1000000", got)
	}
	status := ContextStatus{Used: r.lastContext, Window: r.windowLocked()}
	if status.Percent() != 90 || !status.Handoff() {
		t.Fatalf("seeded pressure = %d%%, want a 90%% handoff", status.Percent())
	}
}

// The harness resolves the model's real window and reports it with usage; the
// control plane must measure against that, not against its profile fallback
// (D80).
func TestRunnerUsesHarnessReportedWindow(t *testing.T) {
	st := newStore(t)
	var prompts []string
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.ContextWindow = 128000 // profile "auto" fallback
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		if len(prompts) == 1 {
			return RoundResult{Tokens: 1, ContextTokens: 500000, ContextWindow: 1000000}, nil
		}
		return RoundResult{Tokens: 1}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "500000/1000000（50%）") {
		t.Fatalf("second round did not use the harness window:\n%s", prompts[1])
	}
	if strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatal("50% of the harness window must not be a handoff round")
	}
}

// A round hands the board to the harness commander instead of calling a model
// here; the session's token total lands in the decision log (D78).
func TestCommanderRunsTheRound(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	var got string
	var resume bool
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	// The runner forwards the caller-resolved session answer to Commander; a
	// live session is what asks the harness to continue it.
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, round string, res bool) (RoundResult, error) {
		got, resume = round, res
		return RoundResult{Tokens: 1234, Actions: []Action{{Kind: "intent", ID: "in_1", Text: "验证"}}}, nil
	}
	d := r.Run(context.Background(), "test", true)
	if d == nil || d.Error != "" {
		t.Fatalf("decision = %+v", d)
	}
	if !resume {
		t.Fatal("the runner should ask to continue the session")
	}
	for _, needle := range []string{"## board", "## 本轮"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("round prompt missing %q: %q", needle, got)
		}
	}
	if len(d.Actions) != 1 || d.Actions[0].Kind != "intent" {
		t.Fatalf("actions = %+v, want the API-recorded action", d.Actions)
	}
	if d.Tokens != 1234 {
		t.Fatalf("tokens = %d, want the session's total", d.Tokens)
	}
}

// A commander round stopped by the liveness detector must land as the round's
// error in the decision log and in the decider status: that is what the
// console's commander card shows, and what tells the operator the round was cut
// by model_stall rather than completing normally (D107). It must not be
// recorded as the usual "由 harness 指挥官会话执行" note.
func TestCommanderStallIsRecordedAsRoundError(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.ModelName = "test-model"
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		return RoundResult{Tokens: 7}, ErrCommanderStalled
	}
	d := r.Run(context.Background(), "first", true)
	if d == nil || !strings.Contains(d.Error, "model_stall") {
		t.Fatalf("decision = %+v, want the stall as the round error", d)
	}
	if hasNote(d.Notes, "由 harness 指挥官会话执行") {
		t.Fatalf("a stalled round was recorded as a normal round: %v", d.Notes)
	}
	if got := r.Status().LastError; !strings.Contains(got, "model_stall") {
		t.Fatalf("decider last_error = %q, want the stall reason", got)
	}
}
