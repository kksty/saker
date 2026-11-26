package lifecycle

import (
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

func TestIdleGuardDoesNotHoldOrCancelReplacementForStaleRun(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "replaced")
	_, old, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateRun(old.ID, func(r *model.Run) { r.StartedAt = time.Now().Add(-time.Hour) }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateIntent(in.ID, func(i *model.Intent) { i.RunID = "replacement" }); err != nil {
		t.Fatal(err)
	}
	canceled := false
	h.svc.cancels[old.ID] = func() { canceled = true }
	h.svc.ReapIdle(time.Minute)
	got, _ := h.store.GetIntent(in.ID)
	if got.Hold || got.RunID != "replacement" || canceled {
		t.Fatalf("old idle run affected replacement: hold=%v run=%s oldCanceled=%v", got.Hold, got.RunID, canceled)
	}
}

func TestRecoveryDoesNotApplyStaleSnapshotAfterRunOrStatusChanges(t *testing.T) {
	for _, change := range []string{"replacement", "status"} {
		for _, uncount := range []bool{true, false} {
			t.Run(change+"/"+map[bool]string{true: "running", false: "claim"}[uncount], func(t *testing.T) {
				h := newHarness(t)
				in := h.createIntent(t, "recovery snapshot")
				observed, _, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
				if err != nil {
					t.Fatal(err)
				}
				if !uncount {
					observed, err = h.store.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusClaimed })
					if err != nil {
						t.Fatal(err)
					}
				}
				current, err := h.store.UpdateIntent(in.ID, func(i *model.Intent) {
					if change == "replacement" {
						i.RunID = "replacement"
					} else {
						i.Status = model.StatusParked
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, applied, err := h.svc.recoverInterruptedIntent(observed, uncount); err != nil || applied {
					t.Fatalf("stale recovery applied: %v %v", applied, err)
				}
				got, _ := h.store.GetIntent(in.ID)
				if got.RunID != current.RunID || got.Status != current.Status || got.Attempts != current.Attempts || got.ClaimedBy != current.ClaimedBy {
					t.Fatalf("stale recovery changed current owner: %+v", got)
				}
			})
		}
	}
}

// The idle guard is the real automatic stop: it must name itself as the holder,
// not leave an unknown-holder hold that the owner cannot restart.
func TestIdleGuardStampsItsHoldActor(t *testing.T) {
	h := newHarness(t)
	in := h.createIntent(t, "stuck")
	_, run, err := h.store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateRun(run.ID, func(r *model.Run) { r.StartedAt = time.Now().Add(-time.Hour) }); err != nil {
		t.Fatal(err)
	}
	h.svc.cancels[run.ID] = func() {}
	h.svc.ReapIdle(time.Minute)
	got, _ := h.store.GetIntent(in.ID)
	if !got.Hold || got.HeldBy != model.HoldActorWatchdog || got.HoldReason == "" {
		t.Fatalf("idle guard hold = hold=%v held_by=%q reason=%q", got.Hold, got.HeldBy, got.HoldReason)
	}
}
