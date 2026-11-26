package store

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

func TestFailedLedgerWritesRestoreWholeCommittedState(t *testing.T) {
	for _, failure := range []string{"BEFORE", "AFTER"} {
		t.Run(failure, func(t *testing.T) {
			s := open(t)
			parent, err := s.CreateIntent(model.Intent{Title: "durable"})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(s.Snapshot())
			rev := s.Revision()
			path := s.path
			if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_meta " + failure + " UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture failure'); END"); err != nil {
				t.Fatal(err)
			}
			hooks := 0
			s.OnFact(func(model.Fact, []string) { hooks++ })
			mutations := []func() error{
				func() error { _, e := s.CreateIntent(model.Intent{Title: "phantom"}); return e },
				func() error {
					_, e := s.UpdateIntent(parent.ID, func(in *model.Intent) { in.Title = "phantom" })
					return e
				},
				func() error { _, e := s.CreateIntent(model.Intent{Title: "phantom"}); return e },
				func() error { _, e := s.AddFact(model.Fact{IntentID: parent.ID, Title: "phantom"}); return e },
				func() error {
					_, _, e := s.AddMessage(model.Message{Text: "phantom", FromParticipantID: "a", ToParticipantID: "b"})
					return e
				},
				func() error { _, _, e := s.StartIntentRun(parent.ID, "local", "", model.Run{}); return e },
				func() error { _, e := s.BeginHandoff(parent.ID, 0); return e },
				func() error {
					_, _, e := s.CreateProposal(proposalFixture("pr_phantom"))
					return e
				},
			}
			for i, mutate := range mutations {
				if err := mutate(); err == nil {
					t.Fatalf("mutation %d did not fail", i)
				}
				after, _ := json.Marshal(s.Snapshot())
				if string(after) != string(before) || s.Revision() != rev {
					t.Fatalf("mutation %d survived failure", i)
				}
			}
			if hooks != 0 {
				t.Fatal("failed fact reached mirror hook")
			}
			s.path = path
			if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_meta"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIntent(model.Intent{Title: "next durable"}); err != nil {
				t.Fatal(err)
			}
			disk, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(disk.ListIntents()) != 2 || len(disk.Messages(10)) != 0 {
				t.Fatal("later write persisted phantom data")
			}
		})
	}
}

func TestHandoffHasOneOwnerThroughSaveReleaseAndClaim(t *testing.T) {
	s := open(t)
	in, _ := s.CreateIntent(model.Intent{Title: "handoff"})
	var wg sync.WaitGroup
	results := make(chan model.Intent, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; held, e := s.BeginHandoff(in.ID, 0); results <- held; errs <- e }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrHandoffConflict) {
			t.Fatal(err)
		}
	}
	owner := ""
	for held := range results {
		if held.HandoffOperation != "" {
			owner = held.HandoffOperation
		}
	}
	if success != 1 || owner == "" {
		t.Fatalf("owners=%d", success)
	}
	if _, err := s.SaveHandoff(in.ID, 0, model.Handoff{}, "loser"); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("loser save=%v", err)
	}
	if err := s.ReleaseHandoffHold(in.ID, "loser", "loser"); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("loser release=%v", err)
	}
	if _, _, err := s.StartIntentRun(in.ID, "local", "", model.Run{}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("unowned start=%v", err)
	}
	saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "new"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Hold {
		t.Fatal("loser released winner's hold")
	}
	if _, _, err := s.StartIntentRun(in.ID, "local", owner, model.Run{}); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetIntent(in.ID)
	if cur.HandoffOperation != "" || cur.Hold || cur.Attempts != 1 {
		t.Fatalf("claim=%+v", cur)
	}
}

func TestInterruptedHandoffRecoveryRequiresExplicitRetry(t *testing.T) {
	s := open(t)
	in, _ := s.CreateIntent(model.Intent{Title: "handoff"})
	if _, err := s.BeginHandoff(in.ID, 0); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RecoverHandoffs(); err != nil {
		t.Fatal(err)
	}
	cur, _ := reopened.GetIntent(in.ID)
	if cur.HandoffOperation != "" || !cur.Hold || cur.Status != model.StatusFailed {
		t.Fatalf("recovery=%+v", cur)
	}
}

// A row claimed but never finished is not evidence that the bytes left. The
// restart put it back on the queue with a [重投] marker, because a duplicate the
// model can recognise is better than a message no path will ever deliver.
func TestMessageDeliveryIsExclusiveAndRestartRequeues(t *testing.T) {
	s := open(t)
	m, _, _ := s.AddMessage(model.Message{FromParticipantID: "a", ToParticipantID: "b", Text: "hello"})
	if _, ok, err := s.BeginMessageDelivery(m.ID); err != nil || !ok {
		t.Fatalf("begin=%v %v", ok, err)
	}
	if _, ok, err := s.BeginMessageDelivery(m.ID); err != nil || ok {
		t.Fatalf("duplicate=%v %v", ok, err)
	}
	if n, err := s.SweepQueued(time.Now().Add(48*time.Hour), time.Hour, 1); err != nil || n != 0 {
		t.Fatalf("sweep=%d %v", n, err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RecoverMessageDeliveries(); err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.MessageByID(m.ID)
	if got.Status != model.MessageQueued || got.Redeliveries != 1 {
		t.Fatalf("recovery=%+v, want queued with one redelivery", got)
	}
	if pending := reopened.PendingFor("b"); len(pending) != 1 || pending[0].ID != m.ID {
		t.Fatalf("recovery queue=%+v", pending)
	}
}

func TestOldRunSettlementCannotConsumeHandoffWait(t *testing.T) {
	for _, terminal := range []string{model.RunDone, model.RunCanceled, model.RunFailed} {
		t.Run(terminal, func(t *testing.T) {
			s := open(t)
			in, _ := s.CreateIntent(model.Intent{Title: "handoff"})
			old, err := s.AddRun(model.Run{IntentID: in.ID, Node: "local"})
			if err != nil {
				t.Fatal(err)
			}
			archiveRun(t, s, old.ID)
			_, _ = s.UpdateIntent(in.ID, func(in *model.Intent) { in.Status = model.StatusRunning; in.RunID = old.ID })
			req, err := s.RegisterOperatorWait(in.ID, old.ID, model.OperatorWait{Request: "login"})
			if err != nil {
				t.Fatal(err)
			}
			held, err := s.BeginHandoff(in.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.SettleIntentForRun(in.ID, old.ID, terminal, model.CancelCauseHandoff); err != nil {
				t.Fatal(err)
			}
			if err := s.ResolveOperatorWait(in.ID, req.ID, "anonymous only", model.OperatorUnavailable); err != nil {
				t.Fatal(err)
			}
			saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{}, held.HandoffOperation)
			if err != nil {
				t.Fatal(err)
			}
			if !saved.Hold || saved.Wait == nil || saved.Handoff.Wait.Operator.Response != "anonymous only" {
				t.Fatalf("saved=%+v", saved)
			}
			// Neither the returned immutable handoff nor its nested receipt can
			// mutate the durable snapshot while the new run consumes the wait.
			saved.Handoff.Wait.Operator.Response = "mutated"
			cur, _ := s.GetIntent(in.ID)
			if cur.Handoff.Wait.Operator.Response != "anonymous only" {
				t.Fatal("snapshot alias")
			}
		})
	}
}
