package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestIntentSnapshotsOwnAllMutableFields(t *testing.T) {
	s := open(t)
	request := model.Intent{
		Title: "detached", FromFactIDs: []string{"f_original"}, Skills: []string{"original"},
		Notes:         []model.IntentNote{{Text: "original"}},
		Interjections: []model.Interjection{{Text: "original"}},
	}
	in, err := s.CreateIntent(request)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := s.GetIntent(in.ID)
	mutate := func(in model.Intent) {
		in.FromFactIDs[0] = "changed"
		in.Skills[0] = "changed"
		in.Notes[0].Text = "changed"
		in.Interjections[0].Text = "changed"
	}
	for name, snapshot := range map[string]model.Intent{
		"input": request, "created": in, "get": want,
		"list": s.ListIntents()[0], "snapshot": s.Snapshot().Intents[0],
	} {
		t.Run(name, func(t *testing.T) {
			mutate(snapshot)
			got, _ := s.GetIntent(in.ID)
			if got.FromFactIDs[0] != "f_original" || got.Skills[0] != "original" ||
				got.Notes[0].Text != "original" || got.Interjections[0].Text != "original" {
				t.Fatal("snapshot mutation changed the stored intent")
			}
		})
	}
	// The earlier snapshot must also stay stable when a writer edits nested fields.
	before, _ := s.GetIntent(in.ID)
	updated, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Skills[0] = "updated" })
	if err != nil {
		t.Fatal(err)
	}
	if before.Skills[0] != "original" || updated.Skills[0] != "updated" {
		t.Fatal("stored update changed an earlier snapshot")
	}
}

func TestOperatorWaitOwnsRequestAccessIDs(t *testing.T) {
	s, in := operatorStore(t)
	ids := []string{"ac_original"}
	request, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "login", AccessIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = "ac_input_mutated"
	request.AccessIDs[0] = "ac_output_mutated"
	got, _ := s.GetIntent(in.ID)
	if got.Wait.Operator.AccessIDs[0] != "ac_original" {
		t.Fatal("request access IDs alias stored wait")
	}
}
