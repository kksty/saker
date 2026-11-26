package decider

import (
	"testing"

	"github.com/kksty/saker/internal/model"
)

// idleLedger builds the canonical idle shape: an open goal after a settled
// direction, i.e. "work was attempted and nothing is in flight".
func idleLedger(t *testing.T) *Runner {
	t.Helper()
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	return NewRunner(st, nil, "")
}

// The idle latch names one pending row, not the idle state. When the operator
// resolves that row while the project stays idle, the fallback and its one
// forced replan must come back; otherwise the project sits in a silent idle with
// an empty operator inbox and the card still says "waiting for the operator".
func TestIdleFallbackRearmsAfterItsRequestIsResolved(t *testing.T) {
	r := idleLedger(t)
	st := r.Store
	r.refreshIdleOperatorRequest()
	first := st.PendingOperatorRequests()
	if len(first) != 1 {
		t.Fatalf("first fallback = %+v", first)
	}
	<-r.wake // the edge-triggered replan
	if _, err := st.ResolveOperatorRequest(first[0].ID, "操作员已处理"); err != nil {
		t.Fatal(err)
	}
	r.refreshIdleOperatorRequest()
	pending := st.PendingOperatorRequests()
	if len(pending) != 1 {
		t.Fatalf("no fallback after its row was resolved: %+v", pending)
	}
	if pending[0].ID == first[0].ID {
		t.Fatalf("the resolved row %s came back", pending[0].ID)
	}
	if got := len(r.wake); got != 1 {
		t.Fatalf("replan wake after re-arming = %d, want 1", got)
	}
	// The new row is latched: the next heartbeat must not duplicate it.
	r.refreshIdleOperatorRequest()
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("the re-armed fallback was duplicated: %d", got)
	}
}

// The watchdog round is owed after repeated no-action rounds. It must not be
// gated on the pending row still being generic: a row the operator already
// detailed (or a round that already filled in) is still a row, and the old
// generic check silenced the last automatic recovery.
func TestIdleEscalationDoesNotNeedAGenericPendingRequest(t *testing.T) {
	r := idleLedger(t)
	st := r.Store
	r.mu.Lock()
	r.idleRequested = true
	r.noActionRounds = 3
	r.mu.Unlock()
	if _, _, err := st.CreateOperatorRequest("动作", "资产", "影响", "授权", model.OperatorRequestFromCommander); err != nil {
		t.Fatal(err)
	}
	r.escalateIdleReplan()
	if got := len(r.wake); got != 1 {
		t.Fatalf("watchdog wake with a detailed pending row = %d, want 1", got)
	}
}

// If no row survives (the operator resolved it and no heartbeat ran yet), the
// escalation round must still fire and must file a fresh row for the operator
// first: the commander can never be woken to re-plan against an empty inbox.
func TestIdleEscalationFilesAFallbackWhenNoneSurvives(t *testing.T) {
	r := idleLedger(t)
	st := r.Store
	r.mu.Lock()
	r.idleRequested = true
	r.noActionRounds = 3
	r.mu.Unlock()
	r.escalateIdleReplan()
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("pending fallback after escalation = %d, want 1", got)
	}
	if got := len(r.wake); got != 1 {
		t.Fatalf("watchdog wake without a surviving row = %d, want 1", got)
	}
}
