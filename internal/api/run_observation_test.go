package api

import (
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func TestRunObservationPreservesSessionAndCountsOnlyStructuredToolFailures(t *testing.T) {
	s, p := newBindingServer(t)
	run, err := p.Store.AddRun(model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	for _, ev := range []executor.Event{
		{Kind: executor.KindSession, SessionID: "native-session"},
		{Kind: executor.KindMessage, Text: "failed error failed"},
		{Kind: executor.KindToolResult, OK: executor.BoolPtr(true), Text: "expected error response"},
		{Kind: executor.KindToolResult, OK: executor.BoolPtr(false), Text: ""},
	} {
		if err := s.appendRunEvent(run.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	_, err = p.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone })
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(p.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.GetRun(run.ID)
	if got.SessionID != "native-session" || got.ToolFailures != 1 {
		t.Fatalf("bad observation: %+v", got)
	}
}
