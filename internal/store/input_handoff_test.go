package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A handoff replacement is dispatched only when the run it replaces archived
// what it was told. Without that, the next model can only read the previous
// model's paraphrase of the authorization, and no later reader can tell the two
// apart — the failure this contract exists to stop.
func TestPrepareRunInputRefusesHandoffWithoutArchivedBrief(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	in, err := st.CreateIntent(model.Intent{Title: "接手的意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}

	// (a) The replaced run does not exist at all.
	missing := *inHandoffOn(t, st, in.ID, &model.Handoff{FromRun: "r_missing", Note: "旧的"})
	if _, _, err := st.PrepareRunInput(run.ID, missing, model.PremiseVerifiedOnly, DispatchGate{}); err == nil ||
		!strings.Contains(err.Error(), "交接缺少下发归档") {
		t.Fatalf("handoff with a missing source run = %v, want the archive refusal", err)
	}

	// (b) The run exists but carries no archive (imported or pre-archive ledger).
	bare, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunDone})
	if err != nil {
		t.Fatal(err)
	}
	unarchived := *inHandoffOn(t, st, in.ID, &model.Handoff{FromRun: bare.ID, Note: "旧的"})
	if _, _, err := st.PrepareRunInput(run.ID, unarchived, model.PremiseVerifiedOnly, DispatchGate{}); err == nil ||
		!strings.Contains(err.Error(), "交接缺少下发归档") {
		t.Fatalf("handoff from an unarchived run = %v, want the archive refusal", err)
	}

	// (c) With the archive in place the same run may be prepared: the refusal is
	// about the missing evidence, not about handoffs in general.
	if _, err := st.UpdateRun(bare.ID, func(r *model.Run) { r.BriefPath = ".saker/briefs/" + r.ID + ".md" }); err != nil {
		t.Fatal(err)
	}
	archived := *inHandoffOn(t, st, in.ID, &model.Handoff{FromRun: bare.ID, Note: "旧的"})
	if _, _, err := st.PrepareRunInput(run.ID, archived, model.PremiseVerifiedOnly, DispatchGate{}); err != nil {
		t.Fatalf("handoff with an archived source = %v, want success", err)
	}

	// A direction that was never handed off is unaffected.
	plain, _ := st.GetIntent(in.ID)
	plain.Handoff = nil
	if _, _, err := st.PrepareRunInput(run.ID, plain, model.PremiseVerifiedOnly, DispatchGate{}); err != nil {
		t.Fatalf("plain dispatch = %v, want success", err)
	}
}

// inHandoffOn rewrites the intent's handoff and returns the fresh row, so the
// caller prepares exactly what the dispatcher would see.
func inHandoffOn(t *testing.T, st *Store, id string, h *model.Handoff) *model.Intent {
	t.Helper()
	updated, err := st.UpdateIntent(id, func(i *model.Intent) { i.Handoff = h })
	if err != nil {
		t.Fatal(err)
	}
	return &updated
}
