package decider

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
)

// The commander's long session gets pointer-only entries for the new read
// surfaces; it must not need to guess that focus/resources/context editing exist.
func TestCommanderRoundPromptNamesCurrentReadEntries(t *testing.T) {
	prompt := CommanderRoundPrompt(board.View{}, "", "10.0.0.0/24", ContextStatus{}, RoundContext{Resume: true})
	for _, want := range []string{
		"/api/focus?intent=in_x",
		"/api/resources?intent=in_x",
		"/api/intents/in_x/context",
		"premise_claim_versions",
		"context_fact_versions",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("commander prompt missing %q:\n%s", want, prompt)
		}
	}
	// The premise discipline is re-anchored every round, not only in the skill.
	if !strings.Contains(prompt, "from_fact_ids") {
		t.Fatalf("commander identity lost the premise contract:\n%s", prompt)
	}
	// Goals are operator-defined: the round prompt must say so and must not
	// invite the commander to create, split or rewrite one.
	if !strings.Contains(prompt, "目标由操作员定义") {
		t.Fatalf("commander identity lost the operator-owned goal contract:\n%s", prompt)
	}
	if strings.Contains(prompt, "POST /api/goals") {
		t.Fatalf("commander identity still invites goal creation:\n%s", prompt)
	}
	for _, want := range []string{"直接互发消息", "记录交接", "意图写清目标与验收"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("commander identity lost collaboration autonomy %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"将军", "士兵", "协调者", "派子意图", "平级发布"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("commander identity reintroduced a role name %q:\n%s", forbidden, prompt)
		}
	}
	// The round-level sections must land before the board, and every anchor must
	// exist. The old version compared strings.Index of a heading that had been
	// retired ("## 授权边界（当前口径）"): both indexes were -1, and -1 > -1 is
	// false, so the assertion passed no matter how the prompt was laid out. Check
	// presence first, then order.
	docs := "## 约束\n不要改数据\n\n## 起点\n10.0.0.0/24\n\n## 目标\n拿到管理员权限\n\n"
	ordered := CommanderRoundPrompt(board.View{}, "", docs, ContextStatus{}, RoundContext{Resume: true})
	boardAt := strings.Index(ordered, "## board\n")
	if boardAt < 0 {
		t.Fatalf("commander prompt has no board anchor:\n%s", ordered)
	}
	last := -1
	for _, heading := range []string{"## 约束", "## 起点", "## 目标"} {
		i := strings.Index(ordered, heading)
		if i < 0 {
			t.Fatalf("commander prompt missing document heading %q:\n%s", heading, ordered)
		}
		if i <= last {
			t.Fatalf("document heading %q out of order (%d after %d):\n%s", heading, i, last, ordered)
		}
		last = i
	}
	if last >= boardAt {
		t.Fatalf("documents are not before the board (last doc %d, board %d):\n%s", last, boardAt, ordered)
	}
}
