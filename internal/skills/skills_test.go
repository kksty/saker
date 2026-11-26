package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWritesEmbeddedSkills(t *testing.T) {
	dir := t.TempDir()
	paths, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, Report, "SKILL.md")
	found := false
	for _, p := range paths {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("paths = %v, want %s", paths, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: saker-report", "findings.json"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("skill body missing %q", want)
		}
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	info1, _ := os.Stat(first[0])
	second, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(second[0])
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("unchanged skill should not be rewritten")
	}
}

func TestResolveByNameAndPath(t *testing.T) {
	dir := t.TempDir()
	paths, err := Ensure(dir)
	if err != nil || len(paths) == 0 {
		t.Fatalf("ensure: %v", err)
	}
	report := filepath.Join(dir, Report, "SKILL.md")
	got, err := Resolve([]string{Report, "", report}, dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("resolved = %v, %v; want 1 (duplicates dropped)", got, err)
	}
	if !strings.HasSuffix(got[0], filepath.Join(Report, "SKILL.md")) {
		t.Fatalf("resolved = %q", got[0])
	}
}

// Harness discovers `.cyber/skills` relative to its working directory; Install
// is how the commander skill lands there, so the session (not saker) expands
// /skill:commander (D78).
func TestInstallPutsSkillWhereHarnessLooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".cyber", "skills")
	p, err := Install(Commander, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, Commander, "SKILL.md")
	if p != want {
		t.Fatalf("installed = %s, want %s", p, want)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// The note contract must travel with the skill: a commander that does not
	// know its note is the memory across sessions will just re-scout after
	// memory loss (D80). The note is also the commander's own file of
	// conclusions and clues, not a chronological round log: the skill must say
	// what belongs in it and that it may rewrite/delete it (D106). The complete
	// contract travels too: the reason is the only thing the console can show
	// for "why is this met" (D96), and the goal judgement belongs to the
	// commander, not to a code count (D97). Completion is the stop rule: when
	// the goals are stamped and no direction is open, the round noops instead
	// of inventing more work (D102). The knowledge base is read in two steps:
	// the hit list carries no body, so the skill must name the field that says
	// a body exists and the call that opens exactly one entry. Without it the
	// commander either pulls nothing or pulls the whole library.
	for _, needle := range []string{"name: commander", "$SAKER_API", "$SAKER_API/api/kb", "has_detail", "api/kb?id=", "$SAKER_AUTH", "$SAKER_BATTLE", "`PUT /api/state`", ".saker/auth", "整份重写", "不是轮次日志", "只留对下一轮有价值", "可以自己整份重写或删改", "操作员定义", "说清目标口径怎么被证据满足", "目标是否达成由你", "是否要求独立复核由项目当前前提政策决定，完成同样须通过准入", "没有待办、在途意图或显式等待事项", "结束本轮", "可复核的原始证据", "不当作已验证结论或目标完成证明", "不依赖项目是否启用约束文档", "复核是控制面的 support run", "项目里已有事实时", "不要用无前提意图绕过", "按本轮提示的复核模式", "review_failed", "不会挂起等操作员", "不要抢跑"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("skill body missing %q", needle)
		}
	}
	// The installed skill must describe the same handoff as the round prompt:
	// a pending intent title describes the action, with completion checked separately.
	const intentHandoff = "意图是下一步交给执行者做的事。意图标题写这一步的动作，验收标准写怎样判断这一步完成。"
	if !strings.Contains(string(body), intentHandoff) {
		t.Errorf("commander skill missing intent handoff guidance %q", intentHandoff)
	}
	// A stale skill can conflict with a current round on stalls, review and
	// premise links, even when the control plane computes them correctly.
	for _, want := range []string{
		"确实无可推进时，写明具体卡点和正在等待的事项即可",
		"事实的展示内容按 board 当前投影读取，核验状态以账本为准",
		"`from_fact_ids` 记录这一步所依赖的必要事实",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("commander skill missing %q", want)
		}
	}
	// Context-window mechanics stay out of the skill (operator, 2026-11-11): the
	// window is per model and the harness reports the real usage, so a fixed
	// percentage or line cap in the skill only seeds invented thresholds (a
	// commander on a 1M window once warned a worker about "approaching 200k").
	// The round prompt carries the handoff instruction when the reported usage
	// actually reaches the line.
	for _, banned := range []string{"85%", "80%", "150 行", "上下文占用", "标准模式"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("commander skill must not hardcode context limits: found %q", banned)
		}
	}
	// The battlefield token no longer lives in the host environment: every
	// session inherits that environment, so the skill reads the per-round
	// identity file instead.
	if strings.Contains(string(body), "SAKER_AGENT_TOKEN") {
		t.Error("commander skill still reads the removed host-environment token")
	}
	// A current commander may apply an explicit operator instruction to a goal;
	// creation and edits must retain that provenance, without support-run access.
	for _, required := range []string{"operator_message_id", "操作员明确要求改变终点", "POST /api/goals/<id>/update", "/met 仍由操作员"} {
		if !strings.Contains(string(body), required) {
			t.Errorf("commander goal contract missing %q", required)
		}
	}
	for _, banned := range []string{"你维护全局目标", "evaluator"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("commander skill carries unsupported authority: %q", banned)
		}
	}
	// The commander's input must not carry report content: report generation and
	// its delivery gates belong to the operator's console, not to the commander
	// session, which ends at the ledger (D102).
	if strings.Contains(string(body), "报告") {
		t.Error(`commander skill must not mention reports: the commander's job ends at the ledger (D102)`)
	}
}

func TestResolveExplicitFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom.md")
	if err := os.WriteFile(p, []byte("---\nname: custom\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve([]string{p}, dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("resolved = %v, %v", got, err)
	}
}

func TestExplicitSkillSelectionCannotSilentlyLoseMissingOrEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"missing", empty, dir} {
		if got, err := Resolve([]string{Report, selected}, dir); err == nil || got != nil {
			t.Fatalf("incomplete selection %q was accepted: %v, %v", selected, got, err)
		}
	}
}

// Executors receive no mandatory skill: the handoff contract lives in the
// dispatch prompt so it cannot fail to load (D32). Two skills still ship: the
// commander reads commander via /skill: expansion (D78), and collaborate is
// discoverable content an executor reads only when it needs a peer or the live
// graph. Both travel with the binary like the report skill.
func TestShippedSkills(t *testing.T) {
	got := Names()
	want := []string{Collaborate, Commander, Report}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
}

// The report skill is the single source of the deliverable's outline (D92):
// customers open index.md and findings/<fact>.md, so a renamed or reordered
// section changes the deliverable. Pin the six-section order for both files,
// plus the fixed subsections of "范围与限制".
func TestReportSkillPinsOutlineAndOrder(t *testing.T) {
	dir := t.TempDir()
	p, err := Install(Report, dir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sections := []string{
		// index.md
		"## 1. 概览",
		"## 2. 结论清单",
		"## 3. 已复核结论",
		"## 4. 待复核结论",
		"## 5. 范围与限制",
		"## 6. 修复建议",
		// findings/<fact>.md
		"## 1. 前置条件",
		"## 2. 复现步骤",
		"## 3. 请求与响应片段",
		"## 4. 影响",
		"## 5. 修复建议",
		"## 6. 止于何处",
	}
	last := -1
	for _, s := range sections {
		i := strings.Index(string(body), s)
		if i < 0 {
			t.Fatalf("report skill missing section %q", s)
		}
		if i < last {
			t.Fatalf("report skill section %q is out of order", s)
		}
		last = i
	}
	for _, s := range []string{
		"### 5.1 授权范围与本轮动作",
		"### 5.2 止于何处",
		"### 5.3 需要客户复核的点",
		"不增、不减、不换序",
	} {
		if !strings.Contains(string(body), s) {
			t.Errorf("report skill missing %q", s)
		}
	}
}

// The collaboration skill must tell the executor where its identity is and that
// messages are not facts; otherwise the model would invent a sender or paste
// chat into facts.jsonl.
func TestCollaborateSkillCarriesIdentityAndBoundary(t *testing.T) {
	dir := t.TempDir()
	p, err := Install(Collaborate, dir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"name: collaborate", ".saker/identity.json", "idempotency_key", "不是事实", "generation", "worker-note.md", "第二份账本", "篇幅由你判断", "战场态势（只读）", "$BATTLE", "has_detail", "$API/kb?id=", "<meta charset=\"utf-8\">", "curl.exe", "内嵌 POSIX sh"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("collaborate skill missing %q", needle)
		}
	}
	// Report/verification runs and the review queue are operator-side
	// post-processing: the executor skill must not name the harness evaluator.
	// The project's start/end text is not part of the executor's input at all —
	// its round is judged by this intent and its acceptance criterion alone, so
	// the skill must not promise it can read the goal document.
	if strings.Contains(string(body), "evaluator") {
		t.Error("collaborate skill must not expose the harness evaluator")
	}
	if !strings.Contains(string(body), "本轮目标就是本意图及其验收标准") {
		t.Error("collaborate skill must say the round's target is this intent and its acceptance criterion")
	}
	if strings.Contains(string(body), "执行者可读取") && strings.Contains(string(body), "终点") {
		t.Error("collaborate skill must not tell the executor the start/end documents are readable")
	}
	// Like the commander skill, the executor skill must not encode context
	// percentages: a run is not shown its own usage, so an "80%/85%" rule is
	// both unobservable and an invitation to invent thresholds. The control
	// plane owns recycling; the skill owns the note contract.
	for _, banned := range []string{"80%", "85%", "上下文会满"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("collaborate skill must not hardcode context limits: found %q", banned)
		}
	}
}
