package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
)

// A project must not be delivered without its endpoint. Creation used to log a
// failed AddGoal and still answer 201, so the console showed a project with
// nothing to judge "done" against (the endpoint is a required project document).
// The commit now runs before attach and any failure removes what this request
// created.
func TestCreateProjectCommitFailureRollsBack(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("half", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("goal write failed (injected)")
	err = h.commitCreatedProject(p, func() error { return boom })
	if err == nil {
		t.Fatal("a failed creation commit reported success")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap the commit failure", err)
	}
	if mgr.Get(p.ID) != nil {
		t.Fatal("a project whose creation commit failed is still in the manager")
	}
	if _, statErr := os.Stat(p.Dir); !os.IsNotExist(statErr) {
		t.Fatalf("rolled-back project left its directory behind: %v", statErr)
	}
	if len(h.kits) != 0 {
		t.Fatalf("a failed create attached a runtime: %v", h.kits)
	}
}

// The real failure the rollback exists for: the ledger rejects the goal (here
// the store is closed, which is what a disk/database failure looks like to
// AddGoal). The error must name the goal and reach the caller instead of being
// logged and dropped.
func TestCreateCommitSurfacesGoalWriteFailure(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("no-ledger", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Close(); err != nil {
		t.Fatal(err)
	}
	err = h.applyCreateCommit(p, createProjectRequest{Goal: "拿到边界主机"}, nil)
	if err == nil {
		t.Fatal("a goal write against a closed ledger succeeded")
	}
	if !strings.Contains(err.Error(), "goal") {
		t.Fatalf("error %q does not name the goal write", err.Error())
	}
}

// Positive control for the same path: a normal create writes the goal through
// the ledger before the project is attached, with the operator as author, and
// the response is still 201.
func TestCreateProjectWritesEndpointGoal(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example",
		"goal":"拿到边界主机","quantified_target":"2 台"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	p := mgr.Get(out["id"].(string))
	if p == nil {
		t.Fatalf("project %v is not in the manager", out["id"])
	}
	goal, _, ok := p.Store.CurrentGoal()
	if !ok {
		t.Fatal("created project has no endpoint goal")
	}
	if goal.Statement != "拿到边界主机" || goal.QuantifiedTarget != "2 台" {
		t.Fatalf("goal = %+v, want the operator's statement and target", goal)
	}
	if goal.UpdatedBy != collab.OperatorID {
		t.Fatalf("goal author = %q, want the operator", goal.UpdatedBy)
	}
	if h.kits[p.ID] == nil {
		t.Fatal("a fully committed project was not attached")
	}
}

// A creation that fails before the commit still must not register anything.
// This pins the shared rollback helper against a manager error path too: the
// project it is handed is the only one it may touch.
func TestCreateProjectCommitRollbackLeavesOtherProjectsAlone(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	keep, err := mgr.Create("keep", "keep.example")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keep.Store.Close() })
	half, err := mgr.Create("half", "half.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.commitCreatedProject(half, func() error { return errors.New("boom") }); err == nil {
		t.Fatal("a failed commit reported success")
	}
	if mgr.Get(keep.ID) != keep {
		t.Fatal("rolling back one create removed an unrelated project")
	}
	if _, err := os.Stat(keep.Dir); err != nil {
		t.Fatalf("rolling back one create touched another project's directory: %v", err)
	}
	if mgr.Get(half.ID) != nil {
		t.Fatal("the rolled-back project is still registered")
	}
}
