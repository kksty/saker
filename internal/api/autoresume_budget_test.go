package api

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// The resume budget exists to bound a retry storm, not to count refusals. A
// direction refused by capacity or by a premise never started a run, so a later
// transport failure is still its first attempt: charging it here means three
// busy minutes can leave the direction permanently unable to heal itself.
func TestAutoResumeRefusalDoesNotSpendTheBudget(t *testing.T) {
	s, p := newBindingServer(t)
	in, err := p.Store.CreateIntent(model.Intent{Title: "传输失败", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusFailed }); err != nil {
		t.Fatal(err)
	}
	resetAutoResumeBudget(in.ID)
	t.Cleanup(func() { resetAutoResumeBudget(in.ID) })

	// The instance is full: the retry cannot start.
	s.MaxWorkers = 1
	s.GlobalRunning = func() int { return 1 }
	s.autoResumeFire("run_transport", in.ID, nil)
	if used := autoResumeUsed(in.ID); used != 0 {
		t.Fatalf("a refused start spent %d of the resume budget, want 0", used)
	}
	if got, _ := p.Store.GetIntent(in.ID); got.Status != model.StatusFailed {
		t.Fatalf("a refused attempt changed the direction to %q", got.Status)
	}

	// Control: with a slot free the timer does start a run and does charge one.
	s.GlobalRunning = func() int { return 0 }
	s.autoResumeFire("run_transport", in.ID, nil)
	if used := autoResumeUsed(in.ID); used != 1 {
		t.Fatalf("a started resume charged %d, want 1", used)
	}
	got, _ := p.Store.GetIntent(in.ID)
	if got.RunID == "" {
		t.Fatalf("the started resume left no run on the direction (status %q)", got.Status)
	}
	waitRun(t, p, got.RunID)
}
