package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/verifier"
)

// The project settings write moves the live switch in the same call as the
// manifest, with the project page's own semantics: closing cancels what is
// running and records why, opening re-arms the queue. The assertions are the run
// row and the pause gate, not the boolean the caller passed in.
func TestSetReviewsOverrideClosesLiveReviews(t *testing.T) {
	s, p := newBindingServer(t)
	block := make(chan struct{})
	s.Verifier = verifier.New(p.Store, &batchExec{block: block}, s.CaseRoot)
	s.Verifier.Track = s.TrackReviewRun
	s.Timeout = 0
	mux := http.NewServeMux()
	s.Register(mux)
	f := addVerifyFact(t, p.Store, "vuln", "unverified")

	if code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify"); code != http.StatusAccepted {
		t.Fatalf("verify = %d %v, want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool { return s.Verifier.Busy(f.ID) })
	waitReviewTracked(t, s)

	closed := true
	canceled, err := s.SetReviewsOverride(&closed, false)
	if err != nil {
		t.Fatal(err)
	}
	if canceled != 1 {
		t.Fatalf("canceled = %d, want the live review", canceled)
	}
	if !s.ReviewsDisabled() || !s.ReviewsPaused() {
		t.Fatalf("switch = disabled %v paused %v; want both closed", s.ReviewsDisabled(), s.ReviewsPaused())
	}
	waitFor(t, 5*time.Second, func() bool { return !s.Verifier.Busy(f.ID) })
	run := lastVerifierRun(t, s)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseReviewPause {
		t.Fatalf("review run = status %q cause %q, want canceled/%s", run.Status, run.CancelCause, model.CancelCauseReviewPause)
	}
	if got, _ := p.Store.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("a canceled review changed the fact to %q", got.Status)
	}
	if v, ok := p.ReviewsDisabledOf(); !ok || !v {
		t.Fatalf("manifest switch = %v / stored %v; want a stored true", v, ok)
	}

	// Clearing the override follows the instance default, which is open here:
	// the runtime has to move with it, not wait for a restart.
	canceled, err = s.SetReviewsOverride(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if canceled != 0 {
		t.Fatalf("clearing canceled %d runs; opening a closed switch cancels nothing", canceled)
	}
	if s.ReviewsDisabled() || s.ReviewsPaused() {
		t.Fatalf("switch = disabled %v paused %v; want both open", s.ReviewsDisabled(), s.ReviewsPaused())
	}
	if _, ok := p.ReviewsDisabledOf(); ok {
		t.Fatal("clearing the override left a stored value in the manifest")
	}
	status := getVerifierStatus(t, mux)
	if status["enabled"] != true || status["disabled"] != false {
		t.Fatalf("status after clearing = %v, want reviews enabled", status)
	}
}

// Clearing the override while the instance default is closed lands on closed:
// "follow the instance" is a live value, not the last one the project stored.
func TestSetReviewsOverrideClearTakesTheClosedDefault(t *testing.T) {
	s, p := newBindingServer(t)
	open := false
	if _, err := s.SetReviewsOverride(&open, false); err != nil {
		t.Fatal(err)
	}
	if s.ReviewsDisabled() {
		t.Fatal("fixture: an explicit open override should have opened the switch")
	}
	canceled, err := s.SetReviewsOverride(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if canceled != 0 || !s.ReviewsDisabled() || !s.ReviewsPaused() {
		t.Fatalf("clear to a closed default = canceled %d disabled %v paused %v", canceled, s.ReviewsDisabled(), s.ReviewsPaused())
	}
	if _, ok := p.ReviewsDisabledOf(); ok {
		t.Fatal("a stored value survived the clear")
	}
}

// A server without a project (the instance surfaces build one) must refuse rather
// than pretend it persisted a per-project switch.
func TestSetReviewsOverrideWithoutProject(t *testing.T) {
	s := &Server{}
	closed := true
	if _, err := s.SetReviewsOverride(&closed, false); err == nil {
		t.Fatal("SetReviewsOverride on a project-less server returned no error")
	}
}
