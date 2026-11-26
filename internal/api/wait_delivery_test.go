package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The crash window this closes: the claim that consumes a ready wait commits,
// then the process dies before the executor receives the prompt. The next
// dispatch must carry the original results, not lose them with the cleared wait
// and not replay the external action that produced them.
func TestInterruptedClaimRedeliversWaitResults(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ex := &promptCaptureExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.SkillsDir = filepath.Join(dir, "skills")
	t.Cleanup(func() {
		s.BeginShutdown()
		s.WaitIdle(3 * time.Second)
	})

	in, err := st.CreateIntent(model.Intent{Title: "等待结果接力", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_, actionRun, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	action, _, err := st.BeginAction(in.ID, actionRun.ID, "wait-1", "fixture action", "ref")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveAction(action.ID, 1, model.ActionCompleted, "action result 42", "", actionRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, actionRun.ID
		i.Wait = &model.Wait{
			Actions:      []string{action.ID},
			Mode:         model.WaitAll,
			WakePending:  true,
			WakeReason:   "action_completed",
			RegisteredAt: time.Now(),
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Claim without dispatching: this is the state a crash leaves behind.
	_, stale, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if stale.WaitDelivery == nil || stale.WaitDelivery.Status != model.WaitDeliveryReady {
		t.Fatalf("stale run delivery = %+v", stale.WaitDelivery)
	}
	ended := time.Now()
	if _, err := st.UpdateRun(stale.ID, func(r *model.Run) {
		r.Status, r.CancelCause, r.EndedAt = model.RunCanceled, model.CancelCauseRestart, &ended
		r.ExitReason = "进程重启对账"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, stale.ID
	}); err != nil {
		t.Fatal(err)
	}

	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runID == stale.ID {
		t.Fatal("recovery reused the interrupted run instead of starting a new one")
	}
	waitFor(t, 3*time.Second, func() bool { return ex.count() > 0 })
	run, ok := st.GetRun(runID)
	if !ok || run.WaitDelivery == nil || run.WaitDelivery.Wait == nil {
		t.Fatalf("recovered run delivery = %+v", run.WaitDelivery)
	}
	t.Logf("delivery status=%s wake=%v results=%+v", run.WaitDelivery.Status, run.WaitDelivery.Wait.WakePending, run.WaitDelivery.Wait.Results)
	prompt := ex.promptAt(0)
	if !strings.Contains(prompt, "action result 42") || !strings.Contains(prompt, "外部动作结果") {
		t.Fatalf("recovered prompt lost the wait result:\n%s", prompt)
	}
	if got := run.WaitDelivery.Wait.Results; len(got) != 1 || got[0].Result != "action result 42" {
		t.Fatalf("recovered run results = %+v", got)
	}
}
