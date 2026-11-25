package decider

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
)

// The role has to be in every round prompt, not only in the first turn: a long
// session can be compacted or resumed without the skill turn still in context.
func TestCommanderRoundPromptStatesTheRoleFirst(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "10.0.0.0/24", ContextStatus{}, RoundContext{})
	role := strings.Index(p, "你是天吴项目的指挥官")
	if role < 0 {
		t.Fatalf("round prompt does not name the role:\n%s", p)
	}
	if role > strings.Index(p, "## 本轮") {
		t.Fatalf("role must lead the prompt, before the situation section:\n%s", p)
	}
	// A handoff round swaps the decision contract for the handoff one, but it is
	// still the commander reading it.
	h := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{Resume: true})
	if !strings.Contains(h, "你是天吴项目的指挥官") {
		t.Fatalf("handoff round lost the role:\n%s", h)
	}
	// The intent definition must survive a compacted or resumed session in
	// either review mode, even when the earlier skill turn is gone.
	for name, prompt := range map[string]string{
		"round":      p,
		"handoff":    h,
		"reviewsOff": CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, RoundContext{ReviewsOff: true}),
	} {
		// 图谱直接展示已保存的意图，续轮也要明确正文语言。
		if !strings.Contains(prompt, "意图标题、说明和验收标准用简体中文撰写，通过接口保存时同样适用") {
			t.Errorf("%s prompt is missing the Simplified Chinese requirement for intent fields", name)
		}
		if !strings.Contains(prompt, "意图是下一步交给执行者做的事") {
			t.Fatalf("%s prompt lost the intent handoff definition:\n%s", name, prompt)
		}
	}
}

// Asking for a result state in the title made pending intents read as already
// completed. Keep the next action in the title and the completion check in eval.
func TestCommanderDispatchFieldsDescribeNextActionAndAcceptance(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, RoundContext{})
	for _, want := range []string{
		"意图标题写这一步的动作",
		"验收标准写怎样判断这一步完成",
		"具体打法留给执行者",
		"只有操作员明确要求某种做法时，才把它写进细节与约束",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("commander prompt missing the dispatch-field guidance %q:\n%s", want, p)
		}
	}
}

// An operator may decide a goal change and ask the commander to record it.
// The role anchor used to rule this out even with the operator's explicit request.
func TestCommanderPromptPreservesOperatorAuthorityOverGoalChanges(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "", "", ContextStatus{}, RoundContext{})
	if !strings.Contains(p, "项目的终点目标由操作员定义") ||
		!strings.Contains(p, "按操作员明确要求代办目标变更时，保留该条消息的出处") {
		t.Fatal("commander prompt must preserve the operator decision and its provenance")
	}
}

// The board in the prompt is the summary layer: long summaries are clipped and
// the full text is read by id — the title → summary → doc split of docs/17 §3.1.
// Ids, titles and counts must survive the projection.
func TestCommanderRoundPromptBoardIsTheSummaryLayer(t *testing.T) {
	long := strings.Repeat("细节", 300) // 600 runes, over the prompt budget
	b := board.View{
		Intents: board.Counts{Total: 1, Running: 1},
		Running: []board.Item{{
			ID: "in_x", Title: "在跑的意图", Status: "running", Summary: long,
		}},
		RecentFacts: []board.Item{{
			ID: "f_x", Title: "一条事实", Status: "verified", Summary: long,
		}},
	}

	p := CommanderRoundPrompt(b, "笔记", "", ContextStatus{}, RoundContext{})

	if strings.Contains(p, long) {
		t.Fatal("the round prompt still carries the full summary")
	}
	for _, want := range []string{`"id":"in_x"`, `"title":"在跑的意图"`, `"id":"f_x"`, "…"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if b.Running[0].Summary != long {
		t.Fatal("round prompt mutated the board it was given")
	}
}

// In review-off rounds, visibility and admission remain separate. A visible
// fact may still be ineligible, so the prompt points to its readiness result.
func TestCommanderIdentitySeparatesReviewVisibilityFromAdmission(t *testing.T) {
	off := commanderIdentityFor(true)
	if strings.Contains(off, "等复核结果再派下游") || strings.Contains(off, "不会进入你的决策上下文") {
		t.Fatalf("review-off identity still tells the commander to wait: %q", off)
	}
	for _, want := range []string{
		"board 的 recent_facts 展示未核验事实的标题与摘要，并保留核验状态",
		"必要前提的准入结果与阻塞原因见 readiness",
	} {
		if !strings.Contains(off, want) {
			t.Errorf("review-off identity missing %q", want)
		}
	}
	if strings.Contains(off, "只有 withdrawn / rejected 不可用") {
		t.Error("review-off identity gives a narrower exclusion list than readiness")
	}
	if !strings.Contains(off, "按操作员明确要求代办目标变更时，保留该条消息的出处") {
		t.Error("review-off identity omits the operator's role in goal changes")
	}
	if commanderIdentityFor(false) != commanderIdentityBase {
		t.Fatal("review-on identity changed")
	}
}
