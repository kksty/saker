package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// writePlaybookLine settles one run whose only declaration is a playbook citing
// the given source ids.
func settlePlaybookLine(t *testing.T, st *store.Store, runID, intentID string, sourceIDs []string) model.Run {
	t.Helper()
	dir := t.TempDir()
	caseRoot := filepath.Join(dir, "cases")
	work := filepath.Join(caseRoot, intentID)
	if err := os.MkdirAll(filepath.Join(work, ".tw"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"kind": "playbook", "title": "字典注入套件", "summary": "参数可控时先用字典探边界",
		"doc": "字典命中后换标记值确认", "from_fact_ids": sourceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".tw", "facts.jsonl"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	g := Ingestor{Store: st}
	g.OnFinish(runID, intentID, caseRoot, work, time.Now().Add(-time.Minute))
	run, ok := st.GetRun(runID)
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	return run
}

// The declaration is stored as the executor wrote it, and its provenance (source
// run, source ids, admitted versions) is kept. Whether it may be called confirmed
// is decided on read: the source was admitted unverified and has no evidence yet,
// so the playbook is recalled as a candidate — storing a "confirmed" row is not
// the verdict.
func TestDeclaredExperiencePreservesExplicitSourceFactsAndRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f, err := st.AddFact(model.Fact{Title: "observed source", EntityRef: "target-A"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: "in_task"})
	if err != nil {
		t.Fatal(err)
	}
	settlePlaybookLine(t, st, run.ID, "in_task", []string{f.ID})

	db := st.Snapshot()
	books := db.Playbooks
	if len(books) != 1 || books[0].SourceRunID != run.ID || len(books[0].FromFactIDs) != 1 || books[0].FromFactVersions[f.ID] != 1 {
		t.Fatalf("provenance lost: %+v", books)
	}
	if books[0].Status != model.PlaybookConfirmed {
		t.Fatalf("the executor's declaration was rewritten at write time: %+v", books[0])
	}
	if books[0].Confirmed(model.FactIndex(db.Facts)) {
		t.Fatal("an unverified, evidence-free source was accepted as confirmed")
	}
}

// A declaration whose source cannot back it must not be silent: the run row names
// the tactic and says it will recall as a candidate. Otherwise the executor sees
// a "confirmed" playbook in the ledger and believes the tactic was proven.
func TestPlaybookWithUnqualifiedSourceWarnsOnTheRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src, err := st.AddFact(model.Fact{Title: "未核验观察", Kind: "vuln"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: "in_task"})
	if err != nil {
		t.Fatal(err)
	}
	settled := settlePlaybookLine(t, st, run.ID, "in_task", []string{src.ID})

	joined := strings.Join(settled.Warnings, "\n")
	for _, want := range []string{"字典注入套件", "候选", "核实来源后自动恢复确认"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning missing %q:\n%s", want, joined)
		}
	}
	books := st.Snapshot().Playbooks
	if len(books) != 1 || books[0].Status != model.PlaybookConfirmed {
		t.Fatalf("the warning must not rewrite the declaration: %+v", books)
	}
}

// A qualified source needs no warning: the warning is about a label the ledger
// cannot honestly show, not about every executor declaration.
func TestPlaybookWithVerifiedSourceIsNotWarned(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A qualified source cites a row the ledger can open: production only ever
	// cites ids that ingest created (ingest.go:153-158), and the write gate refuses
	// a pass that names evidence nobody preserved — a hard-coded "ev_fixture" would
	// make this fixture depend on the hole that rule closes.
	ev, err := st.AddEvidence(model.Evidence{URI: "in_task/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	src, err := st.AddFact(model.Fact{
		Title: "已核验观察", Kind: "vuln", EvidenceIDs: []string{ev.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: src.ID, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: "in_task"})
	if err != nil {
		t.Fatal(err)
	}
	settled := settlePlaybookLine(t, st, run.ID, "in_task", []string{src.ID})
	if joined := strings.Join(settled.Warnings, "\n"); strings.Contains(joined, "候选") {
		t.Fatalf("a qualified source was warned about:\n%s", joined)
	}
}
