package decider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/state"
)

// authorizationLines returns the prompt lines that contain the word 授权.
func authorizationLines(prompt string) []string {
	var out []string
	for _, ln := range strings.Split(prompt, "\n") {
		if strings.Contains(ln, "授权") {
			out = append(out, strings.TrimSpace(ln))
		}
	}
	return out
}

type roundFixture struct {
	docs string
	note string
}

// commanderRounds drives the real Runner for two rounds and returns the prompts
// the commander was handed. The fixture opens a goal, settles one direction and
// leaves a generic operator request pending, so the idle-replan section renders;
// the second round crosses the handoff line so its handoff branch renders too.
//
// CommanderRoundPrompt alone is not enough: the offending strings live in the
// round lead and the idle section that Run() concatenates around it.
func commanderRounds(t *testing.T, f roundFixture) []string {
	t.Helper()
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "拿到管理员权限"}); err != nil {
		t.Fatalf("open goal: %v", err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "写在前面的一条已完成意图"})
	if err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatalf("settle intent: %v", err)
	}
	if _, _, err := st.EnsureIdleOperatorRequest(); err != nil {
		t.Fatalf("idle operator request: %v", err)
	}

	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if f.note != "" {
		if err := mem.Append(f.note); err != nil {
			t.Fatalf("seed note: %v", err)
		}
	}

	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	// 110000/128000 puts the second round over the handoff line.
	r.ContextWindow = 128000
	r.Docs = func() string { return f.docs }
	r.Commander = func(_ context.Context, round string, _ bool) (RoundResult, error) {
		prompts = append(prompts, round)
		return RoundResult{Tokens: 1, ContextTokens: 110000}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "second", true)
	if len(prompts) < 2 {
		t.Fatalf("expected two rounds, got %d", len(prompts))
	}
	// Fixture precondition: round 2 must render both the handoff variant and the
	// idle section, because the handoff wording lives in the idle section's
	// handoff branch. If either is missing, this test would go green with that
	// branch unfixed — the hole it exists to close.
	for _, want := range []string{"## 交接（本轮必做）", "目标未达成，没有在办意图"} {
		if !strings.Contains(prompts[1], want) {
			t.Fatalf("round 2 does not render %q, so that branch is not covered:\n%s", want, prompts[1])
		}
	}
	return prompts
}

// Template purity: with every data source free of the word, a rendered round must
// carry it zero times. A retired-phrase list never keeps up with rewording (the
// eight strings that were left in runner.go are exactly that failure), so the
// rule is counted rather than matched, and it is applied to the whole prompt the
// model actually reads.
func TestCommanderRoundTemplateNeverCarriesTheWord(t *testing.T) {
	docs := "## 约束\n不要删除或修改真实数据。\n\n## 起点\n10.0.0.0/24\n\n## 目标\n拿到管理员权限\n\n"
	for i, prompt := range commanderRounds(t, roundFixture{docs: docs}) {
		if got := strings.Count(prompt, "授权"); got != 0 {
			t.Fatalf("round %d carries 授权 %d times with word-free data, want 0; offending lines:\n%s",
				i+1, got, strings.Join(authorizationLines(prompt), "\n"))
		}
	}
}

// Data passthrough: the documents and the note are data, and the word must reach
// the prompt exactly as often as its sources carry it — not filtered, not
// duplicated. Board and goal statements are kept free of the word on purpose: on
// a real round they also carry history, and there the count has to be attributed
// per segment against each source instead of added up as one denominator.
//
// The note is compared by total, not as one verbatim block: its own headings turn
// into prompt sections, so a block-equality assertion would report a false red on
// a note that was split across headings. Documents keep the block assertion —
// arriving whole is the contract they are injected under.
func TestCommanderRoundCarriesDataVerbatim(t *testing.T) {
	docs := "## 约束\n不要删除或修改真实数据；授权不含 10.0.0.5。\n\n## 起点\n授权范围：10.0.0.0/24\n\n## 目标\n在授权范围内拿到管理员权限\n\n"
	note := "## 授权口径（历史，仍适用）\n上一轮操作员确认过授权范围。\n"
	want := strings.Count(docs, "授权") + strings.Count(note, "授权")

	for i, prompt := range commanderRounds(t, roundFixture{docs: docs, note: note}) {
		if strings.Count(prompt, docs) != 1 {
			t.Fatalf("round %d does not carry the documents verbatim exactly once:\n%s", i+1, prompt)
		}
		if got := strings.Count(prompt, "授权"); got != want {
			t.Fatalf("round %d carries 授权 %d times, want %d (documents + note); offending lines:\n%s",
				i+1, got, want, strings.Join(authorizationLines(prompt), "\n"))
		}
	}
}
