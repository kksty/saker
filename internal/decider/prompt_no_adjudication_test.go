package decider

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
)

// TestCommanderDocsAreNotAdjudicated is the third bad-input class: start and
// goal contradict each other. The commander is the one role that reads all
// three documents, so a template sentence ranking them ("起点优先"/"终点优先")
// would decide a conflict the operator never resolved. The invariant is
// positional, not lexical: the documents arrive as one verbatim block, and the
// very next thing after it must be the round section — nothing in between.
func TestCommanderDocsAreNotAdjudicated(t *testing.T) {
	const start = "起点：只测 10.0.0.0/24 这一段。"
	const goal = "终点：拿到 10.0.1.9 的 shell。"
	const constraints = "约束（唯一硬线）：不要删除或修改真实数据。"
	docs := constraints + "\n" + start + "\n" + goal

	got := CommanderRoundPrompt(board.View{}, "", docs, ContextStatus{}, RoundContext{})

	if n := strings.Count(got, docs); n != 1 {
		t.Fatalf("三份文档原文出现 %d 次，want 1；段落被拆开或被改写：\n%s", n, got)
	}
	idx := strings.Index(got, docs)
	after := strings.TrimSpace(got[idx+len(docs):])
	if !strings.HasPrefix(after, "## 本轮") {
		t.Fatalf("文档块之后被插入了模板内容（对起点/终点做排序或裁决就是这一类）：\n%q", after)
	}
	for _, want := range []string{start, goal, constraints} {
		if !strings.Contains(got, want) {
			t.Fatalf("文档原文缺失 %q：\n%s", want, got)
		}
	}
}
