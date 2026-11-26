package decider

// STATE injection timing (D89): a long commander session already carries the
// note in its own context, so re-pasting it every round burns tokens for
// nothing. The full text is reserved for the states where the memory was
// actually lost or rewritten: fresh start, after a lossy compaction, after the
// handoff round, and after a process restart. These tests pin that table.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/state"
)

const testNote = "唯一标记-NOTE-42：下一步验 /admin"

func promptFor(rc RoundContext) string {
	return CommanderRoundPrompt(board.View{}, testNote, "只测已授权范围", ContextStatus{}, rc)
}

func TestRoundPromptInjectsFullStateOnFreshStart(t *testing.T) {
	p := promptFor(RoundContext{Resume: false, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("fresh start did not inject the full note:\n%s", p)
	}
	for _, needle := range []string{"你没有上一轮的会话记忆", "先核对再动手", "历史结论只对当时条件成立"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("fresh prompt missing %q:\n%s", needle, p)
		}
	}
}

func TestRoundPromptInjectsFullStateAfterCompaction(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Compacted: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("post-compaction round did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "harness 刚压缩过上下文（有损）") || !strings.Contains(p, "历史建议") {
		t.Fatalf("post-compaction prompt does not say the memory was lossy:\n%s", p)
	}
}

// A note is memory, not an instruction (agent.md §3, audit F12.5): the full-note
// preambles must say the current effective instructions and fact record versions
// win, and must not order the model to "execute" the note or treat its next step
// as the current command. This pins all four memory-loss branches, not just one.
func TestStateNoteIsHistoricalAdviceNotInstruction(t *testing.T) {
	cases := []struct {
		name string
		rc   RoundContext
	}{
		{"fresh session", RoundContext{Resume: false}},
		{"after compaction", RoundContext{Resume: true, Compacted: true}},
		{"after handoff round", RoundContext{Resume: true, Handoff: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := stateSection(testNote, tt.rc)
			if !strings.Contains(p, "历史") {
				t.Fatalf("note preamble does not describe the note as history:\n%s", p)
			}
			for _, want := range []string{"不是指令", "当前项目文档", "账本记录版本"} {
				if !strings.Contains(p, want) {
					t.Fatalf("note preamble missing %q:\n%s", want, p)
				}
			}
			for _, banned := range []string{"照它执行", "以它的「下一步」为准"} {
				if strings.Contains(p, banned) {
					t.Fatalf("note preamble still turns history into an order (%q):\n%s", banned, p)
				}
			}
		})
	}
	// The note-taking contract of the handoff round must carry the same rule.
	if strings.Contains(handoffSection, "照它执行") || !strings.Contains(handoffSection, "不是对下一轮的指令") {
		t.Fatalf("handoff note contract lost the historical-advice boundary:\n%s", handoffSection)
	}
}

func TestRoundPromptInjectsFullStateAfterHandoffRound(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Handoff: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("round after handoff did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "上一轮是交接轮") {
		t.Fatalf("prompt does not say the note was rewritten:\n%s", p)
	}
}

// The handoff contract is "the note comes back in full once, in the round that
// opens the new commander session" — and only once. The pressure-cycle flag that
// stops a second handoff must not keep re-pasting the note: later rounds are
// ordinary pointer rounds, and the prompt must stop claiming the previous round
// was the handoff (audit 42 G1).
func TestHandoffNoteIsInjectedOnlyOnTheRoundAfterTheHandoff(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if err := mem.Save("# 笔记\n" + testNote + "\n"); err != nil {
		t.Fatal(err)
	}
	results := []RoundResult{
		{Tokens: 1, ContextTokens: 110000, ContextWindow: 128000},
		{Tokens: 1, ContextTokens: 111000, ContextWindow: 128000, Actions: []Action{{Kind: "state"}}},
		{Tokens: 1, ContextTokens: 20000, ContextWindow: 128000},
		{Tokens: 1, ContextTokens: 30000, ContextWindow: 128000},
		{Tokens: 1, ContextTokens: 40000, ContextWindow: 128000},
	}
	var prompts []string
	idx := 0
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, p string, resume bool) (RoundResult, error) {
		prompts = append(prompts, p)
		res := results[idx]
		idx++
		if idx == 2 {
			res.RunID = completeNoteRound(t, st, ctx, "# 笔记\n"+testNote+"\n")
		}
		return res, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "handoff", true)
	r.Run(context.Background(), "rotation", true)
	r.Run(context.Background(), "later", true)
	r.Run(context.Background(), "later-2", true)
	if len(prompts) != 5 {
		t.Fatalf("rounds = %d, want 5", len(prompts))
	}
	if !strings.Contains(prompts[1], "## 交接（本轮必做）") {
		t.Fatalf("round at 85%% was not the handoff round:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], "## 你的笔记（state.md，全文）") || !strings.Contains(prompts[2], testNote) {
		t.Fatalf("the rotation round did not get the whole note:\n%s", prompts[2])
	}
	if !strings.Contains(prompts[2], "计划内的新会话第一轮") {
		t.Fatalf("the round after the handoff does not describe the session swap:\n%s", prompts[2])
	}
	for i, later := range prompts[3:] {
		if strings.Contains(later, "## 你的笔记（state.md，全文）") || strings.Contains(later, testNote) {
			t.Fatalf("round %d after the handoff re-pasted the note:\n%s", i+1, later)
		}
		if strings.Contains(later, "上一轮是交接轮") {
			t.Fatalf("round %d after the handoff still claims it was the previous round:\n%s", i+1, later)
		}
		if n := StateInjectionBytes(later); n != 0 {
			t.Fatalf("pointer round %d reported %d injected note bytes", i+1, n)
		}
	}
}

func TestRoundPromptInjectsFullStateAfterRestart(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, Restarted: true, StatePath: "/data/state.md"})
	if !strings.Contains(p, "## 你的笔记（state.md，全文）") || !strings.Contains(p, testNote) {
		t.Fatalf("first round after restart did not inject the full note:\n%s", p)
	}
	if !strings.Contains(p, "进程重启过") {
		t.Fatalf("restart prompt does not warn about settled runs:\n%s", p)
	}
}

func TestPausedHandoffDoesNotCompletePressureCycle(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.lastContext, r.ContextWindow = 110000, 128000
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		r.Pause()
		return RoundResult{ContextTokens: 110000}, nil
	}
	r.Run(context.Background(), "manual", true)
	if r.handoffDone || r.handoffRound {
		t.Fatal("canceled handoff was recorded as completed")
	}
	r.Resume()
	var prompt string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompt = p
		return RoundResult{ContextTokens: 110000}, nil
	}
	r.Run(context.Background(), "manual", true)
	if !strings.Contains(prompt, "## 交接（本轮必做）") {
		t.Fatal("resumed pressure cycle skipped the interrupted handoff")
	}
}

func TestRoundPromptInjectsPointerOnlyOnNormalResume(t *testing.T) {
	p := promptFor(RoundContext{Resume: true, StatePath: "/data/projects/default/state.md"})
	if strings.Contains(p, testNote) {
		t.Fatalf("a normal continued round re-pasted the whole note:\n%s", p)
	}
	if !strings.Contains(p, "## 你的笔记（state.md）") {
		t.Fatalf("pointer section missing:\n%s", p)
	}
	for _, needle := range []string{"不要重复粘贴", "/data/projects/default/state.md", "GET /api/state", "版本正文以 API 为准"} {
		if !strings.Contains(p, needle) {
			t.Fatalf("pointer missing %q:\n%s", needle, p)
		}
	}
	// The pointer is a pointer: if it ever grows into a second copy of the note
	// the optimisation is gone.
	if n := len([]rune(stateSection(testNote, RoundContext{Resume: true, StatePath: "/data/projects/default/state.md"}))); n > 380 {
		t.Fatalf("pointer section grew to %d chars", n)
	}
}

func TestRoundPromptOmitsStateSectionWhenEmpty(t *testing.T) {
	p := CommanderRoundPrompt(board.View{}, "  ", "", ContextStatus{}, RoundContext{Resume: false})
	if strings.Contains(p, "## 你的笔记") {
		t.Fatalf("an empty note must not create an empty section:\n%s", p)
	}
}

// The API records "the whole note was injected" from the prompt itself; the
// helper must measure the full section and return 0 for the pointer.
func TestStateInjectionBytesAudit(t *testing.T) {
	full := promptFor(RoundContext{Resume: false, StatePath: "/data/state.md"})
	if n := StateInjectionBytes(full); n < len([]byte(testNote)) {
		t.Fatalf("full-note bytes = %d, want at least the note's %d", n, len([]byte(testNote)))
	}
	pointer := promptFor(RoundContext{Resume: true, StatePath: "/data/state.md"})
	if n := StateInjectionBytes(pointer); n != 0 {
		t.Fatalf("pointer-only prompt reported %d injected bytes", n)
	}
}

func TestRoundPromptPreservesCollaborationWithoutRetiredDispatch(t *testing.T) {
	ordinary := promptFor(RoundContext{Resume: true, StatePath: "/state.md"})
	handoff := CommanderRoundPrompt(board.View{}, testNote, "", ContextStatus{Used: 110000, Window: 128000}, RoundContext{Resume: true})
	for _, prompt := range []string{ordinary, handoff} {
		for _, retired := range []string{"模型档位", "worker_profile", "strong_profile", "tier=", "/api/model-profiles", "派子意图", "平级发布"} {
			if strings.Contains(prompt, retired) {
				t.Fatalf("prompt advertises retired routing: %s", retired)
			}
		}
		for _, kept := range []string{"互发消息", "共享事实", "记录交接"} {
			if !strings.Contains(prompt, kept) {
				t.Fatalf("prompt lost collaboration: %s", kept)
			}
		}
	}
}

// The runner does not guess resume: the caller resolves it (only the executor
// knows whether the AOP session still exists) and the prompt follows.
func TestRunnerPassesSessionContextToPrompt(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	mem := state.New(filepath.Join(dir, "state.md"))
	if err := mem.Save("# 笔记\n" + testNote + "\n"); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Commander = func(ctx context.Context, round string, resume bool) (RoundResult, error) {
		prompts = append(prompts, round)
		return RoundResult{Tokens: 1}, nil
	}
	// Round 1: a real resume — pointer only.
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Run(context.Background(), "first", true)
	if strings.Contains(prompts[0], "## 你的笔记（state.md，全文）") || strings.Contains(prompts[0], testNote) {
		t.Fatalf("a resumed round got the full note:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[0], "GET /api/state") {
		t.Fatalf("a resumed round has no pointer:\n%s", prompts[0])
	}
	// Round 2: the session was lost — inject the note in full.
	r.Session = func() RoundContext { return RoundContext{Resume: false} }
	r.Run(context.Background(), "second", true)
	if !strings.Contains(prompts[1], "## 你的笔记（state.md，全文）") || !strings.Contains(prompts[1], testNote) {
		t.Fatalf("a fresh round did not get the full note:\n%s", prompts[1])
	}
}

// A compaction is recorded by the runner itself, so even a caller that keeps
// reporting a live session must get the full note on the next round.
func TestRunnerInjectsFullNoteAfterCompaction(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	if err := mem.Save(testNote); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	round := 0
	r := NewRunner(st, mem, "")
	r.ModelName = "test-model"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(ctx context.Context, p string, resume bool) (RoundResult, error) {
		prompts = append(prompts, p)
		round++
		if round == 1 {
			return RoundResult{Tokens: 1, Compacted: true}, nil
		}
		return RoundResult{Tokens: 1}, nil
	}
	r.Run(context.Background(), "first", true)
	r.Run(context.Background(), "second", true)
	if strings.Contains(prompts[1], "## 你的笔记（state.md，全文）") == false || !strings.Contains(prompts[1], testNote) {
		t.Fatalf("round after compaction did not inject the full note:\n%s", prompts[1])
	}
}

// The note has no size budget in code. A long note is injected whole on the
// rounds that carry it, and no round is told it is over a line, character or
// byte limit. The reason is the note's role: a hard cap turns it into a form to
// fill, and the model starts copying facts to fill it instead of writing the
// judgement the next round needs (D106). Length stays the model's call.
func TestLongNoteIsInjectedWholeWithNoSizeBudget(t *testing.T) {
	lines := make([]string, 0, 900)
	for i := 1; i <= 900; i++ {
		lines = append(lines, fmt.Sprintf("NOTE-LINE-%d", i))
	}
	note := strings.Join(lines, "\n")
	p := CommanderRoundPrompt(board.View{}, note, "", ContextStatus{}, RoundContext{Resume: false})
	if !strings.Contains(p, "NOTE-LINE-900") {
		t.Fatal("a long note was clipped")
	}
	if n := StateInjectionBytes(p); n < len([]byte(note)) {
		t.Fatalf("injected note = %d bytes, want the whole %d", n, len([]byte(note)))
	}
	for _, banned := range []string{"笔记预算", "注入预算", "只注入了前", "目标 ≤", "字数上限"} {
		if strings.Contains(p, banned) {
			t.Fatalf("a long note still gets a hard size notice (%q):\n%s", banned, p)
		}
	}
	// The pointer round still does not paste the note, and it carries no size
	// nudge either.
	pointer := CommanderRoundPrompt(board.View{}, note, "", ContextStatus{}, RoundContext{Resume: true})
	if strings.Contains(pointer, "NOTE-LINE-") || strings.Contains(pointer, StateFullHeader) {
		t.Fatalf("pointer round leaked note content:\n%s", pointer)
	}
	for _, banned := range []string{"笔记预算", "目标 ≤", "字数上限"} {
		if strings.Contains(pointer, banned) {
			t.Fatalf("pointer round still gets a hard size notice (%q):\n%s", banned, pointer)
		}
	}
}

// The note prompt is a soft reminder about role, not a size rule: the note is
// judgement and clues (not a round log, not a second ledger), facts are cited by
// id instead of copied, and its length is the model's judgement — with the real
// cost named, not a quota.
func TestNotePromptIsASoftReminderNotASizeRule(t *testing.T) {
	for _, rc := range []RoundContext{
		{Resume: false, StatePath: "/data/state.md"}, // full note
		{Resume: true, StatePath: "/data/state.md"},  // pointer only
	} {
		p := CommanderRoundPrompt(board.View{}, "笔记", "", ContextStatus{}, rc)
		for _, want := range []string{
			"不是轮次日志", "第二份账本", "别抄正文", "只写对下一轮有价值的判断与线索",
			"篇幅由你判断", "不会被截断",
		} {
			if !strings.Contains(p, want) {
				t.Fatalf("note prompt (resume=%v) missing %q:\n%s", rc.Resume, want, p)
			}
		}
	}
}

// The note endpoint is per-round and comes from the caller. The shared harness
// token must not read or rewrite state.md, so the prompt names the commander's
// participant-mux endpoint and the case file that holds its bearer.
func TestStateSectionCarriesTheCommanderNoteEndpoint(t *testing.T) {
	const api = "http://127.0.0.1:9000/p/p1/api/state"
	const token = "/data/projects/p1/commander/.saker/auth"
	rc := RoundContext{Resume: false, StateAPI: api, StateToken: token}
	p := CommanderRoundPrompt(board.View{}, testNote, "", ContextStatus{}, rc)
	for _, want := range []string{"GET " + api, "PUT " + api, token, "$SAKER_AUTH"} {
		if !strings.Contains(p, want) {
			t.Fatalf("full-note prompt missing %q:\n%s", want, p)
		}
	}
	// A normal continued round may rewrite the note too: the pointer branch
	// carries the same recipe instead of the legacy path.
	pointer := stateSection(testNote, RoundContext{Resume: true, StateAPI: api, StateToken: token})
	if !strings.Contains(pointer, "PUT "+api) {
		t.Fatalf("pointer prompt has no note endpoint:\n%s", pointer)
	}
	if strings.Contains(pointer, "GET /api/state") {
		t.Fatalf("pointer prompt still names the shared-token path:\n%s", pointer)
	}
}
