package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// sectionBody returns the verbatim body of the first section whose heading is
// exactly "## <name>", up to the next "## " heading. The comparison callers do
// is byte-exact on purpose: a template sentence appended INSIDE a user section
// is an adjudication over the operator's own words, whatever it says.
func sectionBody(prompt, name string) (string, bool) {
	lines := strings.Split(prompt, "\n")
	head := "## " + name
	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, "\r") == head {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimRight(lines[i], "\r"), "## ") {
			end = i
			break
		}
	}
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n"), true
}

type userSections struct {
	constraints, tactic, desc, doc string
}

// checkUserSections asserts the durable invariant on ONE rendered brief: every
// section that carries the operator's or the dispatcher's own text contains
// exactly that text and nothing else. Any appended template sentence changes
// the body, so a reworded adjudication cannot slip through a word list.
func checkUserSections(t *testing.T, got string, want userSections) {
	t.Helper()

	body, ok := sectionBody(got, "约束")
	if !ok {
		t.Fatalf("没有「## 约束」段：\n%s", got)
	}
	if body != want.constraints {
		t.Fatalf("约束段被改写或追加了模板句\nwant:\n%q\ngot:\n%q", want.constraints, body)
	}
	if n := strings.Count(got, want.constraints); n != 1 {
		t.Fatalf("约束原文出现 %d 次，want 1", n)
	}

	body, ok = sectionBody(got, "本轮指派")
	if !ok {
		t.Fatalf("没有「## 本轮指派」段：\n%s", got)
	}
	if got, want2 := fieldBody(body), want.tactic+"\n"+want.desc; got != want2 {
		t.Fatalf("本轮指派段被追加了模板句（裁决「目标 vs 打法」就是这一类）\nwant:\n%q\ngot:\n%q", want2, got)
	}

	body, ok = sectionBody(got, "细节与约束")
	if !ok {
		t.Fatalf("没有「## 细节与约束」段：\n%s", got)
	}
	if got := fieldBody(body); got != want.doc {
		t.Fatalf("细节与约束段被追加了模板句（把「只能 GET」改判成硬/软约束就是这一类）\nwant:\n%q\ngot:\n%q", want.doc, got)
	}
}

// fieldBody removes the rendering that keeps a field from becoming brief
// structure — the labels and the fences — and leaves the words the field
// carried. The structural rule itself is pinned in
// prompt_field_injection_test.go; this file stays about words, and a template
// sentence added alongside the rendering still fails the comparison.
func fieldBody(section string) string {
	var kept []string
	inFence := false
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if backticksOnly(trimmed) {
			inFence = !inFence
			continue
		}
		if !inFence && trimmed == "说明：" {
			continue
		}
		if !inFence {
			if rest, ok := strings.CutPrefix(trimmed, "意图："); ok {
				kept = append(kept, rest)
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestUserSectionsCarryOnlyTheUsersWords covers two of the three bad-input
// classes — a tactic written into the goal, and a hard limit written into the
// constraints — on BOTH render paths. The reduced path skips optional
// summaries; it must never skip or decorate the user's own text.
func TestUserSectionsCarryOnlyTheUsersWords(t *testing.T) {
	want := userSections{
		constraints: "约束（唯一硬线）：不要删除或修改真实数据。\n测试产物清不掉的，写进事实或报告即可。",
		tactic:      "拿 admin 后台：用 sqlmap 跑 /admin 的登录参数",
		desc:        "先确认 /admin 是否存在，再决定用哪种方法。",
		doc:         "只能用 GET 方法探测；不要用 POST。\n先看响应头，再决定下一步。",
	}

	db := store.DB{Intents: []model.Intent{{
		ID: "in_adv", Title: want.tactic, Description: want.desc, Doc: want.doc, Eval: "有可复判的证据",
	}}, Runs: []model.Run{{ID: "r_adv", IntentID: "in_adv"}}}

	regular := BuildPrompt(db, db.Intents[0], want.constraints, "", "", 0, false)
	reduced := buildPromptWithoutOptionalSummaries(db, db.Intents[0], want.constraints, "", "", 0, false, false, "", false)

	for name, got := range map[string]string{"常规路径": regular, "精简路径": reduced} {
		got := got
		t.Run(name, func(t *testing.T) { checkUserSections(t, got, want) })
	}
}

// TestExecutorBriefCarriesNoStartOrGoal is the third bad-input class: start and
// goal contradict each other. The executor has no power to adjudicate them, so
// neither may appear in its brief — it receives the constraint text and this
// round's assignment only.
func TestExecutorBriefCarriesNoStartOrGoal(t *testing.T) {
	const constraint = "约束：不要删除或修改真实数据。"
	const start = "起点：只测 10.0.0.0/24 这一段。"
	const goal = "终点：拿到 10.0.1.9 的 shell。"
	const tactic = "整理 10.0.0.0/24 的可达面"

	db := store.DB{Intents: []model.Intent{{ID: "in_adv2", Title: tactic}}, Runs: []model.Run{{ID: "r_adv2", IntentID: "in_adv2"}}}
	regular := BuildPrompt(db, db.Intents[0], constraint, "", "", 0, false)
	reduced := buildPromptWithoutOptionalSummaries(db, db.Intents[0], constraint, "", "", 0, false, false, "", false)

	for name, got := range map[string]string{"常规路径": regular, "精简路径": reduced} {
		for _, bad := range []string{start, goal, "10.0.1.9"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%s：执行者任务书里出现了起点/终点内容 %q：\n%s", name, bad, got)
			}
		}
		if !strings.Contains(got, constraint) {
			t.Fatalf("%s：约束原文没有进任务书：\n%s", name, got)
		}
	}
}
