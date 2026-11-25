package lifecycle

import (
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func TestWatchdogPreservesActionWaitAndIgnoresReplacedRun(t *testing.T) {
	h := newHarness(t)
	h.paused.Store(true)
	p := h.createIntent(t, "parent")
	_, old, err := h.store.StartIntentRun(p.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	action, _, err := h.store.BeginAction(p.ID, old.ID, "recovery-test", "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.RegisterWait(p.ID, old.ID, model.Wait{Actions: []string{action.ID}}); err != nil {
		t.Fatal(err)
	}
	_, _ = h.store.UpdateRun(old.ID, func(r *model.Run) { r.StartedAt = time.Now().Add(-3 * time.Minute) })
	h.svc.ReapOrphans()
	got, _ := h.store.GetIntent(p.ID)
	if got.Status != model.StatusWaiting || got.Wait == nil || len(got.Wait.Actions) != 1 {
		t.Fatal("watchdog lost explicit wait")
	}
	// Another stale run row must not roll back the current holder.
	_, _ = h.store.UpdateRun(old.ID, func(r *model.Run) { r.Status = model.RunRunning })
	_, _ = h.store.UpdateIntent(p.ID, func(i *model.Intent) { i.Status = model.StatusRunning; i.RunID = "replacement" })
	h.svc.ReapOrphans()
	got, _ = h.store.GetIntent(p.ID)
	if got.Status != model.StatusRunning || got.RunID != "replacement" {
		t.Fatal("stale orphan mutated replacement")
	}
}

func TestCrashBetweenWaitRegistrationAndYieldPreservesWait(t *testing.T) {
	h := newHarness(t)
	h.paused.Store(true)
	p := h.createIntent(t, "parent")
	_, run, err := h.store.StartIntentRun(p.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	action, _, err := h.store.BeginAction(p.ID, run.ID, "recovery-test", "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.RegisterWait(p.ID, run.ID, model.Wait{Actions: []string{action.ID}}); err != nil {
		t.Fatal(err)
	}
	h.svc.Recover()
	got, _ := h.store.GetIntent(p.ID)
	if got.Status != model.StatusWaiting || got.Wait == nil || len(got.Wait.Actions) != 1 || got.Wait.Actions[0] != action.ID {
		t.Fatalf("recovery lost persisted wait: %+v", got)
	}
	if len(h.store.Runs()) != 1 {
		t.Fatal("recovery replayed work while paused")
	}
}

func TestCrashRequeuesUnconfirmedCommanderInputButNotUnknownOrAcknowledged(t *testing.T) {
	h := newHarness(t)
	run, _ := h.store.AddRun(model.Run{Purpose: "commander"})
	now := time.Now()
	a, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "request", Status: model.MessageAccepted, DeliveredRunID: run.ID})
	u, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "uncertain", Status: model.MessageUnknown, DeliveredRunID: run.ID})
	c, _, _ := h.store.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "acked", Status: model.MessageConfirmed, AckedAt: &now, DeliveredRunID: run.ID})
	h.svc.Recover()
	got, _ := h.store.MessageByID(a.ID)
	unknown, _ := h.store.MessageByID(u.ID)
	confirmed, _ := h.store.MessageByID(c.ID)
	if got.Status != model.MessageQueued || got.EventID != a.EventID || unknown.Status != model.MessageUnknown || confirmed.Status != model.MessageConfirmed {
		t.Fatal("restart lost input identity or replayed uncertain work")
	}
}
