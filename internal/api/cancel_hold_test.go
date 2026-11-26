package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A run-level cancel (the API's "kill this run") used to settle the intent back
// to pending, and the pump immediately started a replacement run. An explicit
// stop must hold the direction: only retry/run clears it.
func TestCancelRunHoldsIntentAndPreventsRedispatch(t *testing.T) {
	s, _, in := newSteerServer(t)
	live, _ := s.Store.GetIntent(in.ID)
	if err := s.CancelRun(live.RunID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	})
	if runs := s.Store.Runs(); len(runs) != 1 {
		t.Fatalf("cancel produced %d runs, want 1", len(runs))
	}
	// A scheduler tick must not resurrect the canceled direction.
	s.pump()
	time.Sleep(200 * time.Millisecond)
	if runs := s.Store.Runs(); len(runs) != 1 {
		t.Fatalf("pump re-dispatched a canceled direction: %d runs", len(runs))
	}
	// Retry is the explicit way back: it clears Hold and starts exactly one run.
	runID, err := s.RetryIntent(in.ID, "取消后显式重试", false)
	if err != nil || runID == "" {
		t.Fatalf("retry = %q, %v", runID, err)
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Hold || got.Status != model.StatusRunning && got.Status != model.StatusClaimed {
		t.Fatalf("intent after retry = %+v", got)
	}
	if runs := s.Store.Runs(); len(runs) != 2 {
		t.Fatalf("retry runs = %d, want 2", len(runs))
	}
	// Let the retried blocking run reach its cancel registration, then stop it
	// and wait for the settle so the case directory is not removed under an
	// open run log (Windows).
	waitFor(t, 5*time.Second, func() bool {
		return s.Lifecycle.RunActive(runID)
	})
	if err := s.CancelRun(runID); err != nil {
		t.Fatalf("cancel retried run: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		r, ok := s.Store.GetRun(runID)
		return ok && r.Status == model.RunCanceled
	})
	time.Sleep(150 * time.Millisecond)
}

// Canceling a child run while its parent waits: the child is held (not silently
// re-dispatched) and the parent's wait still settles so the parent can decide.
// A stop-loss reason carries the prefix exactly once. The API adds "止损：" for
// callers that only wrote why; a caller that already wrote it (the commander
// does) must not produce the old "止损：止损：…" duplication in exit_reason.
func TestCancelIntentPrefixesStopLossExactlyOnce(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{"止损：平台已完成，回收槽位", "止损：平台已完成，回收槽位"},
		{"平台已完成，回收槽位", "止损：平台已完成，回收槽位"},
	} {
		s, _, in := newSteerServer(t)
		live, _ := s.Store.GetIntent(in.ID)
		if _, err := s.CancelIntent(in.ID, tc.reason); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			r, ok := s.Store.GetRun(live.RunID)
			return ok && r.Status == model.RunCanceled
		})
		r, ok := s.Store.GetRun(live.RunID)
		if !ok {
			t.Fatalf("run %s missing", live.RunID)
		}
		if r.ExitReason != tc.want {
			t.Fatalf("reason %q: exit = %q, want %q", tc.reason, r.ExitReason, tc.want)
		}
		if r.CancelCause != model.CancelCauseStopLoss {
			t.Fatalf("cause = %q, want %q", r.CancelCause, model.CancelCauseStopLoss)
		}
	}
}

// A stop records who decided it. The graph/worker surfaces read HeldBy to
// answer "may this be restarted?", so an operator stop must not look like the
// commander's (or the reverse).
func TestCancelIntentRecordsTheHolder(t *testing.T) {
	for _, tc := range []struct{ actor, reason string }{
		{model.HoldActorCommander, "指挥官腾槽"},
		{model.HoldActorOperator, "操作员叫停"},
	} {
		s, _, in := newSteerServer(t)
		if _, err := s.CancelIntentBy(in.ID, tc.reason, tc.actor); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			got, ok := s.Store.GetIntent(in.ID)
			return ok && got.Hold
		})
		got, _ := s.Store.GetIntent(in.ID)
		if got.HeldBy != tc.actor || got.HoldReason != tc.reason {
			t.Fatalf("%s stop = held_by %q reason %q", tc.actor, got.HeldBy, got.HoldReason)
		}
	}
}

// Terminal states keep their old answers: canceling a finished direction is a
// 409 (intent API) and killing a finished run is an error, not a state change.
func TestCancelTerminalStatesStayCompatible(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))

	in, err := st.CreateIntent(model.Intent{Title: "已经做完的意图"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelIntent(in.ID, "再取消一次"); err == nil || !strings.Contains(err.Error(), "已经是 done") {
		t.Fatalf("cancel done intent = %v", err)
	}
	w := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/cancel", map[string]string{"reason": "再取消一次"})
	if w.Code != http.StatusConflict {
		t.Fatalf("cancel done intent HTTP = %d %s", w.Code, w.Body.String())
	}

	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(run.ID); err == nil {
		t.Fatal("canceling a finished run must be an error")
	}
	if got, _ := st.GetIntent(in.ID); got.Status != model.StatusDone || got.Hold {
		t.Fatalf("terminal intent changed: %+v", got)
	}
}

// A commander retry must not silently undo an operator stop. The operator can
// always retry; the commander gets a machine-readable refusal instead.
func TestCommanderRetryDoesNotOverrideOperatorHold(t *testing.T) {
	s, _, in := newSteerServer(t)
	if _, err := s.CancelIntentBy(in.ID, "操作员叫停", model.HoldActorOperator); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(in.ID)
		return ok && got.Hold && got.Status == model.StatusFailed
	})
	// A harness session (commander) on the project mux: the surface marker is
	// what cancelIntent/retryIntent use to tell the actor apart.
	req := httptest.NewRequest(http.MethodPost, "/api/intents/"+in.ID+"/retry", strings.NewReader(`{"note":"再试"}`))
	req = req.WithContext(WithAgentSurface(req.Context()))
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.Register(mux)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "held_by_operator") {
		t.Fatalf("commander retry = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.Store.GetIntent(in.ID)
	if !got.Hold || got.HeldBy != model.HoldActorOperator {
		t.Fatalf("commander retry changed the hold: %+v", got)
	}
	// The operator path is exempt.
	rec = call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/retry", map[string]string{"note": "操作员重试"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("operator retry = %d %s", rec.Code, rec.Body.String())
	}
}
