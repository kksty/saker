package decider

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// Budgets are not about a 1M-token window: they catch the accidental growth
// that makes the commander's one stable input carry a second policy document.
// The round prompt should be decision input plus pointers; formatting and
// protocol detail belong in SKILL/tool schemas.
func TestCommanderPromptBudget(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := st.AddFact(model.Fact{
			Title:   "fixture fact",
			Summary: strings.Repeat("这是一条用于预算测试的摘要。", 20),
			Kind:    "note",
		}); err != nil {
			t.Fatal(err)
		}
	}
	b := board.Build(st, 20)
	note := strings.Repeat("- 已确认的结论与线索\n", 120)
	scope := strings.Repeat("授权范围里的资产与约束。", 30)
	variants := []struct {
		name string
		ctx  ContextStatus
		rc   RoundContext
	}{
		// The pointer branch (the ordinary long-session round).
		{"pointer", ContextStatus{Used: 50000, Window: 128000}, RoundContext{Resume: true, StatePath: "/data/state.md"}},
		// The pressure round: it appends the handoff section on top of the note.
		{"handoff", ContextStatus{Used: 110000, Window: 128000, HandoffDone: false}, RoundContext{Resume: true, StatePath: "/data/state.md", Handoff: true}},
		// A planned rotation: new session preamble plus the full note.
		{"rotated", ContextStatus{Used: 50000, Window: 128000}, RoundContext{Rotated: true, StatePath: "/data/state.md"}},
		// A post-compaction round: the full note plus the lossy-memory warning.
		{"compacted", ContextStatus{Used: 50000, Window: 128000, Compacted: true}, RoundContext{Resume: true, Compacted: true, StatePath: "/data/state.md"}},
	}
	for _, v := range variants {
		p := CommanderRoundPrompt(b, note, scope, v.ctx, v.rc)
		if len(p) > 160<<10 {
			t.Fatalf("%s branch: commander prompt grew to %d bytes (budget 163840)", v.name, len(p))
		}
		if n := strings.Count(p, "\n## "); n > 20 {
			t.Fatalf("%s branch: commander prompt has %d sections (budget 20)", v.name, n)
		}
	}
	// The identity also states the dispatch contract the executor relies on
	// (intent is a next step, attached routes are reference-only) and that a
	// helper may be hung under a direction; those are role rules, not protocol
	// detail, so the byte budget moved with them.
	if len(commanderIdentityBase) > 2500 {
		t.Fatalf("commander identity grew to %d bytes (budget 2500)", len(commanderIdentityBase))
	}
	if n := strings.Count(commanderIdentityBase, "\n"); n > 12 {
		t.Fatalf("commander identity has %d lines (budget 12)", n)
	}
	for _, gone := range []string{"需要操作员授权", "持久指令被撤销"} {
		if strings.Contains(commanderIdentityBase, gone) {
			t.Fatalf("commander identity re-acquired a protocol paragraph: %q", gone)
		}
	}
}

// The revocation rule lives once, in the shared instruction section, not in the
// commander identity or SKILL. This keeps one source of truth for every role.
func TestSavedMessagesRenderOnlyWhenPresent(t *testing.T) {
	if executor.InstructionSection(nil) != "" {
		t.Fatal("empty saved messages added a prompt section")
	}
}
