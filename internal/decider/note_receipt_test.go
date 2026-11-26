package decider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/state"
	"github.com/kksty/saker/internal/store"
)

// Use the actual persistence contract in fake commander rounds. A display
// action alone deliberately cannot stand in for a successful handoff anymore.
func completeNoteRound(t *testing.T, st *store.Store, ctx context.Context, text string) string {
	t.Helper()
	run, err := st.AddRun(model.Run{Purpose: "commander", CommanderRound: CommanderRoundInput(ctx).Round})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := st.Notebook()
	if _, err := st.SaveNotebook(note.Version, text, "commander", run.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func TestStateDisplayActionCannotProveHandoff(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	r.ModelName, r.ContextWindow, r.lastContext = "fixture", 128000, 110000
	r.Commander = func(ctx context.Context, _ string, _ bool) (RoundResult, error) {
		completeNoteRound(t, st, ctx, "another round's note")
		run, err := st.AddRun(model.Run{Purpose: "commander", Status: model.RunRunning})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
			t.Fatal(err)
		}
		return RoundResult{RunID: run.ID, Actions: []Action{{Kind: "state"}}, ContextTokens: 110000}, nil
	}
	d := r.Run(context.Background(), "manual", true)
	if d.HandoffReceipt != nil || r.handoffDone || r.handoffRound {
		t.Fatal("another run's note/action satisfied handoff")
	}
}

func TestMissingHandoffReceiptReleasesPressureAcrossCompaction(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "pressure", true: "compaction"}[compact], func(t *testing.T) {
			st := newStore(t)
			r := NewRunner(st, nil, "")
			r.ModelName, r.ContextWindow, r.lastContext = "fixture", 128000, 110000
			var prompts []string
			r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
				prompts = append(prompts, p)
				return RoundResult{ContextTokens: 110000, Compacted: compact}, nil
			}
			first := r.Run(context.Background(), "operator", true)
			second := r.Run(context.Background(), "operator", true)
			third := r.Run(context.Background(), "operator", true)
			if !first.Handoff || !second.Handoff || third.Handoff || !second.HandoffReleased || second.HandoffReceipt != nil || r.handoffDone {
				t.Fatalf("unbounded or fabricated handoff: %+v %+v %+v", first, second, third)
			}
			if !strings.Contains(prompts[0], "操作员请求要求的动作") || !strings.Contains(prompts[2], "不代表交接成功") {
				t.Fatal("operator priority or truthful recovery missing")
			}
		})
	}
}

func TestExternallyChangedNoteIsReinjected(t *testing.T) {
	st := newStore(t)
	f, err := st.AddFact(model.Fact{Kind: "note", Title: "source"})
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := st.FactVersionsAt(st.Watermark())
	if _, err := st.SaveNotebook(0, "old judgement", "operator", "", refs); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, state.NewLedger(filepath.Join(t.TempDir(), "state.md"), st), "")
	r.ModelName = "fixture"
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	var prompts []string
	r.Commander = func(_ context.Context, p string, _ bool) (RoundResult, error) {
		prompts = append(prompts, p)
		return RoundResult{}, nil
	}
	r.Run(context.Background(), "first", true)
	if _, err := st.SaveNotebook(1, "new judgement", "operator", "", refs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "changed source" }); err != nil {
		t.Fatal(err)
	}
	r.Run(context.Background(), "updated", true)
	p := prompts[1]
	if !strings.Contains(p, StateFullHeader) || !strings.Contains(p, "其他轮次或由操作员更新") || !strings.Contains(p, "new judgement") || !strings.Contains(p, "事实已变化") {
		t.Fatal("external edit or stale references were hidden")
	}
}
