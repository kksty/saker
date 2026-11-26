package verifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// dialFailExec models "the host was not reachable": the executor is called but
// no model turn happens, exactly like the 1-second connectex failure that hit
// the real instance. It returns an error instead of a result.
type dialFailExec struct {
	called bool
}

func (e *dialFailExec) Name() string { return "dial-fail" }

func (e *dialFailExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.called = true
	return executor.Result{}, errors.New("aop: 连接 ws://127.0.0.1:18799/cyber.rpc: connectex: 连接尝试失败")
}

// "We never asked" and "we asked and could not tell" are different facts about
// the ledger. A review whose model turn never started must not write a verdict:
// needs_evidence is terminal for the automatic queue, so one dropped loopback
// SYN would permanently retire a real finding from review.
func TestReviewThatNeverStartedWritesNoVerdict(t *testing.T) {
	st, factID, cases := reviewFixture(t)
	ex := &dialFailExec{}

	review, err := New(st, ex, cases).Run(context.Background(), factID)
	if err == nil {
		t.Fatalf("a review that never started reported a verdict: %+v", review)
	}
	if !ex.called {
		t.Fatal("the executor was never asked: the fixture does not exercise a transport failure")
	}

	// 1. No verdict for this fact at any version.
	for _, r := range st.Snapshot().Reviews {
		if r.FactID == factID {
			t.Fatalf("a review that never ran wrote a verdict: %+v", r)
		}
	}
	// 2. The fact stays an automatic candidate, so the queue can retry it.
	candidates, _ := st.ReviewCandidates("", "", 100)
	found := false
	for _, c := range candidates {
		if c.ID == factID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the fact left the automatic queue after a transport failure: %+v", candidates)
	}
	// 3. The fact is still unverified and the run carries the reason instead.
	if got, _ := st.FactByID(factID); got.Status != "unverified" {
		t.Fatalf("fact status = %q, want unverified", got.Status)
	}
	run := lastVerifierRun(t, st)
	if run.Status != model.RunFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if !strings.Contains(run.ExitReason, "connectex") {
		t.Fatalf("run exit reason lost the transport cause: %q", run.ExitReason)
	}
}

// The other half of the rule: a run that DID end normally but left no usable
// verdict is a real judgement about the assertion, so it stays needs_evidence.
func TestReviewThatEndedWithoutAVerdictStillRecordsNeedsEvidence(t *testing.T) {
	st, factID, cases := reviewFixture(t)
	ex := &stubExec{} // exits 0, writes no verdict file

	review, err := New(st, ex, cases).Run(context.Background(), factID)
	if err != nil {
		t.Fatalf("a normal end without a verdict should record needs_evidence, got error: %v", err)
	}
	if review.Verdict != "needs_evidence" {
		t.Fatalf("verdict = %q, want needs_evidence", review.Verdict)
	}
	if !strings.Contains(review.Notes, "没有给出可用结论") {
		t.Fatalf("notes = %q, want the missing-verdict reason", review.Notes)
	}
}

func reviewFixture(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	cases := filepath.Join(root, "cases")
	if err := os.MkdirAll(filepath.Join(cases, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The reviewer is given the cited bytes: a finding with no preserved evidence
	// is never admitted to review at all (the store refuses a pass without it), so
	// a fixture that wants to exercise run mechanics must cite material.
	if err := os.WriteFile(filepath.Join(cases, "in_1", "proof.txt"), []byte("GET /admin -> 200\nadmin password: redacted"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID:    "in_1",
		Kind:        "vuln",
		Title:       "操作日志明文记录管理员登录口令",
		Status:      "unverified",
		EvidenceIDs: []string{ev.ID},
		Origin:      model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, f.ID, cases
}

func lastVerifierRun(t *testing.T, st *store.Store) model.Run {
	t.Helper()
	var last model.Run
	found := false
	for _, r := range st.Snapshot().Runs {
		if r.Purpose == "verifier" {
			last = r
			found = true
		}
	}
	if !found {
		t.Fatal("no verifier run recorded")
	}
	return last
}
