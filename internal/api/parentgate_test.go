package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A retry must be refused before it CASes the row when the direction's live
// subtree still has work: the failed run dropped its wait, so the descendants'
// results have nowhere to land, and the retry would put the parent back on the
// same ground as its own chain. The early refusal also keeps the row failed
// instead of leaving it pending+Hold after a start that the gate would refuse.
func TestRetryRefusesDirectionWithRunningDescendants(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	parent, err := st.CreateIntent(model.Intent{Title: "父"})
	if err != nil {
		t.Fatal(err)
	}
	middle, err := st.CreateIntent(model.Intent{Title: "中间"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := st.CreateIntent(model.Intent{Title: "孙"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusFailed }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(middle.ID, func(i *model.Intent) {
		i.ParentID = parent.ID
		i.Status = model.StatusDone
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(leaf.ID, func(i *model.Intent) {
		i.ParentID = middle.ID
		i.Status = model.StatusRunning
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.retryIntentRun(parent.ID, "", false); err == nil || !strings.Contains(err.Error(), "子意图") {
		t.Fatalf("retry with a running grandchild = %v, want a refusal naming 子意图", err)
	}
	kept, _ := st.GetIntent(parent.ID)
	if kept.Status != model.StatusFailed {
		t.Fatalf("parent status = %q, want failed: the refusal must not leave it pending", kept.Status)
	}
}

// The same rule holds for continue and reopen: both start a run, so a live
// subtree refuses them too.
func TestContinueAndReopenRefuseLiveDescendants(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	parent, err := st.CreateIntent(model.Intent{Title: "父"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.CreateIntent(model.Intent{Title: "子"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(child.ID, func(i *model.Intent) {
		i.ParentID = parent.ID
		i.Status = model.StatusRunning
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.continueIntentRun(parent.ID, ""); err == nil || !strings.Contains(err.Error(), "子意图") {
		t.Fatalf("continue with a running child = %v, want a refusal naming 子意图", err)
	}
	if _, err := st.UpdateIntent(parent.ID, func(i *model.Intent) { i.Status = model.StatusParked }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenIntent(parent.ID); err == nil || !strings.Contains(err.Error(), "子意图") {
		t.Fatalf("reopen with a running child = %v, want a refusal naming 子意图", err)
	}
}
