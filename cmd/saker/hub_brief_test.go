package main

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

// The executor brief carries the built-in constraint document verbatim and never
// the project's start/end text; the commander reads all three documents. The
// boundary is the documents' own wording, so the control plane must neither
// interpret it nor leak the parts the executor is not given: the assignment and
// its acceptance criterion are what the direction is judged by.
func TestExecutorBriefCarriesConstraintsOnlyAndCommanderSeesAll(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const start = "起点：只测 10.0.0.0/24 里的只读接口"
	const constraints = "内置约束：不改数据；测试产物清不掉就记录后继续"
	const goalStatement = "终点：拿到管理员权限"
	p, err := mgr.Create("acme", start)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetDescription(p.ID, constraints); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.AddGoal(model.Goal{Statement: goalStatement}); err != nil {
		t.Fatal(err)
	}

	// The project server's read surface, without the hub's background loops: this
	// test is about what the two prompt inputs contain, not about runtime wiring.
	srv := api.New(p.Store, nil, p.Dir, p.CaseDir)
	srv.Project = p
	srv.Scope = p.ScopeText()
	srv.Description = p.DescriptionText()

	// The executor's whole input is the constraint document, the assignment and
	// its acceptance criterion — no start text, no goal text.
	if got := srv.Constraints(); !strings.Contains(got, constraints) {
		t.Fatalf("executor constraints = %q, want the constraint document", got)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "读 /admin 的响应头", Eval: "拿到响应头"})
	if err != nil {
		t.Fatal(err)
	}
	brief := api.BuildPrompt(p.Store.Snapshot(), in, srv.Constraints(), "", "", 0, false)
	if got := strings.Count(brief, constraints); got != 1 {
		t.Fatalf("executor brief rendered the constraint document %d times, want 1:\n%s", got, brief)
	}
	for _, forbidden := range []string{start, goalStatement} {
		if strings.Contains(brief, forbidden) {
			t.Fatalf("executor brief leaked %q:\n%s", forbidden, brief)
		}
	}
	if !strings.Contains(brief, in.Eval) || !strings.Contains(brief, in.Title) {
		t.Fatalf("executor brief lost the assignment or its acceptance criterion:\n%s", brief)
	}

	// The commander reads the three documents as they are written: constraint,
	// start and the current goal all reach its round input.
	docs := srv.CommanderDocs()
	for _, want := range []string{constraints, start, goalStatement} {
		if !strings.Contains(docs, want) {
			t.Fatalf("commander docs lost %q:\n%s", want, docs)
		}
	}
}
