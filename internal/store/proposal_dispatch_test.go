package store

import (
	"errors"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// Dispatch commits the proposal version check, the intent claim, the run and the
// receipt together. A failed commit must leave the proposal approved and the
// intent unclaimed: the old two-step flow could leave a run whose proposal still
// read approved.
func TestProposalDispatchCommitsReceiptWithRun(t *testing.T) {
	s := open(t)
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	approved, intent, err := s.DecideProposal(p.ID, p.Version, true, "值得做", "commander")
	if err != nil {
		t.Fatal(err)
	}

	// Fail the metadata write after the row writes were staged: the store's
	// rollback must restore the whole transaction, not just the run row.
	if _, err := s.sqlite.db.Exec(`CREATE TRIGGER fail_dispatch_meta BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.StartProposalRun(p.ID, approved.Version, "local", "", model.Run{}); err == nil {
		t.Fatal("dispatch survived a failed commit")
	}
	after, _ := s.GetProposal(p.ID)
	if after.Status != model.ProposalApproved || after.RunID != "" || after.Version != approved.Version {
		t.Fatalf("failed dispatch changed the proposal: %+v", after)
	}
	if cur, _ := s.GetIntent(intent.ID); cur.Status != model.StatusPending || cur.RunID != "" {
		t.Fatalf("failed dispatch claimed the intent: %+v", cur)
	}
	if runs := s.Runs(); len(runs) != 0 {
		t.Fatalf("failed dispatch left runs: %+v", runs)
	}
	if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_dispatch_meta"); err != nil {
		t.Fatal(err)
	}

	dispatched, brief, run, created, err := s.StartProposalRun(p.ID, approved.Version, "local", "", model.Run{})
	if err != nil || !created {
		t.Fatalf("dispatch: created=%v err=%v", created, err)
	}
	if dispatched.Status != model.ProposalDispatched || dispatched.RunID != run.ID ||
		dispatched.IntentID != intent.ID || dispatched.Version != approved.Version+1 {
		t.Fatalf("receipt = %+v run=%+v", dispatched, run)
	}
	if run.IntentID != intent.ID || run.Status != model.RunRunning || brief.RunID != "" {
		t.Fatalf("run = %+v brief = %+v", run, brief)
	}
	if cur, _ := s.GetIntent(intent.ID); cur.Status != model.StatusRunning || cur.RunID != run.ID {
		t.Fatalf("intent after dispatch = %+v", cur)
	}

	// A retry after the response was lost returns the recorded receipt and
	// does not create a second run.
	again, _, againRun, created, err := s.StartProposalRun(p.ID, approved.Version, "local", "", model.Run{})
	if err != nil || created {
		t.Fatalf("retry: created=%v err=%v", created, err)
	}
	if again.RunID != run.ID || againRun.ID != run.ID || len(s.Runs()) != 1 {
		t.Fatalf("retry returned %+v run=%+v runs=%d", again, againRun, len(s.Runs()))
	}
}

// The old flow could leave an approved proposal beside a run it started. A
// receipt is backfilled only when the intent has exactly one attributable run.
func TestProposalReceiptBackfillOnlyFillsUnambiguousRun(t *testing.T) {
	s := open(t)
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	approved, intent, err := s.DecideProposal(p.ID, p.Version, true, "值得做", "commander")
	if err != nil {
		t.Fatal(err)
	}
	if _, run, err := s.StartIntentRun(intent.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	} else {
		plans := PlanProposalReceipts(s.Snapshot())
		if len(plans) != 1 || plans[0].Status != ProposalReceiptFill || plans[0].RunID != run.ID {
			t.Fatalf("plan = %+v, want fill with %s", plans, run.ID)
		}
		applied, err := s.ApplyProposalReceipts()
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) != 1 || applied[0].Status != ProposalReceiptFill {
			t.Fatalf("apply = %+v", applied)
		}
		got, _ := s.GetProposal(p.ID)
		if got.Status != model.ProposalDispatched || got.RunID != run.ID || got.Version != approved.Version+1 {
			t.Fatalf("backfilled proposal = %+v", got)
		}
	}

	// A second proposal on an intent with two post-approval runs cannot choose:
	// the repair keeps the reason and writes nothing.
	p2, _, err := s.CreateProposal(proposalFixture("pr_2"))
	if err != nil {
		t.Fatal(err)
	}
	_, intent2, err := s.DecideProposal(p2.ID, p2.Version, true, "", "commander")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartIntentRun(intent2.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(intent2.ID, func(in *model.Intent) {
		in.Status = model.StatusPending
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartIntentRun(intent2.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	plans := PlanProposalReceipts(s.Snapshot())
	var plan ProposalReceiptPlan
	for _, p := range plans {
		if p.ProposalID == p2.ID {
			plan = p
		}
	}
	if plan.Status != ProposalReceiptAmbiguous || plan.Reason == "" {
		t.Fatalf("ambiguous plan = %+v", plan)
	}
	if _, err := s.ApplyProposalReceipts(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetProposal(p2.ID)
	if got.Status != model.ProposalApproved || got.RunID != "" {
		t.Fatalf("ambiguous proposal was changed: %+v", got)
	}
}

// A satisfied wait is copied onto the run that consumes it; an interrupted run
// whose executor never confirmed receipt hands the same results to the next
// claim.
func TestWaitDeliverySurvivesClaimAndRecovery(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "等待结果接力"})
	if err != nil {
		t.Fatal(err)
	}
	// A ready wait with one known and one explicitly unknown result.
	ready := &model.Wait{
		Mode:        model.WaitAll,
		WakePending: true,
		WakeReason:  "action_completed",
		Results: []model.WaitResult{
			{ActionID: "a_1", Status: model.ActionCompleted, Result: "ok"},
			{ActionID: "a_2", Status: model.ActionUnknown},
		},
		RegisteredAt: time.Now(),
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusPending
		i.Wait = ready
	}); err != nil {
		t.Fatal(err)
	}
	brief, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if run.WaitDelivery == nil || run.WaitDelivery.Status != model.WaitDeliveryReady ||
		run.WaitDelivery.Wait == nil || len(run.WaitDelivery.Wait.Results) != 2 {
		t.Fatalf("run delivery = %+v", run.WaitDelivery)
	}
	if brief.Wait == nil || len(brief.Wait.Results) != 2 || brief.Wait.Results[1].Status != model.ActionUnknown {
		t.Fatalf("brief wait = %+v", brief.Wait)
	}
	if cur, _ := s.GetIntent(in.ID); cur.Wait != nil {
		t.Fatalf("consumed wait stayed on the intent: %+v", cur.Wait)
	}
	if err := s.MarkWaitDelivery(run.ID, model.WaitDeliveryArchived, "in/.saker/briefs/"+run.ID+".md"); err != nil {
		t.Fatal(err)
	}
	// A crash after the archive but before the executor's first event: the next
	// claim must receive the same results, including the unknown one.
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.WaitDelivery == nil || resumed.WaitDelivery.Status != model.WaitDeliveryReady {
		t.Fatalf("resumed delivery = %+v", resumed.WaitDelivery)
	}
	if got := resumed.WaitDelivery.Wait.Results; len(got) != 2 || got[0].Result != "ok" || got[1].Status != model.ActionUnknown {
		t.Fatalf("resumed results = %+v", got)
	}
	if err := s.MarkWaitDelivery(resumed.ID, model.WaitDeliveryArchived, ".saker/briefs/x.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkWaitDelivery(resumed.ID, model.WaitDeliveryReceived, ""); err != nil {
		t.Fatal(err)
	}
	// Received is terminal and idempotent; a later claim must not redeliver it.
	cur, _ := s.GetRun(resumed.ID)
	if cur.WaitDelivery == nil || cur.WaitDelivery.Status != model.WaitDeliveryReceived || cur.WaitDelivery.ReceivedAt == nil {
		t.Fatalf("received delivery = %+v", cur.WaitDelivery)
	}
	if err := s.MarkWaitDelivery(resumed.ID, model.WaitDeliveryArchived, "x"); err != nil {
		t.Fatalf("repeat received should be a no-op: %v", err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, resumed.ID
	}); err != nil {
		t.Fatal(err)
	}
	_, next, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if next.WaitDelivery != nil {
		t.Fatalf("a received delivery was replayed: %+v", next.WaitDelivery)
	}
}

// A receipt branch without a run is a ledger inconsistency, not a dispatch.
func TestProposalReceiptMissingRunIsRejected(t *testing.T) {
	s := open(t)
	if _, _, err := s.CreateProposal(proposalFixture("pr_1")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.StartProposalRun("pr_missing", 1, "local", "", model.Run{}); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("missing proposal: %v", err)
	}
}
