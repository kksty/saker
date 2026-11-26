package store

import (
	"errors"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func editString(s string) *string { return &s }

// A pre-dispatch edit writes only the fields it names, bumps its own version
// (not context/handoff), and records the editor. A stale editor is refused
// without touching the row.
func TestEditIntentPlanBumpsVersionAndKeepsUnsentFields(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "旧标题", Description: "旧说明", Eval: "旧验收"})
	if err != nil {
		t.Fatal(err)
	}
	if in.EditVersion != 0 || in.EditedBy != "" {
		t.Fatalf("a fresh direction must start unedited: %+v", in)
	}

	got, err := s.EditIntentPlan(in.ID, 0, "operator", IntentPlanEdit{
		Title: editString(" 新标题 "),
		Eval:  editString("新验收"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "新标题" || got.Eval != "新验收" || got.Description != "旧说明" {
		t.Fatalf("edit fields = %+v", got)
	}
	if got.EditVersion != 1 || got.EditedBy != "operator" {
		t.Fatalf("edit provenance = v%d by %q", got.EditVersion, got.EditedBy)
	}
	if got.ContextVersion != 0 || got.HandoffVersion != 0 {
		t.Fatalf("a text edit moved another version: context=%d handoff=%d", got.ContextVersion, got.HandoffVersion)
	}

	// The stale editor read v0; the row is now v1. Its write must fail and
	// leave the accepted text alone.
	if _, err := s.EditIntentPlan(in.ID, 0, "commander", IntentPlanEdit{Eval: editString("覆盖")}); !errors.Is(err, ErrIntentEditConflict) {
		t.Fatalf("stale edit = %v, want ErrIntentEditConflict", err)
	}
	unchanged, _ := s.GetIntent(in.ID)
	if unchanged.Eval != "新验收" || unchanged.EditVersion != 1 || unchanged.EditedBy != "operator" {
		t.Fatalf("stale edit changed the row: %+v", unchanged)
	}

	// Clearing the acceptance criterion is a real edit, not "no field".
	cleared, err := s.EditIntentPlan(in.ID, 1, "commander", IntentPlanEdit{Eval: editString("  ")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Eval != "" || cleared.EditVersion != 2 || cleared.EditedBy != "commander" {
		t.Fatalf("cleared edit = %+v", cleared)
	}
	// An empty title is refused before anything is written.
	if _, err := s.EditIntentPlan(in.ID, 2, "operator", IntentPlanEdit{Title: editString(" ")}); !errors.Is(err, ErrIntentEditInvalid) {
		t.Fatalf("empty title = %v, want ErrIntentEditInvalid", err)
	}
	if after, _ := s.GetIntent(in.ID); after.EditVersion != 2 || after.Title != "新标题" {
		t.Fatalf("refused title changed the row: %+v", after)
	}
	if _, err := s.EditIntentPlan("in_missing", 0, "operator", IntentPlanEdit{Eval: editString("x")}); !errors.Is(err, ErrIntentEditNotFound) {
		t.Fatalf("missing intent = %v, want ErrIntentEditNotFound", err)
	}
}

// Once a run exists the row stops being the only copy of the plan: that run
// archived the brief it used. Editing stays out even if the direction later
// returns to pending (canceled/reset), because it has a dispatched history.
func TestEditIntentPlanRefusesADispatchedDirection(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "要改的", Eval: "旧验收"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditIntentPlan(in.ID, 0, "operator", IntentPlanEdit{Eval: editString("新验收")}); err != nil {
		t.Fatal(err)
	}
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if run.IntentVersion != 1 {
		t.Fatalf("run intent_version = %d, want the v1 it dispatched", run.IntentVersion)
	}

	if _, err := s.EditIntentPlan(in.ID, 1, "operator", IntentPlanEdit{Eval: editString("运行中改写")}); !errors.Is(err, ErrIntentNotEditable) {
		t.Fatalf("edit while running = %v, want ErrIntentNotEditable", err)
	}

	// A canceled run puts the intent back to pending but leaves RunID and
	// Attempts behind; the never-dispatched gate must still hold.
	if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	back, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending })
	if err != nil {
		t.Fatal(err)
	}
	if back.RunID == "" || back.Attempts == 0 {
		t.Fatalf("fixture did not keep dispatch history: %+v", back)
	}
	if _, err := s.EditIntentPlan(in.ID, 1, "operator", IntentPlanEdit{Eval: editString("回到 pending 再改")}); !errors.Is(err, ErrIntentNotEditable) {
		t.Fatalf("edit after a finished run = %v, want ErrIntentNotEditable", err)
	}
}

// An unedited direction is dispatched as v0; the run row says which plan
// version it actually used instead of leaving that to the current row.
func TestDispatchStampsThePlanVersionItRead(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "未编辑"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if run.IntentVersion != 0 {
		t.Fatalf("unedited run intent_version = %d, want 0", run.IntentVersion)
	}
}
