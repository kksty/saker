package decider

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A process restart seeds the pressure from the last commander round. The
// resumed harness session holds the size the harness reported at that round's
// END, so a round that compacted must seed FinalContextTokens, not the peak
// ContextTokens it climbed to before compacting. Seeding the peak made the first
// round after a restart a spurious 85% handoff; a handoff round deliberately
// dispatches nothing, and with no run in flight no later event wakes the work it
// deferred, so the operator's explicit continue ended in an idle project.
func TestRestartSeedsTheCompactedSizeNotTheOldPeak(t *testing.T) {
	st := newStore(t)
	if _, err := st.AddGoal(model.Goal{Statement: "拿到站点权限或 RCE"}); err != nil {
		t.Fatal(err)
	}
	prev, err := st.AddRun(model.Run{
		Purpose: "commander", ContextTokens: 997391, FinalContextTokens: 48671, ContextWindow: 1000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(prev.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true, Restarted: true} }
	var prompt string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompt = p
		return RoundResult{ContextTokens: 69737, ContextWindow: 1000000}, nil
	}
	d := r.Run(context.Background(), "operator", true)
	if d == nil {
		t.Fatal("the operator's forced round did not run")
	}
	if d.Handoff || strings.Contains(prompt, "## 交接（本轮必做）") {
		t.Fatalf("a restarted compacted session was handed off again (old peak seeded as pressure): %+v\n%s", d, prompt)
	}
}
