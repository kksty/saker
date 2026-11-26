package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/vulnlib"
)

// --no-verify is the instance boot value; the operator's per-project switch wins.
// A project whose reviews are explicitly open must have its durable candidate set
// armed while it is attached, not on the watchdog's later tick: the assertion is a
// review run row, because a run is what an operator pays for and a switch's
// boolean would not prove the queue moved.
//
// The 10s bound is deliberately shorter than the watchdog interval (30s): waiting
// past that tick would let a boot path that read h.cfg.NoVerify instead of the
// effective value pass on the watchdog's re-arm.
func TestBootNoVerifyStillSchedulesReviewsWhenTheProjectSaysOpen(t *testing.T) {
	// Best-effort removal: after the assertion the review pump and the verifier
	// run still own goroutines, and t.TempDir's fatal cleanup turns that overlap
	// into a "directory not empty" failure that has nothing to do with the
	// behaviour under test.
	dir, err := os.MkdirTemp("", "saker-review-schedule-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	// Persisted: reviews open. Boot: --no-verify. The persisted value must win.
	if err := p.SetReviewsDisabled(false); err != nil {
		t.Fatal(err)
	}

	// A real candidate: an unverified vuln fact produced by one direction and
	// cited as a necessary premise by another. Production runs RequiredOnly, so
	// cited premises are exactly the eligible set; an unreferenced fact would
	// prove nothing. The citing direction must be a different one — the ledger
	// refuses a fact that feeds its own producing direction (a cycle).
	origin, err := p.Store.CreateIntent(model.Intent{Title: "枚举越权接口", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// A reviewable finding cites the material a reviewer replays: the store
	// refuses a pass on a vuln with no evidence, so a candidate without one would
	// only buy a refusal.
	proof, err := p.Store.AddEvidence(model.Evidence{IntentID: origin.ID, URI: origin.ID + "/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "越权读取他人订单", IntentID: origin.ID, EvidenceIDs: []string{proof.ID}})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := p.Store.CreateIntent(model.Intent{Title: "复核越权读取", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(consumer.ID, func(i *model.Intent) {
		i.FromFactIDs = []string{fact.ID}
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lib, err := vulnlib.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{
		cfg:           hubConfig{Nodes: &node.Registry{}, NoVerify: true, NoDecider: true},
		mgr:           mgr,
		ctx:           ctx,
		vulns:         lib,
		kits:          map[string]*kit{},
		auxiliaryGate: executor.NewGate(1),
	}
	h.start()

	k := h.kits[p.ID]
	if k == nil {
		t.Fatal("boot did not attach the project")
	}
	if disabled, ok := p.ReviewsDisabledOf(); !ok || disabled {
		t.Fatalf("fixture: persisted switch = disabled %v (set %v), want an explicit open", disabled, ok)
	}
	// Deliberately no assertion on k.srv.ReviewsDisabled(): a switch's boolean is
	// not what the operator buys. The run row below is the observation.

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if id := verifierRunID(p.Store); id != "" {
			cancel()
			waitRunSettled(p.Store, id)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no review run started within 10s of attach for candidate %s: the queue was left unarmed by the boot path", fact.ID)
}

// verifierRunID finds the review run the attach path started. The row is written
// before the verifier has any model output, so its presence — not its verdict —
// is what proves scheduling happened.
func verifierRunID(st *store.Store) string {
	for _, run := range st.Runs() {
		if run.Purpose == "verifier" {
			return run.ID
		}
	}
	return ""
}

// waitRunSettled bounds the post-assertion wait, so the review run is not still
// writing its own state while the temp directory is removed. A run that never
// settles inside the bound is not a failure: the assertion already passed.
func waitRunSettled(st *store.Store, id string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, ok := st.GetRun(id)
		if !ok || run.Status != model.RunRunning {
			// One quiet window for the settle path's own follow-up writes.
			time.Sleep(100 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
