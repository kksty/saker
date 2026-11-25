package api

// Prompt/role contract tests (audit F12, items 1–6). They pin the contract
// text, not an implementation detail: authorization has one rule across roles,
// verified_only never suggests a bypass, the worker brief has no operator-owned
// project goals, the declaration schema has one source, notes are history rather
// than orders, and a continuation never promises a written index that may have
// failed.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// readSkillAsset reads one shipped skill body. The skills are file content under
// internal/skills/assets; the test runs in the api package directory.
func readSkillAsset(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "skills", "assets", name, "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill %s: %v", name, err)
	}
	return string(b)
}

// F12.2: verified_only must not teach "move the required fact into
// context_fact_ids to get past admission". A real premise waits for review; an
// unverified lead can spawn an independent verification direction instead.
func TestVerifiedOnlyGuidanceSeparatesPremisesFromLeads(t *testing.T) {
	skill := readSkillAsset(t, "commander")

	for _, banned := range []string{
		"把它放进 `context_fact_ids` 当参考",
		"或等复核通过再派",
	} {
		if strings.Contains(skill, banned) {
			t.Errorf("verified_only guidance still suggests a bypass: %q", banned)
		}
	}
	for _, want := range []string{
		"真正必要的条件不能靠改标签/换字段放行",
		"把它变成阅读参考",
		"未验证线索与必要前提不同",
		"单独立一条「验证该线索」的意图",
		// The admission rule is written once, in the round brief. The skill keeps
		// only the premise-vs-lead judgement and points at the brief; repeating
		// the bypass sentence here is how the two copies drifted apart.
		"以本轮任务书的「新意图必须写清准入依据」那段为准",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("verified_only guidance missing the premise/lead distinction %q:\n%s", want, skill)
		}
	}
}

// F12.4: the mandatory handoff contract is the one place that defines the
// declaration schema; the collaborate skill defers to it instead of repeating a
// competing example.
func TestHandoffContractIsTheSingleDeclarationSchema(t *testing.T) {
	var b strings.Builder
	writeHandoff(&b)
	handoff := b.String()

	for _, want := range []string{
		`"declaration_id":"…"`,
		`"entity_ref":"host:10.0.0.5"`,
		`"source_ref":"…"`,
		`"tags":["technique:rce"]`,
		`"evidence":["api-users.json"]`,
		`"observed_at"`,
		`"execution_context_ref"`,
		"声明字段（唯一来源",
		"`entity_ref`：被观察的**实体身份**",
		"事实不写 `from_fact_ids`",
		"`vuln` 必须交代等级",
		"等级判不出",
	} {
		if !strings.Contains(handoff, want) {
			t.Errorf("mandatory handoff schema missing %q:\n%s", want, handoff)
		}
	}
	// The fact schema must not offer a field the ledger drops: a citation the
	// writer believes was recorded is worse than none.
	if strings.Contains(handoff, `"from_fact_ids":["f_x"]`) {
		t.Error("fact declaration schema still offers from_fact_ids")
	}

	collaborate := readSkillAsset(t, "collaborate")
	for _, want := range []string{"本轮任务书的「交接（必做）」", "不重复定义"} {
		if !strings.Contains(collaborate, want) {
			t.Errorf("collaborate skill does not defer to the handoff contract (%q):\n%s", want, collaborate)
		}
	}
	// A second, competing declaration example in the skill would recreate the
	// multiple-sources problem this item exists to remove.
	if strings.Contains(collaborate, `"declaration_id":"…"`) {
		t.Error("collaborate skill carries a second declaration JSON example")
	}
}

func TestWorkerNoteBodyIsExcludedFromPrompt(t *testing.T) {
	note := "独立的历史笔记内容"
	p := BuildPromptWithWorkerNote(testDB(), testDB().Intents[0], "", "", "", 0, false, note)

	for _, want := range []string{
		"正文由操作员审阅",
		"按当前任务书和账本事实 ID 核对状态",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("worker-note contract missing %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"以它的下一步为准", "照它执行", "以 facts 与现场为准，冲突以实测为准"} {
		if strings.Contains(p, banned) {
			t.Errorf("worker-note contract still turns history into an order: %q", banned)
		}
	}
	if strings.Contains(p, note) {
		t.Errorf("worker note body entered the prompt:\n%s", p)
	}
}

// Continuation builders run before snapshot publication and cannot use old files.
func TestContinuationPromptsDoNotPromiseUnwrittenIndexes(t *testing.T) {
	s, _ := newBindingServer(t)
	in := model.Intent{ID: "in_x", Title: "继续", RunID: "r_prev"}
	prev := model.Run{ID: "r_prev", Status: model.RunFailed, StopReason: "completed"}

	prompts := map[string]string{
		"retry resume": s.retryResumePrompt(in, prev, ""),
		"continue":     s.continuePrompt(in, prev, "", ""),
		"fresh wake":   s.wakePrompt(in, false),
	}
	for name, prompt := range prompts {
		for _, banned := range []string{"本轮已刷新", "已刷新", "本轮已写", ".tw/graph.json", runInputFile} {
			if strings.Contains(prompt, banned) {
				t.Errorf("%s prompt claims an index write that may not have happened (%q):\n%s", name, banned, prompt)
			}
		}
		if !strings.Contains(prompt, "账本") {
			t.Errorf("%s prompt lacks a ledger fallback:\n%s", name, prompt)
		}
	}
}

// promptCaptureExec records each prompt without writing a ledger declaration.
// Run executes on the dispatch goroutine, so the slice is mutex-guarded: tests
// poll it while the run is still finishing, and an unguarded read is a data race
// under -race.
type promptCaptureExec struct {
	mu      sync.Mutex
	prompts []string
}

func (e *promptCaptureExec) Name() string { return "capture" }

func (e *promptCaptureExec) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	e.mu.Unlock()
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *promptCaptureExec) promptAt(i int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.prompts) {
		return ""
	}
	return e.prompts[i]
}

func (e *promptCaptureExec) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.prompts)
}

// F12.6 (second half): a handoff brief must say the snapshot is a capped index,
// never "all known context".
func TestHandoffBriefDoesNotClaimCompleteContext(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A settled run can still have a background settlement write holding the
	// SQLite connection when the test returns. Close the ledger first so Linux
	// t.TempDir cleanup does not race it ("directory not empty", leftover
	// ledger.json-journal); Windows timing hides the same race.
	t.Cleanup(func() { _ = st.Close() })
	ex := &promptCaptureExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.SkillsDir = filepath.Join(dir, "skills")

	in, err := st.CreateIntent(model.Intent{Title: "接手", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// The handoff contract refuses a replacement whose source run has no archived
	// dispatch: this fixture therefore carries one, instead of naming a run that
	// never existed.
	src, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunDone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(src.ID, func(r *model.Run) { r.BriefPath = ".tw/briefs/" + src.ID + ".md" }); err != nil {
		t.Fatal(err)
	}
	in, err = st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Handoff = &model.Handoff{FromRun: src.ID, Note: "旧会话交接"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})
	if ex.count() == 0 {
		t.Fatal("handoff brief was never captured")
	}
	prompt := ex.promptAt(0)
	if !strings.Contains(prompt, handoffIndexNote) {
		t.Fatalf("handoff brief is missing the index caveat:\n%s", prompt)
	}
	for _, want := range []string{"不是全部已知信息", "不是完整账本", "按 ID 或分页从账本续读"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("handoff brief does not bound the snapshot %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"全部已知上下文", "以下是全部", "包含所有已知"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("handoff brief claims complete context: %q", banned)
		}
	}
}

// D152 revised: the harness-context-limit slim brief keeps every hard
// constraint and replaces the optional summaries with id/API pointers instead of
// clipping them silently.
func TestSlimBriefKeepsHardConstraints(t *testing.T) {
	db := testDB()
	db.Facts[0].Summary = strings.Repeat("很长的摘要", 500)
	db.Playbooks = []model.Playbook{{
		ID: "pb_1", Title: "历史打法", When: "when", Doc: strings.Repeat("doc ", 400),
		Status: model.PlaybookConfirmed,
	}}
	db.Goals = []model.Goal{{ID: "g_1", Statement: "全局目标标记"}}
	in := db.Intents[0]
	in.Eval = "验收标准标记"
	in.FromFactIDs = []string{"f_1"}
	full := BuildPromptWithFocus(db, in, "10.0.0.0/24", "", "", 0, true, true, "笔记标记")
	slim := buildPromptWithoutOptionalSummaries(db, in, "10.0.0.0/24", "", "", 0, true, true, "", false)

	// Hard material survives the degradation, whole.
	for _, want := range []string{
		"## 约束",
		"10.0.0.0/24",
		"## 本轮指派",
		"验收标准标记",
		runInputFile,
		"## 纪律",
		"先分清是这条打法不行还是这个意图不可达",
		"## 证据（原始材料）",
		"## 交接（必做）",
		"声明字段（唯一来源",
	} {
		if !strings.Contains(slim, want) {
			t.Errorf("degraded brief dropped hard material %q:\n%s", want, slim)
		}
	}
	// Optional summaries are gone and named as omissions with read paths.
	for _, banned := range []string{"很长的摘要", "历史打法", "全局目标标记", "笔记标记"} {
		if strings.Contains(slim, banned) {
			t.Errorf("degraded brief still carries optional material %q", banned)
		}
	}
	for _, want := range []string{"本轮可选摘要有界省略", "premises_query", "未列出或截断不表示不存在"} {
		if !strings.Contains(slim, want) {
			t.Errorf("degraded brief does not point at the omitted material (%q):\n%s", want, slim)
		}
	}
	if runeLen(slim) >= runeLen(full) {
		t.Fatalf("degraded brief is not smaller: %d vs %d runes", runeLen(slim), runeLen(full))
	}
}

// F10: bounded optional lists say where the full list is, so a cut is an
// omission with a stable pointer, not a silent disappearance.
func TestTruncatedBriefListsPointAtTheirSource(t *testing.T) {
	db := store.DB{Intents: []model.Intent{{ID: "in_x", Title: "当前"}}}
	for i := 0; i < maxBriefItems+5; i++ {
		db.Intents = append(db.Intents, model.Intent{
			ID: "in_p" + string(rune('a'+i)), Title: "停放意图", Status: model.StatusParked, ParkReason: "原因",
		})
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "清单已截断") || !strings.Contains(p, "GET /api/intents") {
		t.Fatalf("truncated list has no stable pointer:\n%s", p)
	}
	if strings.Contains(p, "…（清单已截断）\n") {
		t.Fatal("truncated list still ends without a pointer")
	}
}

// The dispatcher, not the template, judges which sentence of a dispatch is a hard
// condition and which is only a suggested route: the executor brief carries the
// user's own text verbatim. See prompt_no_adjudication_test.go for the body-exact
// fixture that pins this and catches any reworded adjudication.

// The battlefield read commands belong to the collaborate skill. The dispatch
// prompt names the entry points only, so a solo run does not carry a tool manual
// (agent.md: the executor prompt carries intent, authorization, acceptance and
// the handoff contract — not tool usage).
func TestBattlefieldReadDetailsLiveInTheSkill(t *testing.T) {
	p := BuildPrompt(testDB(), testDB().Intents[0], "", "", "", 0, false)
	for _, want := range []string{"战场态势（只读）", "命令细节见 `collaborate` skill"} {
		if !strings.Contains(p, want) {
			t.Errorf("dispatch prompt lost the battlefield entry point %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"不得再用 python 解析", "--ids f_x", "--cursor C"} {
		if strings.Contains(p, banned) {
			t.Errorf("dispatch prompt still carries the command manual (%q):\n%s", banned, p)
		}
	}

	skill := readSkillAsset(t, "collaborate")
	for _, want := range []string{`"$TW_BIN" board`, "truncated/dropped/has_more/next_cursor", "不要再拿 python 解析"} {
		if !strings.Contains(skill, want) {
			t.Errorf("collaborate skill lost the moved read detail %q", want)
		}
	}
}
