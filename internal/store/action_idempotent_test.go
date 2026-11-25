package store

// A closeout resends receipts: a retried HTTP call, a report line introduced
// twice, an operator reconciling an action the previous run already settled.
// The stored outcome is identical, so the second submission describes the same
// fact. Treating it as a conflict taught the fleet to report a failure for work
// that was done — the one kind of noise a closeout cannot afford.
//
// These assertions pin both halves: an identical resubmission is a no-op
// success, and everything that is not identical still fails.

import (
	"errors"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestIdenticalActionResolutionIsANoOpSuccess(t *testing.T) {
	s, in, run := actionFixture(t)
	a, created, err := s.BeginAction(in.ID, run.ID, "flag-01", "提交 flag", "c01")
	if err != nil || !created {
		t.Fatalf("begin: created=%v err=%v", created, err)
	}

	first, duplicate, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "已提交 c01", "receipt:1", run.ID)
	if err != nil || duplicate {
		t.Fatalf("first resolve: duplicate=%v err=%v", duplicate, err)
	}

	// Same payload through the same (now stale) version: the caller re-sent a
	// receipt, not a new outcome.
	again, duplicate, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "已提交 c01", "receipt:1", run.ID)
	if err != nil || !duplicate {
		t.Fatalf("resubmission: duplicate=%v err=%v", duplicate, err)
	}
	if again.Version != first.Version {
		t.Fatalf("duplicate bumped version %d -> %d", first.Version, again.Version)
	}
	if !again.UpdatedAt.Equal(first.UpdatedAt) || again.Result != first.Result || again.ResultRef != first.ResultRef {
		t.Fatalf("duplicate rewrote the stored outcome: %+v vs %+v", again, first)
	}
	if n := len(s.Actions(in.ID)); n != 1 {
		t.Fatalf("duplicate created a second action row (%d)", n)
	}

	// Whitespace-only differences are the same receipt after trimming.
	if _, duplicate, err = s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "  已提交 c01\n", "receipt:1", run.ID); err != nil || !duplicate {
		t.Fatalf("trimmed resubmission: duplicate=%v err=%v", duplicate, err)
	}
}

func TestActionResolutionStillRefusesWhatIsNotTheSameOutcome(t *testing.T) {
	s, in, run := actionFixture(t)
	a, _, err := s.BeginAction(in.ID, run.ID, "flag-02", "提交 flag", "c02")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "已提交 c02", "", run.ID); err != nil {
		t.Fatal(err)
	}

	// A different result for the same action is a different claim about what
	// happened, not a duplicate.
	if _, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "已提交 c03", "", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("different result = %v, want ErrActionConflict", err)
	}
	// A completed action can never be reset to unknown, in any wording.
	if _, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionUnknown, "结果其实未知", "", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("completed->unknown = %v, want ErrActionConflict", err)
	}
	// A run that no longer owns the direction cannot restate anything.
	if _, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "已提交 c02", "", "r_someone_else"); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("foreign run = %v, want ErrActionConflict", err)
	}
}

// The reconciliation path keeps its CAS: unknown can still become completed,
// but only through the current version, and its own resubmission is a no-op.
func TestActionReconciliationKeepsItsVersionCheck(t *testing.T) {
	s, in, run := actionFixture(t)
	a, _, err := s.BeginAction(in.ID, run.ID, "upload", "上传结果", "")
	if err != nil {
		t.Fatal(err)
	}
	unknown, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionUnknown, "结果未知，待核对", "", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := s.ResolveActionIdempotent(a.ID, unknown.Version, model.ActionUnknown, "结果未知，待核对", "", run.ID); err != nil || !duplicate {
		t.Fatalf("unknown resubmission: duplicate=%v err=%v", duplicate, err)
	}
	// Stale version for a real state change: still refused.
	if _, _, err := s.ResolveActionIdempotent(a.ID, a.Version, model.ActionCompleted, "回执确认上传成功", "r:9", run.ID); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("stale reconciliation = %v, want ErrActionConflict", err)
	}
	out, duplicate, err := s.ResolveActionIdempotent(a.ID, unknown.Version, model.ActionCompleted, "回执确认上传成功", "r:9", run.ID)
	if err != nil || duplicate || out.State != model.ActionCompleted {
		t.Fatalf("reconciliation: %+v duplicate=%v err=%v", out, duplicate, err)
	}
}
