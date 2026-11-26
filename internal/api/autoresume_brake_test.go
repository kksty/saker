package api

// The per-direction budget counts attempts per intent. When one gateway or one
// scan node is down, every direction fails for the same reason and each may
// spend its own three chances: nobody exceeds a limit and the fleet still sends
// N×3 dials at a link that is already down. The brake makes the unit of recovery
// the shared dependency, so the second direction waits (or is handed back to the
// commander) instead of arriving with the herd.
//
// Falsification: drop the Reserve call in autoResumeAfterFailure and the second
// direction schedules a resume, so its journal has no skipped reason and its
// budget is spent.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/brake"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

func failedTransportDirection(t *testing.T, p *project.Project, title string) (intentID, runID string) {
	t.Helper()
	// The runtime creates the journal directory with the project; the fixture
	// has to, or the decision cannot be read back from the run log.
	if err := os.MkdirAll(filepath.Join(p.Dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	resetAutoResumeBudget(in.ID)
	t.Cleanup(func() { resetAutoResumeBudget(in.ID) })
	return in.ID, run.ID
}

func TestSharedDependencyGateStopsTheSecondRecovery(t *testing.T) {
	s, p := newBindingServer(t)
	// One slot, so the second direction is refused and says why.
	previousBrake := autoResumeBrake
	autoResumeBrake = brake.New(brake.Options{Cooldown: time.Hour, Window: time.Hour, Max: 1})
	t.Cleanup(func() { autoResumeBrake = previousBrake })
	// Keep the timer from firing inside the test: the subject is the decision,
	// not the wait.
	previousDelays := autoResumeDelays
	autoResumeDelays = []time.Duration{time.Hour}
	t.Cleanup(func() { autoResumeDelays = previousDelays })

	res := executor.Result{FailureClass: aop.FailureTransport}
	firstIntent, firstRun := failedTransportDirection(t, p, "第一条传输失败的方向")
	secondIntent, secondRun := failedTransportDirection(t, p, "第二条传输失败的方向")

	s.autoResumeAfterFailure(firstRun, firstIntent, model.RunFailed, res)
	if autoResumeUsed(firstIntent) != 0 {
		t.Fatal("the budget is charged when the retry starts, not when the timer is armed")
	}
	if skipped := autoresumeSkip(t, s, firstRun); skipped != "" {
		t.Fatalf("the first direction was skipped: %q", skipped)
	}

	s.autoResumeAfterFailure(secondRun, secondIntent, model.RunFailed, res)
	if used := autoResumeUsed(secondIntent); used != 0 {
		t.Fatalf("a refused recovery spent %d of the second direction's budget", used)
	}
	skipped := autoresumeSkip(t, s, secondRun)
	if !strings.Contains(skipped, "共享依赖闸") {
		t.Fatalf("second direction skip note = %q, want the shared-dependency refusal", skipped)
	}
	if got, _ := p.Store.GetIntent(secondIntent); got.Status != model.StatusFailed {
		t.Fatalf("a refused recovery changed the direction to %q", got.Status)
	}
}

// Once the dependency carries a run to completion again, the gate forgets it:
// the next failure is a new episode and must not inherit the cooldown. The
// operator otherwise waits out a window that describes a link that already
// recovered.
func TestCompletedRunClearsTheSharedDependencyGate(t *testing.T) {
	s, p := newBindingServer(t)
	previousBrake := autoResumeBrake
	autoResumeBrake = brake.New(brake.Options{Cooldown: time.Hour, Window: time.Hour, Max: 1})
	t.Cleanup(func() { autoResumeBrake = previousBrake })
	previousDelays := autoResumeDelays
	autoResumeDelays = []time.Duration{time.Hour}
	t.Cleanup(func() { autoResumeDelays = previousDelays })

	res := executor.Result{FailureClass: aop.FailureTransport}
	firstIntent, firstRun := failedTransportDirection(t, p, "先占住闸门")
	s.autoResumeAfterFailure(firstRun, firstIntent, model.RunFailed, res)

	key := s.recoveryKey(firstRun)
	if slot, _ := autoResumeBrake.Reserve(key); slot != nil {
		t.Fatal("fixture did not fill the gate")
	}
	// A run on the same dependency completes. finish() must clear the gate, or
	// the operator waits out a window describing a link that already recovered.
	doneIntent, doneRun := failedTransportDirection(t, p, "同一依赖上的完成")
	s.finish(doneRun, doneIntent, model.RunDone, "", executor.Result{}, 0)
	if slot, reason := autoResumeBrake.Reserve(key); slot == nil {
		t.Fatalf("gate was not cleared after a completed run: %s", reason)
	}
}

// autoresumeSkip returns the skipped reason journaled for a run, or "" when the
// recovery was scheduled.
func autoresumeSkip(t *testing.T, s *Server, runID string) string {
	t.Helper()
	for _, line := range readJournalFileLines(t, s.DataDir, runID) {
		if line["kind"] != "run_autoresume" {
			continue
		}
		if skipped, ok := line["skipped"].(string); ok {
			return skipped
		}
	}
	return ""
}
