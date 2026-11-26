package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/contract"
	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// retiredPromptPhrases are the meta sentences the team removed on 2026-11-21.
// They are matched as whole phrases, not as a single word: 「授权」alone appears
// in legitimate user text and in the commander's own identity, so a one-word
// assertion is both too broad (it fails on honest text) and too narrow (it
// misses「文档即边界…以文档为准」).
var retiredPromptPhrases = []string{
	"授权来源",
	"文档即边界",
	"文档全文即边界",
	"不是授权",
	"不是重新授权",
	"不是新的授权",
	"冲突时停手",
	"冲突停手",
	"以文档为准",
	"不构成新的授权",
	// The retired template contracts: one classified a dispatch into goal vs
	// tactic, the other into hard constraint vs suggested route. Both let the
	// template adjudicate the user's prose, which is the dispatcher's call.
	"本意图的目标是这段意图",
	"以上「细节与约束」中，范围、禁止项与必须满足的条件是硬约束",
	// The retired stop/authorization meta-language (source scan, L2).
	"按上方「约束」「本轮指派」和「当前持久指令」执行",
	"只有清单外目标或不可逆动作才先问",
	"当前授权边界",
	"不是逐资产白名单",
	"目标优先不等于默认授权",
	"授权边界是硬线",
	"授权变化、跨意图取舍",
	// The commander identity carried these two until 2026-11-22: the first made
	// "no premises" a phrase the dispatcher must copy, the second made the
	// operator's authorization the condition for naming a tactic.
	"纯授权驱动",
	"基于授权首轮探测",
	"只有操作员授权本身限定了做法时",
}

func assertNoRetiredPhrases(t *testing.T, where, prompt string) {
	t.Helper()
	for _, phrase := range retiredPromptPhrases {
		if strings.Contains(prompt, phrase) {
			t.Fatalf("%s still carries the retired phrase %q:\n%s", where, phrase, prompt)
		}
	}
}

// The executor brief is: 约束 → 本轮指派 → the rest, each section exactly once.
// The operator-priority sentence is the very first paragraph, before any heading.
//
// Falsification: move the priority sentence below a heading, drop a section or
// render one twice, and this fails.
func TestExecutorBriefSectionOrder(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "读 /admin 的响应头", Description: "核对缓存策略", Eval: "拿到响应头"}},
		Runs:    []model.Run{{ID: "r_x", IntentID: "in_x"}},
	}
	docs := "不要删除或修改真实数据。测试产物清不掉就记录后继续。"
	p := BuildPrompt(db, db.Intents[0], docs, "", "", 0, false)

	if !strings.HasPrefix(p, contract.OperatorPriority) {
		t.Fatalf("operator priority is not the first paragraph:\n%s", p)
	}
	order := []string{"## 约束", "## 本轮指派", "## 验收标准"}
	at := -1
	for _, heading := range order {
		next := strings.Index(p, heading)
		if next < 0 {
			t.Fatalf("section %q missing:\n%s", heading, p)
		}
		if next <= at {
			t.Fatalf("section %q is out of order (offset %d after %d):\n%s", heading, next, at, p)
		}
		if got := strings.Count(p, heading); got != 1 {
			t.Fatalf("section %q rendered %d times, want 1", heading, got)
		}
		at = next
	}
	if got := strings.Count(p, contract.OperatorPriority); got != 1 {
		t.Fatalf("priority sentence rendered %d times, want 1", got)
	}
	if got := strings.Count(p, docs); got != 1 {
		t.Fatalf("constraint document rendered %d times, want 1", got)
	}
	assertNoRetiredPhrases(t, "executor brief", p)
}

// A document that happens to contain「授权」passes through untouched: the
// retirement rule is about template sentences, not about filtering user text.
func TestConstraintDocumentPassesThroughVerbatim(t *testing.T) {
	db := store.DB{Intents: []model.Intent{{ID: "in_x", Title: "t"}}}
	docs := "授权文档：只测 A 段，不要碰 B 段。"
	p := BuildPrompt(db, db.Intents[0], docs, "", "", 0, false)
	if got := strings.Count(p, docs); got != 1 {
		t.Fatalf("a document containing the word was filtered or duplicated (%d)", got)
	}
	// The user's document is not filtered: a document that happens to contain the
	// word keeps it. That is why the retirement rule is a phrase list over
	// template fixtures, not a one-word ban over user data.
	assertNoRetiredPhrases(t, "executor brief", p)
}

// Both render paths carry no template meta-language. Scanning only the regular
// brief is how the earlier phrase list "passed" while the compact brief still
// rendered its own sentences; the context-limit brief is a separate render.
func TestBothBriefPathsCarryNoRetiredPhrases(t *testing.T) {
	db := store.DB{
		Intents: []model.Intent{{ID: "in_x", Title: "读 /admin 的响应头", Description: "核对缓存策略", Doc: "先用只读账号抓响应头", Eval: "拿到响应头"}},
		Runs:    []model.Run{{ID: "r_x", IntentID: "in_x"}},
	}
	docs := "不删改真实数据。"
	full := BuildPrompt(db, db.Intents[0], docs, "", "", 0, false)
	compact := buildPromptWithoutOptionalSummaries(db, db.Intents[0], docs, "", "", 0, false, false, "", false)
	for name, p := range map[string]string{"regular": full, "compact": compact} {
		assertNoRetiredPhrases(t, name+" brief", p)
		if got := strings.Count(p, docs); got != 1 {
			t.Fatalf("%s brief rendered the constraint document %d times, want 1:\n%s", name, got, p)
		}
		// The template itself must not carry the word; user text may. This fixture
		// has none, so any hit is template wording.
		if strings.Contains(p, "授权") {
			t.Fatalf("%s brief still carries template authorization wording:\n%s", name, p)
		}
	}
}

// A persistent instruction is injected as its own text: no sentence explains
// where it comes from or how it relates to the documents.
func TestInstructionSectionRendersOnlyTheText(t *testing.T) {
	rows := []model.Instruction{{ID: "instruction_1", Version: 3, Text: "多关注运维口"}}
	section := instructionSection(rows, "r_x")
	assertNoRetiredPhrases(t, "instruction section", section)
	for _, banned := range []string{"来源", "范围依据"} {
		if strings.Contains(section, banned) {
			t.Fatalf("instruction section still explains %q:\n%s", banned, section)
		}
	}
	if !strings.Contains(section, "多关注运维口") || !strings.Contains(section, "instruction_1") {
		t.Fatalf("instruction text or id missing:\n%s", section)
	}
}

// The commander round carries the priority sentence once, between the role line
// and the documents; the retired phrases stay out of it too.
func TestCommanderRoundCarriesOperatorPriorityOnce(t *testing.T) {
	in := testDB()
	docs := "## 约束\n不要改数据\n\n## 起点\n10.0.0.0/24\n\n"
	prompt := decider.CommanderRoundPrompt(board.BuildFromDB(in, 0), "", docs, decider.ContextStatus{}, decider.RoundContext{})
	if got := strings.Count(prompt, contract.OperatorPriority); got != 1 {
		t.Fatalf("commander round renders the priority sentence %d times, want 1", got)
	}
	role := strings.Index(prompt, "你是Saker项目的指挥官")
	prio := strings.Index(prompt, contract.OperatorPriority)
	docsAt := strings.Index(prompt, "## 约束")
	if !(role >= 0 && role < prio && prio < docsAt) {
		t.Fatalf("priority sentence is not between the role and the documents (role=%d prio=%d docs=%d)", role, prio, docsAt)
	}
	// Scan the whole round prompt, including the commander identity: the identity
	// is the part that carried the last two adjudicating sentences, and scanning
	// only prompt[prio:] skipped exactly that prefix.
	assertNoRetiredPhrases(t, "commander round", prompt)
}

// The retired sentences were checked only in the generated prompts, so the same
// class came back through the skill assets: the harness expands those files into
// a session, so they reach the model too. Scan them as files and assert the whole
// family, not the single word 「授权来源」 — 「不构成新的授权」and 「成为前提或授权」
// do not contain it.
//
// The review-mode family is here for the same reason: admission is described in
// exactly one place, commanderIdentityFor(reviewsOff). A static copy in a skill
// would sit next to the other mode's sentence instead of replacing it.
var retiredSkillPhrases = []string{
	"授权来源",
	"不是授权",
	"不构成授权",
	"不构成新的授权",
	"不扩展授权",
	"也不会扩大范围",
	"成为前提或授权",
	"不是新的授权",
	"等复核",
	"不能基于它派发",
	"没有自动复核开关",
	"复核仍是前提准入",
}

func TestSkillAssetsCarryNoRetiredMetaLanguage(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "skills", "assets")
	var files []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("no skill assets found under %s", root)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, phrase := range retiredSkillPhrases {
			if strings.Contains(string(raw), phrase) {
				t.Errorf("%s still carries the retired phrase %q", path, phrase)
			}
		}
	}
}
