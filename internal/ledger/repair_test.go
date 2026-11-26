package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/evidence"
)

func TestExplicitCursorRepairRequiresPreservedClaimsAndUnchangedFile(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "in_1")
	st, run := newWarningLedger(t)
	raw := `{"kind":"note","title":"fixture","declaration_id":"fixture-observation","summary":"kept"}` + "\n"
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), raw)
	plan, err := PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || len(plan.Problems) == 0 {
		t.Fatalf("missing fact was skippable: %+v %v", plan, err)
	}
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", root, work, time.Now().Add(time.Hour))
	if err = CommitDeclared(work, 0); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || len(plan.Problems) > 0 || plan.Matched != 1 {
		t.Fatalf("preserved claim did not match: %+v %v", plan, err)
	}
	before := st.Snapshot().Facts[0]
	if err = CommitCursorRepair(work, plan); err != nil || Consumed(work) == 0 {
		t.Fatalf("commit: %v", err)
	}
	after := st.Snapshot().Facts[0]
	if before.ClaimVersion != after.ClaimVersion {
		t.Fatal("cursor repair rewrote a claim")
	}
	if err = CommitDeclared(work, 0); err != nil {
		t.Fatal(err)
	}
	plan, _ = PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err = os.WriteFile(filepath.Join(work, HandoffDir, HandoffFile), []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = CommitCursorRepair(work, plan); err == nil {
		t.Fatal("changed handoff file was consumed")
	}
}

func TestCursorRepairCannotSkipAnUnpreservedPlaybook(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "in_1")
	st, run := newWarningLedger(t)
	raw := `{"kind":"playbook","title":"fixture tactic","summary":"applicable","doc":"kept content","from_fact_ids":[]}` + "\n"
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), raw)
	plan, err := PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || len(plan.Problems) == 0 {
		t.Fatalf("uncommitted playbook was skippable: %+v %v", plan, err)
	}
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", root, work, time.Now().Add(time.Hour))
	if err = CommitDeclared(work, 0); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || len(plan.Problems) != 0 {
		t.Fatalf("preserved playbook rejected: %+v %v", plan, err)
	}
}

func TestCursorRepairRequiresIntactPreservedOriginal(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "in_1")
	st, run := newWarningLedger(t)
	defer st.Close()
	writeFile(t, filepath.Join(work, "proof.txt"), "fixture original")
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), `{"declaration_id":"proved","kind":"note","title":"fixture","evidence":["proof.txt"]}`+"\n")
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", root, work, time.Now().Add(time.Hour))
	if err := CommitDeclared(work, 0); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || plan.Persisted != 1 || len(plan.Pending) != 0 {
		t.Fatalf("intact original was not confirmed: %+v, %v", plan, err)
	}
	rows := st.Evidence()
	if len(rows) != 1 {
		t.Fatalf("expected one evidence row, got %d", len(rows))
	}
	original, err := evidence.Path(root, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	// The ledger row and working copy can still match after the archived
	// original disappears. A cursor commit must not skip that declaration.
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanCursorRepair(st.Snapshot(), root, work, "in_1")
	if err != nil || plan.Persisted != 0 || len(plan.Pending) != 1 || len(plan.Pending[0].Reasons) != 1 || !strings.Contains(plan.Pending[0].Reasons[0], rows[0].ID) {
		t.Fatalf("missing original was not identified: %+v, %v", plan, err)
	}
	if err := CommitCursorRepair(work, plan); err == nil || Consumed(work) != 0 {
		t.Fatalf("missing original was skipped: offset=%d, err=%v", Consumed(work), err)
	}
}
