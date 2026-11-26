package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// A delete used to give every live run a bounded 90-second evidence window
// before canceling it. Measured on a fresh project, the commander round that
// starts on attach made DELETE take about 90 seconds. The operator asked for the
// engagement to be gone, so a delete now cancels immediately.
//
// The fixture is a purpose run registered through the same production tracker a
// commander/report run uses (Server.TrackPurposeRun), whose cancel func closes a
// channel instead of waiting for a signal: like a real round, it would never end
// on its own. The closest a cmd/saker test can get to a live run without
// production test hooks — the ledger row, executor and model call are the parts
// left out, and none of them is what the elapsed-time assertion measures.
func TestDeleteCancelsLiveRunImmediately(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("cancel-me", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	k := h.attach(p)
	if k == nil {
		t.Fatal("attach returned no runtime")
	}
	t.Cleanup(k.cancel)

	canceled := make(chan struct{})
	settled := make(chan string, 1)
	release := k.srv.TrackPurposeRun("r_waits_forever", func() { close(canceled) })
	go func() {
		<-canceled
		// A run settles at its own pace after the cancel; this fixture
		// unregisters the moment it is canceled, the fastest possible settle.
		settled <- release()
	}()

	rec := httptest.NewRecorder()
	started := time.Now()
	h.deleteProject(rec, httptest.NewRequest(http.MethodDelete,
		"/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	elapsed := time.Since(started)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	// The old path waited the 90s drain window before canceling, so this bound
	// is the regression assertion: a delete that starts waiting for evidence
	// fails it instead of passing slowly.
	if elapsed > 5*time.Second {
		t.Fatalf("delete took %s: a live run still gets an evidence window instead of an immediate cancel", elapsed)
	}
	select {
	case cause := <-settled:
		if cause != model.CancelCauseProjectStop {
			t.Fatalf("run settled with cause %q, want %q", cause, model.CancelCauseProjectStop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live run was never canceled: delete did not reach it")
	}
	if mgr.Get(p.ID) != nil {
		t.Fatal("project survived the delete")
	}
	if h.kit(p.ID) != nil {
		t.Fatal("a canceled runtime is still registered")
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatalf("deleted project directory still exists: %v", err)
	}
}

// The settle budget is a safety net, not a waiting window: a run that ignores its
// cancel must not let the delete remove a tree something is still writing into.
// The delete answers 409 and leaves the project usable (gate reopened, runtime
// and directory intact) so the operator can retry.
func TestDeleteKeepsProjectWhenARunIgnoresCancel(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("stuck", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	k := h.attach(p)
	if k == nil {
		t.Fatal("attach returned no runtime")
	}
	t.Cleanup(k.cancel)

	release := k.srv.TrackPurposeRun("r_ignores_cancel", func() {})
	t.Cleanup(func() { release() })

	rec := httptest.NewRecorder()
	h.deleteProject(rec, httptest.NewRequest(http.MethodDelete,
		"/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete with a run that ignores its cancel = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if mgr.Get(p.ID) != p {
		t.Fatal("the project was removed even though its run never settled")
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatalf("the project directory was touched: %v", err)
	}
	if k.srv.Lifecycle.Deleting() {
		t.Fatal("the delete gate stayed closed after the 409: the project would look live with dispatch refused")
	}
}
